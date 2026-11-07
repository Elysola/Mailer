package mail

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/elysola/mailer/internal/config"
	"github.com/elysola/mailer/internal/oauth"
)

func writeTokenFile(t *testing.T, dir, refreshToken string, expiry time.Time) string {
	t.Helper()
	path := filepath.Join(dir, "token.json")
	tok := &oauth.Token{
		Account:      "me@example.com",
		ClientID:     "cid",
		RefreshToken: refreshToken,
		AccessToken:  "cached-access",
		Expiry:       expiry,
	}
	if err := oauth.SaveToken(path, tok); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}
	return path
}

func TestTokenForUsesCachedAccessToken(t *testing.T) {
	dir := t.TempDir()
	path := writeTokenFile(t, dir, "1//rt", time.Now().Add(time.Hour))

	// No network should be touched: the cached token is still valid.
	acc := &config.Account{
		Name: "me",
		OAuth2: &config.AccountOAuth2{
			ClientID:     "cid",
			ClientSecret: "secret",
			TokenFile:    path,
			TokenURL:     "http://127.0.0.1:1/unreachable",
		},
	}

	got, err := TokenFor(context.Background(), acc)
	if err != nil {
		t.Fatalf("TokenFor: %v", err)
	}
	if got != "cached-access" {
		t.Errorf("token = %q, want the cached value", got)
	}
}

func TestTokenForRefreshesExpiredToken(t *testing.T) {
	dir := t.TempDir()
	path := writeTokenFile(t, dir, "1//rt", time.Now().Add(-time.Hour))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		if got := r.PostForm.Get("grant_type"); got != "refresh_token" {
			t.Errorf("grant_type = %q, want refresh_token", got)
		}
		if got := r.PostForm.Get("refresh_token"); got != "1//rt" {
			t.Errorf("refresh_token = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"fresh-access","expires_in":3600}`))
	}))
	defer srv.Close()

	acc := &config.Account{
		Name: "me",
		OAuth2: &config.AccountOAuth2{
			ClientID:     "cid",
			ClientSecret: "secret",
			TokenFile:    path,
			TokenURL:     srv.URL,
		},
	}

	got, err := TokenFor(context.Background(), acc)
	if err != nil {
		t.Fatalf("TokenFor: %v", err)
	}
	if got != "fresh-access" {
		t.Errorf("token = %q, want fresh-access", got)
	}

	// The refreshed access token should be persisted so a restart does not
	// need another round trip; the refresh token must be preserved.
	saved, err := oauth.LoadToken(path)
	if err != nil {
		t.Fatalf("LoadToken: %v", err)
	}
	if saved.AccessToken != "fresh-access" {
		t.Errorf("persisted access token = %q, want fresh-access", saved.AccessToken)
	}
	if saved.RefreshToken != "1//rt" {
		t.Errorf("refresh token = %q, must be preserved", saved.RefreshToken)
	}
}

func TestTokenForReportsDeadCredential(t *testing.T) {
	dir := t.TempDir()
	path := writeTokenFile(t, dir, "1//dead", time.Now().Add(-time.Hour))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`))
	}))
	defer srv.Close()

	acc := &config.Account{
		Name: "me",
		OAuth2: &config.AccountOAuth2{
			ClientID:     "cid",
			ClientSecret: "secret",
			TokenFile:    path,
			TokenURL:     srv.URL,
		},
	}

	_, err := TokenFor(context.Background(), acc)
	if err == nil {
		t.Fatal("expected an error")
	}
	// This is the alertable condition: the daemon cannot recover on its own.
	if !oauth.IsAuthError(err) {
		t.Errorf("IsAuthError = false, want true for invalid_grant; err = %v", err)
	}
}

func TestTokenForRejectsClientIDMismatch(t *testing.T) {
	dir := t.TempDir()
	path := writeTokenFile(t, dir, "1//rt", time.Now().Add(time.Hour))

	acc := &config.Account{
		Name: "me",
		OAuth2: &config.AccountOAuth2{
			ClientID:     "different-client",
			ClientSecret: "secret",
			TokenFile:    path,
		},
	}

	// A token issued for another client can never work; failing here with a
	// clear message beats a confusing 401 from IMAP.
	_, err := TokenFor(context.Background(), acc)
	if err == nil {
		t.Fatal("expected a client ID mismatch error")
	}
	if !strings.Contains(err.Error(), "mailer auth gmail") {
		t.Errorf("error should tell the user how to fix it, got %v", err)
	}
}

func TestTokenForMissingTokenFile(t *testing.T) {
	acc := &config.Account{
		Name: "me",
		OAuth2: &config.AccountOAuth2{
			ClientID:  "cid",
			TokenFile: filepath.Join(t.TempDir(), "absent.json"),
		},
	}

	_, err := TokenFor(context.Background(), acc)
	if err == nil {
		t.Fatal("expected an error for a missing token file")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("error should wrap os.ErrNotExist, got %v", err)
	}
}

func TestResetTokenCacheForcesRefresh(t *testing.T) {
	dir := t.TempDir()
	path := writeTokenFile(t, dir, "1//rt", time.Now().Add(time.Hour))

	refreshed := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshed = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"after-reset","expires_in":3600}`))
	}))
	defer srv.Close()

	acc := &config.Account{
		Name: "me",
		OAuth2: &config.AccountOAuth2{
			ClientID:     "cid",
			ClientSecret: "secret",
			TokenFile:    path,
			TokenURL:     srv.URL,
		},
	}

	// Warm the cache first.
	if _, err := TokenFor(context.Background(), acc); err != nil {
		t.Fatalf("first TokenFor: %v", err)
	}
	if refreshed {
		t.Fatal("should not have refreshed a valid cached token")
	}

	ResetTokenCache(acc)

	// After a rejection the next call must refresh rather than replay the
	// cached token.
	got, err := TokenFor(context.Background(), acc)
	if err != nil {
		t.Fatalf("TokenFor after reset: %v", err)
	}
	if !refreshed {
		t.Error("expected a refresh after ResetTokenCache")
	}
	if got != "after-reset" {
		t.Errorf("token = %q, want after-reset", got)
	}
}

func TestTokenForWithoutOAuth2(t *testing.T) {
	if _, err := TokenFor(context.Background(), &config.Account{Name: "plain"}); err == nil {
		t.Fatal("expected an error for an account without oauth2 config")
	}
	if _, err := TokenFor(context.Background(), nil); err == nil {
		t.Fatal("expected an error for a nil account")
	}
}

// contains is a tiny helper to keep error assertions readable.
