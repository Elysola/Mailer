// Package templateproc is the template processor: it re-renders the
// notification title/body mid-pipeline, after extraction processors have
// filled Fields. Where route-level templates format output, this processor
// exists so different processor chains can shape text before routing
// decisions — e.g. an OTP template that only makes sense when a code was
// extracted.
//
//   - name: otp-format
//     type: template
//     when: has(fields.code)
//     title: "验证码 {{.Fields.code}}"
//     body: "来自 {{.Message.From}} 的验证码：{{.Fields.code}}"
package templateproc

import (
	"bytes"
	"context"
	"fmt"
	"text/template"

	"github.com/elysola/mailer/internal/component"
	"github.com/elysola/mailer/internal/core"
	"github.com/elysola/mailer/internal/selector"
)

func init() {
	component.RegisterProcessor("template", New)
}

type Config struct {
	When  string `yaml:"when"`
	Title string `yaml:"title"`
	Body  string `yaml:"body"`
}

type Processor struct {
	name  string
	when  string
	title *template.Template
	body  *template.Template
}

// New is the component factory for `type: template`.
func New(name string, raw map[string]any, deps component.Deps) (core.Processor, error) {
	var cfg Config
	if err := component.Decode(component.ExpandMap(raw), &cfg); err != nil {
		return nil, fmt.Errorf("template processor %q: %w", name, err)
	}
	if cfg.Title == "" && cfg.Body == "" {
		return nil, fmt.Errorf("template processor %q: title or body is required", name)
	}
	if cfg.When != "" {
		if err := selector.CheckSelector(cfg.When); err != nil {
			return nil, fmt.Errorf("template processor %q: bad when: %w", name, err)
		}
	}
	// text/template is correct here as well: notification bodies are plain
	// text on every notifier platform mailer ships; html/template would
	// entity-escape apostrophes and CJK punctuation into noise.
	p := &Processor{name: name, when: cfg.When}
	var err error
	if cfg.Title != "" {
		if p.title, err = template.New("title").Parse(cfg.Title); err != nil {
			return nil, fmt.Errorf("template processor %q: bad title template: %w", name, err)
		}
	}
	if cfg.Body != "" {
		if p.body, err = template.New("body").Parse(cfg.Body); err != nil {
			return nil, fmt.Errorf("template processor %q: bad body template: %w", name, err)
		}
	}
	return p, nil
}

func (p *Processor) Name() string { return p.name }
func (p *Processor) Type() string { return "template" }

func (p *Processor) Start(context.Context) error { return nil }
func (p *Processor) Stop(context.Context) error  { return nil }

func (p *Processor) Process(ctx context.Context, env core.Envelope) (core.Verdict, error) {
	if p.when != "" && !selector.MatchSelector(p.when, env) {
		return core.Verdict{}, nil
	}
	v := core.Verdict{}
	if p.title != nil {
		var buf bytes.Buffer
		if err := p.title.Execute(&buf, env); err != nil {
			return core.Verdict{}, fmt.Errorf("title template: %w", err)
		}
		v.Title = buf.String()
	}
	if p.body != nil {
		var buf bytes.Buffer
		if err := p.body.Execute(&buf, env); err != nil {
			return core.Verdict{}, fmt.Errorf("body template: %w", err)
		}
		v.Body = buf.String()
	}
	return v, nil
}
