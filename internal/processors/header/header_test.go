package header

import (
	"context"
	"testing"

	"github.com/elysola/mailer/internal/component"
	"github.com/elysola/mailer/internal/core"
)

func envWithHeaders(headers map[string]string) core.Envelope {
	m := core.Message{Headers: headers}
	return core.NewEnvelope(m)
}

func TestCopiesNamedHeaders(t *testing.T) {
	p, err := New("h", map[string]any{
		"copy": map[string]any{"List-Id": "list", "X-Spam-Flag": "spam"},
	}, component.Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	env := envWithHeaders(map[string]string{
		"List-Id":     "<dev.golang.org>",
		"X-Spam-Flag": "NO",
		"Other":       "ignored",
	})
	v, err := p.Process(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	if v.Fields["list"] != "<dev.golang.org>" {
		t.Errorf("list = %q", v.Fields["list"])
	}
	if v.Fields["spam"] != "NO" {
		t.Errorf("spam = %q", v.Fields["spam"])
	}
	if _, ok := v.Fields["Other"]; ok {
		t.Error("uncopied headers must not leak into fields")
	}
}

func TestCaseInsensitiveMatch(t *testing.T) {
	p, _ := New("h", map[string]any{
		"copy": map[string]any{"list-id": "list"},
	}, component.Deps{})
	env := envWithHeaders(map[string]string{"List-ID": "<x>"})
	v, err := p.Process(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	if v.Fields["list"] != "<x>" {
		t.Errorf("case-insensitive match failed: %v", v.Fields)
	}
}

func TestPrefixNamespacesFields(t *testing.T) {
	p, _ := New("h", map[string]any{
		"copy":   map[string]any{"List-Id": "list"},
		"prefix": "hdr_",
	}, component.Deps{})
	env := envWithHeaders(map[string]string{"List-Id": "<x>"})
	v, _ := p.Process(context.Background(), env)
	if v.Fields["hdr_list"] != "<x>" {
		t.Errorf("prefixed field missing: %v", v.Fields)
	}
}

func TestMissingHeaderIsSilent(t *testing.T) {
	p, _ := New("h", map[string]any{
		"copy": map[string]any{"List-Id": "list"},
	}, component.Deps{})
	v, err := p.Process(context.Background(), envWithHeaders(map[string]string{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Fields) != 0 {
		t.Errorf("absent headers must not create empty fields: %v", v.Fields)
	}
}

func TestWhenGatesCopy(t *testing.T) {
	p, _ := New("h", map[string]any{
		"copy": map[string]any{"X-Spam-Flag": "spam"},
		"when": "subject contains \"offer\"",
	}, component.Deps{})
	env := core.NewEnvelope(core.Message{
		Subject: "regular mail",
		Headers: map[string]string{"X-Spam-Flag": "YES"},
	})
	v, _ := p.Process(context.Background(), env)
	if len(v.Fields) != 0 {
		t.Errorf("when-gated copy must skip non-matching messages: %v", v.Fields)
	}
}

func TestEmptyCopyRejected(t *testing.T) {
	if _, err := New("h", map[string]any{}, component.Deps{}); err == nil {
		t.Fatal("missing copy map must fail")
	}
}

func TestDottedFieldRejected(t *testing.T) {
	if _, err := New("h", map[string]any{
		"copy": map[string]any{"List-Id": "my.list"},
	}, component.Deps{}); err == nil {
		t.Fatal("dotted field names must fail — selectors index bare names")
	}
}

func TestBadWhenRejected(t *testing.T) {
	if _, err := New("h", map[string]any{
		"copy": map[string]any{"List-Id": "list"},
		"when": "(((",
	}, component.Deps{}); err == nil {
		t.Fatal("broken selector must fail at construction")
	}
}
