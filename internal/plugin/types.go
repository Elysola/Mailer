// Package plugin defines the stable contract between mailer and WASM mail processors.
package plugin

import "time"

const APIVersion = "mailer.plugin.v1"

// Fallback limits used when neither the manifest nor the application config
// specify a value.
const (
	DefaultTimeout   = 100 * time.Millisecond
	DefaultMaxInput  = 512 * 1024
	DefaultMaxOutput = 16 * 1024
)

type Mail struct {
	Account   string            `json:"account"`
	UID       uint32            `json:"uid"`
	MessageID string            `json:"message_id"`
	From      string            `json:"from"`
	To        []string          `json:"to,omitempty"`
	Cc        []string          `json:"cc,omitempty"`
	Subject   string            `json:"subject"`
	Date      time.Time         `json:"date"`
	Text      string            `json:"text,omitempty"`
	HTML      string            `json:"html,omitempty"`
	Preview   string            `json:"preview,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
}

type Result struct {
	Matched   bool              `json:"matched"`
	Plugin    string            `json:"plugin,omitempty"`
	Fields    map[string]string `json:"fields,omitempty"`
	Title     string            `json:"title,omitempty"`
	Text      string            `json:"text,omitempty"`
	Notifiers []string          `json:"notifiers,omitempty"`
}

type Manifest struct {
	Name       string `yaml:"name"`
	Version    string `yaml:"version"`
	APIVersion string `yaml:"api_version"`
	Entrypoint string `yaml:"entrypoint"`
	Priority   int    `yaml:"priority"`
	Enabled    *bool  `yaml:"enabled"`
	Limits     Limits `yaml:"limits"`

	// Patterns are host-evaluated extraction rules. They run before the WASM
	// module and populate Result.Fields, so a plugin can rely on regex
	// captures without bundling a regex engine (TinyGo's regexp cannot run
	// inside a wazero reactor module).
	Patterns []Pattern `yaml:"patterns,omitempty"`
}

type Limits struct {
	Timeout   time.Duration `yaml:"timeout"`
	MaxInput  int           `yaml:"max_input"`
	MaxOutput int           `yaml:"max_output"`
	MemoryMiB uint32        `yaml:"memory_mib"`
}
