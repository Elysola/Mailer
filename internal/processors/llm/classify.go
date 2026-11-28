package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/elysola/mailer/internal/component"
	"github.com/elysola/mailer/internal/core"
)

func init() {
	component.RegisterProcessor("llmclassify", NewClassify)
}

type classifyConfig struct {
	BaseConfig `yaml:",inline"`
	// Classes is the label vocabulary the model must choose from. Required —
	// an open-ended label set makes routing unpredictable.
	Classes []string `yaml:"classes"`
	// Field is the envelope field the winning class is written to.
	// Default "class".
	Field string `yaml:"field"`
}

type classifier struct {
	*base
	cfg classifyConfig
}

func NewClassify(name string, raw map[string]any, deps component.Deps) (core.Processor, error) {
	var cfg classifyConfig
	if err := component.Decode(component.ExpandMap(raw), &cfg); err != nil {
		return nil, fmt.Errorf("llmclassify %q: %w", name, err)
	}
	if err := cfg.BaseConfig.normalize(name); err != nil {
		return nil, err
	}
	if len(cfg.Classes) == 0 {
		return nil, fmt.Errorf("llmclassify %q: classes is required", name)
	}
	if cfg.Field == "" {
		cfg.Field = "class"
	}
	if cfg.Prompt == "" {
		cfg.Prompt = defaultClassifyPrompt
	}
	b, err := newBase(name, cfg.BaseConfig, deps)
	if err != nil {
		return nil, err
	}
	return &classifier{base: b, cfg: cfg}, nil
}

const defaultClassifyPrompt = `把这封邮件分类为 {{.Classes}} 之一。只输出类别名,不要输出任何其他内容。不确定时输出 other。

{{.Text}}`

func (c *classifier) Name() string                    { return c.name }
func (c *classifier) Type() string                    { return "llmclassify" }
func (c *classifier) Start(ctx context.Context) error { return nil }
func (c *classifier) Stop(ctx context.Context) error  { return nil }

func (c *classifier) Process(ctx context.Context, env core.Envelope) (core.Verdict, error) {
	// Substitute the class vocabulary into the prompt before the shared
	// input substitution runs; the two template vocabularies do not overlap.
	prompt := strings.ReplaceAll(c.cfg.Prompt, "{{.Classes}}", strings.Join(c.cfg.Classes, "/"))
	text, _, v, err := c.complete(ctx, env, "classify-v1:"+strings.Join(c.cfg.Classes, ","), prompt, nil, 16)
	if err == errSkip {
		return core.Verdict{}, nil
	}
	if err != nil {
		return core.Verdict{}, err
	}
	if v.Action != "" {
		return v, nil
	}
	class := strings.TrimSpace(strings.ToLower(text))
	// The model occasionally answers with a sentence despite instructions;
	// accept the first word when it matches a known class, and fall back to
	// scanning the whole reply. A class we do not recognise is dropped rather
	// than trusted — an arbitrary label would poison routing rules.
	if idx := strings.IndexAny(class, " \n\t.,;"); idx >= 0 {
		if c.known(class[:idx]) {
			class = class[:idx]
		}
	}
	if !c.known(class) {
		for _, candidate := range c.cfg.Classes {
			if strings.Contains(class, strings.ToLower(candidate)) {
				class = strings.ToLower(candidate)
				break
			}
		}
	}
	if !c.known(class) {
		return core.Verdict{}, nil
	}
	return core.Verdict{Fields: map[string]string{c.cfg.Field: class}}, nil
}

func (c *classifier) known(class string) bool {
	for _, candidate := range c.cfg.Classes {
		if strings.EqualFold(candidate, class) {
			return true
		}
	}
	return false
}

// extractJSON pulls the first balanced JSON object out of a model response.
// Models frequently wrap JSON in prose or markdown fences even when asked
// not to; structured-output APIs make this rare, but the plain-text path
// (cache hits, non-strict endpoints) still needs it.
func extractJSON(s string) (map[string]any, error) {
	s = strings.TrimSpace(s)
	// Strip markdown fences when present.
	if idx := strings.Index(s, "```"); idx >= 0 {
		rest := s[idx+3:]
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			rest = rest[nl+1:]
		}
		if end := strings.Index(rest, "```"); end >= 0 {
			s = strings.TrimSpace(rest[:end])
		}
	}
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return nil, fmt.Errorf("no JSON object in response")
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		ch := s[i]
		switch {
		case escaped:
			escaped = false
		case ch == '\\' && inString:
			escaped = true
		case ch == '"':
			inString = !inString
		case inString:
		case ch == '{':
			depth++
		case ch == '}':
			depth--
			if depth == 0 {
				var out map[string]any
				if err := json.Unmarshal([]byte(s[start:i+1]), &out); err != nil {
					return nil, err
				}
				return out, nil
			}
		}
	}
	return nil, fmt.Errorf("unbalanced JSON in response")
}
