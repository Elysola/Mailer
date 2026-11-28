package cli

import (
	"flag"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// RunMigrateConfig implements `mailer migrate-config`: reads a v1 config and
// emits a v2 draft. The conversion is mechanical for the parts that map
// cleanly (accounts become imap sources, telegram/discord become notifier
// instances, daemon knobs move under daemon:) and annotated where operator
// judgement is needed (routing, plugin chain, templates).
func RunMigrateConfig(args []string) int {
	fs := flag.NewFlagSet("migrate-config", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "path to the v1 YAML config file")
	out := fs.String("out", "", "write here instead of stdout")
	fs.Parse(args)

	raw, err := os.ReadFile(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read config: %v\n", err)
		return 1
	}
	var v1 v1Config
	if err := yaml.Unmarshal(raw, &v1); err != nil {
		fmt.Fprintf(os.Stderr, "parse v1 config: %v\n", err)
		return 1
	}
	if len(v1.Accounts) == 0 {
		fmt.Fprintf(os.Stderr, "input does not look like a v1 config (no accounts)\n")
		return 1
	}

	v2 := migrateV1Config(&v1)

	var node yaml.Node
	if err := node.Encode(v2); err != nil {
		fmt.Fprintf(os.Stderr, "encode v2 config: %v\n", err)
		return 1
	}
	data, err := yaml.Marshal(v2)
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal v2 config: %v\n", err)
		return 1
	}

	header := `# Migrated from v1 by mailer migrate-config. Review before use:
#  - routes are a catch-all chain; split them when accounts need different handling
#  - plugin_dir becomes a wasm processor instance; enable it in route.processors
#  - verify secrets still resolve (files/env vars are copied verbatim)
`
	data = append([]byte(header), data...)

	if *out == "" {
		fmt.Print(string(data))
		return 0
	}
	if err := os.WriteFile(*out, data, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "write %s: %v\n", *out, err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "v2 config written to %s — review the comments at the top before using it\n", *out)
	return 0
}

// v1Config mirrors the v1 file format closely enough for migration. It is
// intentionally local to the CLI: nothing in the v2 runtime should ever need
// to know this shape again.
type v1Config struct {
	PollInterval         time.Duration `yaml:"poll_interval"`
	StateFile            string        `yaml:"state_file"`
	HealthPort           *int          `yaml:"health_port"`
	RetryAttempts        int           `yaml:"retry_attempts"`
	RetryDelay           time.Duration `yaml:"retry_delay"`
	NoopInterval         time.Duration `yaml:"noop_interval"`
	PreviewLen           int           `yaml:"preview_len"`
	SeenRetention        time.Duration `yaml:"seen_retention"`
	MaxPendingPerAccount int           `yaml:"max_pending_per_account"`
	Accounts             []v1Account   `yaml:"accounts"`
	Telegram             v1Telegram    `yaml:"telegram"`
	Discord              v1Discord     `yaml:"discord"`
	MessageTemplate      *v1Template   `yaml:"message_template"`
	PluginDir            string        `yaml:"plugin_dir"`
	PluginTimeout        time.Duration `yaml:"plugin_timeout"`
	PluginMaxInput       int           `yaml:"plugin_max_input"`
	PluginMaxOutput      int           `yaml:"plugin_max_output"`
	ReadButton           bool          `yaml:"read_button"`
}

type v1Account struct {
	Name            string      `yaml:"name"`
	Host            string      `yaml:"host"`
	Port            int         `yaml:"port"`
	Username        string      `yaml:"username"`
	Password        string      `yaml:"password"`
	PasswordFile    string      `yaml:"password_file"`
	OAuth2          *v1OAuth2   `yaml:"oauth2"`
	TLS             bool        `yaml:"tls"`
	Mailbox         string      `yaml:"mailbox"`
	Idle            bool        `yaml:"idle"`
	MarkSeen        bool        `yaml:"mark_seen"`
	NotifyExisting  bool        `yaml:"notify_existing"`
	SendID          bool        `yaml:"send_id"`
	PreviewLen      int         `yaml:"preview_len"`
	Notifiers       []string    `yaml:"notifiers"`
	MessageTemplate *v1Template `yaml:"message_template"`
}

type v1OAuth2 struct {
	ClientID         string `yaml:"client_id"`
	ClientSecret     string `yaml:"client_secret"`
	ClientSecretFile string `yaml:"client_secret_file"`
	TokenFile        string `yaml:"token_file"`
	Scope            string `yaml:"scope"`
	TokenURL         string `yaml:"token_url"`
}

type v1Telegram struct {
	Enabled      bool     `yaml:"enabled"`
	BotToken     string   `yaml:"bot_token"`
	BotTokenFile string   `yaml:"bot_token_file"`
	ChatIDs      []string `yaml:"chat_ids"`
	APIBase      string   `yaml:"api_base"`
}

type v1Discord struct {
	Enabled        bool   `yaml:"enabled"`
	WebhookURL     string `yaml:"webhook_url"`
	WebhookURLFile string `yaml:"webhook_url_file"`
	BotToken       string `yaml:"bot_token"`
	BotTokenFile   string `yaml:"bot_token_file"`
	ChannelID      string `yaml:"channel_id"`
}

type v1Template struct {
	Title string `yaml:"title"`
	Text  string `yaml:"text"`
}

func migrateV1Config(v1 *v1Config) map[string]any {
	out := map[string]any{"version": 2}

	daemon := map[string]any{}
	if v1.PollInterval > 0 {
		daemon["poll_interval"] = v1.PollInterval.String()
	}
	if v1.HealthPort != nil {
		daemon["health_port"] = *v1.HealthPort
	}
	if v1.RetryAttempts > 0 {
		daemon["retry_attempts"] = v1.RetryAttempts
	}
	if v1.RetryDelay > 0 {
		daemon["retry_delay"] = v1.RetryDelay.String()
	}
	if v1.SeenRetention > 0 {
		daemon["seen_retention"] = v1.SeenRetention.String()
	}
	if v1.MaxPendingPerAccount > 0 {
		daemon["max_pending_per_source"] = v1.MaxPendingPerAccount
	}
	out["daemon"] = daemon
	out["state"] = map[string]any{"driver": "sqlite", "file": v1.StateFile}

	var sources []map[string]any
	for _, a := range v1.Accounts {
		src := map[string]any{
			"name":     a.Name,
			"type":     "imap",
			"host":     a.Host,
			"username": a.Username,
		}
		if a.Port != 0 {
			src["port"] = a.Port
		}
		if a.Password != "" {
			src["password"] = a.Password
		}
		if a.PasswordFile != "" {
			src["password_file"] = a.PasswordFile
		}
		if a.OAuth2 != nil {
			oauth := map[string]any{
				"client_id":  a.OAuth2.ClientID,
				"token_file": a.OAuth2.TokenFile,
			}
			if a.OAuth2.ClientSecret != "" {
				oauth["client_secret"] = a.OAuth2.ClientSecret
			}
			if a.OAuth2.ClientSecretFile != "" {
				oauth["client_secret_file"] = a.OAuth2.ClientSecretFile
			}
			if a.OAuth2.Scope != "" {
				oauth["scope"] = a.OAuth2.Scope
			}
			if a.OAuth2.TokenURL != "" {
				oauth["token_url"] = a.OAuth2.TokenURL
			}
			src["oauth2"] = oauth
		}
		if a.TLS {
			src["tls"] = true
		}
		if a.Mailbox != "" {
			src["mailbox"] = a.Mailbox
		}
		if a.Idle {
			src["idle"] = true
		}
		if a.MarkSeen {
			src["mark_seen"] = true
		}
		if a.NotifyExisting {
			src["notify_existing"] = true
		}
		if a.SendID {
			src["send_id"] = true
		}
		if a.PreviewLen > 0 {
			src["preview_len"] = a.PreviewLen
		}
		sources = append(sources, src)
	}
	out["sources"] = sources

	var notifiers []map[string]any
	var defaultTo []string
	if v1.Telegram.Enabled {
		tg := map[string]any{
			"name":        "telegram",
			"type":        "telegram",
			"chat_ids":    v1.Telegram.ChatIDs,
			"interactive": v1.ReadButton,
		}
		if v1.Telegram.BotToken != "" {
			tg["bot_token"] = v1.Telegram.BotToken
		}
		if v1.Telegram.BotTokenFile != "" {
			tg["bot_token_file"] = v1.Telegram.BotTokenFile
		}
		if v1.Telegram.APIBase != "" {
			tg["api_base"] = v1.Telegram.APIBase
		}
		notifiers = append(notifiers, tg)
		defaultTo = append(defaultTo, "telegram")
	}
	if v1.Discord.Enabled {
		dc := map[string]any{
			"name":        "discord",
			"type":        "discord",
			"interactive": v1.ReadButton,
		}
		if v1.Discord.WebhookURL != "" {
			dc["webhook_url"] = v1.Discord.WebhookURL
		}
		if v1.Discord.WebhookURLFile != "" {
			dc["webhook_url_file"] = v1.Discord.WebhookURLFile
		}
		if v1.Discord.BotToken != "" {
			dc["bot_token"] = v1.Discord.BotToken
		}
		if v1.Discord.BotTokenFile != "" {
			dc["bot_token_file"] = v1.Discord.BotTokenFile
		}
		if v1.Discord.ChannelID != "" {
			dc["channel_id"] = v1.Discord.ChannelID
		}
		notifiers = append(notifiers, dc)
		defaultTo = append(defaultTo, "discord")
	}
	out["notifiers"] = notifiers

	var processors []map[string]any
	if v1.PluginDir != "" {
		proc := map[string]any{
			"name": "plugins",
			"type": "wasm",
			"dir":  v1.PluginDir,
		}
		if v1.PluginTimeout > 0 {
			proc["timeout"] = v1.PluginTimeout.String()
		}
		if v1.PluginMaxInput > 0 {
			proc["max_input"] = v1.PluginMaxInput
		}
		if v1.PluginMaxOutput > 0 {
			proc["max_output"] = v1.PluginMaxOutput
		}
		processors = append(processors, proc)
	}
	out["processors"] = processors

	// Templates: v1 message_template.title/text map onto v2 title/body.
	if v1.MessageTemplate != nil {
		out["templates"] = map[string]any{
			"default": map[string]any{
				"title": v1.MessageTemplate.Title,
				"body":  v1.MessageTemplate.Text,
			},
		}
	}

	// Routes: one catch-all preserving the old fan-out. Account-specific
	// notifiers cannot be expressed automatically — v1 notifiers per account
	// become a comment-level decision for the operator.
	route := map[string]any{
		"name": "default",
		"to":   defaultTo,
	}
	if v1.PluginDir != "" {
		route["processors"] = []string{"plugins"}
	}
	if v1.MessageTemplate != nil {
		route["templates"] = "default"
	}
	out["routes"] = []map[string]any{route}

	return out
}
