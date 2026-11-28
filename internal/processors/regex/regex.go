// Package regex is the regex extraction processor: the zero-dependency way
// to pull structured fields out of mail. One instance extracts one or more
// named fields in a single pass — verification codes, amounts, order numbers
// — without involving WASM or an LLM.
//
// Example:
//
//   - name: otp-extract
//     type: regex
//     extract:
//     code: '(?:code|验证码)[:：\s]*(\d{4,8})'
//     fields: [code]
//
// Actually the simplest useful form is even shorter — `extract` keys ARE the
// fields, so the fields list is unnecessary. See README docs in config.
package regex

import (
	"context"
	"fmt"
	"regexp"

	"github.com/elysola/mailer/internal/component"
	"github.com/elysola/mailer/internal/core"
	"github.com/elysola/mailer/internal/selector"
)

func init() {
	component.RegisterProcessor("regex", New)
}

type Config struct {
	// When gates the processor: only matching messages are touched.
	When string `yaml:"when"`
	// Extract maps field names to regular expressions. The first capture
	// group wins; when the pattern has no group, the whole match is used.
	// Patterns are matched against subject + preview text.
	Extract map[string]string `yaml:"extract"`
	// Replace rewrites the body: key is a regex, value the replacement
	// (supporting $1 group references). Applied after extraction, so a
	// stripped secret is still extractable first.
	Replace map[string]string `yaml:"replace"`
	// DropOnMatch discards the message entirely when the pattern matches —
	// a convenient spam/noise shredder without a separate filter instance.
	DropOnMatch string `yaml:"drop_on_match"`
}

type rule struct {
	re  *regexp.Regexp
	out string
}

type Processor struct {
	name    string
	when    string
	extract map[string]*regexp.Regexp
	replace []rule
	drop    *regexp.Regexp
}

// New is the component factory for `type: regex`.
func New(name string, raw map[string]any, deps component.Deps) (core.Processor, error) {
	var cfg Config
	if err := component.Decode(component.ExpandMap(raw), &cfg); err != nil {
		return nil, fmt.Errorf("regex processor %q: %w", name, err)
	}
	if len(cfg.Extract) == 0 && len(cfg.Replace) == 0 && cfg.DropOnMatch == "" {
		return nil, fmt.Errorf("regex processor %q: at least one of extract, replace, drop_on_match is required", name)
	}
	if cfg.When != "" {
		if err := selector.CheckSelector(cfg.When); err != nil {
			return nil, fmt.Errorf("regex processor %q: bad when: %w", name, err)
		}
	}
	p := &Processor{name: name, when: cfg.When, extract: map[string]*regexp.Regexp{}}
	for field, pat := range cfg.Extract {
		re, err := regexp.Compile(pat)
		if err != nil {
			return nil, fmt.Errorf("regex processor %q: extract %q: %w", name, field, err)
		}
		p.extract[field] = re
	}
	for pat, out := range cfg.Replace {
		re, err := regexp.Compile(pat)
		if err != nil {
			return nil, fmt.Errorf("regex processor %q: replace %q: %w", name, pat, err)
		}
		p.replace = append(p.replace, rule{re: re, out: out})
	}
	if cfg.DropOnMatch != "" {
		re, err := regexp.Compile(cfg.DropOnMatch)
		if err != nil {
			return nil, fmt.Errorf("regex processor %q: drop_on_match: %w", name, err)
		}
		p.drop = re
	}
	return p, nil
}

func (p *Processor) Name() string { return p.name }
func (p *Processor) Type() string { return "regex" }

func (p *Processor) Start(context.Context) error { return nil }
func (p *Processor) Stop(context.Context) error  { return nil }

func (p *Processor) Process(ctx context.Context, env core.Envelope) (core.Verdict, error) {
	if p.when != "" && !selector.MatchSelector(p.when, env) {
		return core.Verdict{}, nil
	}
	// haystack joins subject and preview: extraction patterns should not care
	// whether the bank put the amount in the subject line or the first
	// sentence of the body.
	haystack := env.Message.Subject + "\n" + env.Message.Preview
	if env.Body != "" {
		haystack += "\n" + env.Body
	}

	if p.drop != nil && p.drop.MatchString(haystack) {
		return core.Verdict{Action: core.ActionDrop}, nil
	}

	v := core.Verdict{Fields: map[string]string{}}
	for field, re := range p.extract {
		m := re.FindStringSubmatch(haystack)
		if m == nil {
			continue
		}
		val := m[0]
		if len(m) > 1 {
			val = m[1]
		}
		if val != "" {
			v.Fields[field] = val
		}
	}
	if len(v.Fields) == 0 {
		v.Fields = nil
	}

	if len(p.replace) > 0 {
		body := env.Body
		if body == "" {
			body = env.Message.Preview
		}
		for _, r := range p.replace {
			body = r.re.ReplaceAllString(body, r.out)
		}
		if body != env.Body {
			v.Body = body
		}
	}
	return v, nil
}
