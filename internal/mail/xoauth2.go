package mail

import (
	"errors"
	"fmt"

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
	return xoauth2, []byte(a.username + "\x01auth=Bearer " + a.token + "\x01\x01"), nil
}

// Next handles the server's error challenge. Per Google's spec the server
// replies with base64 JSON on failure; the client must send an empty line to
// make the server complete the rejection, otherwise the connection hangs.
func (a *xoauth2Client) Next(challenge []byte) ([]byte, error) {
	if len(challenge) == 0 {
		return nil, errors.New("xoauth2: empty challenge")
	}
	return nil, fmt.Errorf("xoauth2: authentication failed: %s", challenge)
}
