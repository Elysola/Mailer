package mail

import (
	"strings"
	"testing"

	"github.com/elysola/mailer/internal/oauth"
)

func TestXOAUTH2Start(t *testing.T) {
	c := NewXOAUTH2Client("me@example.com", "ya29.token")

	mech, resp, err := c.Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if mech != "XOAUTH2" {
		t.Errorf("mechanism = %q, want XOAUTH2", mech)
	}

	// Google's exact wire format. The separators are SOH (0x01), and the
	// credentials are NOT base64 encoded by the client: go-imap encodes the
	// initial response when it writes the AUTHENTICATE command.
	want := "me@example.com\x01auth=Bearer ya29.token\x01\x01"
	if string(resp) != want {
		t.Errorf("initial response = %q, want %q", resp, want)
	}
}

func TestXOAUTH2ChallengeIsAnError(t *testing.T) {
	c := NewXOAUTH2Client("me@example.com", "ya29.token")

	// A server challenge after the initial response means the credentials were
	// rejected; the client must surface it rather than looping.
	_, err := c.Next([]byte(`{"status":"400","schemes":"Bearer","scope":"https://mail.google.com/"}`))
	if err == nil {
		t.Fatal("expected an error on challenge")
	}
	if !strings.Contains(err.Error(), "rejected the access token") {
		t.Errorf("unexpected error text: %v", err)
	}
	// The raw JSON must survive in the message: it is the only evidence the
	// server gives, and hiding it makes support impossible.
	if !strings.Contains(err.Error(), `"status":"400"`) {
		t.Errorf("error lost the server detail: %v", err)
	}
}

// A rejected SASL exchange is not proof that the refresh token is dead, so it
// must not trigger the re-authorise alert. Misconfigured scope, a different
// mailbox and disabled IMAP all land here while the grant is still healthy.
func TestXOAUTH2RejectionIsNotAnAuthError(t *testing.T) {
	c := NewXOAUTH2Client("me@example.com", "ya29.token")
	_, err := c.Next([]byte(`{"status":"400"}`))
	if err == nil {
		t.Fatal("expected an error")
	}
	if oauth.IsAuthError(err) {
		t.Error("a rejected access token must not be classified as a dead credential")
	}
}

func TestXOAUTH2EmptyChallenge(t *testing.T) {
	c := NewXOAUTH2Client("me@example.com", "ya29.token")
	if _, err := c.Next(nil); err == nil {
		t.Fatal("expected an error on an empty challenge")
	}
}
