// Package set is the static-field processor: it stamps fixed key/value pairs
// onto matching envelopes. Its purpose is composability — a route `when`
// clause can match on fields nothing else extracted:
//
//   - name: tag-work
//     type: set
//     when: account == "work"
//     fields: { mailbox_type: work, priority: high }
package set

import (
	"context"
	"fmt"

	"github.com/elysola/mailer/internal/component"
	"github.com/elysola/mailer/internal/core"
	"github.com/elysola/mailer/internal/selector"
)

func init() {
	component.RegisterProcessor("set", New)
}

type Config struct {
	When   string            `yaml:"when"`
	Fields map[string]string `yaml:"fields"`
	// Drop, when true, discards matching messages. This makes `set` double
	// as the simplest possible filter — usually `filter` is clearer for
	// this, but having it here avoids an empty-fields edge case.
	Drop bool `yaml:"drop"`
}

type Processor struct {
	name   string
	when   string
	fields map[string]string
	drop   bool
}

// New is the component factory for `type: set`.
func New(name string, raw map[string]any, deps component.Deps) (core.Processor, error) {
	var cfg Config
	if err := component.Decode(component.ExpandMap(raw), &cfg); err != nil {
		return nil, fmt.Errorf("set processor %q: %w", name, err)
	}
	if len(cfg.Fields) == 0 && !cfg.Drop {
		return nil, fmt.Errorf("set processor %q: fields is required (or set drop: true)", name)
	}
	if cfg.When != "" {
		if err := selector.CheckSelector(cfg.When); err != nil {
			return nil, fmt.Errorf("set processor %q: bad when: %w", name, err)
		}
	}
	return &Processor{name: name, when: cfg.When, fields: cfg.Fields, drop: cfg.Drop}, nil
}

func (p *Processor) Name() string { return p.name }
func (p *Processor) Type() string { return "set" }

func (p *Processor) Start(context.Context) error { return nil }
func (p *Processor) Stop(context.Context) error  { return nil }

func (p *Processor) Process(ctx context.Context, env core.Envelope) (core.Verdict, error) {
	if p.when != "" && !selector.MatchSelector(p.when, env) {
		return core.Verdict{}, nil
	}
	v := core.Verdict{Fields: p.fields}
	if p.drop {
		v.Action = core.ActionDrop
	}
	return v, nil
}
