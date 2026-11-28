package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const validV2 = `
version: 2
daemon:
  poll_interval: 30s
sources:
  - name: work
    type: imap
    host: imap.example.com
    username: me@example.com
    password: secret
notifiers:
  - name: tg
    type: telegram
    bot_token: t
    chat_ids: ["1"]
routes:
  - name: default
    to: [tg]
`

func TestLoadV2Defaults(t *testing.T) {
	path := writeTemp(t, validV2)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Daemon.PollInterval != 30*time.Second {
		t.Errorf("PollInterval = %v, want 30s", cfg.Daemon.PollInterval)
	}
	if cfg.State.File != "state.db" {
		t.Errorf("State.File = %q, want state.db", cfg.State.File)
	}
	if cfg.State.Driver != "sqlite" {
		t.Errorf("State.Driver = %q, want sqlite", cfg.State.Driver)
	}
	if cfg.Daemon.RetryAttempts != 2 {
		t.Errorf("RetryAttempts = %d, want 2", cfg.Daemon.RetryAttempts)
	}
	if cfg.Daemon.SeenRetention != 90*24*time.Hour {
		t.Errorf("SeenRetention = %v, want 90d", cfg.Daemon.SeenRetention)
	}
	if cfg.Daemon.RetryScanInterval != 30*time.Second {
		t.Errorf("RetryScanInterval = %v, want 30s", cfg.Daemon.RetryScanInterval)
	}
}

func TestInstanceRawPreservesComponentFields(t *testing.T) {
	path := writeTemp(t, validV2)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Sources) != 1 {
		t.Fatalf("sources = %d, want 1", len(cfg.Sources))
	}
	src := cfg.Sources[0]
	if src.Name != "work" || src.Type != "imap" {
		t.Errorf("source meta = %q/%q", src.Name, src.Type)
	}
	// name and type must be stripped from Raw; component fields preserved.
	if _, ok := src.Raw["name"]; ok {
		t.Error("Raw still contains name")
	}
	if _, ok := src.Raw["type"]; ok {
		t.Error("Raw still contains type")
	}
	if src.Raw["host"] != "imap.example.com" {
		t.Errorf("Raw host = %v", src.Raw["host"])
	}
	if src.Raw["password"] != "secret" {
		t.Errorf("Raw password = %v", src.Raw["password"])
	}
}

func TestVersionRequired(t *testing.T) {
	path := writeTemp(t, `
sources:
  - name: x
    type: imap
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for missing version")
	}
	// v1 file must be rejected with a helpful message.
	path = writeTemp(t, `
accounts:
  - host: x
    username: y
telegram:
  enabled: true
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for v1 config")
	}
}

func TestUnknownRouteReferencesRejected(t *testing.T) {
	path := writeTemp(t, `
version: 2
sources:
  - name: w
    type: imap
notifiers:
  - name: tg
    type: telegram
routes:
  - name: default
    to: [nonexistent]
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for unknown notifier in route")
	}
}

func TestCatchAllRouteRequired(t *testing.T) {
	path := writeTemp(t, `
version: 2
sources:
  - name: w
    type: imap
notifiers:
  - name: tg
    type: telegram
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for missing routes")
	}
}

func TestDuplicateInstanceNamesRejected(t *testing.T) {
	path := writeTemp(t, `
version: 2
sources:
  - name: w
    type: imap
  - name: w
    type: imap
notifiers:
  - name: tg
    type: telegram
routes:
  - name: default
    to: [tg]
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for duplicate source names")
	}
}

func TestHealthPortDisableAndDefault(t *testing.T) {
	path := writeTemp(t, validV2)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	port, enabled := cfg.HealthListenPort()
	if !enabled || port != DefaultHealthPort {
		t.Errorf("default health port = %d/%v", port, enabled)
	}

	path = writeTemp(t, validV2+"\ndaemon:\n  health_port: 0\n  poll_interval: 30s\n")
	// The second daemon block conflicts with the first; write a full config instead.
	path = writeTemp(t, `
version: 2
daemon:
  health_port: 0
sources:
  - name: w
    type: imap
notifiers:
  - name: tg
    type: telegram
routes:
  - name: default
    to: [tg]
`)
	cfg, err = Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, enabled := cfg.HealthListenPort(); enabled {
		t.Error("health_port: 0 must disable the server")
	}
}

func TestTemplateReferenceValidation(t *testing.T) {
	path := writeTemp(t, `
version: 2
sources:
  - name: w
    type: imap
notifiers:
  - name: tg
    type: telegram
routes:
  - name: default
    to: [tg]
    templates: missing
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for unknown template reference")
	}

	path = writeTemp(t, `
version: 2
sources:
  - name: w
    type: imap
notifiers:
  - name: tg
    type: telegram
templates:
  terse:
    title: x
    body: y
routes:
  - name: default
    to: [tg]
    templates: terse
`)
	if _, err := Load(path); err != nil {
		t.Fatalf("valid template reference rejected: %v", err)
	}
}

func TestProcessorReferenceValidation(t *testing.T) {
	path := writeTemp(t, `
version: 2
sources:
  - name: w
    type: imap
notifiers:
  - name: tg
    type: telegram
routes:
  - name: default
    to: [tg]
    processors: [nope]
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for unknown processor in route")
	}
}
