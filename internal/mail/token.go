package mail

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/elysola/mailer/internal/config"
	"github.com/elysola/mailer/internal/oauth"
)

// tokenCache keeps the access token warm across connections.
//
// A token lives about an hour while IMAP sessions live about 24 hours, so a
// long-lived connection outlasts its token. Gmail tolerates that (it only
// checks at AUTHENTICATE), but a reconnect must not pay a refresh round trip
// every time. The cache is keyed by token file because two accounts can share
// a client but never a refresh token.
var tokenCache sync.Map // map[string]*oauth.Token

// TokenFor returns a usable access token for an account configured with
// OAuth2, refreshing it if the cached copy is missing or about to expire.
//
// Failures are wrapped so callers can tell a dead credential from transient
// network trouble: oauth.IsAuthError reports the former.
func TokenFor(ctx context.Context, acc *config.Account) (string, error) {
	if acc == nil || acc.OAuth2 == nil {
		return "", fmt.Errorf("account %q: TokenFor called without oauth2 config", accountName(acc))
	}

	path := acc.OAuth2.TokenFile

	// Consult the warm cache before touching the disk: the common case is a
	// reconnect within the token's lifetime, which should not re-read a file.
	if cached, ok := tokenCache.Load(path); ok {
		if c, isTok := cached.(*oauth.Token); isTok && c.Valid(time.Now()) {
			return c.AccessToken, nil
		}
	}

	tok, err := oauth.LoadToken(path)
	if err != nil {
		return "", err
	}

	// The account's client identity wins over whatever the file recorded: the
	// file is portable between machines, the config is authoritative.
	if acc.OAuth2.ClientID != "" && tok.ClientID != "" && tok.ClientID != acc.OAuth2.ClientID {
		return "", fmt.Errorf(
			"account %q: token file %s was issued for client %q but the config specifies %q; re-run `mailer auth gmail`",
			acc.Name, path, tok.ClientID, acc.OAuth2.ClientID,
		)
	}

	if tok.Valid(time.Now()) {
		if err := checkGrantedScope(acc, tok.Scope); err != nil {
			return "", err
		}
		tokenCache.Store(path, tok)
		return tok.AccessToken, nil
	}

	// The token file may not record an endpoint (hand-written or older file),
	// so fall back to the configured one, then to Google's default.
	tokenURL := tok.TokenURL
	if tokenURL == "" {
		tokenURL = acc.OAuth2.TokenURLOrDefault()
	}

	refreshed, err := oauth.Refresh(ctx, nil, tokenURL, acc.OAuth2.ClientID, acc.OAuth2.ClientSecret, tok.RefreshToken)
	if err != nil {
		return "", fmt.Errorf("account %q: refresh access token: %w", acc.Name, err)
	}
	if refreshed.RefreshToken == "" {
		refreshed.RefreshToken = tok.RefreshToken
	}

	// Preserve the fields the refresh grant does not echo back. Scope is not one
	// of them: Google returns the scope actually granted on every refresh, and
	// copying the file's value over it would hide a token that no longer
	// carries the IMAP scope -- exactly the case that turns into an
	// unexplained IMAP authentication failure.
	refreshed.Account = tok.Account
	if refreshed.Scope == "" {
		refreshed.Scope = tok.Scope
	}
	if refreshed.TokenURL == "" {
		refreshed.TokenURL = tok.TokenURL
	}
	if refreshed.Expiry.IsZero() {
		// No expires_in: the endpoint did not tell us. Adopt the old expiry so
		// the token is not treated as valid forever and never refreshed again.
		refreshed.Expiry = tok.Expiry
	}

	if err := checkGrantedScope(acc, refreshed.Scope); err != nil {
		return "", err
	}

	// The refresh token is unchanged, so a failed write costs only a refresh
	// round trip after a restart. It still deserves a warning: it was silent
	// before, and a token file that never changes on disk is alarming when
	// someone is trying to work out why authentication fails.
	if err := oauth.SaveToken(path, refreshed); err != nil {
		log.Printf("account %q: could not persist the refreshed access token (continuing with the in-memory copy): %v", acc.Name, err)
	}

	tokenCache.Store(path, refreshed)
	return refreshed.AccessToken, nil
}

// checkGrantedScope rejects a credential whose granted scope cannot satisfy
// IMAP, before the token is ever handed to the server.
//
// Gmail answers an out-of-scope token with a bare
// {"status":"400","schemes":"Bearer","scope":"..."} challenge, which reads
// like a server problem and sends people looking at the wrong thing. Comparing
// the recorded grant against the requested one turns that into a single
// actionable message. A token file with no recorded scope cannot be checked and
// is left alone, since older and hand-written files legitimately omit it.
func checkGrantedScope(acc *config.Account, granted string) error {
	if acc == nil || acc.OAuth2 == nil || granted == "" {
		return nil
	}
	want := acc.OAuth2.ScopeOrDefault()
	if scopeCovers(granted, want) {
		return nil
	}
	return oauth.NewAuthError(
		"invalid_scope",
		fmt.Sprintf(
			"account %q: the stored token grants %q but IMAP requires %q, so the mail server will reject it; re-run `mailer auth gmail` and approve the requested scope",
			acc.Name, granted, want,
		),
	)
}

// scopeCovers reports whether the space-separated granted scope contains every
// requested scope. Google returns the full grant verbatim on refresh, so a
// plain set comparison is enough; the server, not this function, is the
// authority on whether a scope actually unlocks a given mailbox.
//
// Comparison ignores a trailing slash. The two spellings are equivalent to
// Google, and a false negative here would refuse a working credential and send
// the operator to re-authorise for no reason, which is worse than the vague
// server error this check replaces.
func scopeCovers(granted, want string) bool {
	have := make(map[string]struct{})
	for _, s := range strings.Fields(granted) {
		have[strings.TrimSuffix(s, "/")] = struct{}{}
	}
	for _, s := range strings.Fields(want) {
		if _, ok := have[strings.TrimSuffix(s, "/")]; !ok {
			return false
		}
	}
	return true
}

// ResetTokenCache drops the cached access token for an account. Used when a
// login fails so the next attempt forces a refresh instead of retrying the
// same token.
//
// Clearing the in-memory entry alone is not enough: the refreshed access token
// is also persisted to the token file, so a subsequent TokenFor would load that
// still-unexpired token from disk and replay it. The persisted access token is
// therefore invalidated too, leaving the refresh token (and the file itself)
// untouched.
func ResetTokenCache(acc *config.Account) {
	if acc == nil || acc.OAuth2 == nil {
		return
	}
	invalidateCachedAccessToken(acc.OAuth2.TokenFile)
}

// invalidateCachedAccessToken drops both the in-memory entry and the persisted
// access token for a token file, so the next TokenFor must refresh.
func invalidateCachedAccessToken(path string) {
	tokenCache.Delete(path)
	tok, err := oauth.LoadToken(path)
	if err != nil {
		return
	}
	tok.AccessToken = ""
	tok.Expiry = time.Time{}
	if err := oauth.SaveToken(path, tok); err != nil {
		// Worth saying out loud: the documented promise is that the next
		// attempt refreshes, and that stops being true if the file still holds
		// a stale-but-unexpired token.
		log.Printf("could not invalidate the persisted access token in %s: %v", path, err)
	}
}

func accountName(acc *config.Account) string {
	if acc == nil {
		return "<nil>"
	}
	return acc.Name
}
