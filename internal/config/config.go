// Package config implements the v2 configuration format: component instances
// with type dispatch, an ordered routing table, named templates and LLM
// services. It is a breaking change from v1; `mailer migrate-config` produces
// a v2 draft from a v1 file.
package config

import (
	"fmt"
	"os"
	"time"

	"github.com/elysola/mailer/internal/selector"
	"gopkg.in/yaml.v3"
)

// DefaultHealthPort is used when `daemon.health_port` is omitted.
const DefaultHealthPort = 9100

// Config is the v2 top-level configuration.
type Config struct {
	Version int `yaml:"version"`

	Daemon DaemonConfig `yaml:"daemon"`
	State  StateConfig  `yaml:"state"`

	// Component instance blocks. Each instance is `name`/`type` plus a
	// free-form block the component factory decodes itself — the config
	// package never learns a component's own fields, which is what makes new
	// components zero-touch upstream.
	Sources    []Instance `yaml:"sources"`
	Notifiers  []Instance `yaml:"notifiers"`
	Processors []Instance `yaml:"processors"`
	LLMs       []Instance `yaml:"llms"`

	Routes    []Route             `yaml:"routes"`
	Templates map[string]Template `yaml:"templates"`
}

// DaemonConfig holds process-wide behaviour that is not specific to any
// component.
type DaemonConfig struct {
	PollInterval time.Duration `yaml:"poll_interval"`
	// HealthPort is a pointer so "absent" and "explicitly disabled" stay
	// distinguishable: `health_port: 0` means no HTTP server.
	HealthPort          *int          `yaml:"health_port"`
	RetryAttempts       int           `yaml:"retry_attempts"`
	RetryDelay          time.Duration `yaml:"retry_delay"`
	SeenRetention       time.Duration `yaml:"seen_retention"`
	MaxPendingPerSource int           `yaml:"max_pending_per_source"`
	RetryScanInterval   time.Duration `yaml:"retry_scan_interval"`
}

// StateConfig selects the persistence backend.
type StateConfig struct {
	Driver string `yaml:"driver"`
	File   string `yaml:"file"`
}

// Instance is one component instance entry. The raw block is handed to the
// component factory unchanged (after env expansion); validation of the
// component's own fields is the component's business.
type Instance struct {
	Name string `yaml:"name"`
	Type string `yaml:"type"`
	// Raw carries every field except name/type. yaml.v3 cannot decode "the
	// rest of the map" directly, so Load re-marshals the instance node into
	// this field; see decodeInstances.
	Raw map[string]any `yaml:"-"`
}

// Route is one row of the ordered routing table.
type Route struct {
	Name string `yaml:"name"`
	// When is the selector expression; empty matches everything.
	When string `yaml:"when"`
	// Processors overrides the processor chain for matching messages.
	// Empty means the route inherits no processing (the v2 default chain is
	// expressed as a catch-all route).
	Processors []string `yaml:"processors"`
	To         []string `yaml:"to"`
	Templates  string   `yaml:"templates"`
	// Template is an inline template, taking precedence over the named
	// reference.
	Template Template `yaml:"template"`
	// Stop ends route evaluation after this row matches. Rows are evaluated
	// in order regardless; stop only controls whether a hit is terminal.
	Stop bool `yaml:"stop"`
}

// Template is a named or inline title/body template pair.
type Template struct {
	Title string `yaml:"title"`
	Body  string `yaml:"body"`
}

// Load reads, parses and validates the config file at path.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := decodeInstances(raw, &cfg); err != nil {
		return nil, err
	}

	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// decodeInstances re-decodes the instance blocks preserving every field.
// yaml.v3 has no inline remainder, so each instance entry is re-marshalled
// into a plain map with name/type stripped.
func decodeInstances(raw []byte, cfg *Config) error {
	// A generic pass over the document captures the raw instance maps.
	var generic struct {
		Sources    []map[string]any `yaml:"sources"`
		Notifiers  []map[string]any `yaml:"notifiers"`
		Processors []map[string]any `yaml:"processors"`
		LLMs       []map[string]any `yaml:"llms"`
	}
	if err := yaml.Unmarshal(raw, &generic); err != nil {
		return fmt.Errorf("parse config instances: %w", err)
	}
	fill := func(instances []Instance, rawMaps []map[string]any, section string) error {
		if len(instances) != len(rawMaps) {
			return fmt.Errorf("config: %s section is inconsistent", section)
		}
		for i := range instances {
			m := make(map[string]any, len(rawMaps[i]))
			for k, v := range rawMaps[i] {
				if k == "name" || k == "type" {
					continue
				}
				m[k] = v
			}
			instances[i].Raw = m
		}
		return nil
	}
	for _, sec := range []struct {
		name      string
		instances []Instance
		rawMaps   []map[string]any
	}{
		{"sources", cfg.Sources, generic.Sources},
		{"notifiers", cfg.Notifiers, generic.Notifiers},
		{"processors", cfg.Processors, generic.Processors},
		{"llms", cfg.LLMs, generic.LLMs},
	} {
		if err := fill(sec.instances, sec.rawMaps, sec.name); err != nil {
			return err
		}
	}
	return nil
}

// HealthListenPort returns the port the health/status/metrics server should
// bind, and whether it should run at all.
func (c *Config) HealthListenPort() (port int, enabled bool) {
	if c.Daemon.HealthPort == nil {
		return DefaultHealthPort, true
	}
	if *c.Daemon.HealthPort <= 0 {
		return 0, false
	}
	return *c.Daemon.HealthPort, true
}

func (c *Config) applyDefaults() {
	d := &c.Daemon
	if d.PollInterval <= 0 {
		d.PollInterval = 60 * time.Second
	}
	if d.HealthPort == nil {
		port := DefaultHealthPort
		d.HealthPort = &port
	}
	if d.RetryAttempts <= 0 {
		d.RetryAttempts = 2
	}
	if d.RetryDelay <= 0 {
		d.RetryDelay = 5 * time.Second
	}
	if d.SeenRetention <= 0 {
		d.SeenRetention = 90 * 24 * time.Hour
	}
	if d.MaxPendingPerSource <= 0 {
		d.MaxPendingPerSource = 200
	}
	if d.RetryScanInterval <= 0 {
		d.RetryScanInterval = 30 * time.Second
	}
	if c.State.Driver == "" {
		c.State.Driver = "sqlite"
	}
	if c.State.File == "" {
		c.State.File = "state.db"
	}
}

func (c *Config) validate() error {
	if c.Version != 2 {
		return fmt.Errorf("config version must be 2 (got %d); run `mailer migrate-config` to upgrade a v1 config", c.Version)
	}

	seen := make(map[string]bool)
	check := func(instances []Instance, section string) error {
		for _, inst := range instances {
			if inst.Name == "" {
				return fmt.Errorf("%s: every instance needs a name", section)
			}
			if inst.Type == "" {
				return fmt.Errorf("%s %q: type is required", section, inst.Name)
			}
			key := section + "/" + inst.Name
			if seen[key] {
				return fmt.Errorf("%s: duplicate instance name %q", section, inst.Name)
			}
			seen[key] = true
		}
		return nil
	}
	for _, sec := range []struct {
		name      string
		instances []Instance
	}{
		{"sources", c.Sources},
		{"notifiers", c.Notifiers},
		{"processors", c.Processors},
		{"llms", c.LLMs},
	} {
		if err := check(sec.instances, sec.name); err != nil {
			return err
		}
	}
	if len(c.Sources) == 0 {
		return fmt.Errorf("at least one source is required")
	}
	if len(c.Notifiers) == 0 {
		return fmt.Errorf("at least one notifier is required")
	}

	// Routes must reference instances that exist; an unroutable config
	// should fail at startup, not at 3am when the first mail arrives.
	notifierNames := make(map[string]bool, len(c.Notifiers))
	for _, n := range c.Notifiers {
		notifierNames[n.Name] = true
	}
	processorNames := make(map[string]bool, len(c.Processors))
	for _, p := range c.Processors {
		processorNames[p.Name] = true
	}
	if len(c.Routes) == 0 {
		return fmt.Errorf("at least one route is required (use a catch-all route with no `when` to match everything)")
	}
	for _, r := range c.Routes {
		if r.When != "" {
			if err := selector.CheckSelector(r.When); err != nil {
				return fmt.Errorf("route %q: bad when: %w", r.Name, err)
			}
		}
		for _, n := range r.To {
			if !notifierNames[n] {
				return fmt.Errorf("route %q: unknown notifier %q", r.Name, n)
			}
		}
		for _, p := range r.Processors {
			if !processorNames[p] {
				return fmt.Errorf("route %q: unknown processor %q", r.Name, p)
			}
		}
		if r.Templates != "" {
			if _, ok := c.Templates[r.Templates]; !ok {
				return fmt.Errorf("route %q: unknown template %q", r.Name, r.Templates)
			}
		}
	}
	return nil
}

// LLMRef returns the raw instance config for a named LLM, so processors can
// pass it to the provider factory without knowing its fields.
func (c *Config) LLMRef(name string) (Instance, bool) {
	for _, l := range c.LLMs {
		if l.Name == name {
			return l, true
		}
	}
	return Instance{}, false
}
