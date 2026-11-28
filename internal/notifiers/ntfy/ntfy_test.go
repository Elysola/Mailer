package ntfy

import (
	"context"
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
	env.Title = "标题"
	env.Body = "正文内容"
	return env
}

func TestPostsToTopicWithHeaders(t *testing.T) {
	var path, title, prio, tags, auth string
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		title = r.Header.Get("Title")
		prio = r.Header.Get("Priority")
		tags = r.Header.Get("Tags")
		auth = r.Header.Get("Authorization")
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n, err := New("n", map[string]any{
		"server":   srv.URL,
		"topic":    "mytopic",
		"token":    "tk_abc",
		"priority": 4,
		"tags":     []any{"email", "warning"},
	}, component.Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := n.Send(context.Background(), testEnv()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if path != "/mytopic" {
		t.Errorf("path = %q", path)
	}
	if prio != "4" {
		t.Errorf("priority = %q", prio)
	}
	if tags != "email,warning" {
		t.Errorf("tags = %q", tags)
	}
	if auth != "Bearer tk_abc" {
		t.Errorf("authorization = %q", auth)
	}
	if string(body) != "正文内容" {
		t.Errorf("body = %q", body)
	}
	// CJK title must be percent-encoded into ASCII.
	for _, c := range title {
		if c > 127 {
			t.Errorf("title header contains non-ascii byte: %q", title)
			break
		}
	}
	if !strings.Contains(title, "%E6%A0%87") {
		t.Errorf("title not percent-encoded as expected: %q", title)
	}
}

func TestBasicAuth(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	n, _ := New("n", map[string]any{
		"server": srv.URL, "topic": "t", "user": "u", "password": "p",
	}, component.Deps{})
	if err := n.Send(context.Background(), testEnv()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !strings.HasPrefix(auth, "Basic ") {
		t.Errorf("authorization = %q", auth)
	}
}

func TestRateLimitSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "10")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	n, _ := New("n", map[string]any{"server": srv.URL, "topic": "t"}, component.Deps{})
	err := n.Send(context.Background(), testEnv())
	var rl *core.RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("want RateLimitError, got %v", err)
	}
}

func TestMissingTopicRejected(t *testing.T) {
	if _, err := New("n", map[string]any{}, component.Deps{}); err == nil {
		t.Fatal("missing topic must fail")
	}
}

func TestBadPriorityRejected(t *testing.T) {
	if _, err := New("n", map[string]any{"topic": "t", "priority": 9}, component.Deps{}); err == nil {
		t.Fatal("priority 9 must fail")
	}
}

func TestPlainHTTPRejected(t *testing.T) {
	if _, err := New("n", map[string]any{
		"server": "http://ntfy.example.com", "topic": "t",
	}, component.Deps{}); err == nil {
		t.Fatal("plain http server must be rejected")
	}
}
