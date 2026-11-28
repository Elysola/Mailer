// Package llm implements the built-in AI processors: llmsummarize,
// llmclassify and llmextract. They share a base that handles the concerns
// every AI stage needs — the `when` zero-cost gate, input trimming, result
// caching in the KV store, and the on_error policy — so each processor only
// carries its own prompt construction and output mapping.
//
// The LLM itself is a service component (core.LLM) referenced by name, so
// provider choice, concurrency and token accounting live in exactly one
// place; see docs/design/architecture-v2.md §2.7.
package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/elysola/mailer/internal/component"
	"github.com/elysola/mailer/internal/core"
	"github.com/elysola/mailer/internal/selector"
)

// BaseConfig is the configuration vocabulary shared by all AI processors.
type BaseConfig struct {
	// LLM names the llms: instance to use. Required.
	LLM string `yaml:"llm"`
	// When is a selector expression evaluated before any LLM call; a
	// non-matching message skips the processor at zero cost.
	When string `yaml:"when"`
	// Cache enables KV-backed result caching keyed by model+prompt+input.
	Cache bool `yaml:"cache"`
	// MaxInputRunes caps the text sent to the model. Default 4000.
	MaxInputRunes int `yaml:"max_input_runes"`
	// OnError is passthrough (default) | fail | drop.
	OnError string `yaml:"on_error"`
	// Prompt overrides the built-in prompt template.
	Prompt string `yaml:"prompt"`
}

func (c *BaseConfig) normalize(name string) error {
	if c.LLM == "" {
		return fmt.Errorf("llm processor %q: llm is required", name)
	}
	if c.MaxInputRunes <= 0 {
		c.MaxInputRunes = 4000
	}
	switch c.OnError {
	case "":
		c.OnError = "passthrough"
	case "passthrough", "fail", "drop":
	default:
		return fmt.Errorf("llm processor %q: unknown on_error %q", name, c.OnError)
	}
	if c.When != "" {
		if err := selector.CheckSelector(c.When); err != nil {
			return fmt.Errorf("llm processor %q: bad when: %w", name, err)
		}
	}
	return nil
}

// base carries the resolved dependencies every AI processor shares.
type base struct {
	name  string
	cfg   BaseConfig
	deps  component.Deps
	llm   core.LLM
	store core.KVStore
}

func newBase(name string, cfg BaseConfig, deps component.Deps) (*base, error) {
	llm, ok := deps.LLMs[cfg.LLM]
	if !ok {
		return nil, fmt.Errorf("llm processor %q: unknown llm instance %q", name, cfg.LLM)
	}
	return &base{name: name, cfg: cfg, deps: deps, llm: llm, store: deps.Store}, nil
}

// complete runs the shared lifecycle: when-gate, trim, cache lookup, LLM
// call, cache store. It returns the raw response text, whether the result
// came from cache, and the on_error verdict when the call failed.
func (b *base) complete(ctx context.Context, env core.Envelope, promptVersion, prompt string, schema map[string]any, maxTokens int) (text string, cached bool, v core.Verdict, err error) {
	if b.cfg.When != "" && !selector.MatchSelector(b.cfg.When, env) {
		return "", false, core.Verdict{}, errSkip
	}

	input := trimRunes(inputText(env), b.cfg.MaxInputRunes)
	body := strings.ReplaceAll(prompt, "{{.Text}}", input)
	body = strings.ReplaceAll(body, "{{.Subject}}", env.Message.Subject)
	body = strings.ReplaceAll(body, "{{.From}}", env.Message.From)

	cacheKey := ""
	if b.cfg.Cache && b.store != nil {
		cacheKey = b.cacheKey(promptVersion, body, schema)
		if cached, found, err := b.store.KVGet("llmcache", cacheKey); err == nil && found {
			return string(cached), true, core.Verdict{}, nil
		}
	}

	resp, callErr := b.llm.Complete(ctx, core.CompletionRequest{
		Prompt:     body,
		JSONSchema: schema,
		MaxTokens:  maxTokens,
	})
	if callErr != nil {
		return "", false, b.errorVerdict(callErr), nil
	}
	if b.cfg.Cache && b.store != nil && cacheKey != "" {
		// Cache TTL is 30 days: long enough that mailing-list duplicates and
		// retry storms never re-bill, short enough that model upgrades (which
		// change the key anyway) are not shadowed.
		if err := b.store.KVSet("llmcache", cacheKey, []byte(resp.Text), 30*24*time.Hour); err != nil {
			// A cache write failure must never fail the pipeline.
			_ = err
		}
	}
	return resp.Text, false, core.Verdict{}, nil
}

// errorVerdict maps on_error onto pipeline disposals.
func (b *base) errorVerdict(cause error) core.Verdict {
	switch b.cfg.OnError {
	case "drop":
		return core.Verdict{Action: core.ActionDrop}
	case "fail":
		return core.Verdict{Action: core.ActionFail}
	default:
		// passthrough: continue with the original text; the error is logged
		// by the pipeline's processor-error path only when we return one, so
		// log-free degradation is intentional — LLMs are unreliable services
		// and noise-free degradation is the point of the default.
		_ = cause
		return core.Verdict{}
	}
}

func (b *base) cacheKey(promptVersion, body string, schema map[string]any) string {
	h := sha256.New()
	h.Write([]byte(b.llm.Model()))
	h.Write([]byte{0})
	h.Write([]byte(promptVersion))
	h.Write([]byte{0})
	if schema != nil {
		if raw, err := json.Marshal(schema); err == nil {
			h.Write(raw)
		}
	}
	h.Write([]byte{0})
	h.Write([]byte(body))
	return hex.EncodeToString(h.Sum(nil))
}

// errSkip is an internal sentinel: the when-gate did not match. It is never
// returned to the pipeline; processors translate it into an empty verdict.
var errSkip = fmt.Errorf("llm: when gate did not match")

func inputText(env core.Envelope) string {
	// The preview is the cleaned body extract the source produced; processors
	// never see raw MIME. Subject is included because previews frequently
	// truncate away the point of short mails.
	var sb strings.Builder
	sb.WriteString("Subject: ")
	sb.WriteString(env.Message.Subject)
	sb.WriteString("\nFrom: ")
	sb.WriteString(env.Message.From)
	sb.WriteString("\n\n")
	sb.WriteString(env.Message.Preview)
	return sb.String()
}

func trimRunes(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max]) + "…"
}
