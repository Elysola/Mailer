package templateproc

import (
	"context"
	"testing"

	"github.com/elysola/mailer/internal/component"
	"github.com/elysola/mailer/internal/core"
)

func TestRendersTitleAndBody(t *testing.T) {
	p, err := New("tpl", map[string]any{
		"title": "验证码 {{.Fields.code}}",
		"body":  "来自 {{.Message.From}}：{{.Fields.code}}",
	}, component.Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	env := core.NewEnvelope(core.Message{From: "bank@example.com"})
	env.Fields["code"] = "482913"
	v, err := p.Process(context.Background(), env)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if v.Title != "验证码 482913" {
		t.Errorf("title = %q", v.Title)
	}
	if v.Body != "来自 bank@example.com：482913" {
		t.Errorf("body = %q", v.Body)
	}
}

func TestWhenGate(t *testing.T) {
	p, _ := New("tpl", map[string]any{
		"when":  `has(fields.code)`,
		"title": "code {{.Fields.code}}",
	}, component.Deps{})
	v, _ := p.Process(context.Background(), core.NewEnvelope(core.Message{}))
	if v.Title != "" {
		t.Error("non-matching when must produce an empty verdict")
	}
}

func TestBadTemplateRejected(t *testing.T) {
	if _, err := New("tpl", map[string]any{"title": "{{.Broken"}, component.Deps{}); err == nil {
		t.Fatal("unparseable template must fail at construction")
	}
}

func TestEmptyConfigRejected(t *testing.T) {
	if _, err := New("tpl", map[string]any{}, component.Deps{}); err == nil {
		t.Fatal("empty config must fail")
	}
}
