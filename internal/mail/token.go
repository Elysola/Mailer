package mail

import (
	"context"
	"fmt"
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
	// Preserve the field the refresh grant does not echo back.
	refreshed.Account = tok.Account
	refreshed.Scope = tok.Scope
	if refreshed.TokenURL == "" {
		refreshed.TokenURL = tok.TokenURL
	}

	// Persisting the refreshed access token is an optimisation, not a
	// requirement: failing to write it must not break a poll. The refresh
	// token itself is unchanged, so nothing is lost.
	_ = oauth.SaveToken(path, refreshed)

	tokenCache.Store(path, refreshed)
	return refreshed.AccessToken, nil
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
	_ = oauth.SaveToken(path, tok)
}

func accountName(acc *config.Account) string {
	if acc == nil {
		return "<nil>"
	}
	return acc.Name
}
