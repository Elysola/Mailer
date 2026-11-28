package wasm

import (
	"context"
	"fmt"
	"strconv"

	"github.com/elysola/mailer/internal/component"
	"github.com/elysola/mailer/internal/core"
)

func init() {
	component.RegisterProcessor("wasm", New)
}

type Config struct {
	// Dir is the plugin directory (flat or per-plugin-folder layout, see the
	// engine docs). Exactly one of dir/plugin must be set.
	Dir string `yaml:"dir"`
	// ManifestPath + Entrypoint can replace dir for a single-plugin instance.
	ManifestPath string `yaml:"manifest"`
	// Defaults become the fallback limits for every plugin in the engine;
	// per-plugin manifest limits still win.
	Defaults Limits `yaml:"limits"`
}

// Processor adapts a WASM plugin Engine to the core.Processor contract. The
// engine itself is unchanged ABI v1: the pipeline's Verdict semantics are
// layered on top here.
type Processor struct {
	name   string
	engine *Engine
}

func New(name string, raw map[string]any, deps component.Deps) (core.Processor, error) {
	var cfg Config
	if err := component.Decode(component.ExpandMap(raw), &cfg); err != nil {
		return nil, fmt.Errorf("wasm processor %q: %w", name, err)
	}
	dir := cfg.Dir
	if dir == "" {
		dir = cfg.ManifestPath
	}
	if dir == "" {
		return nil, fmt.Errorf("wasm processor %q: dir is required", name)
	}
	engine, err := LoadDir(dir, cfg.Defaults)
	if err != nil {
		return nil, fmt.Errorf("wasm processor %q: %w", name, err)
	}
	if len(engine.Names()) == 0 {
		return nil, fmt.Errorf("wasm processor %q: no plugins found in %s", name, dir)
	}
	return &Processor{name: name, engine: engine}, nil
}

func (p *Processor) Name() string                    { return p.name }
func (p *Processor) Type() string                    { return "wasm" }
func (p *Processor) Start(ctx context.Context) error { return nil }
func (p *Processor) Stop(ctx context.Context) error  { return nil }

// Process runs the ABI v1 plugin chain and translates its first-match result
// into a Verdict. The envelope is not mutated here — the pipeline applies the
// verdict, so every mutation passes through one place.
func (p *Processor) Process(ctx context.Context, env core.Envelope) (core.Verdict, error) {
	m := env.Message
	var uid uint32
	if u, err := strconv.ParseUint(m.Ref["uid"], 10, 32); err == nil {
		uid = uint32(u)
	}
	result, err := p.engine.Process(ctx, Mail{
		Account:     m.Account,
		UID:         uid,
		MessageID:   m.ID,
		From:        m.From,
		To:          m.To,
		Cc:          m.Cc,
		Subject:     m.Subject,
		Date:        m.Date,
		Text:        m.Text,
		HTML:        m.HTML,
		Preview:     m.Preview,
		Headers:     m.Headers,
		Attachments: toPluginAttachments(m.Attachments),
	})
	if err != nil {
		return core.Verdict{}, err
	}
	if !result.Matched {
		return core.Verdict{}, nil
	}
	v := core.Verdict{
		Title:  result.Title,
		Body:   result.Text,
		Fields: result.Fields,
		Route:  result.Notifiers,
	}
	if len(v.Fields) == 0 && v.Title == "" && v.Body == "" && len(v.Route) == 0 {
		// A plugin matched but produced nothing at all: there is no verdict
		// worth applying, so treat it as no match rather than a pointless
		// field-conflict no-op.
		return core.Verdict{}, nil
	}
	return v, nil
}

// toPluginAttachments converts attachment metadata for the plugin ABI, which
// is a separate type so the sandbox contract can evolve without dragging
// core's internals into it.
func toPluginAttachments(atts []core.Attachment) []Attachment {
	if len(atts) == 0 {
		return nil
	}
	out := make([]Attachment, 0, len(atts))
	for _, a := range atts {
		out = append(out, Attachment{
			Filename:    a.Filename,
			ContentType: a.ContentType,
			Size:        a.Size,
			ContentID:   a.ContentID,
			Inline:      a.Inline,
		})
	}
	return out
}
