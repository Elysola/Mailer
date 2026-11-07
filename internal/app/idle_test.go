package app

import (
	"context"
	"testing"
	"time"

	"github.com/elysola/mailer/internal/config"
)

// TestIdleDeliversWithoutWaitingForTick is the end-to-end claim of the IDLE
// feature: mail appended to the mailbox is delivered in a fraction of
// poll_interval. The interval here is an hour — if IDLE does not fire, the
// test cannot pass by accident.
func TestIdleDeliversWithoutWaitingForTick(t *testing.T) {
	env := startMemEnv(t)
	store := openStore(t)
	notif := newFake("telegram", false)

	acc := env.acc
	acc.Idle = true
	cfg := testCfg(acc, func(c *config.Config) {
		c.PollInterval = time.Hour
	})
	p := newTestPoller(t, cfg, store, notif)
	p.firstRun[acc.Name] = false // notify mail that arrives after startup

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.startIdle(ctx)
	defer p.idle.Stop()

	// Establish the baseline. The initial poll must not see the new mail.
	if err := p.pollAccount(ctx, acc); err != nil {
		t.Fatalf("baseline poll: %v", err)
	}

	// Give the watcher time to dial, SELECT and enter IDLE, otherwise the
	// EXISTS notification would be sent before anyone is listening.
	time.Sleep(500 * time.Millisecond)

	env.appendMail(t, rawMail("<idle-1@example.com>", "idle-subject"))

	select {
	case name := <-p.idle.Events():
		if name != acc.Name {
			t.Fatalf("event account = %q, want %q", name, acc.Name)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("IDLE did not report the new message within 15s (poll interval is 1h, so polling cannot have covered it)")
	}

	// The event must translate into a delivery through the normal UID path.
	p.pollOne(ctx, acc.Name)
	if got := notif.sentCount(); got != 1 {
		t.Fatalf("delivered %d notification(s), want 1", got)
	}
	if got := notif.sent[0].Subject; got != "idle-subject" {
		t.Fatalf("subject = %q, want %q", got, "idle-subject")
	}
}
