// Package oauth implements the OAuth2 pieces needed to authenticate to IMAP
// with XOAUTH2: token file storage, refresh-token exchange and the
// authorization-code flow used by `mailer auth gmail`.
//
// It is deliberately small and dependency-free. Only the refresh-token grant
// and the authorization-code grant are implemented, because those are the only
// flows a self-hosted daemon needs; there is no support for client credentials
// or device codes.
package oauth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// LoadToken reads a token file written by `mailer auth`.
//
// The file is a credential, so permissions are checked rather than silently
// accepted: a world-readable refresh token is equivalent to a leaked password
// and the user should find out immediately, not after an incident.
func LoadToken(path string) (*Token, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("read token file %s: %w", path, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("token file %s is a directory", path)
	}
	if err := checkPermissions(path, info.Mode()); err != nil {
		return nil, err
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read token file %s: %w", path, err)
	}

	var tok Token
	if err := json.Unmarshal(raw, &tok); err != nil {
		return nil, fmt.Errorf("parse token file %s: %w", path, err)
	}
	if tok.RefreshToken == "" {
		return nil, fmt.Errorf("token file %s has no refresh_token; re-run `mailer auth gmail`", path)
	}
	return &tok, nil
}

// permBits is the set of permission bits that must not be granted to group or
// other. Owner bits are left alone: the daemon only needs to read the file.
const permBits = 0o077

func checkPermissions(path string, mode os.FileMode) error {
	if mode.Perm()&permBits == 0 {
		return nil
	}
	return fmt.Errorf(
		"token file %s is readable by group/other (%#o); it holds a refresh token, so restrict it with: chmod 600 %s",
		path, mode.Perm(), path,
	)
}

// MarshalToken renders a token as indented JSON for printing to stdout.
//
// Kept separate from SaveToken so the interactive flow can show the credential
// without writing it to disk on a machine that is not the final destination.
func MarshalToken(tok *Token) (string, error) {
	if tok == nil || tok.RefreshToken == "" {
		return "", errors.New("refusing to marshal a token without a refresh_token")
	}
	raw, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode token: %w", err)
	}
	return string(raw), nil
}

// SaveToken writes the token atomically with 0600 permissions.
//
// Write-to-temp-then-rename keeps a crash from leaving a truncated token file,
// which would otherwise read as "no refresh_token" and force a re-authorisation
// for no reason.
func SaveToken(path string, tok *Token) error {
	if tok == nil || tok.RefreshToken == "" {
		return errors.New("refusing to save a token without a refresh_token")
	}

	raw, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return fmt.Errorf("encode token: %w", err)
	}
	raw = append(raw, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create token directory %s: %w", dir, err)
	}

	// CreateTemp uses 0600, so the secret is never world-readable, not even
	// briefly between creation and chmod.
	tmp, err := os.CreateTemp(dir, ".token-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp token file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename

	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("write token: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync token: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close token: %w", err)
	}
	// Re-apply the mode explicitly: CreateTemp honours umask on some systems.
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("chmod token: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace token file %s: %w", path, err)
	}
	return nil
}
