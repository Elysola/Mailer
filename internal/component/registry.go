// Package component implements the registry that makes mailer extensible:
// every source, notifier, processor and LLM provider registers a factory
// under a type string, and the daemon assembles instances from config without
// the core packages ever importing an implementation.
//
// Adding a new notifier is one directory plus one registration line:
//
//	func init() { component.RegisterNotifier("ntfy", New) }
//
// Nothing in core, pipeline, config or daemon changes.
package component

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/elysola/mailer/internal/core"
	"github.com/elysola/mailer/internal/metricsx"
	"gopkg.in/yaml.v3"
)

// Deps are the cross-cutting services factories may need. Not every factory
// uses every field; a nil field means the daemon does not offer it.
type Deps struct {
	Metrics *metricsx.Metrics
	Store   core.Store
	// Commander routes write-back commands (mark-as-read buttons) to the
	// source that produced the message. Available to notifiers.
	Commander Commander
	// LLMs resolves an LLM instance by config name, for AI processors.
	LLMs map[string]core.LLM
}

// Commander executes a command on the named source. It is implemented by the
// daemon after sources are wired (sources must exist before notifiers that
// reference them are built).
type Commander interface {
	Command(ctx context.Context, source string, cmd core.Command) error
}

// Factory builds one component instance from its free-form config block.
// name is the instance name; cfg is the raw YAML mapping (already excluding
// the consumed keys "name" and "type").
type Factory[C any] func(name string, cfg map[string]any, deps Deps) (C, error)

var (
	mu                 sync.Mutex
	sourceFactories    = map[string]Factory[core.Source]{}
	notifierFactories  = map[string]Factory[core.Notifier]{}
	processorFactories = map[string]Factory[core.Processor]{}
	llmFactories       = map[string]Factory[core.LLM]{}
)

func register[C any](kind string, m map[string]Factory[C], typ string, f Factory[C]) {
	mu.Lock()
	defer mu.Unlock()
	if typ == "" {
		panic(fmt.Sprintf("component: empty %s type", kind))
	}
	if _, dup := m[typ]; dup {
		panic(fmt.Sprintf("component: duplicate %s type %q", kind, typ))
	}
	m[typ] = f
}

func RegisterSource(typ string, f Factory[core.Source]) { register("source", sourceFactories, typ, f) }
func RegisterNotifier(typ string, f Factory[core.Notifier]) {
	register("notifier", notifierFactories, typ, f)
}
func RegisterProcessor(typ string, f Factory[core.Processor]) {
	register("processor", processorFactories, typ, f)
}
func RegisterLLM(typ string, f Factory[core.LLM]) { register("llm", llmFactories, typ, f) }

func lookup[C any](kind string, m map[string]Factory[C], typ string) (Factory[C], error) {
	mu.Lock()
	defer mu.Unlock()
	f, ok := m[typ]
	if !ok {
		return nil, fmt.Errorf("unknown %s type %q (registered: %v)", kind, typ, keys(m))
	}
	return f, nil
}

func keys[C any](m map[string]Factory[C]) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// BuildSource constructs a source instance of the given type.
func BuildSource(typ, name string, cfg map[string]any, deps Deps) (core.Source, error) {
	f, err := lookup("source", sourceFactories, typ)
	if err != nil {
		return nil, err
	}
	return f(name, cfg, deps)
}

func BuildNotifier(typ, name string, cfg map[string]any, deps Deps) (core.Notifier, error) {
	f, err := lookup("notifier", notifierFactories, typ)
	if err != nil {
		return nil, err
	}
	return f(name, cfg, deps)
}

func BuildProcessor(typ, name string, cfg map[string]any, deps Deps) (core.Processor, error) {
	f, err := lookup("processor", processorFactories, typ)
	if err != nil {
		return nil, err
	}
	return f(name, cfg, deps)
}

func BuildLLM(typ, name string, cfg map[string]any, deps Deps) (core.LLM, error) {
	f, err := lookup("llm", llmFactories, typ)
	if err != nil {
		return nil, err
	}
	return f(name, cfg, deps)
}

// RegisteredTypes reports the registered type strings, for `mailer doctor`
// and error messages.
func RegisteredTypes() map[string][]string {
	mu.Lock()
	defer mu.Unlock()
	return map[string][]string{
		"source":    keys(sourceFactories),
		"notifier":  keys(notifierFactories),
		"processor": keys(processorFactories),
		"llm":       keys(llmFactories),
	}
}

// Decode unmarshals a free-form config block into a typed struct. Going
// through YAML (rather than mapstructure) keeps exactly the semantics the
// rest of the config file has — duration strings, env-expanded values and
// yaml tags all behave identically.
func Decode(cfg map[string]any, out any) error {
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encode component config: %w", err)
	}
	if err := yaml.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode component config: %w", err)
	}
	return nil
}

// Secret expands environment variables in a config value, reading from a file
// when a companion "_file"-style path is given. Shared by every component so
// secret handling is uniform:
//
//	token_file: /run/secrets/tg_token   # file wins
//	token: ${TG_TOKEN}                  # env expansion on the final value
func Secret(inline, file string) (string, error) {
	v := inline
	if file != "" {
		data, err := readSecretFile(file)
		if err != nil {
			return "", err
		}
		v = data
	}
	return ExpandEnv(v), nil
}
