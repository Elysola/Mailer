package webhook

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

func testEnvelope() core.Envelope {
	env := core.NewEnvelope(core.Message{
		Subject: "Test subject",
		From:    "a@b.c",
		Preview: "preview text",
	})
	env.Title = "T"
	env.Body = "B"
	return env
}

func TestPostsEnvelopeJSON(t *testing.T) {
	var got []byte
	var gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		gotCT = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n, err := New("wh", map[string]any{"url": srv.URL}, component.Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := n.Send(context.Background(), testEnvelope()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotCT != "application/json" {
		t.Errorf("content-type = %q", gotCT)
	}
	var payload struct {
		Title    string `json:"title"`
		Body     string `json:"body"`
		Envelope struct {
			Message struct {
				Subject string `json:"subject"`
			} `json:"message"`
		} `json:"envelope"`
	}
	if err := json.Unmarshal(got, &payload); err != nil {
		t.Fatalf("response not json: %v (%s)", err, got)
	}
	if payload.Title != "T" || payload.Envelope.Message.Subject != "Test subject" {
		t.Errorf("payload = %+v", payload)
	}
}

func TestCustomBodyTemplate(t *testing.T) {
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n, err := New("wh", map[string]any{
		"url":  srv.URL,
		"body": `{"text": {{.Message.Subject | json}}}`,
	}, component.Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	env := core.NewEnvelope(core.Message{Subject: `quotes " inside`})
	if err := n.Send(context.Background(), env); err != nil {
		t.Fatalf("Send: %v", err)
	}
	// The json helper must have escaped the quotes properly.
	var parsed struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("body is not valid json: %v — %s", err, got)
	}
	if parsed.Text != `quotes " inside` {
		t.Errorf("text = %q", parsed.Text)
	}
}

func TestCustomHeadersSent(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n, err := New("wh", map[string]any{
		"url":     srv.URL,
		"headers": map[string]any{"Authorization": "Bearer tok123"},
	}, component.Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := n.Send(context.Background(), testEnvelope()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if auth != "Bearer tok123" {
		t.Errorf("authorization = %q", auth)
	}
}

func TestErrorStatusFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	n, _ := New("wh", map[string]any{"url": srv.URL}, component.Deps{})
	if err := n.Send(context.Background(), testEnvelope()); err == nil {
		t.Fatal("500 must fail the send")
	}
}

func TestRateLimitSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	n, _ := New("wh", map[string]any{"url": srv.URL}, component.Deps{})
	err := n.Send(context.Background(), testEnvelope())
	var rl *core.RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("429 must surface RateLimitError, got %v", err)
	}
	if rl.RetryAfter.Seconds() != 30 {
		t.Errorf("retry-after = %v", rl.RetryAfter)
	}
}

func TestPlainHTTPRejectedExceptLoopback(t *testing.T) {
	if _, err := New("wh", map[string]any{"url": "http://example.com/hook"}, component.Deps{}); err == nil {
		t.Fatal("plain http to a remote host must be rejected")
	}
	if _, err := New("wh", map[string]any{"url": "http://127.0.0.1:8080/hook"}, component.Deps{}); err != nil {
		t.Errorf("loopback http must be allowed: %v", err)
	}
}

func TestMissingURLRejected(t *testing.T) {
	if _, err := New("wh", map[string]any{}, component.Deps{}); err == nil {
		t.Fatal("missing url must fail")
	}
}

func TestBadTemplateRejected(t *testing.T) {
	if _, err := New("wh", map[string]any{
		"url":  "http://127.0.0.1/x",
		"body": "{{.Broken",
	}, component.Deps{}); err == nil {
		t.Fatal("bad template must fail at construction")
	}
}

func TestSuccessStatusOverride(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	defer srv.Close()

	n, _ := New("wh", map[string]any{"url": srv.URL, "success_status": 418}, component.Deps{})
	if err := n.Send(context.Background(), testEnvelope()); err != nil {
		t.Errorf("custom success status must accept 418: %v", err)
	}
	// And now the same endpoint with default expectations must fail.
	n2, _ := New("wh", map[string]any{"url": srv.URL}, component.Deps{})
	if err := n2.Send(context.Background(), testEnvelope()); err == nil {
		t.Fatal("default 2xx check must reject 418")
	}
}

func TestURLEnvExpansion(t *testing.T) {
	t.Setenv("WH_TEST_URL", "http://127.0.0.1:9999/hook")
	n, err := New("wh", map[string]any{"url": "${WH_TEST_URL}"}, component.Deps{})
	if err != nil {
		t.Fatalf("env expansion failed: %v", err)
	}
	got := n.(*Notifier).url
	if !strings.Contains(got, "127.0.0.1") {
		t.Errorf("url = %q", got)
	}
}
