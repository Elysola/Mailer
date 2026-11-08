package mail

import (
	"errors"
	"fmt"
	"strings"

	"github.com/emersion/go-sasl"
)

// xoauth2 is the SASL mechanism Gmail (and most other providers that dropped
// password auth) expect for IMAP. The wire format is defined by Google:
//
//	base64("user=" + user + "\x01auth=Bearer " + token + "\x01\x01")
//
// go-sasl ships OAUTHBEARER but not XOAUTH2, and Gmail's OAUTHBEARER support is
// less predictable across backends, so the mechanism is implemented here. It is
// a dozen lines and removes a version dependency.
const xoauth2 = "XOAUTH2"

type xoauth2Client struct {
	username string
	token    string
}

// NewXOAUTH2Client returns a SASL client for IMAP AUTHENTICATE XOAUTH2.
func NewXOAUTH2Client(username, accessToken string) sasl.Client {
	return &xoauth2Client{username: username, token: accessToken}
}

func (a *xoauth2Client) Start() (string, []byte, error) {
	// The format has no escaping, so a 0x01 inside either field would let the
	// value inject extra SASL fields (a second auth=Bearer, say). Nothing
	// legitimate contains it, so refusing is strictly safer than sending it.
	if strings.ContainsAny(a.username, "\x01\x00") || strings.ContainsAny(a.token, "\x01\x00") {
		return "", nil, errors.New("xoauth2: username or token contains a control character")
	}
	// The `user=` prefix is part of the documented message, not decoration.
	// Leaving it out makes Gmail answer with a bare
	// {"status":"400",...} challenge that names nothing, so the failure looks
	// like a bad token rather than a malformed message.
	return xoauth2, []byte("user=" + a.username + "\x01auth=Bearer " + a.token + "\x01\x01"), nil
}

// Next handles the server's error challenge. Per Google's spec the server
// replies with base64 JSON on failure; the client must send an empty line to
// make the server complete the rejection, otherwise the connection hangs.
//
// go-imap hands the decoded challenge straight to this method, and the raw JSON
// is unreadable in a log line:
//
//	{"status":"400","schemes":"Bearer","scope":"https://mail.google.com/"}
//
// Note what that payload does not contain: any reason. status 400 with a
// schemes/scope echo is Google's generic "this bearer token is not acceptable",
// so the error text points at the causes worth checking rather than restating
// the JSON. Next never sees the cause -- the token was opaque to us too.
func (a *xoauth2Client) Next(challenge []byte) ([]byte, error) {
	if len(challenge) == 0 {
		return nil, errors.New("xoauth2: empty challenge")
	}
	return nil, &xoauth2Error{detail: string(challenge)}
}

// xoauth2Error is a rejected SASL exchange.
//
// It deliberately fails IsAuthError: oauth.IsAuthError drives the "re-run
// mailer auth gmail" alert, and a rejected token does not prove the refresh
// token is dead -- the account can be a different address than the one that
// consented, the project may have IMAP switched off, or the grant may be
// missing the IMAP scope. Those are configuration problems, and telling the
// operator to re-authorise would send them in a circle.
type xoauth2Error struct {
	detail string
}

func (e *xoauth2Error) Error() string {
	return fmt.Sprintf(
		"xoauth2: the mail server rejected the access token (%s); "+
			"check that the token was issued for this mailbox and client, "+
			"that the OAuth grant includes the IMAP scope, and that IMAP is enabled for the account",
		e.detail,
	)
}
