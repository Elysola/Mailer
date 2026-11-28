package llm

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/elysola/mailer/internal/component"
	"github.com/elysola/mailer/internal/core"
	"github.com/elysola/mailer/internal/metricsx"
	"github.com/elysola/mailer/internal/store/sqlite"
)

// fakeLLM is a programmable core.LLM: it returns scripted responses and
// counts calls so cache behaviour is directly observable.
type fakeLLM struct {
	calls atomic.Int64
	reply string
	err   error
}

func (f *fakeLLM) Name() string             { return "fake" }
func (f *fakeLLM) Type() string             { return "fake" }
func (f *fakeLLM) Model() string            { return "fake-1" }
func (f *fakeLLM) Start(context.Context) error { return nil }
func (f *fakeLLM) Stop(context.Context) error  { return nil }

func (f *fakeLLM) Complete(ctx context.Context, req core.CompletionRequest) (core.CompletionResponse, error) {
	f.calls.Add(1)
	if f.err != nil {
		return core.CompletionResponse{}, f.err
	}
	return core.CompletionResponse{Text: f.reply}, nil
}

func testDeps(t *testing.T, llm core.LLM) component.Deps {
	t.Helper()
	st, err := sqlite.Load(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return component.Deps{
		Store:   st,
		Metrics: metricsx.NewMetrics(),
		LLMs:    map[string]core.LLM{"fake": llm},
	}
}

func testEnv() core.Envelope {
	env := core.NewEnvelope(core.Message{
		From:    "billing@vendor.example",
		Subject: "Your invoice",
		Preview: "Amount due: 42.00 EUR by Friday",
		Source:  "work",
	})
	return env
}

func build(t *testing.T, factory func(string, map[string]any, component.Deps) (core.Processor, error), name string, raw map[string]any, deps component.Deps) core.Processor {
	t.Helper()
	p, err := factory(name, raw, deps)
	if err != nil {
		t.Fatalf("build %s: %v", name, err)
	}
	return p
}

func TestSummarizeReplacesBody(t *testing.T) {
	fake := &fakeLLM{reply: "账单 42 欧元，周五到期。"}
	p := build(t, NewSummarize, "sum", map[string]any{"llm": "fake"}, testDeps(t, fake))
	v, err := p.Process(context.Background(), testEnv())
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if v.Body != fake.reply {
		t.Errorf("body = %q, want %q", v.Body, fake.reply)
	}
}

func TestSummarizeFieldsOutput(t *testing.T) {
	fake := &fakeLLM{reply: "summary text"}
	p := build(t, NewSummarize, "sum", map[string]any{"llm": "fake", "output": "fields"}, testDeps(t, fake))
	v, err := p.Process(context.Background(), testEnv())
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if v.Body != "" {
		t.Error("fields output must not touch the body")
	}
	if v.Fields["summary"] != "summary text" {
		t.Errorf("fields.summary = %q", v.Fields["summary"])
	}
}

func TestCacheSkipsSecondCall(t *testing.T) {
	fake := &fakeLLM{reply: "cached"}
	deps := testDeps(t, fake)
	p := build(t, NewSummarize, "sum", map[string]any{"llm": "fake", "cache": true}, deps)
	for i := 0; i < 3; i++ {
		if _, err := p.Process(context.Background(), testEnv()); err != nil {
			t.Fatalf("Process: %v", err)
		}
	}
	if got := fake.calls.Load(); got != 1 {
		t.Errorf("llm calls = %d, want 1 (cache must absorb repeats)", got)
	}
}

func TestWhenGateSkipsAtZeroCost(t *testing.T) {
	fake := &fakeLLM{reply: "x"}
	p := build(t, NewSummarize, "sum", map[string]any{
		"llm":  "fake",
		"when": `fields.class == "bill"`,
	}, testDeps(t, fake))
	v, err := p.Process(context.Background(), testEnv())
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if v.Action != "" || v.Body != "" {
		t.Errorf("non-matching when must produce an empty verdict, got %+v", v)
	}
	if fake.calls.Load() != 0 {
		t.Error("when gate must skip without calling the LLM")
	}
	// Matching messages do call it.
	env := testEnv()
	env.Fields["class"] = "bill"
	if _, err := p.Process(context.Background(), env); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if fake.calls.Load() != 1 {
		t.Error("matching when must call the LLM")
	}
}

func TestOnErrorSemantics(t *testing.T) {
	broken := &fakeLLM{err: errors.New("provider down")}

	// passthrough: zero verdict, pipeline continues with original text.
	p := build(t, NewSummarize, "s1", map[string]any{"llm": "fake"}, testDeps(t, broken))
	v, err := p.Process(context.Background(), testEnv())
	if err != nil || v.Action != "" {
		t.Errorf("passthrough: verdict=%+v err=%v", v, err)
	}

	// drop: message discarded.
	p = build(t, NewSummarize, "s2", map[string]any{"llm": "fake", "on_error": "drop"}, testDeps(t, broken))
	v, _ = p.Process(context.Background(), testEnv())
	if v.Action != core.ActionDrop {
		t.Errorf("drop: action = %q", v.Action)
	}

	// fail: retry the whole delivery.
	p = build(t, NewSummarize, "s3", map[string]any{"llm": "fake", "on_error": "fail"}, testDeps(t, broken))
	v, _ = p.Process(context.Background(), testEnv())
	if v.Action != core.ActionFail {
		t.Errorf("fail: action = %q", v.Action)
	}
}

func TestClassifyProducesField(t *testing.T) {
	fake := &fakeLLM{reply: "bill"}
	p := build(t, NewClassify, "cls", map[string]any{
		"llm":     "fake",
		"classes": []any{"bill", "notice", "other"},
	}, testDeps(t, fake))
	v, err := p.Process(context.Background(), testEnv())
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if v.Fields["class"] != "bill" {
		t.Errorf("class = %q", v.Fields["class"])
	}
}

func TestClassifyRejectsUnknownLabel(t *testing.T) {
	// A hallucinated label must not leak into routing: better unclassified
	// than misrouted.
	fake := &fakeLLM{reply: "invoices-are-cool"}
	p := build(t, NewClassify, "cls", map[string]any{
		"llm":     "fake",
		"classes": []any{"bill", "notice", "other"},
	}, testDeps(t, fake))
	v, _ := p.Process(context.Background(), testEnv())
	if _, ok := v.Fields["class"]; ok {
		t.Errorf("unknown label must be dropped, got %q", v.Fields["class"])
	}
}

func TestClassifyToleratesChattyModel(t *testing.T) {
	fake := &fakeLLM{reply: "Bill."}
	p := build(t, NewClassify, "cls", map[string]any{
		"llm":     "fake",
		"classes": []any{"bill", "other"},
	}, testDeps(t, fake))
	v, _ := p.Process(context.Background(), testEnv())
	if v.Fields["class"] != "bill" {
		t.Errorf("chatty reply should still classify, got %q", v.Fields["class"])
	}
}

func TestExtractMergesFields(t *testing.T) {
	fake := &fakeLLM{reply: `{"amount": 42.0, "currency": "EUR", "vendor": "ACME", "missing": null}`}
	p := build(t, NewExtract, "ex", map[string]any{
		"llm":    "fake",
		"prefix": "inv_",
		"schema": map[string]any{"type": "object"},
	}, testDeps(t, fake))
	v, err := p.Process(context.Background(), testEnv())
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if v.Fields["inv_amount"] != "42" {
		t.Errorf("inv_amount = %q", v.Fields["inv_amount"])
	}
	if v.Fields["inv_vendor"] != "ACME" {
		t.Errorf("inv_vendor = %q", v.Fields["inv_vendor"])
	}
	if _, ok := v.Fields["inv_missing"]; ok {
		t.Error("null fields must not be merged")
	}
}

func TestExtractToleratesMarkdownFences(t *testing.T) {
	fake := &fakeLLM{reply: "Here you go:\n```json\n{\"vendor\": \"ACME\"}\n```\nDone."}
	p := build(t, NewExtract, "ex", map[string]any{
		"llm":    "fake",
		"schema": map[string]any{"type": "object"},
	}, testDeps(t, fake))
	v, _ := p.Process(context.Background(), testEnv())
	if v.Fields["vendor"] != "ACME" {
		t.Errorf("vendor = %q", v.Fields["vendor"])
	}
}

func TestExtractGarbageHonorsOnError(t *testing.T) {
	fake := &fakeLLM{reply: "I cannot help with that."}
	p := build(t, NewExtract, "ex", map[string]any{
		"llm":      "fake",
		"schema":   map[string]any{"type": "object"},
		"on_error": "drop",
	}, testDeps(t, fake))
	v, _ := p.Process(context.Background(), testEnv())
	if v.Action != core.ActionDrop {
		t.Errorf("unparseable extraction with on_error=drop must drop, got %q", v.Action)
	}
}

func TestUnknownLLMInstanceRejected(t *testing.T) {
	deps := testDeps(t, &fakeLLM{})
	if _, err := NewSummarize("sum", map[string]any{"llm": "nonexistent"}, deps); err == nil {
		t.Fatal("unknown llm reference must fail at construction")
	}
}

func TestMaxInputRunesTrims(t *testing.T) {
	fake := &fakeLLM{reply: "ok"}
	p := build(t, NewSummarize, "sum", map[string]any{"llm": "fake", "max_input_runes": 10}, testDeps(t, fake))
	env := testEnv()
	env.Message.Preview = "a very long preview that definitely exceeds ten runes"
	if _, err := p.Process(context.Background(), env); err != nil {
		t.Fatalf("Process: %v", err)
	}
	// Indirectly verified: trimming must not break the pipeline. Direct
	// verification of the prompt contents is internal; the trimRunes unit
	// test below covers the mechanics.
	if fake.calls.Load() != 1 {
		t.Error("llm should still be called once")
	}
}

func TestTrimRunes(t *testing.T) {
	if got := trimRunes("hello", 10); got != "hello" {
		t.Errorf("short input must pass through, got %q", got)
	}
	got := trimRunes("hello world", 5)
	if len([]rune(got)) != 6 || got[:5] != "hello" {
		t.Errorf("trimRunes = %q", got)
	}
	// Multibyte safety: never split a rune.
	s := fmt.Sprintf("%s", "你好世界你好")
	got = trimRunes(s, 3)
	if got[:len(got)-len("…")] != "你好世" {
		t.Errorf("multibyte trim = %q", got)
	}
}
