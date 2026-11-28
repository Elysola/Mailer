package gotify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/elysola/mailer/internal/component"
	"github.com/elysola/mailer/internal/core"
)

func testEnv() core.Envelope {
	env := core.NewEnvelope(core.Message{Subject: "subj"})
	env.Title = "T"
	env.Body = "B"
	return env
}

func TestPostsMessageWithToken(t *testing.T) {
	var path, token string
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		token = r.URL.Query().Get("token")
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n, err := New("g", map[string]any{
		"url": srv.URL, "token": "apptoken", "priority": 5,
	}, component.Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := n.Send(context.Background(), testEnv()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if path != "/message" {
		t.Errorf("path = %q", path)
	}
	if token != "apptoken" {
		t.Errorf("token = %q", token)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("not json: %v", err)
	}
	if payload["title"] != "T" || payload["message"] != "B" {
		t.Errorf("payload = %v", payload)
	}
	if payload["priority"].(float64) != 5 {
		t.Errorf("priority = %v", payload["priority"])
	}
}

func TestRateLimitSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "15")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	n, _ := New("g", map[string]any{"url": srv.URL, "token": "t"}, component.Deps{})
	err := n.Send(context.Background(), testEnv())
	var rl *core.RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("want RateLimitError, got %v", err)
	}
	if rl.RetryAfter.Seconds() != 15 {
		t.Errorf("retry-after = %v", rl.RetryAfter)
	}
}

func TestAuthErrorIsDescriptive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	n, _ := New("g", map[string]any{"url": srv.URL, "token": "bad"}, component.Deps{})
	err := n.Send(context.Background(), testEnv())
	if err == nil {
		t.Fatal("401 must fail")
	}
}

func TestMissingFieldsRejected(t *testing.T) {
	if _, err := New("g", map[string]any{"token": "t"}, component.Deps{}); err == nil {
		t.Fatal("missing url must fail")
	}
	if _, err := New("g", map[string]any{"url": "http://127.0.0.1"}, component.Deps{}); err == nil {
		t.Fatal("missing token must fail")
	}
}

func TestPlainHTTPRejectedExceptLoopback(t *testing.T) {
	if _, err := New("g", map[string]any{
		"url": "http://gotify.example.com", "token": "t",
	}, component.Deps{}); err == nil {
		t.Fatal("plain http to remote host must fail — the token travels in the url")
	}
	if _, err := New("g", map[string]any{
		"url": "http://127.0.0.1:8080", "token": "t",
	}, component.Deps{}); err != nil {
		t.Errorf("loopback http must be allowed: %v", err)
	}
}

func TestBadPriorityRejected(t *testing.T) {
	if _, err := New("g", map[string]any{
		"url": "http://127.0.0.1", "token": "t", "priority": 99,
	}, component.Deps{}); err == nil {
		t.Fatal("priority 99 must fail")
	}
}
