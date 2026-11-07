package oauth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// AuthURL is Google's consent endpoint.
const AuthURL = "https://accounts.google.com/o/oauth2/v2/auth"

// AuthRequest collects the inputs for an authorization-code flow.
type AuthRequest struct {
	ClientID     string
	ClientSecret string
	Scope        string
	// RedirectURI must match the one registered on the OAuth client byte for
	// byte, and the same value must be sent again on exchange.
	RedirectURI string
	TokenURL    string
	// LoginHint pre-fills the account field on the consent screen.
	LoginHint string
}

// BuildConsentURL returns the URL the user must visit.
//
// access_type=offline is what makes Google issue a refresh token; without it
// the flow returns only a short-lived access token, which is useless for a
// daemon. prompt=consent forces the refresh token to be re-issued even when the
// user has authorised this client before, which is the common case when moving
// a token between machines.
func BuildConsentURL(req AuthRequest, state string) string {
	q := url.Values{
		"client_id":     {req.ClientID},
		"redirect_uri":  {req.RedirectURI},
		"response_type": {"code"},
		"scope":         {req.Scope},
		"access_type":   {"offline"},
		"prompt":        {"consent"},
		"state":         {state},
	}
	if req.LoginHint != "" {
		q.Set("login_hint", req.LoginHint)
	}
	return AuthURL + "?" + q.Encode()
}

// NewState returns a random state parameter. It guards against a forged
// callback being accepted as if it came from the flow we started.
func NewState() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// Result is the outcome of an authorization flow.
type Result struct {
	Code        string
	RedirectURI string
}

// ListenLoopback starts a local HTTP server on 127.0.0.1 that captures the
// single OAuth callback.
//
// Binding explicitly to 127.0.0.1 (not ":0") keeps the code off the network:
// on a shared or public host, listening on all interfaces would hand the
// authorization code to anyone who can reach the port. Google allows this for
// desktop clients, which is why the loopback redirect is the documented
// approach for installed apps.
//
// The returned function shuts the server down. The caller must call it.
func ListenLoopback(state string) (redirectURI string, wait func(context.Context) (*Result, error), stop func(), err error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, nil, fmt.Errorf("listen on loopback: %w", err)
	}

	port := ln.Addr().(*net.TCPAddr).Port
	redirectURI = fmt.Sprintf("http://127.0.0.1:%d/callback", port)

	type callback struct {
		res *Result
		err error
	}
	done := make(chan callback, 1)
	send := func(c callback) {
		select {
		case done <- c:
		default: // a second callback (browser retry) must not block
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()

		if got := q.Get("state"); subtle.ConstantTimeCompare([]byte(got), []byte(state)) != 1 {
			// Do not echo the mismatch details: the state is a CSRF token.
			http.Error(w, "state mismatch; restart the authorization", http.StatusBadRequest)
			send(callback{err: errors.New("oauth: state mismatch in callback (possible CSRF); restart the flow")})
			return
		}
		if e := q.Get("error"); e != "" {
			desc := q.Get("error_description")
			http.Error(w, "authorization failed: "+e, http.StatusBadRequest)
			send(callback{err: fmt.Errorf("oauth: authorization failed: %s (%s)", e, desc)})
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			send(callback{err: errors.New("oauth: callback had no code")})
			return
		}

		fmt.Fprint(w, "Authorization received. You can close this tab and return to the terminal.\n")
		send(callback{res: &Result{Code: code, RedirectURI: redirectURI}})
	})

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second, // slowloris guard on a public-ish port
	}
	go func() { _ = srv.Serve(ln) }()

	stop = func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}

	wait = func(ctx context.Context) (*Result, error) {
		select {
		case c := <-done:
			return c.res, c.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return redirectURI, wait, stop, nil
}

// ParseRedirectInput extracts the authorization code from whatever the user
// pastes back.
//
// In a browserless setup the browser is often on another machine, so the final
// redirect to http://127.0.0.1:PORT cannot load. Rather than ask the user to
// copy the query string by hand, accept the whole URL (or a bare code), which
// is what is actually in the address bar.
func ParseRedirectInput(raw, wantState string) (*Result, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("empty input")
	}

	// A bare code has no scheme; keep it as-is after basic sanity checks.
	if !strings.Contains(raw, "://") && !strings.HasPrefix(raw, "?") {
		if strings.ContainsAny(raw, " \t\r\n") {
			return nil, fmt.Errorf("input looks like prose, not a code: %q", raw)
		}
		return &Result{Code: raw}, nil
	}

	if strings.HasPrefix(raw, "?") {
		raw = "http://127.0.0.1/" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse pasted URL: %w", err)
	}
	q := u.Query()

	if e := q.Get("error"); e != "" {
		return nil, fmt.Errorf("authorization failed: %s (%s)", e, q.Get("error_description"))
	}
	if wantState != "" {
		if got := q.Get("state"); subtle.ConstantTimeCompare([]byte(got), []byte(wantState)) != 1 {
			return nil, errors.New("state in the pasted URL does not match this flow; did you paste an old URL?")
		}
	}
	code := q.Get("code")
	if code == "" {
		return nil, errors.New("no code in the pasted URL; copy the full address bar after authorizing")
	}

	redirect := *u
	redirect.RawQuery = ""
	redirect.Fragment = ""
	return &Result{Code: code, RedirectURI: redirect.String()}, nil
}
