package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Token is the on-disk credential. RefreshToken is the long-lived secret and
// the only field that cannot be regenerated without a browser, so the file
// holding it must be treated as a password.
type Token struct {
	Account      string `json:"account"`
	ClientID     string `json:"client_id"`
	RefreshToken string `json:"refresh_token"`
	// AccessToken and Expiry are a cache. They are written back after a
	// refresh so a restart does not need a network round trip, but they are
	// never required: a missing access token is simply refreshed.
	AccessToken string    `json:"access_token,omitempty"`
	Expiry      time.Time `json:"expiry,omitempty"`
	// TokenURL records which endpoint issued the token. It lets a token file
	// be self-describing for non-Gmail providers.
	TokenURL string `json:"token_url,omitempty"`
	Scope    string `json:"scope,omitempty"`
}

// Valid reports whether the cached access token is still usable. The skew
// keeps a token from expiring mid-handshake: IMAP authenticates once per
// connection, so a token that dies during the TLS/AUTH exchange produces a
// confusing login failure rather than a clean refresh.
func (t *Token) Valid(now time.Time) bool {
	if t.AccessToken == "" || t.Expiry.IsZero() {
		return false
	}
	const skew = 60 * time.Second
	return now.Add(skew).Before(t.Expiry)
}

// authError is a refresh/authorization failure that no amount of retrying will
// fix (invalid_grant, invalid_client). Callers use it to distinguish "the
// credential is dead, a human must re-authorise" from transient network
// trouble, so that a dead token produces an alert instead of a silent stall.
type authError struct {
	Code string
	Desc string
}

func (e *authError) Error() string {
	if e.Desc != "" {
		return fmt.Sprintf("oauth: %s: %s", e.Code, e.Desc)
	}
	return "oauth: " + e.Code
}

// IsAuthError reports whether err means the stored credential is no longer
// usable. This is the condition worth alerting on: the daemon will keep
// failing to log in until someone runs `mailer auth gmail` again.
func IsAuthError(err error) bool {
	var ae *authError
	return errors.As(err, &ae)
}

// permanentCodes are the token-endpoint error codes that indicate a dead or
// misconfigured credential rather than a transient failure.
var permanentCodes = map[string]bool{
	"invalid_grant":       true, // refresh token expired, revoked or already used
	"invalid_client":      true, // wrong client id/secret
	"unauthorized_client": true,
	"invalid_scope":       true,
}

type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	ExpiresIn        int    `json:"expires_in"`
	RefreshToken     string `json:"refresh_token"`
	Scope            string `json:"scope"`
	TokenType        string `json:"token_type"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// Refresh exchanges a refresh token for a new access token.
//
// The refresh token itself is never returned by Google, so an empty
// RefreshToken in the response means "keep using the one you have".
func Refresh(ctx context.Context, client *http.Client, tokenURL, clientID, clientSecret, refreshToken string) (*Token, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if tokenURL == "" {
		tokenURL = "https://oauth2.googleapis.com/token"
	}

	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
	}
	resp, err := postForm(ctx, client, tokenURL, form)
	if err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return nil, classify(resp)
	}
	if resp.AccessToken == "" {
		return nil, fmt.Errorf("oauth: token endpoint returned no access_token")
	}

	tok := &Token{
		ClientID:     clientID,
		AccessToken:  resp.AccessToken,
		RefreshToken: refreshToken,
		Scope:        resp.Scope,
		TokenURL:     tokenURL,
	}
	if resp.RefreshToken != "" {
		tok.RefreshToken = resp.RefreshToken
	}
	if resp.ExpiresIn > 0 {
		tok.Expiry = time.Now().Add(time.Duration(resp.ExpiresIn) * time.Second)
	}
	return tok, nil
}

// Exchange trades an authorization code for tokens. redirectURI must match the
// one used to build the consent URL byte for byte, otherwise Google rejects the
// exchange with invalid_grant.
func Exchange(ctx context.Context, client *http.Client, tokenURL, clientID, clientSecret, code, redirectURI string) (*Token, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if tokenURL == "" {
		tokenURL = "https://oauth2.googleapis.com/token"
	}

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"redirect_uri":  {redirectURI},
	}
	resp, err := postForm(ctx, client, tokenURL, form)
	if err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return nil, classify(resp)
	}
	if resp.AccessToken == "" || resp.RefreshToken == "" {
		// An empty refresh token here means either the user had already
		// granted access (Google only issues it on first consent, unless
		// prompt=consent is sent) or the scope did not request offline access.
		return nil, fmt.Errorf("oauth: no refresh_token returned; re-run with consent (the authorization URL sets prompt=consent)")
	}

	tok := &Token{
		ClientID:     clientID,
		AccessToken:  resp.AccessToken,
		RefreshToken: resp.RefreshToken,
		Scope:        resp.Scope,
		TokenURL:     tokenURL,
	}
	if resp.ExpiresIn > 0 {
		tok.Expiry = time.Now().Add(time.Duration(resp.ExpiresIn) * time.Second)
	}
	return tok, nil
}

func postForm(ctx context.Context, client *http.Client, endpoint string, form url.Values) (*tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("oauth: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	httpResp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oauth: token request: %w", err)
	}
	defer httpResp.Body.Close()

	// Bound the read: the token endpoint returns a few hundred bytes, and an
	// unbounded io.ReadAll on a misbehaving proxy is a memory hazard.
	body, err := io.ReadAll(io.LimitReader(httpResp.Body, 64<<10))
	if err != nil {
		return nil, fmt.Errorf("oauth: read token response: %w", err)
	}

	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		// Google returns JSON for errors, but a proxy or captive portal may
		// return HTML. Surface the status code rather than a JSON parse error.
		return nil, fmt.Errorf("oauth: unexpected response (HTTP %d): %.200s", httpResp.StatusCode, body)
	}
	if httpResp.StatusCode != http.StatusOK && tr.Error == "" {
		return nil, fmt.Errorf("oauth: HTTP %d from token endpoint", httpResp.StatusCode)
	}
	return &tr, nil
}

func classify(tr *tokenResponse) error {
	if permanentCodes[tr.Error] {
		return &authError{Code: tr.Error, Desc: tr.ErrorDescription}
	}
	if tr.ErrorDescription != "" {
		return fmt.Errorf("oauth: %s: %s", tr.Error, tr.ErrorDescription)
	}
	return fmt.Errorf("oauth: %s", tr.Error)
}
