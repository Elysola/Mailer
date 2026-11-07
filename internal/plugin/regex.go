package plugin

import (
	"fmt"
	"regexp"
	"strings"
)

// Pattern is a declarative extraction rule evaluated by the host, not by the
// plugin. Regex support lives here because TinyGo's regexp package cannot run
// as a wazero reactor module (it panics during its first stack growth), so
// keeping patterns in the manifest gives plugins real regex semantics without
// pulling a regex engine into WASM.
//
// Patterns run against the decoded mail body and populate Result.Fields, which
// templates reference as {{.Fields.<key>}}. They are evaluated before the
// plugin runs; a plugin's own fields win on key conflicts.
type Pattern struct {
	// Name is the field key the capture is stored under.
	Name string `yaml:"name"`
	// Regexp is a Go regular expression (RE2 syntax).
	Regexp string `yaml:"regexp"`
	// Source selects the text searched: "text" (default), "html", "subject",
	// "preview" or "from".
	Source string `yaml:"source"`
	// Group selects the submatch to use. 0 (default) means the whole match.
	Group int `yaml:"group"`
	// Required makes the pattern's absence a plugin error instead of a miss.
	Required bool `yaml:"required"`
}

// patternSource is one searchable text field of a mail.
type patternSource int

const (
	srcText patternSource = iota
	srcHTML
	srcSubject
	srcPreview
	srcFrom
)

func parseSource(s string) (patternSource, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "text":
		return srcText, nil
	case "html":
		return srcHTML, nil
	case "subject":
		return srcSubject, nil
	case "preview":
		return srcPreview, nil
	case "from":
		return srcFrom, nil
	default:
		return 0, fmt.Errorf("unknown source %q (want text, html, subject, preview or from)", s)
	}
}

// compiledPattern is a Pattern with its regexp and source resolved.
type compiledPattern struct {
	name     string
	re       *regexp.Regexp
	source   patternSource
	group    int
	required bool
}

func compilePatterns(patterns []Pattern) ([]compiledPattern, error) {
	out := make([]compiledPattern, 0, len(patterns))
	for i, p := range patterns {
		if strings.TrimSpace(p.Name) == "" {
			return nil, fmt.Errorf("pattern %d: name is required", i)
		}
		if strings.TrimSpace(p.Regexp) == "" {
			return nil, fmt.Errorf("pattern %q: regexp is required", p.Name)
		}
		re, err := regexp.Compile(p.Regexp)
		if err != nil {
			return nil, fmt.Errorf("pattern %q: %w", p.Name, err)
		}
		if p.Group < 0 || p.Group > re.NumSubexp() {
			return nil, fmt.Errorf("pattern %q: group %d out of range (have %d subexpressions)",
				p.Name, p.Group, re.NumSubexp())
		}
		src, err := parseSource(p.Source)
		if err != nil {
			return nil, fmt.Errorf("pattern %q: %w", p.Name, err)
		}
		out = append(out, compiledPattern{
			name:     p.Name,
			re:       re,
			source:   src,
			group:    p.Group,
			required: p.Required,
		})
	}
	return out, nil
}

// textFor returns the mail field a pattern searches.
func (p *Mail) textFor(src patternSource) string {
	switch src {
	case srcHTML:
		return p.HTML
	case srcSubject:
		return p.Subject
	case srcPreview:
		return p.Preview
	case srcFrom:
		return p.From
	default:
		return p.Text
	}
}

// apply evaluates every pattern and returns the captured fields. A required
// pattern that finds nothing is reported as an error so the plugin's failure
// is visible in the log rather than silently producing an empty field.
func applyPatterns(patterns []compiledPattern, m *Mail) (map[string]string, error) {
	if len(patterns) == 0 {
		return nil, nil
	}
	fields := make(map[string]string, len(patterns))
	var missing []string

	for _, p := range patterns {
		sub := p.re.FindStringSubmatch(m.textFor(p.source))
		if sub == nil {
			if p.required {
				missing = append(missing, p.name)
			}
			continue
		}
		if p.group >= len(sub) {
			if p.required {
				missing = append(missing, p.name)
			}
			continue
		}
		fields[p.name] = sub[p.group]
	}

	if len(missing) > 0 {
		return fields, fmt.Errorf("required pattern(s) did not match: %s", strings.Join(missing, ", "))
	}
	return fields, nil
}
