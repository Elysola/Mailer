package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level application configuration.
type Config struct {
	PollInterval         time.Duration    `yaml:"poll_interval"`
	StateFile            string           `yaml:"state_file"`
	HealthPort           int              `yaml:"health_port"`
	RetryAttempts        int              `yaml:"retry_attempts"`
	RetryDelay           time.Duration    `yaml:"retry_delay"`
	NoopInterval         time.Duration    `yaml:"noop_interval"`
	PreviewLen           int              `yaml:"preview_len"`
	SeenRetention        time.Duration    `yaml:"seen_retention"`
	MaxPendingPerAccount int              `yaml:"max_pending_per_account"`
	Accounts             []Account        `yaml:"accounts"`
	Telegram             Telegram         `yaml:"telegram"`
	Discord              Discord          `yaml:"discord"`
	MessageTemplate      *MessageTemplate `yaml:"message_template"`
	PluginDir            string           `yaml:"plugin_dir"`
	PluginTimeout        time.Duration    `yaml:"plugin_timeout"`
	PluginMaxInput       int              `yaml:"plugin_max_input"`
	PluginMaxOutput      int              `yaml:"plugin_max_output"`
	ReadButton           bool             `yaml:"read_button"`
}

// MessageTemplate defines custom notification text layout.
type MessageTemplate struct {
	Title string `yaml:"title"`
	Text  string `yaml:"text"`
}

// Account describes a single IMAP mailbox to poll.
type Account struct {
	Name            string           `yaml:"name"`
	Host            string           `yaml:"host"`
	Port            int              `yaml:"port"`
	Username        string           `yaml:"username"`
	Password        string           `yaml:"password"`
	PasswordFile    string           `yaml:"password_file"`
	TLS             bool             `yaml:"tls"`
	Mailbox         string           `yaml:"mailbox"`
	MarkSeen        bool             `yaml:"mark_seen"`
	NotifyExisting  bool             `yaml:"notify_existing"`
	SendID          bool             `yaml:"send_id"`
	PreviewLen      int              `yaml:"preview_len"`
	Notifiers       []string         `yaml:"notifiers"`
	Discord         *AccountDiscord  `yaml:"discord"`
	MessageTemplate *MessageTemplate `yaml:"message_template"`
}

// AccountDiscord routes a single account's notifications to a Discord channel
// or thread (子区). It overrides the global Discord destination.
type AccountDiscord struct {
	Mode           string `yaml:"mode"`
	ChannelID      string `yaml:"channel_id"`
	ThreadID       string `yaml:"thread_id"`
	WebhookURL     string `yaml:"webhook_url"`
	WebhookURLFile string `yaml:"webhook_url_file"`
}

// Discord routing modes.
const (
	DiscordModeChannel = "channel"
	DiscordModeThread  = "thread"
)

// Telegram holds Telegram Bot API push settings.
type Telegram struct {
	Enabled      bool     `yaml:"enabled"`
	BotToken     string   `yaml:"bot_token"`
	BotTokenFile string   `yaml:"bot_token_file"`
	ChatIDs      []string `yaml:"chat_ids"`
	APIBase      string   `yaml:"api_base"`
}

// Discord holds Discord push settings. Use either a webhook URL, or a bot
// token together with a channel ID.
type Discord struct {
	Enabled        bool   `yaml:"enabled"`
	WebhookURL     string `yaml:"webhook_url"`
	WebhookURLFile string `yaml:"webhook_url_file"`
	BotToken       string `yaml:"bot_token"`
	BotTokenFile   string `yaml:"bot_token_file"`
	ChannelID      string `yaml:"channel_id"`
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

	cfg.applyDefaults()
	if err := cfg.resolveSecrets(); err != nil {
		return nil, err
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.PollInterval <= 0 {
		c.PollInterval = 60 * time.Second
	}
	if c.StateFile == "" {
		c.StateFile = "state.db"
	}
	if c.HealthPort <= 0 {
		c.HealthPort = 9100
	}
	if c.RetryAttempts <= 0 {
		c.RetryAttempts = 2
	}
	if c.RetryDelay <= 0 {
		c.RetryDelay = 5 * time.Second
	}
	if c.NoopInterval <= 0 {
		c.NoopInterval = 30 * time.Second
	}
	if c.PreviewLen <= 0 {
		c.PreviewLen = 400
	}
	if c.SeenRetention <= 0 {
		c.SeenRetention = 90 * 24 * time.Hour
	}
	if c.MaxPendingPerAccount <= 0 {
		c.MaxPendingPerAccount = 200
	}
	if c.PluginTimeout <= 0 {
		c.PluginTimeout = 100 * time.Millisecond
	}
	if c.PluginMaxInput <= 0 {
		c.PluginMaxInput = 512 * 1024
	}
	if c.PluginMaxOutput <= 0 {
		c.PluginMaxOutput = 16 * 1024
	}
	for i := range c.Accounts {
		a := &c.Accounts[i]
		if a.PreviewLen <= 0 {
			a.PreviewLen = c.PreviewLen
		}
		if a.Mailbox == "" {
			a.Mailbox = "INBOX"
		}
		if a.Port == 0 {
			if a.TLS {
				a.Port = 993
			} else {
				a.Port = 143
			}
		}
		if a.Name == "" {
			a.Name = a.Username
		}
		if a.Discord != nil && a.Discord.Mode == "" {
			a.Discord.Mode = DiscordModeChannel
		}
	}
	if c.Telegram.APIBase == "" {
		c.Telegram.APIBase = "https://api.telegram.org"
	}
}

// resolveSecrets reads secrets from referenced files and expands env vars.
// File paths take precedence over inline values; env var expansion is applied
// on top of the final value so users can do either:
//
//	password_file: /run/secrets/imap_pass   # age/gpg encrypted file, pre-decrypted
//	password: ${MAILER_PASS}                 # environment variable
//	password: ${MAILER_PASS:-fallback}       # with default
func (c *Config) resolveSecrets() error {
	readFile := func(path, ctx string) (string, error) {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("%s: read %s: %w", ctx, path, err)
		}
		return strings.TrimSpace(string(data)), nil
	}

	for i := range c.Accounts {
		a := &c.Accounts[i]

		if a.PasswordFile != "" {
			val, err := readFile(a.PasswordFile, a.Name)
			if err != nil {
				return err
			}
			a.Password = val
		}
		a.Password = os.ExpandEnv(a.Password)

		if a.Discord != nil && a.Discord.WebhookURLFile != "" {
			val, err := readFile(a.Discord.WebhookURLFile, a.Name+".discord")
			if err != nil {
				return err
			}
			a.Discord.WebhookURL = val
		}
		if a.Discord != nil {
			a.Discord.WebhookURL = os.ExpandEnv(a.Discord.WebhookURL)
		}
	}

	if c.Telegram.BotTokenFile != "" {
		val, err := readFile(c.Telegram.BotTokenFile, "telegram")
		if err != nil {
			return err
		}
		c.Telegram.BotToken = val
	}
	c.Telegram.BotToken = os.ExpandEnv(c.Telegram.BotToken)

	if c.Discord.BotTokenFile != "" {
		val, err := readFile(c.Discord.BotTokenFile, "discord.bot_token")
		if err != nil {
			return err
		}
		c.Discord.BotToken = val
	}
	c.Discord.BotToken = os.ExpandEnv(c.Discord.BotToken)

	if c.Discord.WebhookURLFile != "" {
		val, err := readFile(c.Discord.WebhookURLFile, "discord.webhook_url")
		if err != nil {
			return err
		}
		c.Discord.WebhookURL = val
	}
	c.Discord.WebhookURL = os.ExpandEnv(c.Discord.WebhookURL)

	return nil
}

func (c *Config) validate() error {
	if len(c.Accounts) == 0 {
		return fmt.Errorf("no accounts configured")
	}
	names := make(map[string]int, len(c.Accounts))
	for i, a := range c.Accounts {
		if a.Host == "" {
			return fmt.Errorf("account %d (%s): host is required", i, a.Name)
		}
		if a.Username == "" || a.Password == "" {
			return fmt.Errorf("account %d (%s): username and password are required", i, a.Name)
		}
		if prev, dup := names[a.Name]; dup {
			return fmt.Errorf("accounts %d and %d share the name %q; names must be unique (used for state, dedup and button callbacks)", prev, i, a.Name)
		}
		names[a.Name] = i
	}
	if !c.Telegram.Enabled && !c.Discord.Enabled {
		return fmt.Errorf("no notifier enabled (enable telegram and/or discord)")
	}
	if c.Telegram.Enabled {
		if c.Telegram.BotToken == "" {
			return fmt.Errorf("telegram enabled but bot_token is empty")
		}
		if len(c.Telegram.ChatIDs) == 0 {
			return fmt.Errorf("telegram enabled but no chat_ids configured")
		}
	}
	if c.Discord.Enabled {
		if c.Discord.WebhookURL == "" && c.Discord.BotToken == "" {
			return fmt.Errorf("discord enabled but neither webhook_url nor bot_token set")
		}
		if err := c.validateDiscordRouting(); err != nil {
			return err
		}
	}
	return nil
}

// validateDiscordRouting ensures every account can resolve a Discord
// destination (either a global default or a per-account override).
func (c *Config) validateDiscordRouting() error {
	globalWebhook := c.Discord.WebhookURL != ""
	globalChannel := c.Discord.ChannelID != ""

	for _, a := range c.Accounts {
		ad := a.Discord
		if ad != nil {
			switch ad.Mode {
			case DiscordModeChannel, DiscordModeThread:
			default:
				return fmt.Errorf("account %q: discord.mode must be %q or %q",
					a.Name, DiscordModeChannel, DiscordModeThread)
			}
			if ad.Mode == DiscordModeThread && ad.ThreadID == "" {
				return fmt.Errorf("account %q: discord.mode=thread requires thread_id", a.Name)
			}
			// A per-account webhook, channel or thread makes it self-contained.
			if ad.WebhookURL != "" || ad.ChannelID != "" || ad.ThreadID != "" {
				continue
			}
		}
		// No usable per-account override: fall back to a global destination.
		if !globalWebhook && !globalChannel {
			return fmt.Errorf("account %q: no discord destination (set a global webhook_url/channel_id or a per-account discord route)", a.Name)
		}
	}
	return nil
}
