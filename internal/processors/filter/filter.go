// Package filter is the built-in rule processor. It matches the envelope
// against a `when` selector and applies a configured disposal: drop the
// message, pass it through, or rewrite parts of it. Config-only rules cover
// the majority of what custom WASM plugins used to be written for.
package filter

import (
	"context"
	"fmt"
	"time"

	"github.com/elysola/mailer/internal/component"
	"github.com/elysola/mailer/internal/core"
	"github.com/elysola/mailer/internal/selector"
)

func init() {
	component.RegisterProcessor("filter", New)
}

type Config struct {
	// When is the selector expression. An empty when matches everything,
	// which makes filter usable as a pure rewriting stage.
	When string `yaml:"when"`
	// Action is drop / pass / rewrite. Default rewrite.
	Action string `yaml:"action"`
	// Rewrite outputs. Templates are NOT expanded here — they are literal
	// strings, rendered later by the pipeline or a notifier as-is.
	Title  string            `yaml:"title"`
	Body   string            `yaml:"body"`
	Fields map[string]string `yaml:"fields"`
	Route  []string          `yaml:"notifiers"`
	// DeferFor re-delivers the message after this delay when action is defer.
	DeferFor time.Duration `yaml:"defer_for"`
}

type Processor struct {
	name string
	cfg  Config
}

func New(name string, raw map[string]any, deps component.Deps) (core.Processor, error) {
	var cfg Config
	if err := component.Decode(component.ExpandMap(raw), &cfg); err != nil {
		return nil, fmt.Errorf("filter processor %q: %w", name, err)
	}
	switch cfg.Action {
	case "", "rewrite", "drop", "pass", "defer":
	default:
		return nil, fmt.Errorf("filter processor %q: unknown action %q (want drop/pass/rewrite/defer)", name, cfg.Action)
	}
	if cfg.Action == "" {
		cfg.Action = "rewrite"
	}
	if cfg.When != "" {
		if err := selector.CheckSelector(cfg.When); err != nil {
			return nil, fmt.Errorf("filter processor %q: bad when: %w", name, err)
		}
	}
	return &Processor{name: name, cfg: cfg}, nil
}

func (p *Processor) Name() string                    { return p.name }
func (p *Processor) Type() string                    { return "filter" }
func (p *Processor) Start(ctx context.Context) error { return nil }
func (p *Processor) Stop(ctx context.Context) error  { return nil }

func (p *Processor) Process(ctx context.Context, env core.Envelope) (core.Verdict, error) {
	if p.cfg.When != "" && !selector.MatchSelector(p.cfg.When, env) {
		return core.Verdict{}, nil
	}
	switch p.cfg.Action {
	case "drop":
		return core.Verdict{Action: core.ActionDrop}, nil
	case "pass":
		// A match that explicitly means "nothing changes". Useful with `when`
		// as a documented exception before a broader drop rule.
		return core.Verdict{}, nil
	case "defer":
		return core.Verdict{Action: core.ActionDefer, DeferUntil: time.Now().Add(p.cfg.DeferFor)}, nil
	}
	return core.Verdict{
		Title:  p.cfg.Title,
		Body:   p.cfg.Body,
		Fields: p.cfg.Fields,
		Route:  p.cfg.Route,
	}, nil
}
