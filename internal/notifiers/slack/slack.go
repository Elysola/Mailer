// Package slack is the Slack notifier component, built on incoming webhooks:
// the simplest Slack integration, needing no bot tokens or OAuth flows.
// Workspaces that need threads or reactions can reach Slack through the
// generic webhook notifier with a custom body template instead.
package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/elysola/mailer/internal/component"
	"github.com/elysola/mailer/internal/core"
)

func init() {
	component.RegisterNotifier("slack", New)
}

// slackMaxRunes is Slack's practical message text limit (the API documents
// 4000 for the text field of a block).
const slackMaxRunes = 4000

type Config struct {
	// WebhookURL is the incoming webhook URL from the Slack app directory.
	// It is a credential: anyone holding it can post to the channel.
	WebhookURL     string `yaml:"webhook_url"`
	WebhookURLFile string `yaml:"webhook_url_file"`
	// Channel overrides the webhook's default channel ("#alerts" or
	// "@user"). Requires the webhook to allow overrides.
	Channel string `yaml:"channel"`
	// Username is the display name the messages appear under.
	Username string `yaml:"username"`
	// IconEmoji customises the bot avatar, e.g. ":mailbox:".
	IconEmoji string `yaml:"icon_emoji"`
}

type Notifier struct {
	name      string
	url       string
	channel   string
	username  string
	iconEmoji string
	client    *http.Client
}

// New is the component factory for `type: slack`.
func New(name string, raw map[string]any, deps component.Deps) (core.Notifier, error) {
	var cfg Config
	if err := component.Decode(component.ExpandMap(raw), &cfg); err != nil {
		return nil, fmt.Errorf("slack notifier %q: %w", name, err)
	}
	u, err := component.Secret(cfg.WebhookURL, cfg.WebhookURLFile)
	if err != nil {
		return nil, fmt.Errorf("slack notifier %q: %w", name, err)
	}
	if u == "" {
		return nil, fmt.Errorf("slack notifier %q: webhook_url is required", name)
	}
	if !strings.HasPrefix(u, "https://") && !isLoopback(u) {
		return nil, fmt.Errorf("slack notifier %q: webhook url must be https — the url is a credential (plain http allowed only for loopback)", name)
	}
	return &Notifier{
		name:      name,
		url:       u,
		channel:   cfg.Channel,
		username:  cfg.Username,
		iconEmoji: cfg.IconEmoji,
		client:    &http.Client{Timeout: 20 * time.Second},
	}, nil
}

// isLoopback mirrors the webhook notifier's exception: a webhook pointed at
// a local relay never leaves the machine, so http is safe there.
func isLoopback(u string) bool {
	return strings.Contains(u, "//127.") || strings.Contains(u, "//localhost") ||
		strings.Contains(u, "//[::1]")
}

func (n *Notifier) Name() string { return n.name }
func (n *Notifier) Type() string { return "slack" }

func (n *Notifier) Capabilities() core.Capabilities {
	return core.Capabilities{MaxBodyRunes: slackMaxRunes}
}

func (n *Notifier) Start(context.Context) error { return nil }
func (n *Notifier) Stop(context.Context) error  { return nil }

func (n *Notifier) Send(ctx context.Context, env core.Envelope) error {
	title := env.Title
	if title == "" {
		title = env.Message.Subject
	}
	body := env.Body
	if body == "" {
		body = env.Message.Preview
	}
	text := "*" + title + "*\n" + body
	if r := []rune(text); len(r) > slackMaxRunes {
		text = string(r[:slackMaxRunes-1]) + "…"
	}

	payload := map[string]any{"text": text}
	if n.channel != "" {
		payload["channel"] = n.channel
	}
	if n.username != "" {
		payload["username"] = n.username
	}
	if n.iconEmoji != "" {
		payload["icon_emoji"] = n.iconEmoji
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("slack request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		// Slack honours the Retry-After header on 429s, which the shared
		// constructor reads.
		return core.NewRateLimitError(n.Name(), resp, 0)
	}
	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("slack: status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return nil
}
