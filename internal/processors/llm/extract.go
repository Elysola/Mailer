package llm

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/elysola/mailer/internal/component"
	"github.com/elysola/mailer/internal/core"
)

func init() {
	component.RegisterProcessor("llmextract", NewExtract)
}

type extractConfig struct {
	BaseConfig `yaml:",inline"`
	// Schema is the JSON Schema describing the object to extract. Required —
	// it drives structured output and is part of the cache key.
	Schema map[string]any `yaml:"schema"`
	// Prefix is prepended to every extracted key when merging into envelope
	// fields, so several extractors cannot collide. Empty means no prefix.
	Prefix string `yaml:"prefix"`
}

type extractor struct {
	*base
	cfg extractConfig
}

func NewExtract(name string, raw map[string]any, deps component.Deps) (core.Processor, error) {
	var cfg extractConfig
	if err := component.Decode(component.ExpandMap(raw), &cfg); err != nil {
		return nil, fmt.Errorf("llmextract %q: %w", name, err)
	}
	if err := cfg.BaseConfig.normalize(name); err != nil {
		return nil, err
	}
	if len(cfg.Schema) == 0 {
		return nil, fmt.Errorf("llmextract %q: schema is required", name)
	}
	if cfg.Prompt == "" {
		cfg.Prompt = defaultExtractPrompt
	}
	b, err := newBase(name, cfg.BaseConfig, deps)
	if err != nil {
		return nil, err
	}
	return &extractor{base: b, cfg: cfg}, nil
}

const defaultExtractPrompt = `从这封邮件中提取结构化信息,严格按要求的 JSON 格式输出,缺失字段填 null。

{{.Text}}`

func (e *extractor) Name() string                    { return e.name }
func (e *extractor) Type() string                    { return "llmextract" }
func (e *extractor) Start(ctx context.Context) error { return nil }
func (e *extractor) Stop(ctx context.Context) error  { return nil }

func (e *extractor) Process(ctx context.Context, env core.Envelope) (core.Verdict, error) {
	schemaRaw, _ := json.Marshal(e.cfg.Schema)
	text, _, v, err := e.complete(ctx, env, "extract-v1:"+string(schemaRaw), e.cfg.Prompt, e.cfg.Schema, 1024)
	if err == errSkip {
		return core.Verdict{}, nil
	}
	if err != nil {
		return core.Verdict{}, err
	}
	if v.Action != "" {
		return v, nil
	}

	obj, err := extractJSON(text)
	if err != nil {
		// A malformed extraction is a failure of the LLM call as much as a
		// network error is: on_error decides. Passthrough degrades silently,
		// fail retries, drop discards.
		return e.errorVerdict(fmt.Errorf("parse extraction: %w", err)), nil
	}

	fields := make(map[string]string, len(obj))
	for key, val := range obj {
		if val == nil {
			continue
		}
		s, ok := val.(string)
		if !ok {
			// Numbers and booleans are stringified so templates and routing
			// can use them uniformly; nested objects are dropped because
			// envelope fields are flat by design.
			switch tv := val.(type) {
			case float64, bool:
				s = fmt.Sprintf("%v", tv)
			default:
				continue
			}
		}
		if s == "" {
			continue
		}
		fields[e.cfg.Prefix+key] = s
	}
	if len(fields) == 0 {
		return core.Verdict{}, nil
	}
	return core.Verdict{Fields: fields}, nil
}
