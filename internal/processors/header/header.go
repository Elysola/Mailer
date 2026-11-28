// Package header copies selected message headers into envelope fields so
// routes and later processors can match on them. Mailing lists are the
// classic case: List-Id is the reliable routing key where Subject prefixes
// and From addresses lie. Headers stay out of fields by default because
// most of them are noise — this processor is the explicit opt-in.
package header

import (
	"context"
	"fmt"
	"strings"

	"github.com/elysola/mailer/internal/component"
	"github.com/elysola/mailer/internal/core"
	"github.com/elysola/mailer/internal/selector"
)

func init() {
	component.RegisterProcessor("header", New)
}

type Config struct {
	// Copy maps a header name to the field it becomes. Keys are matched
	// case-insensitively, as RFC 5322 requires.
	Copy map[string]string `yaml:"copy"`
	// Prefix prepends every copied field name, keeping extraction output
	// namespaced the same way llmextract does.
	Prefix string `yaml:"prefix"`
	// When gates the copy — copying spam headers only for spam means the
	// fields never exist elsewhere, which selectors treat as no-match.
	When string `yaml:"when"`
}

type Processor struct {
	name string
	cfg  Config
}

// New is the component factory for `type: header`.
func New(name string, raw map[string]any, deps component.Deps) (core.Processor, error) {
	var cfg Config
	if err := component.Decode(component.ExpandMap(raw), &cfg); err != nil {
		return nil, fmt.Errorf("header processor %q: %w", name, err)
	}
	if len(cfg.Copy) == 0 {
		return nil, fmt.Errorf("header processor %q: copy is required — name the headers you need", name)
	}
	for h, f := range cfg.Copy {
		if strings.TrimSpace(h) == "" || strings.TrimSpace(f) == "" {
			return nil, fmt.Errorf("header processor %q: copy entries need both a header and a field name", name)
		}
		if strings.ContainsAny(f, ". ") {
			return nil, fmt.Errorf("header processor %q: field name %q must not contain dots or spaces — selectors index fields by bare name", name, f)
		}
	}
	if cfg.When != "" {
		if err := selector.CheckSelector(cfg.When); err != nil {
			return nil, fmt.Errorf("header processor %q: when: %w", name, err)
		}
	}
	return &Processor{name: name, cfg: cfg}, nil
}

func (p *Processor) Name() string                { return p.name }
func (p *Processor) Type() string                { return "header" }
func (p *Processor) Start(context.Context) error { return nil }
func (p *Processor) Stop(context.Context) error  { return nil }

func (p *Processor) Process(ctx context.Context, env core.Envelope) (core.Verdict, error) {
	if p.cfg.When != "" && !selector.MatchSelector(p.cfg.When, env) {
		return core.Verdict{}, nil
	}
	fields := map[string]string{}
	for hdr, field := range p.cfg.Copy {
		// Headers in the envelope keep their canonical MIME case; compare
		// case-insensitively because mail servers rewrite casing freely.
		for k, v := range env.Message.Headers {
			if strings.EqualFold(k, hdr) {
				if v != "" {
					fields[p.cfg.Prefix+field] = v
				}
				break
			}
		}
	}
	if len(fields) == 0 {
		return core.Verdict{}, nil
	}
	return core.Verdict{Fields: fields}, nil
}
