package oauth

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestBuildConsentURLRequestsOfflineAccess(t *testing.T) {
	req := AuthRequest{
		ClientID:    "cid",
		Scope:       "https://mail.google.com/",
		RedirectURI: "http://127.0.0.1:8765/callback",
		LoginHint:   "me@example.com",
	}
	u := BuildConsentURL(req, "state123")

	// access_type=offline is what makes Google return a refresh token. Without
	// it the daemon gets an hour-long access token and nothing else.
	for _, want := range []string{
		"access_type=offline",
		"prompt=consent",
		"response_type=code",
		"state=state123",
		"login_hint=me%40example.com",
		"redirect_uri=http%3A%2F%2F127.0.0.1%3A8765%2Fcallback",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("consent URL missing %q:\n%s", want, u)
		}
	}
}

func TestParseRedirectInputFullURL(t *testing.T) {
	raw := "http://127.0.0.1:8765/callback?code=4/0Abc&state=st&scope=x"

	res, err := ParseRedirectInput(raw, "st")
	if err != nil {
		t.Fatalf("ParseRedirectInput: %v", err)
	}
	if res.Code != "4/0Abc" {
		t.Errorf("Code = %q, want 4/0Abc", res.Code)
	}
	// The stored redirect URI must not carry the query string.
	if strings.Contains(res.RedirectURI, "?") {
		t.Errorf("RedirectURI should be stripped of the query, got %q", res.RedirectURI)
	}
}

func TestParseRedirectInputBareCode(t *testing.T) {
	res, err := ParseRedirectInput("  4/0Abc\n", "")
	if err != nil {
		t.Fatalf("ParseRedirectInput: %v", err)
	}
	if res.Code != "4/0Abc" {
		t.Errorf("Code = %q, want 4/0Abc", res.Code)
	}
}

func TestParseRedirectInputRejectsWrongState(t *testing.T) {
	// A pasted URL from an earlier flow must not be accepted: the code would
	// be bound to a different PKCE/state context.
	_, err := ParseRedirectInput("http://127.0.0.1:8765/callback?code=abc&state=other", "expected")
	if err == nil {
		t.Fatal("expected a state mismatch error")
	}
	if !strings.Contains(err.Error(), "state") {
		t.Errorf("error should mention state, got %v", err)
	}
}

func TestParseRedirectInputSurfacesError(t *testing.T) {
	_, err := ParseRedirectInput("http://127.0.0.1:8765/callback?error=access_denied&state=st", "st")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "access_denied") {
		t.Errorf("error should carry the provider code, got %v", err)
	}
}

func TestParseRedirectInputMissingCode(t *testing.T) {
	_, err := ParseRedirectInput("http://127.0.0.1:8765/callback?state=st", "st")
	if err == nil {
		t.Fatal("expected an error when no code is present")
	}
}

func TestListenLoopbackCapturesCallback(t *testing.T) {
	state, err := NewState()
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}
	redirectURI, wait, stop, err := ListenLoopback(state)
	if err != nil {
		t.Fatalf("ListenLoopback: %v", err)
	}
	defer stop()

	// The listener must be loopback-only: binding 0.0.0.0 on a public host
	// would hand the authorization code to anyone who can reach the port.
	if !strings.HasPrefix(redirectURI, "http://127.0.0.1:") {
		t.Errorf("redirect URI = %q, want a 127.0.0.1 address", redirectURI)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		resp, err := http.Get(redirectURI + "?code=4/0xyz&state=" + state)
		if err == nil {
			resp.Body.Close()
		}
	}()

	res, err := wait(ctx)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if res.Code != "4/0xyz" {
		t.Errorf("Code = %q, want 4/0xyz", res.Code)
	}
}

func TestListenLoopbackRejectsWrongState(t *testing.T) {
	redirectURI, wait, stop, err := ListenLoopback("correct-state")
	if err != nil {
		t.Fatalf("ListenLoopback: %v", err)
	}
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		resp, err := http.Get(redirectURI + "?code=4/0xyz&state=forged")
		if err == nil {
			resp.Body.Close()
		}
	}()

	// A forged callback must not be reported as success, and the wait must
	// resolve rather than hang until the timeout.
	if _, err := wait(ctx); err == nil {
		t.Fatal("expected a state mismatch error")
	}
}

func TestNewStateIsUnique(t *testing.T) {
	seen := make(map[string]bool, 32)
	for i := 0; i < 32; i++ {
		s, err := NewState()
		if err != nil {
			t.Fatalf("NewState: %v", err)
		}
		if s == "" {
			t.Fatal("empty state")
		}
		if seen[s] {
			t.Fatalf("duplicate state %q", s)
		}
		seen[s] = true
	}
}
