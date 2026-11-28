package set

import (
	"context"
	"testing"

	"github.com/elysola/mailer/internal/component"
	"github.com/elysola/mailer/internal/core"
)

func TestStampsFields(t *testing.T) {
	p, err := New("s", map[string]any{
		"fields": map[string]any{"priority": "high", "mailbox_type": "work"},
	}, component.Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	v, err := p.Process(context.Background(), core.NewEnvelope(core.Message{}))
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if v.Fields["priority"] != "high" || v.Fields["mailbox_type"] != "work" {
		t.Errorf("fields = %v", v.Fields)
	}
}

func TestWhenGate(t *testing.T) {
	p, _ := New("s", map[string]any{
		"when":   `account == "work"`,
		"fields": map[string]any{"k": "v"},
	}, component.Deps{})
	env := core.NewEnvelope(core.Message{Account: "home"})
	v, _ := p.Process(context.Background(), env)
	if len(v.Fields) != 0 {
		t.Error("non-matching when must not stamp fields")
	}
	env = core.NewEnvelope(core.Message{Account: "work"})
	v, _ = p.Process(context.Background(), env)
	if v.Fields["k"] != "v" {
		t.Error("matching when must stamp fields")
	}
}

func TestDropMode(t *testing.T) {
	p, err := New("s", map[string]any{"drop": true}, component.Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	v, _ := p.Process(context.Background(), core.NewEnvelope(core.Message{}))
	if v.Action != core.ActionDrop {
		t.Errorf("action = %q, want drop", v.Action)
	}
}

func TestEmptyConfigRejected(t *testing.T) {
	if _, err := New("s", map[string]any{}, component.Deps{}); err == nil {
		t.Fatal("empty config must fail")
	}
}
