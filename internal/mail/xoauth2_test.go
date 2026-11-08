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

	// Google's exact wire format, taken from the protocol documentation rather
	// than from our own implementation. The separators are SOH (0x01), the
	// `user=` prefix is mandatory, and the credentials are NOT base64 encoded by
	// the client: go-imap encodes the initial response when it writes the
	// AUTHENTICATE command.
	//
	// Deriving this expectation from Start's output is what let the original
	// missing prefix ship: implementation and assertion shared the same bug and
	// the suite stayed green while every real login was rejected with an opaque
	// {"status":"400"} challenge.
	want := "user=me@example.com\x01auth=Bearer ya29.token\x01\x01"
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

// A username containing the SOH separator would let a crafted account name
// inject extra SASL fields. Google's format has no escaping, so the only safe
// answer is to refuse the value.
func TestXOAUTH2RejectsInjectedSeparator(t *testing.T) {
	c := NewXOAUTH2Client("me@example.com\x01auth=Bearer stolen", "ya29.token")
	if _, _, err := c.Start(); err == nil {
		t.Fatal("expected an error for a username containing 0x01")
	}
}
