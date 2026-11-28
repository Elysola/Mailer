// Package throttle suppresses repeat notifications. A sender that fires the
// same alert every few minutes — a monitoring system, a cron job, a broken
// bot — would otherwise ring the user's phone every time. The throttle keys
// on a configurable selector projection (default: sender+subject) and
// drops duplicates inside the window; the first occurrence always passes,
// so nothing is lost — only the noise is.
//
// Unlike dedup (which keys on the immutable Message-ID and exists to make
// at-least-once delivery safe), throttle keys on content and exists to
// protect the human.
package throttle

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/elysola/mailer/internal/component"
	"github.com/elysola/mailer/internal/core"
	"github.com/elysola/mailer/internal/selector"
)

func init() {
	component.RegisterProcessor("throttle", New)
}

type Config struct {
	// Window is how long a repeat of the same key is suppressed.
	Window time.Duration `yaml:"window"`
	// Keys is the list of envelope values that form the repeat key, joined
	// by NUL. Default covers the classic repeated-alert shape.
	Keys []string `yaml:"keys"`
	// When gates the whole processor — untrottled subjects skip the check
	// entirely rather than consuming key space.
	When string `yaml:"when"`
}

type Processor struct {
	name string
	cfg  Config

	mu   sync.Mutex
	seen map[string]time.Time
}

// New is the component factory for `type: throttle`.
func New(name string, raw map[string]any, deps component.Deps) (core.Processor, error) {
	var cfg Config
	if err := component.Decode(component.ExpandMap(raw), &cfg); err != nil {
		return nil, fmt.Errorf("throttle processor %q: %w", name, err)
	}
	if cfg.Window <= 0 {
		cfg.Window = time.Hour
	}
	if len(cfg.Keys) == 0 {
		cfg.Keys = []string{"from", "subject"}
	}
	for _, k := range cfg.Keys {
		switch k {
		case "from", "subject", "account", "preview":
		default:
			return nil, fmt.Errorf("throttle processor %q: unknown key %q (want from|subject|account|preview)", name, k)
		}
	}
	if cfg.When != "" {
		if err := selector.CheckSelector(cfg.When); err != nil {
			return nil, fmt.Errorf("throttle processor %q: when: %w", name, err)
		}
	}
	return &Processor{name: name, cfg: cfg, seen: map[string]time.Time{}}, nil
}

func (p *Processor) Name() string                { return p.name }
func (p *Processor) Type() string                { return "throttle" }
func (p *Processor) Start(context.Context) error { return nil }
func (p *Processor) Stop(context.Context) error  { return nil }

func (p *Processor) key(env *core.Envelope) string {
	parts := make([]string, 0, len(p.cfg.Keys))
	for _, k := range p.cfg.Keys {
		switch k {
		case "from":
			parts = append(parts, env.Message.From)
		case "subject":
			parts = append(parts, env.Message.Subject)
		case "account":
			parts = append(parts, env.Message.Account)
		case "preview":
			parts = append(parts, env.Message.Preview)
		}
	}
	// NUL-join: printable parts cannot collide across combinations.
	out := ""
	for i, part := range parts {
		if i > 0 {
			out += "\x00"
		}
		out += part
	}
	return out
}

func (p *Processor) Process(ctx context.Context, env core.Envelope) (core.Verdict, error) {
	if p.cfg.When != "" && !selector.MatchSelector(p.cfg.When, env) {
		return core.Verdict{}, nil
	}
	now := time.Now()
	key := p.key(&env)

	p.mu.Lock()
	// Opportunistic GC: expire entries while we hold the lock anyway. The
	// map stays small because keys vanish with their window.
	for k, t := range p.seen {
		if now.Sub(t) > p.cfg.Window {
			delete(p.seen, k)
		}
	}
	last, dup := p.seen[key]
	if dup && now.Sub(last) < p.cfg.Window {
		p.mu.Unlock()
		log.Printf("[%s] throttled repeat from %q subject %q (window %s)", p.name, env.Message.From, env.Message.Subject, p.cfg.Window)
		return core.Verdict{
			Action: core.ActionDrop,
			Fields: map[string]string{"throttled": "true"},
		}, nil
	}
	p.seen[key] = now
	p.mu.Unlock()
	return core.Verdict{}, nil
}
