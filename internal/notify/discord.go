package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"time"

	"github.com/elysola/mailer/internal/config"
	"github.com/elysola/mailer/internal/mail"
)

// discordDest is a resolved delivery target for one account.
type discordDest struct {
	useBot     bool
	channelID  string // bot mode: channel OR thread id (a thread is a channel)
	webhookURL string // webhook mode base URL
	threadID   string // webhook mode: post into this thread (子区)
}

func (d discordDest) valid() bool {
	if d.useBot {
		return d.channelID != ""
	}
	return d.webhookURL != ""
}

// Discord pushes notifications via webhook or bot REST API, with optional
// per-account routing to a specific channel or thread.
type Discord struct {
	botToken string
	def      discordDest            // global default destination
	routes   map[string]discordDest // per-account overrides, keyed by account name
	client   *http.Client
	readBtn  bool
	markRead MarkReadFunc

	ctx    context.Context
	cancel context.CancelFunc
}

// NewDiscord builds a Discord notifier. The accounts slice is used to resolve
// per-account channel/thread routing. If a bot token is configured, a Gateway
// connection is started in the background to show the bot as online and, when
// readBtn is enabled, to handle message component interactions.
func NewDiscord(cfg config.Discord, accounts []config.Account, readBtn bool, markRead MarkReadFunc) (*Discord, error) {
	d := &Discord{
		botToken: cfg.BotToken,
		def:      resolveDest(cfg, nil),
		routes:   make(map[string]discordDest),
		client:   &http.Client{Timeout: 20 * time.Second},
		readBtn:  readBtn,
		markRead: markRead,
	}

	for _, a := range accounts {
		if a.Discord == nil {
			continue
		}
		dest := resolveDest(cfg, a.Discord)
		if !dest.valid() {
			return nil, fmt.Errorf("discord: account %q resolves to an empty destination", a.Name)
		}
		d.routes[a.Name] = dest
	}

	if readBtn && cfg.BotToken == "" {
		log.Print("[discord] read button requires bot_token to handle interactions; button will be inert")
	}

	if cfg.BotToken != "" {
		d.ctx, d.cancel = context.WithCancel(context.Background())
		intents := 0
		if readBtn {
			// GUILD_MESSAGES | DIRECT_MESSAGES: deliver message component interactions.
			intents = 1<<9 | 1<<12
		}
		startGateway(d.ctx, cfg.BotToken, intents, d.handleInteraction)
	}

	return d, nil
}

func (d *Discord) Close() {
	if d.cancel != nil {
		d.cancel()
	}
}

// resolveDest computes the delivery target from the global config and an
// optional per-account override.
func resolveDest(cfg config.Discord, ad *config.AccountDiscord) discordDest {
	webhook := cfg.WebhookURL
	if ad != nil && ad.WebhookURL != "" {
		webhook = ad.WebhookURL
	}

	// Webhook transport takes precedence when a URL is available.
	if webhook != "" {
		dest := discordDest{useBot: false, webhookURL: webhook}
		if ad != nil && ad.Mode == config.DiscordModeThread {
			dest.threadID = ad.ThreadID
		}
		return dest
	}

	// Bot transport: a thread is itself a channel, so we just pick the right ID.
	channelID := cfg.ChannelID
	if ad != nil {
		switch {
		case ad.Mode == config.DiscordModeThread && ad.ThreadID != "":
			channelID = ad.ThreadID
		case ad.ChannelID != "":
			channelID = ad.ChannelID
		}
	}
	return discordDest{useBot: true, channelID: channelID}
}

// Name implements Notifier.
func (d *Discord) Name() string { return "discord" }

// Send delivers the message as a Discord embed to the account's destination.
func (d *Discord) Send(ctx context.Context, msg mail.Message) error {
	dest, ok := d.routes[msg.Account]
	if !ok {
		dest = d.def
	}
	if !dest.valid() {
		return fmt.Errorf("discord: no destination for account %q", msg.Account)
	}

	embed := map[string]any{
		"title":       truncateField(msg.Title(), 256),
		"description": truncateField(msg.Text(), 4000),
		"color":       0x2ecc71,
	}
	if !msg.Date.IsZero() {
		embed["timestamp"] = msg.Date.UTC().Format(time.RFC3339)
	}
	payload := map[string]any{"embeds": []any{embed}}
	if d.readBtn {
		if cb := readCallbackData(msg.Account, msg.UID); len(cb) <= 100 { // Discord custom_id limit
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
		} else {
			log.Printf("[discord] account %q: custom_id too long (%d bytes), read button omitted", msg.Account, len(cb))
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	endpoint, auth := d.endpoint(dest)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
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

	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("discord: status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// endpoint returns the target URL and optional Authorization header.
func (d *Discord) endpoint(dest discordDest) (string, string) {
	if dest.useBot {
		u := fmt.Sprintf("https://discord.com/api/v10/channels/%s/messages", dest.channelID)
		return u, "Bot " + d.botToken
	}
	u := dest.webhookURL
	if dest.threadID != "" {
		parsed, err := url.Parse(u)
		if err == nil {
			q := parsed.Query()
			q.Set("thread_id", dest.threadID)
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

func (d *Discord) handleInteraction(ctx context.Context, raw json.RawMessage) {
	var it gwInteraction
	if err := json.Unmarshal(raw, &it); err != nil {
		return
	}
	if it.Type != 3 { // MESSAGE_COMPONENT
		return
	}

	// Acknowledge deferred within the 3s limit, then do the work async.
	d.ackInteraction(it.ID, it.Token)

	account, uid, ok := parseReadCallback(it.Data.CustomID)
	if !ok || d.markRead == nil {
		return
	}
	go func() {
		mctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		text := "✅ 已标记为已读"
		if err := d.markRead(mctx, account, uid); err != nil {
			log.Printf("[discord] mark read %s:%d: %v", account, uid, err)
			text = "❌ 标记失败，请稍后重试"
			d.sendFollowup(it.ApplicationID, it.Token, text)
			return
		}
		log.Printf("[discord] marked %s:%d as read", account, uid)
		d.sendFollowup(it.ApplicationID, it.Token, text)
	}()
}

func (d *Discord) ackInteraction(id, token string) {
	body, _ := json.Marshal(map[string]any{"type": 6}) // DEFERRED_UPDATE_MESSAGE
	d.postJSON(fmt.Sprintf("https://discord.com/api/v10/interactions/%s/%s/callback", id, token), body)
}

func (d *Discord) sendFollowup(appID, token, content string) {
	body, _ := json.Marshal(map[string]any{"content": content})
	d.postJSON(fmt.Sprintf("https://discord.com/api/v10/webhooks/%s/%s", appID, token), body)
}

func (d *Discord) postJSON(url string, body []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bot "+d.botToken)
	resp, err := d.client.Do(req)
	if err != nil {
		log.Printf("[discord] api call %s failed: %v", url, err)
		return
	}
	resp.Body.Close()
}

func truncateField(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}
