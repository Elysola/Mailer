package regex

import (
	"context"
	"testing"

	"github.com/elysola/mailer/internal/component"
	"github.com/elysola/mailer/internal/core"
)

func deps() component.Deps { return component.Deps{} }

func envWith(subject, preview string) core.Envelope {
	return core.NewEnvelope(core.Message{Subject: subject, Preview: preview})
}

func TestExtractsCaptureGroup(t *testing.T) {
	p, err := New("rx", map[string]any{
		"extract": map[string]any{"code": `验证码[:：\s]*(\d{4,8})`},
	}, deps())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	v, err := p.Process(context.Background(), envWith("登录验证", "您的验证码：482913，5 分钟内有效"))
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if v.Fields["code"] != "482913" {
		t.Errorf("code = %q", v.Fields["code"])
	}
}

func TestWholeMatchWhenNoGroup(t *testing.T) {
	p, err := New("rx", map[string]any{
		"extract": map[string]any{"amount": `\d+\.\d{2} EUR`},
	}, deps())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	v, _ := p.Process(context.Background(), envWith("invoice", "Total: 42.50 EUR due"))
	if v.Fields["amount"] != "42.50 EUR" {
		t.Errorf("amount = %q", v.Fields["amount"])
	}
}

func TestNoMatchYieldsNoFields(t *testing.T) {
	p, _ := New("rx", map[string]any{
		"extract": map[string]any{"code": `code: (\d+)`},
	}, deps())
	v, _ := p.Process(context.Background(), envWith("hello", "no code here"))
	if len(v.Fields) != 0 {
		t.Errorf("unexpected fields: %v", v.Fields)
	}
}

func TestDropOnMatch(t *testing.T) {
	p, err := New("rx", map[string]any{
		"drop_on_match": `(?i)unsubscribe`,
	}, deps())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	v, _ := p.Process(context.Background(), envWith("Newsletter", "click to Unsubscribe"))
	if v.Action != core.ActionDrop {
		t.Errorf("action = %q, want drop", v.Action)
	}
	v, _ = p.Process(context.Background(), envWith("Hi", "important stuff"))
	if v.Action != "" {
		t.Errorf("non-matching must pass through, got %q", v.Action)
	}
}

func TestReplaceRewritesBody(t *testing.T) {
	p, err := New("rx", map[string]any{
		"replace": map[string]any{`\d{16}`: "[card]"},
	}, deps())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	env := envWith("payment", "card 1234567890123456 charged")
	env.Body = "card 1234567890123456 charged"
	v, _ := p.Process(context.Background(), env)
	if v.Body != "card [card] charged" {
		t.Errorf("body = %q", v.Body)
	}
}

func TestWhenGate(t *testing.T) {
	p, _ := New("rx", map[string]any{
		"when":    `subject contains "otp"`,
		"extract": map[string]any{"code": `(\d{6})`},
	}, deps())
	v, _ := p.Process(context.Background(), envWith("normal mail", "123456"))
	if len(v.Fields) != 0 {
		t.Error("non-matching when must skip extraction")
	}
	v, _ = p.Process(context.Background(), envWith("otp inside", "123456"))
	if v.Fields["code"] != "123456" {
		t.Errorf("matching when must extract, got %v", v.Fields)
	}
}

func TestInvalidPatternRejected(t *testing.T) {
	if _, err := New("rx", map[string]any{
		"extract": map[string]any{"bad": `(`},
	}, deps()); err == nil {
		t.Fatal("invalid regex must fail at construction")
	}
}

func TestEmptyConfigRejected(t *testing.T) {
	if _, err := New("rx", map[string]any{}, deps()); err == nil {
		t.Fatal("empty config must fail at construction")
	}
}
