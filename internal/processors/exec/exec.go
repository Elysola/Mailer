// Package exec is the external-command processor: it pipes the envelope to a
// local program as JSON on stdin and reads a verdict JSON document from
// stdout. It is the escape hatch for languages the WASM toolchain does not
// cover and for quick one-off scripts, at the cost of a process fork per
// message.
package exec

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"github.com/elysola/mailer/internal/component"
	"github.com/elysola/mailer/internal/core"
)

func init() {
	component.RegisterProcessor("exec", New)
}

const (
	defaultTimeout   = 5 * time.Second
	defaultMaxOutput = 64 * 1024
)

type Config struct {
	Command   []string      `yaml:"command"`
	Timeout   time.Duration `yaml:"timeout"`
	MaxOutput int           `yaml:"max_output"`
}

type Processor struct {
	name string
	cfg  Config
}

func New(name string, raw map[string]any, deps component.Deps) (core.Processor, error) {
	var cfg Config
	if err := component.Decode(component.ExpandMap(raw), &cfg); err != nil {
		return nil, fmt.Errorf("exec processor %q: %w", name, err)
	}
	if len(cfg.Command) == 0 {
		return nil, fmt.Errorf("exec processor %q: command is required", name)
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.MaxOutput <= 0 {
		cfg.MaxOutput = defaultMaxOutput
	}
	if _, err := exec.LookPath(cfg.Command[0]); err != nil {
		return nil, fmt.Errorf("exec processor %q: %w", name, err)
	}
	return &Processor{name: name, cfg: cfg}, nil
}

func (p *Processor) Name() string { return p.name }
func (p *Processor) Type() string { return "exec" }
func (p *Processor) Start(ctx context.Context) error { return nil }
func (p *Processor) Stop(ctx context.Context) error  { return nil }

// verdictOut mirrors core.Verdict in wire form. It is declared separately so
// the JSON contract is stable even if the Go type evolves.
type verdictOut struct {
	Action     string            `json:"action,omitempty"`
	Title      string            `json:"title,omitempty"`
	Body       string            `json:"body,omitempty"`
	Fields     map[string]string `json:"fields,omitempty"`
	Route      []string          `json:"route,omitempty"`
	DeferAfter string            `json:"defer_after,omitempty"` // Go duration, e.g. "15m"
}

func (p *Processor) Process(ctx context.Context, env core.Envelope) (core.Verdict, error) {
	input, err := json.Marshal(env)
	if err != nil {
		return core.Verdict{}, fmt.Errorf("marshal input: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, p.cfg.Command[0], p.cfg.Command[1:]...)
	cmd.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return core.Verdict{}, fmt.Errorf("timeout after %s", p.cfg.Timeout)
		}
		tail := stderr.String()
		if len(tail) > 300 {
			tail = tail[:300] + "…"
		}
		return core.Verdict{}, fmt.Errorf("exit: %w: %s", err, tail)
	}

	out := stdout.Bytes()
	if len(out) == 0 {
		// Empty output means "no opinion": the common case for a filter
		// script that only occasionally rewrites.
		return core.Verdict{}, nil
	}
	if len(out) > p.cfg.MaxOutput {
		return core.Verdict{}, fmt.Errorf("output exceeds limit (%d > %d)", len(out), p.cfg.MaxOutput)
	}
	var v verdictOut
	if err := json.Unmarshal(out, &v); err != nil {
		return core.Verdict{}, fmt.Errorf("decode output: %w", err)
	}
	verdict := core.Verdict{
		Title:  v.Title,
		Body:   v.Body,
		Fields: v.Fields,
		Route:  v.Route,
	}
	switch v.Action {
	case "drop":
		verdict.Action = core.ActionDrop
	case "defer":
		verdict.Action = core.ActionDefer
		if v.DeferAfter != "" {
			d, err := time.ParseDuration(v.DeferAfter)
			if err != nil {
				return core.Verdict{}, fmt.Errorf("bad defer_after %q: %w", v.DeferAfter, err)
			}
			verdict.DeferUntil = time.Now().Add(d)
		}
	}
	return verdict, nil
}
