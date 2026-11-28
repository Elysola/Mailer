package slack

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/elysola/mailer/internal/component"
	"github.com/elysola/mailer/internal/core"
)

func testEnv() core.Envelope {
	env := core.NewEnvelope(core.Message{Subject: "S", Preview: "P"})
	env.Title = "Alert title"
	env.Body = "body text"
	return env
}

func TestPostsSlackPayload(t *testing.T) {
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n, err := New("s", map[string]any{
		"webhook_url": srv.URL,
		"channel":     "#alerts",
		"username":    "mailer",
		"icon_emoji":  ":mailbox:",
	}, component.Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := n.Send(context.Background(), testEnv()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(got, &payload); err != nil {
		t.Fatalf("not json: %v", err)
	}
	text, _ := payload["text"].(string)
	if !strings.Contains(text, "*Alert title*") || !strings.Contains(text, "body text") {
		t.Errorf("text = %q", text)
	}
	if payload["channel"] != "#alerts" || payload["username"] != "mailer" || payload["icon_emoji"] != ":mailbox:" {
		t.Errorf("payload = %v", payload)
	}
}

func TestMissingURLRejected(t *testing.T) {
	if _, err := New("s", map[string]any{}, component.Deps{}); err == nil {
		t.Fatal("missing webhook_url must fail")
	}
}

func TestNonHTTPSRejected(t *testing.T) {
	if _, err := New("s", map[string]any{"webhook_url": "http://example.com/y"}, component.Deps{}); err == nil {
		t.Fatal("non-https webhook to a remote host must fail")
	}
}

func TestErrorStatusFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	n, err := New("s", map[string]any{"webhook_url": srv.URL}, component.Deps{})
	if err != nil {
		t.Fatalf("loopback url must be accepted: %v", err)
	}
	if err := n.Send(context.Background(), testEnv()); err == nil {
		t.Fatal("403 must fail the send")
	}
}

func TestRateLimitSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	// Construct directly since httptest is plain http.
	n := &Notifier{name: "s", url: srv.URL, client: srv.Client()}
	err := n.Send(context.Background(), testEnv())
	var rl *core.RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("want RateLimitError, got %v", err)
	}
	if rl.RetryAfter.Seconds() != 5 {
		t.Errorf("retry-after = %v", rl.RetryAfter)
	}
}

func TestLongBodyTruncated(t *testing.T) {
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	n := &Notifier{name: "s", url: srv.URL, client: srv.Client()}
	env := testEnv()
	env.Body = strings.Repeat("x", slackMaxRunes*2)
	if err := n.Send(context.Background(), env); err != nil {
		t.Fatalf("Send: %v", err)
	}
	var payload map[string]any
	_ = json.Unmarshal(got, &payload)
	text, _ := payload["text"].(string)
	if len([]rune(text)) > slackMaxRunes+20 { // title + newline slack
		t.Errorf("text not truncated: %d runes", len([]rune(text)))
	}
}
