package throttle

import (
	"context"
	"testing"
	"time"

	"github.com/elysola/mailer/internal/component"
	"github.com/elysola/mailer/internal/core"
)

func envWith(from, subject string) core.Envelope {
	return core.NewEnvelope(core.Message{From: from, Subject: subject})
}

func newThrottle(t *testing.T, raw map[string]any) core.Processor {
	t.Helper()
	p, err := New("th", raw, component.Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

func TestFirstPassesRepeatDrops(t *testing.T) {
	p := newThrottle(t, map[string]any{"window": "1h"})

	v1, err := p.Process(context.Background(), envWith("alert@x.com", "disk full"))
	if err != nil {
		t.Fatal(err)
	}
	if v1.Action != core.ActionContinue {
		t.Errorf("first occurrence must pass, got %v", v1.Action)
	}

	v2, err := p.Process(context.Background(), envWith("alert@x.com", "disk full"))
	if err != nil {
		t.Fatal(err)
	}
	if v2.Action != core.ActionDrop {
		t.Errorf("repeat inside window must drop, got %v", v2.Action)
	}
	if v2.Fields["throttled"] != "true" {
		t.Errorf("throttled field missing: %v", v2.Fields)
	}

	// A different subject is a different key — passes.
	v3, err := p.Process(context.Background(), envWith("alert@x.com", "disk recovered"))
	if err != nil {
		t.Fatal(err)
	}
	if v3.Action != core.ActionContinue {
		t.Errorf("distinct key must pass, got %v", v3.Action)
	}
}

func TestWindowExpiryPassesAgain(t *testing.T) {
	p := newThrottle(t, map[string]any{"window": "50ms"})
	env := envWith("a@b.c", "same")

	if v, _ := p.Process(context.Background(), env); v.Action != core.ActionContinue {
		t.Fatal("first must pass")
	}
	if v, _ := p.Process(context.Background(), env); v.Action != core.ActionDrop {
		t.Fatal("immediate repeat must drop")
	}
	time.Sleep(60 * time.Millisecond)
	if v, _ := p.Process(context.Background(), env); v.Action != core.ActionContinue {
		t.Error("after the window the same key must pass again")
	}
}

func TestWhenGatesEverything(t *testing.T) {
	p := newThrottle(t, map[string]any{
		"when":   "subject contains \"alert\"",
		"window": "1h",
	})
	// Non-matching subjects never consume key space and never drop.
	env := envWith("a@b.c", "ordinary mail")
	for i := 0; i < 3; i++ {
		v, err := p.Process(context.Background(), env)
		if err != nil {
			t.Fatal(err)
		}
		if v.Action != core.ActionContinue {
			t.Fatalf("when-gated message must always pass (iteration %d)", i)
		}
	}
}

func TestCustomKeys(t *testing.T) {
	p := newThrottle(t, map[string]any{"window": "1h", "keys": []any{"subject"}})
	// Same subject, different senders — still a repeat when keys=[subject].
	if v, _ := p.Process(context.Background(), envWith("a@x", "notice")); v.Action != core.ActionContinue {
		t.Fatal("first passes")
	}
	if v, _ := p.Process(context.Background(), envWith("b@x", "notice")); v.Action != core.ActionDrop {
		t.Error("keys=[subject] must treat different senders as the same key")
	}
}

func TestInvalidKeyRejected(t *testing.T) {
	if _, err := New("th", map[string]any{"keys": []any{"nonsense"}}, component.Deps{}); err == nil {
		t.Fatal("unknown key must fail at construction")
	}
}

func TestDefaultWindowAndKeys(t *testing.T) {
	p := newThrottle(t, nil).(*Processor)
	if p.cfg.Window != time.Hour {
		t.Errorf("default window = %v", p.cfg.Window)
	}
	if len(p.cfg.Keys) != 2 || p.cfg.Keys[0] != "from" {
		t.Errorf("default keys = %v", p.cfg.Keys)
	}
}
