package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func tokenServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		if got := r.PostForm.Get("grant_type"); got == "" {
			t.Errorf("missing grant_type")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRefreshSuccess(t *testing.T) {
	srv := tokenServer(t, http.StatusOK, `{
		"access_token": "ya29.new",
		"expires_in": 3599,
		"scope": "https://mail.google.com/",
		"token_type": "Bearer"
	}`)

	tok, err := Refresh(context.Background(), nil, srv.URL, "cid", "secret", "1//refresh")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if tok.AccessToken != "ya29.new" {
		t.Errorf("AccessToken = %q, want ya29.new", tok.AccessToken)
	}
	// Google does not echo the refresh token; the caller must keep its own.
	if tok.RefreshToken != "1//refresh" {
		t.Errorf("RefreshToken = %q, want the original preserved", tok.RefreshToken)
	}
	if !tok.Valid(time.Now()) {
		t.Error("freshly refreshed token should be valid")
	}
}

func TestRefreshKeepsRotatedRefreshToken(t *testing.T) {
	srv := tokenServer(t, http.StatusOK, `{
		"access_token": "ya29.new",
		"expires_in": 100,
		"refresh_token": "1//rotated"
	}`)

	tok, err := Refresh(context.Background(), nil, srv.URL, "cid", "secret", "1//old")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if tok.RefreshToken != "1//rotated" {
		t.Errorf("RefreshToken = %q, want the rotated value", tok.RefreshToken)
	}
}

func TestRefreshInvalidGrantIsPermanent(t *testing.T) {
	// This is the 7-day-Testing-expiry / revoked-token case: retrying is
	// pointless, so it must be distinguishable from a network error.
	srv := tokenServer(t, http.StatusBadRequest, `{
		"error": "invalid_grant",
		"error_description": "Token has been expired or revoked."
	}`)

	_, err := Refresh(context.Background(), nil, srv.URL, "cid", "secret", "1//dead")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !IsAuthError(err) {
		t.Errorf("IsAuthError = false, want true for invalid_grant; err = %v", err)
	}
	if !strings.Contains(err.Error(), "expired or revoked") {
		t.Errorf("error should carry the provider description, got %v", err)
	}
}

func TestRefreshServerErrorIsTransient(t *testing.T) {
	srv := tokenServer(t, http.StatusInternalServerError, `{"error":"backend_error"}`)

	_, err := Refresh(context.Background(), nil, srv.URL, "cid", "secret", "1//rt")
	if err == nil {
		t.Fatal("expected an error")
	}
	if IsAuthError(err) {
		t.Errorf("a 500 is transient and must not be treated as a dead credential: %v", err)
	}
}

func TestRefreshRejectsNonJSON(t *testing.T) {
	// A captive portal or proxy returning HTML must not surface as a JSON
	// parse error with no context.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<html>login required</html>"))
	}))
	t.Cleanup(srv.Close)

	_, err := Refresh(context.Background(), nil, srv.URL, "cid", "secret", "1//rt")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "unexpected response") {
		t.Errorf("want an 'unexpected response' error, got %v", err)
	}
}

func TestExchangeRequiresRefreshToken(t *testing.T) {
	srv := tokenServer(t, http.StatusOK, `{"access_token":"ya29.a","expires_in":3600}`)

	_, err := Exchange(context.Background(), nil, srv.URL, "cid", "secret", "code", "http://127.0.0.1:8080/callback")
	if err == nil {
		t.Fatal("expected an error when no refresh_token is returned")
	}
	if !strings.Contains(err.Error(), "no refresh_token") {
		t.Errorf("want a refresh_token explanation, got %v", err)
	}
}

func TestExchangeSuccess(t *testing.T) {
	srv := tokenServer(t, http.StatusOK, `{
		"access_token":"ya29.a",
		"refresh_token":"1//rt",
		"expires_in":3600,
		"scope":"https://mail.google.com/"
	}`)

	tok, err := Exchange(context.Background(), nil, srv.URL, "cid", "secret", "code", "http://127.0.0.1:8080/callback")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if tok.RefreshToken != "1//rt" || tok.AccessToken != "ya29.a" {
		t.Errorf("unexpected tokens: %+v", tok)
	}
}

func TestTokenValidSkew(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name  string
		token Token
		want  bool
	}{
		{"no access token", Token{}, false},
		{"no expiry", Token{AccessToken: "a"}, false},
		{"expiring within skew", Token{AccessToken: "a", Expiry: now.Add(30 * time.Second)}, false},
		{"comfortably valid", Token{AccessToken: "a", Expiry: now.Add(30 * time.Minute)}, true},
		{"already expired", Token{AccessToken: "a", Expiry: now.Add(-time.Minute)}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.token.Valid(now); got != tc.want {
				t.Errorf("Valid = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSaveAndLoadTokenRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.json")
	want := &Token{
		Account:      "me@example.com",
		ClientID:     "cid",
		RefreshToken: "1//rt",
		AccessToken:  "ya29.a",
		Expiry:       time.Now().Add(time.Hour).Truncate(time.Second),
	}

	if err := SaveToken(path, want); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("permissions = %#o, want 0600 (the file holds a refresh token)", perm)
	}

	got, err := LoadToken(path)
	if err != nil {
		t.Fatalf("LoadToken: %v", err)
	}
	if got.RefreshToken != want.RefreshToken || got.AccessToken != want.AccessToken {
		t.Errorf("round trip mismatch: %+v", got)
	}
	if !got.Expiry.Equal(want.Expiry) {
		t.Errorf("Expiry = %v, want %v", got.Expiry, want.Expiry)
	}
}

func TestLoadTokenRejectsLoosePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.json")
	if err := SaveToken(path, &Token{RefreshToken: "1//rt"}); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	_, err := LoadToken(path)
	if err == nil {
		t.Fatal("expected a permission error for a world-readable token file")
	}
	if !strings.Contains(err.Error(), "chmod 600") {
		t.Errorf("error should tell the user how to fix it, got %v", err)
	}
}

func TestLoadTokenRequiresRefreshToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.json")
	raw, err := json.Marshal(Token{AccessToken: "only-access"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = LoadToken(path)
	if err == nil {
		t.Fatal("expected an error for a token file without a refresh token")
	}
	if !strings.Contains(err.Error(), "refresh_token") {
		t.Errorf("error should mention refresh_token, got %v", err)
	}
}

func TestSaveTokenRefusesEmptyRefreshToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.json")
	if err := SaveToken(path, &Token{Account: "me"}); err == nil {
		t.Fatal("expected an error when saving a token with no refresh token")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Error("no file should have been written")
	}
}

func TestSaveTokenIsAtomicOverExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token.json")
	if err := SaveToken(path, &Token{RefreshToken: "1//first"}); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}
	if err := SaveToken(path, &Token{RefreshToken: "1//second"}); err != nil {
		t.Fatalf("SaveToken overwrite: %v", err)
	}

	got, err := LoadToken(path)
	if err != nil {
		t.Fatalf("LoadToken: %v", err)
	}
	if got.RefreshToken != "1//second" {
		t.Errorf("RefreshToken = %q, want the overwritten value", got.RefreshToken)
	}

	// The temp file must be cleaned up, not left next to the token.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory should contain only the token file, got %v", names)
	}
}
