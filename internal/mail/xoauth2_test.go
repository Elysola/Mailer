package mail

import (
	"strings"
	"testing"
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
	_, err := c.Next([]byte(`{"status":"401","schemes":"Bearer"}`))
	if err == nil {
		t.Fatal("expected an error on challenge")
	}
	if !strings.Contains(err.Error(), "authentication failed") {
		t.Errorf("unexpected error text: %v", err)
	}
}

func TestXOAUTH2EmptyChallenge(t *testing.T) {
	c := NewXOAUTH2Client("me@example.com", "ya29.token")
	if _, err := c.Next(nil); err == nil {
		t.Fatal("expected an error on an empty challenge")
	}
}
