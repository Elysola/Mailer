package app

import (
	"context"
	"testing"
	"time"

	"github.com/elysola/mailer/internal/config"
	"github.com/elysola/mailer/internal/mail"
	"github.com/elysola/mailer/internal/notify"
)

// notifier is a local alias so the tests below can build slices without
// importing notify just for its interface.
type notifier = notify.Notifier

// testCfgWithRetries returns a minimal config whose dispatch retries the given
// number of times. The delay between attempts is long on purpose: a cancelled
// context is then the only ready branch of the wait, which keeps the
// cancellation tests deterministic instead of racing a timer.
func testCfgWithRetries(attempts int) *config.Config {
	return &config.Config{RetryAttempts: attempts, RetryDelay: 10 * time.Second}
}

// testCfgWithFastRetries is the same but with a negligible wait, for tests
// that expect the retries to actually run.
func testCfgWithFastRetries(attempts int) *config.Config {
	return &config.Config{RetryAttempts: attempts, RetryDelay: time.Microsecond}
}

// blockingNotifier ignores its context and reports failure, so a cancelled
// poll context is the only thing that can stop dispatch's retry loop.
type blockingNotifier struct {
	name  string
	calls int
}

func (b *blockingNotifier) Name() string { return b.name }

func (b *blockingNotifier) Send(context.Context, mail.Message) error {
	b.calls++
	return errAlwaysFail
}

var errAlwaysFail = errTest("always fails")

type errTest string

func (e errTest) Error() string { return string(e) }

// TestDispatchStopsRetryingOnCancelledContext guards the regression where
// `case <-ctx.Done(): break` only left the select and kept retrying with a
// dead context for every remaining attempt.
func TestDispatchStopsRetryingOnCancelledContext(t *testing.T) {
	cfg := testCfgWithRetries(3)
	p := &Poller{cfg: cfg, metrics: NewMetrics()}

	n := &blockingNotifier{name: "blocked"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled: the wait between attempts must abort at once

	done := make(chan []string, 1)
	go func() {
		done <- p.dispatch(ctx, mail.Message{Account: "a"}, []notifier{n})
	}()

	select {
	case failed := <-done:
		if len(failed) != 1 || failed[0] != "blocked" {
			t.Fatalf("failed = %v, want [blocked]", failed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("dispatch did not return after context cancellation")
	}

	// One attempt, then abort: a cancelled context must not spend the
	// remaining retry budget. The bug this guards against kept retrying after
	// cancellation and produced RetryAttempts+1 = 4 calls.
	if n.calls != 1 {
		t.Fatalf("Send called %d time(s), want 1 (cancelled context must abort retries)", n.calls)
	}
}

// TestDispatchRetriesWhenContextLive ensures the abort path did not disable
// ordinary retries.
func TestDispatchRetriesWhenContextLive(t *testing.T) {
	cfg := testCfgWithFastRetries(2)
	p := &Poller{cfg: cfg, metrics: NewMetrics()}

	n := &blockingNotifier{name: "flaky"}
	failed := p.dispatch(context.Background(), mail.Message{Account: "a"}, []notifier{n})
	if len(failed) != 1 {
		t.Fatalf("failed = %v, want [flaky]", failed)
	}
	if want := cfg.RetryAttempts + 1; n.calls != want {
		t.Fatalf("Send called %d time(s), want %d", n.calls, want)
	}
}

// TestPollDeadlineDerivation pins the interval→timeout relationship: no
// overlapping polls on short intervals, no premature abandonment on long ones.
func TestPollDeadlineDerivation(t *testing.T) {
	cases := []struct {
		interval time.Duration
		want     time.Duration
	}{
		{1 * time.Second, 5 * time.Second},   // floor
		{10 * time.Second, 8 * time.Second},  // 80% of interval
		{60 * time.Second, 30 * time.Second}, // capped at pollTimeout
		{5 * time.Minute, 30 * time.Second},  // capped
	}
	for _, tc := range cases {
		if got := pollDeadline(tc.interval); got != tc.want {
			t.Errorf("pollDeadline(%v) = %v, want %v", tc.interval, got, tc.want)
		}
	}
}

// rateLimitedNotifier fails with a platform rate limit for the first n calls,
// then succeeds, recording the wait it was given each time.
type rateLimitedNotifier struct {
	name     string
	calls    int
	failFor  int
	retryFor time.Duration
}

func (r *rateLimitedNotifier) Name() string { return r.name }

func (r *rateLimitedNotifier) Send(context.Context, mail.Message) error {
	r.calls++
	if r.calls <= r.failFor {
		return &notify.RateLimitError{
			Notifier:   r.name,
			RetryAfter: r.retryFor,
			StatusCode: 429,
		}
	}
	return nil
}

// TestDispatchAbsorbsShortRateLimit checks that a brief platform-imposed wait
// is waited out inline and the message still delivers, rather than being
// parked in the retry queue for the platform's own retry window.
func TestDispatchAbsorbsShortRateLimit(t *testing.T) {
	p := &Poller{cfg: testCfgWithFastRetries(0), metrics: NewMetrics()}
	n := &rateLimitedNotifier{name: "telegram", failFor: 1, retryFor: 5 * time.Millisecond}

	failed := p.dispatch(context.Background(), mail.Message{Account: "a"}, []notifier{n})
	if len(failed) != 0 {
		t.Fatalf("failed = %v, want empty (short rate limit must be absorbed)", failed)
	}
	if n.calls != 2 {
		t.Fatalf("Send called %d time(s), want 2: one throttled, one delivered", n.calls)
	}

	// The window must be recorded so the next message does not retry into it.
	if wait := p.throttledFor("telegram"); wait > 0 {
		t.Fatalf("throttle still active (%v) after the wait elapsed", wait)
	}
}

// TestDispatchDefersLongRateLimit checks the opposite trade: a long window is
// not waited out inline (that would stall the whole poll cycle) but handed to
// the retry queue, with the throttle recorded so the next send skips it.
func TestDispatchDefersLongRateLimit(t *testing.T) {
	p := &Poller{cfg: testCfgWithFastRetries(3), metrics: NewMetrics()}
	n := &rateLimitedNotifier{name: "telegram", failFor: 100, retryFor: time.Hour}

	failed := p.dispatch(context.Background(), mail.Message{Account: "a"}, []notifier{n})
	if len(failed) != 1 || failed[0] != "telegram" {
		t.Fatalf("failed = %v, want [telegram]", failed)
	}
	// One attempt only: a long window must not consume the inline retry budget.
	if n.calls != 1 {
		t.Fatalf("Send called %d time(s), want 1 (long rate limit defers immediately)", n.calls)
	}
	if wait := p.throttledFor("telegram"); wait <= 0 {
		t.Fatal("throttle was not recorded for the notifier")
	}
}

// TestDispatchSkipsThrottledNotifier checks that a known rate limit is honoured
// before sending, so we never hammer a channel that just asked us to stop.
func TestDispatchSkipsThrottledNotifier(t *testing.T) {
	p := &Poller{cfg: testCfgWithFastRetries(3), metrics: NewMetrics()}
	p.noteThrottle("telegram", time.Minute)
	n := &rateLimitedNotifier{name: "telegram", failFor: 0}

	failed := p.dispatch(context.Background(), mail.Message{Account: "a"}, []notifier{n})
	if len(failed) != 1 || failed[0] != "telegram" {
		t.Fatalf("failed = %v, want [telegram]", failed)
	}
	if n.calls != 0 {
		t.Fatalf("Send called %d time(s), want 0: throttled notifier must be skipped", n.calls)
	}
}

// TestThrottleExpiry verifies the map does not leak entries forever.
func TestThrottleExpiry(t *testing.T) {
	p := &Poller{cfg: testCfgWithFastRetries(0), metrics: NewMetrics()}
	p.noteThrottle("telegram", time.Millisecond)
	time.Sleep(5 * time.Millisecond)

	if wait := p.throttledFor("telegram"); wait != 0 {
		t.Fatalf("throttledFor = %v after expiry, want 0", wait)
	}
	p.mu.Lock()
	_, present := p.throttles["telegram"]
	p.mu.Unlock()
	if present {
		t.Fatal("expired throttle entry was not pruned")
	}
}

// TestNoteThrottleKeepsLongest guards against a short window from a later
// response shortening an already-known longer one.
func TestNoteThrottleKeepsLongest(t *testing.T) {
	p := &Poller{cfg: testCfgWithFastRetries(0), metrics: NewMetrics()}
	p.noteThrottle("telegram", time.Hour)
	p.noteThrottle("telegram", time.Millisecond)

	if wait := p.throttledFor("telegram"); wait < 50*time.Minute {
		t.Fatalf("throttledFor = %v, want the longer window to win", wait)
	}
}
