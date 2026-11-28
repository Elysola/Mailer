// Package discord is the Discord notifier component, registered under the
// type "discord". Delivery is a single destination per instance; per-account
// routing to different channels or threads is expressed in the v2 route
// table by defining multiple discord instances.
package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/elysola/mailer/internal/component"
	"github.com/elysola/mailer/internal/core"
)

func init() {
	component.RegisterNotifier("discord", New)
}

const discordMaxLen = 4000 // embed description limit; title is capped at 256

type Config struct {
	WebhookURL string `yaml:"webhook_url"`
	BotToken   string `yaml:"bot_token"`
	// ChannelID is the bot-mode target. A thread id works here too: a thread
	// is a channel as far as the REST API is concerned.
	ChannelID string `yaml:"channel_id"`
	// ThreadID posts a webhook-mode message into the given thread (子区).
	ThreadID    string `yaml:"thread_id"`
	Interactive bool   `yaml:"interactive"`
}

type Notifier struct {
	name      string
	cfg       Config
	client    *http.Client
	commander component.Commander

	ctx    context.Context
	cancel context.CancelFunc
}

// New is the component factory for `type: discord`.
func New(name string, raw map[string]any, deps component.Deps) (core.Notifier, error) {
	var cfg Config
	if err := component.Decode(component.ExpandMap(raw), &cfg); err != nil {
		return nil, fmt.Errorf("discord notifier %q: %w", name, err)
	}
	if cfg.BotToken == "" && cfg.WebhookURL == "" {
		return nil, fmt.Errorf("discord notifier %q: webhook_url or bot_token is required", name)
	}
	if cfg.WebhookURL == "" && cfg.ChannelID == "" {
		return nil, fmt.Errorf("discord notifier %q: channel_id is required for bot delivery", name)
	}
	if cfg.Interactive && cfg.BotToken == "" {
		log.Printf("[%s] read button requires bot_token to handle interactions; button will be inert", name)
	}
	return &Notifier{
		name:      name,
		cfg:       cfg,
		client:    &http.Client{Timeout: 20 * time.Second},
		commander: deps.Commander,
	}, nil
}

func (d *Notifier) Name() string { return d.name }
func (d *Notifier) Type() string { return "discord" }

func (d *Notifier) Capabilities() core.Capabilities {
	return core.Capabilities{
		InteractiveButtons: d.cfg.Interactive && d.cfg.BotToken != "" && d.commander != nil,
		MaxBodyRunes:       discordMaxLen,
	}
}

func (d *Notifier) Start(ctx context.Context) error {
	if d.cfg.BotToken != "" {
		d.ctx, d.cancel = context.WithCancel(context.Background())
		intents := 0
		if d.cfg.Interactive {
			// GUILD_MESSAGES | DIRECT_MESSAGES: deliver message component interactions.
			intents = 1<<9 | 1<<12
		}
		startGateway(d.ctx, d.cfg.BotToken, intents, d.handleInteraction)
	}
	return nil
}

func (d *Notifier) Stop(ctx context.Context) error {
	if d.cancel != nil {
		d.cancel()
	}
	return nil
}

// Send delivers the message as a Discord embed.
func (d *Notifier) Send(ctx context.Context, env core.Envelope) error {
	title := env.Title
	if title == "" {
		title = env.Message.Subject
	}
	body := env.Body
	if body == "" {
		body = env.Message.Preview
	}

	embed := map[string]any{
		"title":       truncateField(title, 256),
		"description": truncateField(body, discordMaxLen),
		"color":       0x2ecc71,
	}
	if !env.Message.Date.IsZero() {
		embed["timestamp"] = env.Message.Date.UTC().Format(time.RFC3339)
	}
	payload := map[string]any{"embeds": []any{embed}}
	if d.Capabilities().InteractiveButtons {
		if cb := readCallbackData(env.Message); cb != "" && len(cb) <= 100 { // Discord custom_id limit
			payload["components"] = []any{
				map[string]any{
					"type": 1,
					"components": []any{
						map[string]any{
							"type":      2,
							"style":     1,
							"label":     "标记已读",
							"custom_id": cb,
						},
					},
				},
			}
		} else if cb != "" {
			log.Printf("[%s] custom_id too long (%d bytes), read button omitted", d.name, len(cb))
		}
	}
	bodyJSON, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	endpoint, auth := d.endpoint()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyJSON))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("discord request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		var apiErr struct {
			RetryAfter float64 `json:"retry_after"`
			Global     bool    `json:"global"`
		}
		_ = json.Unmarshal(respBody, &apiErr)
		if apiErr.Global {
			// A global limit covers every channel of this bot, not just the one
			// we were posting to, so it is worth its own log line.
			log.Printf("[%s] global rate limit hit, waiting before any further send", d.name)
		}
		return core.NewRateLimitError(d.Name(), resp, apiErr.RetryAfter)
	}

	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("discord: status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// endpoint returns the target URL and optional Authorization header.
func (d *Notifier) endpoint() (string, string) {
	if d.cfg.WebhookURL == "" {
		u := fmt.Sprintf("https://discord.com/api/v10/channels/%s/messages", d.cfg.ChannelID)
		return u, "Bot " + d.cfg.BotToken
	}
	u := d.cfg.WebhookURL
	if d.cfg.ThreadID != "" {
		parsed, err := url.Parse(u)
		if err == nil {
			q := parsed.Query()
			q.Set("thread_id", d.cfg.ThreadID)
			parsed.RawQuery = q.Encode()
			u = parsed.String()
		}
	}
	return u, ""
}

type gwInteraction struct {
	ID            string `json:"id"`
	Type          int    `json:"type"`
	Token         string `json:"token"`
	ApplicationID string `json:"application_id"`
	Data          struct {
		CustomID string `json:"custom_id"`
	} `json:"data"`
}

func (d *Notifier) handleInteraction(ctx context.Context, raw json.RawMessage) {
	var it gwInteraction
	if err := json.Unmarshal(raw, &it); err != nil {
		return
	}
	if it.Type != 3 { // MESSAGE_COMPONENT
		return
	}

	// Acknowledge deferred within the 3s limit, then do the work async.
	d.ackInteraction(it.ID, it.Token)

	source, uid, ok := parseReadCallback(it.Data.CustomID)
	if !ok || d.commander == nil {
		return
	}
	go func() {
		mctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		text := "✅ 已标记为已读"
		cmd := core.Command{Kind: "mark_read", Args: map[string]string{"uid": fmt.Sprintf("%d", uid)}}
		if err := d.commander.Command(mctx, source, cmd); err != nil {
			log.Printf("[%s] mark read %s:%d: %v", d.name, source, uid, err)
			text = "❌ 标记失败，请稍后重试"
			d.sendFollowup(it.ApplicationID, it.Token, text)
			return
		}
		log.Printf("[%s] marked %s:%d as read", d.name, source, uid)
		d.sendFollowup(it.ApplicationID, it.Token, text)
	}()
}

func (d *Notifier) ackInteraction(id, token string) {
	body, _ := json.Marshal(map[string]any{"type": 6}) // DEFERRED_UPDATE_MESSAGE
	d.postJSON(fmt.Sprintf("https://discord.com/api/v10/interactions/%s/%s/callback", id, token), body)
}

func (d *Notifier) sendFollowup(appID, token, content string) {
	body, _ := json.Marshal(map[string]any{"content": content})
	d.postJSON(fmt.Sprintf("https://discord.com/api/v10/webhooks/%s/%s", appID, token), body)
}

func (d *Notifier) postJSON(url string, body []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bot "+d.cfg.BotToken)
	resp, err := d.client.Do(req)
	if err != nil {
		log.Printf("[%s] api call %s failed: %v", d.name, url, err)
		return
	}
	resp.Body.Close()
}

// readCallbackData encodes a source+uid pair into a button callback id. It is
// empty when the message carries no uid ref, in which case the button is
// omitted entirely.
func readCallbackData(msg core.Message) string {
	uid := msg.Ref["uid"]
	if uid == "" || msg.Source == "" {
		return ""
	}
	return fmt.Sprintf("read:%s:%s", msg.Source, uid)
}

// parseReadCallback decodes a callback id back into source+uid.
func parseReadCallback(data string) (string, uint64, bool) {
	rest, ok := strings.CutPrefix(data, "read:")
	if !ok {
		return "", 0, false
	}
	idx := strings.LastIndex(rest, ":")
	if idx <= 0 {
		return "", 0, false
	}
	var uid uint64
	if _, err := fmt.Sscanf(rest[idx+1:], "%d", &uid); err != nil {
		return "", 0, false
	}
	return rest[:idx], uid, true
}

func truncateField(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}
