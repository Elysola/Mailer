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

func TestLoadDefaults(t *testing.T) {
	path := writeTemp(t, `
accounts:
  - host: imap.example.com
    username: me@example.com
    password: secret
    tls: true
telegram:
  enabled: true
  bot_token: "t"
  chat_ids: ["1"]
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PollInterval != 60*time.Second {
		t.Errorf("PollInterval = %v, want 60s", cfg.PollInterval)
	}
	if cfg.StateFile != "state.db" {
		t.Errorf("StateFile = %q, want state.db", cfg.StateFile)
	}
	a := cfg.Accounts[0]
	if a.Port != 993 {
		t.Errorf("Port = %d, want 993 for TLS", a.Port)
	}
	if a.Mailbox != "INBOX" {
		t.Errorf("Mailbox = %q, want INBOX", a.Mailbox)
	}
	if a.Name != "me@example.com" {
		t.Errorf("Name = %q, want username fallback", a.Name)
	}
}

func TestPortDefaultSTARTTLS(t *testing.T) {
	path := writeTemp(t, `
accounts:
  - host: imap.example.com
    username: u
    password: p
    tls: false
discord:
  enabled: true
  webhook_url: "https://x"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Accounts[0].Port != 143 {
		t.Errorf("Port = %d, want 143 for non-TLS", cfg.Accounts[0].Port)
	}
}

func TestValidateNoNotifier(t *testing.T) {
	path := writeTemp(t, `
accounts:
  - host: h
    username: u
    password: p
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error when no notifier enabled")
	}
}

func TestDiscordThreadRequiresThreadID(t *testing.T) {
	path := writeTemp(t, `
accounts:
  - name: a
    host: h
    username: u
    password: p
    discord:
      mode: thread
discord:
  enabled: true
  bot_token: "b"
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error: thread mode without thread_id")
	}
}

func TestDiscordPerAccountRoutingDefaults(t *testing.T) {
	path := writeTemp(t, `
accounts:
  - name: a
    host: h
    username: u
    password: p
    discord:
      channel_id: "123"
  - name: b
    host: h
    username: u
    password: p
    discord:
      mode: thread
      thread_id: "999"
discord:
  enabled: true
  bot_token: "b"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Accounts[0].Discord.Mode; got != DiscordModeChannel {
		t.Errorf("account a mode = %q, want default %q", got, DiscordModeChannel)
	}
	if got := cfg.Accounts[1].Discord.Mode; got != DiscordModeThread {
		t.Errorf("account b mode = %q, want %q", got, DiscordModeThread)
	}
}

func TestHealthPortDisableAndDefault(t *testing.T) {
	base := `
accounts:
  - name: a
    host: h
    username: u
    password: p
telegram:
  enabled: true
  bot_token: "t"
  chat_ids: ["1"]
`

	// Omitted key: default port, server runs.
	cfg, err := Load(writeTemp(t, base))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if port, ok := cfg.HealthListenPort(); !ok || port != DefaultHealthPort {
		t.Errorf("default HealthListenPort() = (%d, %v), want (%d, true)", port, ok, DefaultHealthPort)
	}

	// Explicit 0: server disabled. This is the documented behaviour and used
	// to be silently overridden back to 9100.
	cfg, err = Load(writeTemp(t, base+"health_port: 0\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if port, ok := cfg.HealthListenPort(); ok {
		t.Errorf("HealthListenPort() = (%d, true), want disabled for health_port: 0", port)
	}

	// Explicit custom port is honoured.
	cfg, err = Load(writeTemp(t, base+"health_port: 8080\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if port, ok := cfg.HealthListenPort(); !ok || port != 8080 {
		t.Errorf("HealthListenPort() = (%d, %v), want (8080, true)", port, ok)
	}
}

func TestHealthPortOutOfRangeRejected(t *testing.T) {
	path := writeTemp(t, `
health_port: 70000
accounts:
  - name: a
    host: h
    username: u
    password: p
telegram:
  enabled: true
  bot_token: "t"
  chat_ids: ["1"]
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for out-of-range health_port")
	}
}

func TestValidateNoAccounts(t *testing.T) {
	path := writeTemp(t, `
telegram:
  enabled: true
  bot_token: t
  chat_ids: ["1"]
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error when no accounts configured")
	}
}

func TestNewFieldDefaults(t *testing.T) {
	path := writeTemp(t, `
accounts:
  - name: a
    host: imap.example.com
    username: u
    password: p
telegram:
  enabled: true
  bot_token: "t"
  chat_ids: ["1"]
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PreviewLen != 400 {
		t.Errorf("PreviewLen = %d, want 400", cfg.PreviewLen)
	}
	if cfg.SeenRetention != 90*24*time.Hour {
		t.Errorf("SeenRetention = %v, want 2160h", cfg.SeenRetention)
	}
	if cfg.MaxPendingPerAccount != 200 {
		t.Errorf("MaxPendingPerAccount = %d, want 200", cfg.MaxPendingPerAccount)
	}
	if cfg.Accounts[0].PreviewLen != 400 {
		t.Errorf("account PreviewLen inheritance = %d, want 400", cfg.Accounts[0].PreviewLen)
	}
}

func TestPreviewLenExplicitAndInherit(t *testing.T) {
	path := writeTemp(t, `
preview_len: 800
accounts:
  - name: a
    host: h
    username: u
    password: p
  - name: b
    host: h
    username: u2
    password: p2
    preview_len: 120
telegram:
  enabled: true
  bot_token: "t"
  chat_ids: ["1"]
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Accounts[0].PreviewLen != 800 {
		t.Errorf("account a PreviewLen = %d, want inherit 800", cfg.Accounts[0].PreviewLen)
	}
	if cfg.Accounts[1].PreviewLen != 120 {
		t.Errorf("account b PreviewLen = %d, want explicit 120", cfg.Accounts[1].PreviewLen)
	}
}

func TestDuplicateAccountNamesRejected(t *testing.T) {
	path := writeTemp(t, `
accounts:
  - name: dup
    host: h
    username: u1
    password: p
  - name: dup
    host: h
    username: u2
    password: p
telegram:
  enabled: true
  bot_token: "t"
  chat_ids: ["1"]
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for duplicate account names")
	}
}

func TestDuplicateImplicitNamesRejected(t *testing.T) {
	// Name falls back to username; two accounts sharing a username collide.
	path := writeTemp(t, `
accounts:
  - host: h1
    username: same
    password: p
  - host: h2
    username: same
    password: p
telegram:
  enabled: true
  bot_token: "t"
  chat_ids: ["1"]
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for implicit duplicate names")
	}
}

// TestTelegramAPIBaseMustBeSafeHTTPS: the bot token is interpolated into the
// request path, so a plaintext or malformed api_base would leak it or produce
// broken URLs. A proxy over https must still be allowed.
func TestTelegramAPIBaseMustBeSafeHTTPS(t *testing.T) {
	base := `
accounts:
  - name: a
    host: h
    username: u
    password: p
telegram:
  enabled: true
  bot_token: "1:abc"
  chat_ids: ["42"]
`

	rejected := map[string]string{
		"http scheme":  "http://api.telegram.org",
		"empty scheme": "api.telegram.org",
		"path":         "https://api.telegram.org/bot",
		"query":        "https://api.telegram.org?x=1",
	}
	for name, apiBase := range rejected {
		t.Run(name, func(t *testing.T) {
			path := writeTemp(t, base+`  api_base: "`+apiBase+`"`+"\n")
			if _, err := Load(path); err == nil {
				t.Fatalf("expected api_base %q to be rejected", apiBase)
			}
		})
	}

	// A local Bot API server or proxy over https is a legitimate setup.
	path := writeTemp(t, base+`  api_base: "https://tg-proxy.internal:8081"`+"\n")
	if _, err := Load(path); err != nil {
		t.Fatalf("https proxy base should be accepted: %v", err)
	}

	// Trailing slash is fine: the notifier trims it.
	path = writeTemp(t, base+`  api_base: "https://api.telegram.org/"`+"\n")
	if _, err := Load(path); err != nil {
		t.Fatalf("trailing slash should be accepted: %v", err)
	}
}

// TestDiscordWebhookMustBeHTTPS: the webhook URL is itself the credential.
func TestDiscordWebhookMustBeHTTPS(t *testing.T) {
	base := `
accounts:
  - name: a
    host: h
    username: u
    password: p
discord:
  enabled: true
`
	if _, err := Load(writeTemp(t, base+`  webhook_url: "http://discord.com/api/webhooks/1/x"`+"\n")); err == nil {
		t.Fatal("expected an http webhook_url to be rejected")
	}

	// The same rule applies to a per-account override, which bypasses the
	// global value entirely.
	perAccount := `
accounts:
  - name: a
    host: h
    username: u
    password: p
    discord:
      mode: channel
      webhook_url: "http://discord.com/api/webhooks/1/x"
discord:
  enabled: true
  bot_token: "b"
`
	if _, err := Load(writeTemp(t, perAccount)); err == nil {
		t.Fatal("expected a per-account http webhook_url to be rejected")
	}

	if _, err := Load(writeTemp(t, base+`  webhook_url: "https://discord.com/api/webhooks/1/x"`+"\n")); err != nil {
		t.Fatalf("https webhook should be accepted: %v", err)
	}
}
