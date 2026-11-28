package llm

import (
	"context"
	"fmt"
	"strings"

	"github.com/elysola/mailer/internal/component"
	"github.com/elysola/mailer/internal/core"
)

func init() {
	component.RegisterProcessor("llmsummarize", NewSummarize)
}

const defaultSummarizePrompt = `用三句话以内总结这封邮件,保留金额、日期、行动要求和发件人意图。直接输出总结,不要解释。

{{.Text}}`

type summarizeConfig struct {
	BaseConfig `yaml:",inline"`
	// Output selects where the summary lands: "body" replaces the
	// notification text; "fields" writes fields.summary and leaves the body
	// alone. Default body.
	Output string `yaml:"output"`
}

type summarizer struct {
	*base
	cfg summarizeConfig
}

func NewSummarize(name string, raw map[string]any, deps component.Deps) (core.Processor, error) {
	var cfg summarizeConfig
	if err := component.Decode(component.ExpandMap(raw), &cfg); err != nil {
		return nil, fmt.Errorf("llmsummarize %q: %w", name, err)
	}
	if err := cfg.BaseConfig.normalize(name); err != nil {
		return nil, err
	}
	switch cfg.Output {
	case "":
		cfg.Output = "body"
	case "body", "fields":
	default:
		return nil, fmt.Errorf("llmsummarize %q: unknown output %q (want body|fields)", name, cfg.Output)
	}
	if cfg.Prompt == "" {
		cfg.Prompt = defaultSummarizePrompt
	}
	b, err := newBase(name, cfg.BaseConfig, deps)
	if err != nil {
		return nil, err
	}
	return &summarizer{base: b, cfg: cfg}, nil
}

func (s *summarizer) Name() string                    { return s.name }
func (s *summarizer) Type() string                    { return "llmsummarize" }
func (s *summarizer) Start(ctx context.Context) error { return nil }
func (s *summarizer) Stop(ctx context.Context) error  { return nil }

func (s *summarizer) Process(ctx context.Context, env core.Envelope) (core.Verdict, error) {
	text, _, v, err := s.complete(ctx, env, "summarize-v1", s.cfg.Prompt, nil, 512)
	if err == errSkip {
		return core.Verdict{}, nil
	}
	if err != nil {
		return core.Verdict{}, err
	}
	// v is non-zero only when the LLM call failed and on_error chose a
	// disposal; passthrough yields the zero verdict and we are done.
	if v.Action != "" {
		return v, nil
	}
	summary := strings.TrimSpace(text)
	if summary == "" {
		return core.Verdict{}, nil
	}
	if s.cfg.Output == "fields" {
		return core.Verdict{Fields: map[string]string{"summary": summary}}, nil
	}
	return core.Verdict{Body: summary}, nil
}
