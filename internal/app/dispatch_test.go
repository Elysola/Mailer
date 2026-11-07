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
// number of times with a negligible delay.
func testCfgWithRetries(attempts int) *config.Config {
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
	// remaining retry budget.
	if n.calls != 1 {
		t.Fatalf("Send called %d time(s), want 1 (cancelled context must abort retries)", n.calls)
	}
}

// TestDispatchRetriesWhenContextLive ensures the abort path did not disable
// ordinary retries.
func TestDispatchRetriesWhenContextLive(t *testing.T) {
	cfg := testCfgWithRetries(2)
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
