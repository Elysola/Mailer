package notify

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/elysola/mailer/internal/config"
	"github.com/elysola/mailer/internal/mail"
)

// Rate limits are the one failure mode where guessing wrong is expensive:
// retrying too fast gets a longer ban. These tests pin the parsing rules so a
// platform changing its units cannot silently turn a 1s wait into a 1µs wait.

func telegramTestConfig(apiBase string) config.Telegram {
	return config.Telegram{
		Enabled:  true,
		BotToken: "test-token",
		ChatIDs:  []string{"42"},
		APIBase:  apiBase,
	}
}

func newTestDiscord(t *testing.T, webhookURL string) *Discord {
	t.Helper()
	d, err := NewDiscord(config.Discord{Enabled: true, WebhookURL: webhookURL}, nil, false, nil)
	if err != nil {
		t.Fatalf("NewDiscord: %v", err)
	}
	t.Cleanup(d.Close)
	return d
}

func testMessage() mail.Message {
	return mail.Message{
		Account: "test",
		UID:     1,
		From:    "Sender <sender@example.com>",
		Subject: "hello",
		Preview: "body",
	}
}

func TestParseRetryAfterSecondsNumeric(t *testing.T) {
	cases := map[string]time.Duration{
		"0":    0,
		"1":    time.Second,
		"5.5":  5500 * time.Millisecond,
		" 17 ": 17 * time.Second,
		"":     0,
		"n/a":  0,
	}
	for in, want := range cases {
		if got := parseRetryAfterSeconds(in); got != want {
			t.Errorf("parseRetryAfterSeconds(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestParseRetryAfterSecondsHTTPDate(t *testing.T) {
	// The HTTP spec allows an absolute date; treat it as a delta from now.
	future := time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat)
	got := parseRetryAfterSeconds(future)
	if got < 25*time.Second || got > 31*time.Second {
		t.Errorf("HTTP-date Retry-After = %v, want ~30s", got)
	}
}

func TestSecondsToDurationRoundsUp(t *testing.T) {
	// Rounding down would wake us a hair before the window closes and earn a
	// longer ban, so any positive value must round up.
	if got := secondsToDuration(0.1); got != 100*time.Millisecond {
		t.Errorf("0.1s = %v, want 100ms", got)
	}
	if got := secondsToDuration(0.000001); got != time.Microsecond {
		t.Errorf("0.000001s = %v, want 1µs", got)
	}
	for _, bad := range []float64{0, -1, -0.5} {
		if got := secondsToDuration(bad); got != 0 {
			t.Errorf("secondsToDuration(%v) = %v, want 0", bad, got)
		}
	}
}

func TestNewRateLimitErrorPrefersHeader(t *testing.T) {
	resp := &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{}}
	resp.Header.Set("Retry-After", "7")

	// Body hint disagrees with the header; the header wins.
	err := newRateLimitError("telegram", resp, 3)
	if err.RetryAfter != 7*time.Second {
		t.Errorf("RetryAfter = %v, want 7s from header", err.RetryAfter)
	}
	if err.StatusCode != http.StatusTooManyRequests {
		t.Errorf("StatusCode = %d, want 429", err.StatusCode)
	}
}

func TestNewRateLimitErrorFallsBackToBody(t *testing.T) {
	resp := &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{}}
	err := newRateLimitError("discord", resp, 2.75)
	if err.RetryAfter != 2750*time.Millisecond {
		t.Errorf("RetryAfter = %v, want 2.75s from body", err.RetryAfter)
	}
}

func TestNewRateLimitErrorDefaultsAndCaps(t *testing.T) {
	resp := &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{}}

	// No hint at all must still produce a non-zero wait, or we would retry
	// immediately and get throttled harder.
	if got := newRateLimitError("telegram", resp, 0); got.RetryAfter != defaultRetryAfter {
		t.Errorf("RetryAfter = %v, want default %v", got.RetryAfter, defaultRetryAfter)
	}

	// A wild hint must not park the queue for years.
	for _, wild := range []float64{86400, 1e12} {
		if got := newRateLimitError("telegram", resp, wild); got.RetryAfter != maxRetryAfter {
			t.Errorf("RetryAfter = %v, want cap %v", got.RetryAfter, maxRetryAfter)
		}
	}

	// A negative hint is meaningless and must not become a negative wait.
	if got := newRateLimitError("telegram", resp, -5); got.RetryAfter != defaultRetryAfter {
		t.Errorf("RetryAfter = %v, want default for negative hint", got.RetryAfter)
	}
}

func TestRateLimitErrorIsDistinguishable(t *testing.T) {
	resp := &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{}}
	var err error = newRateLimitError("telegram", resp, 3)

	// Callers branch on this to choose "wait then retry" over "fail this
	// attempt", so it must be reachable through errors.As.
	var rl *RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("errors.As failed for %T", err)
	}
	if rl.Notifier != "telegram" {
		t.Errorf("Notifier = %q, want telegram", rl.Notifier)
	}
	if msg := err.Error(); msg == "" {
		t.Error("Error() is empty")
	}
}

// TestTelegramSendParsesRetryAfterFromBody pins the shape of Telegram's 429.
// Telegram sends the wait inside the JSON body (parameters.retry_after), not in
// a Retry-After header, so a header-only implementation would silently fall
// back to the default every time.
func TestTelegramSendParsesRetryAfterFromBody(t *testing.T) {
	var gotRetryAfter float64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":429,` +
			`"description":"Too Many Requests: retry after 9",` +
			`"parameters":{"retry_after":9}}`))
		gotRetryAfter = 9
	}))
	defer srv.Close()

	tg := NewTelegram(telegramTestConfig(srv.URL), false, nil)
	err := tg.Send(t.Context(), testMessage())
	if err == nil {
		t.Fatal("expected a rate-limit error")
	}
	var rl *RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("error is %T, want *RateLimitError: %v", err, err)
	}
	if rl.RetryAfter != time.Duration(gotRetryAfter)*time.Second {
		t.Errorf("RetryAfter = %v, want %v", rl.RetryAfter, time.Duration(gotRetryAfter)*time.Second)
	}
}

// TestDiscordSendUsesRetryAfterHeader covers the other platform's shape: a
// Retry-After header carrying fractional seconds.
func TestDiscordSendUsesRetryAfterHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "1.5")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"message":"You are being rate limited.","retry_after":1.5,"global":false}`))
	}))
	defer srv.Close()

	d := newTestDiscord(t, srv.URL)
	err := d.Send(t.Context(), testMessage())
	if err == nil {
		t.Fatal("expected a rate-limit error")
	}
	var rl *RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("error is %T, want *RateLimitError: %v", err, err)
	}
	if rl.RetryAfter != 1500*time.Millisecond {
		t.Errorf("RetryAfter = %v, want 1.5s", rl.RetryAfter)
	}
}
