// Package telegram is the Telegram notifier component. It registers itself
// under the type "telegram"; multiple instances (different bots or chats) can
// coexist because every instance carries its own config.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/elysola/mailer/internal/component"
	"github.com/elysola/mailer/internal/core"
)

func init() {
	component.RegisterNotifier("telegram", New)
}

const telegramMaxLen = 4096

// maxCallbackData is the Telegram Bot API limit for inline keyboard
// callback_data (bytes). Sources with long names could overflow it.
const maxCallbackData = 64

type Config struct {
	BotToken     string   `yaml:"bot_token"`
	BotTokenFile string   `yaml:"bot_token_file"`
	ChatIDs      []string `yaml:"chat_ids"`
	APIBase      string   `yaml:"api_base"`
	// Interactive enables the "mark as read" inline button and the updates
	// loop that answers its callbacks.
	Interactive bool `yaml:"interactive"`
}

type Notifier struct {
	name      string
	token     string
	chatIDs   []string
	apiBase   string
	client    *http.Client
	readBtn   bool
	commander component.Commander

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New is the component factory for `type: telegram`.
func New(name string, raw map[string]any, deps component.Deps) (core.Notifier, error) {
	var cfg Config
	if err := component.Decode(component.ExpandMap(raw), &cfg); err != nil {
		return nil, fmt.Errorf("telegram notifier %q: %w", name, err)
	}
	token, err := component.Secret(cfg.BotToken, cfg.BotTokenFile)
	if err != nil {
		return nil, fmt.Errorf("telegram notifier %q: %w", name, err)
	}
	if token == "" {
		return nil, fmt.Errorf("telegram notifier %q: bot_token (or bot_token_file) is required", name)
	}
	if len(cfg.ChatIDs) == 0 {
		return nil, fmt.Errorf("telegram notifier %q: chat_ids is required", name)
	}
	if err := validateAPIBase(cfg.APIBase); err != nil {
		return nil, fmt.Errorf("telegram notifier %q: %w", name, err)
	}
	base := strings.TrimRight(cfg.APIBase, "/")
	if base == "" {
		base = "https://api.telegram.org"
	}
	return &Notifier{
		name:      name,
		token:     token,
		chatIDs:   cfg.ChatIDs,
		apiBase:   base,
		client:    &http.Client{Timeout: 20 * time.Second},
		readBtn:   cfg.Interactive,
		commander: deps.Commander,
	}, nil
}

func (t *Notifier) Name() string { return t.name }
func (t *Notifier) Type() string { return "telegram" }

func (t *Notifier) Capabilities() core.Capabilities {
	return core.Capabilities{
		InteractiveButtons: t.readBtn,
		MaxBodyRunes:       telegramMaxLen,
	}
}

func (t *Notifier) Start(ctx context.Context) error {
	if t.readBtn && t.commander != nil {
		t.ctx, t.cancel = context.WithCancel(context.Background())
		t.wg.Add(1)
		go t.updatesLoop()
	} else if t.readBtn {
		log.Printf("[%s] interactive buttons configured but no command router is available; button disabled", t.name)
	}
	return nil
}

func (t *Notifier) Stop(ctx context.Context) error {
	if t.cancel != nil {
		t.cancel()
		t.wg.Wait()
	}
	return nil
}

func (t *Notifier) Send(ctx context.Context, env core.Envelope) error {
	title := env.Title
	if title == "" {
		title = env.Message.Subject
	}
	body := env.Body
	if body == "" {
		body = env.Message.Preview
	}
	text := fmt.Sprintf("*%s*\n%s", escapeMarkdown(title), escapeMarkdownPreserveLinks(body))
	// Telegram counts message length after entity escaping; cap the final
	// payload at the API limit (4096).
	text = capText(text, telegramMaxLen)

	var firstErr error
	for _, chatID := range t.chatIDs {
		if err := t.sendOne(ctx, chatID, text, env); err != nil {
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func (t *Notifier) sendOne(ctx context.Context, chatID, text string, env core.Envelope) error {
	payload := map[string]any{
		"chat_id":                  chatID,
		"text":                     text,
		"parse_mode":               "MarkdownV2",
		"disable_web_page_preview": true,
	}
	if t.readBtn {
		if cb := readCallbackData(env.Message); len(cb) <= maxCallbackData && cb != "" {
			payload["reply_markup"] = map[string]any{
				"inline_keyboard": [][]map[string]string{{
					{"text": "标记已读", "callback_data": cb},
				}},
			}
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	url := fmt.Sprintf("%s/bot%s/sendMessage", t.apiBase, t.token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("telegram request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		// Telegram puts the wait in the JSON body (parameters.retry_after) and
		// not in a Retry-After header, so the body has to be parsed here.
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		var apiErr struct {
			Parameters struct {
				RetryAfter float64 `json:"retry_after"`
			} `json:"parameters"`
		}
		_ = json.Unmarshal(respBody, &apiErr)
		return core.NewRateLimitError(t.Name(), resp, apiErr.Parameters.RetryAfter)
	}

	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("telegram chat %s: status %d: %s", chatID, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return nil
}

type tgUpdate struct {
	UpdateID      int              `json:"update_id"`
	CallbackQuery *tgCallbackQuery `json:"callback_query"`
}

type tgCallbackQuery struct {
	ID   string `json:"id"`
	Data string `json:"data"`
}

type tgAPIResponse struct {
	OK          bool       `json:"ok"`
	Description string     `json:"description"`
	Result      []tgUpdate `json:"result"`
}

func (t *Notifier) updatesLoop() {
	defer t.wg.Done()
	client := &http.Client{Timeout: 45 * time.Second}
	offset := 0
	for {
		updates, err := t.getUpdates(client, offset)
		if err != nil {
			if t.ctx.Err() != nil {
				return
			}
			log.Printf("[%s] getUpdates: %v (retry in 5s)", t.name, err)
			select {
			case <-t.ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}
		for _, u := range updates {
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			if u.CallbackQuery != nil {
				t.handleCallback(u.CallbackQuery)
			}
		}
	}
}

func (t *Notifier) getUpdates(client *http.Client, offset int) ([]tgUpdate, error) {
	body, _ := json.Marshal(map[string]any{
		"offset":          offset,
		"timeout":         25,
		"allowed_updates": []string{"callback_query"},
	})
	url := fmt.Sprintf("%s/bot%s/getUpdates", t.apiBase, t.token)
	req, err := http.NewRequestWithContext(t.ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var out tgAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if !out.OK {
		return nil, fmt.Errorf("api error: %s", out.Description)
	}
	return out.Result, nil
}

func (t *Notifier) handleCallback(cb *tgCallbackQuery) {
	source, uid, ok := parseReadCallback(cb.Data)
	if !ok || t.commander == nil {
		t.answerCallbackQuery(cb.ID, "未知操作")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := core.Command{
		Kind: "mark_read",
		Args: map[string]string{"uid": fmt.Sprintf("%d", uid)},
	}
	if err := t.commander.Command(ctx, source, cmd); err != nil {
		log.Printf("[%s] mark read %s:%d: %v", t.name, source, uid, err)
		t.answerCallbackQuery(cb.ID, "❌ 标记失败，请稍后重试")
		return
	}
	log.Printf("[%s] marked %s:%d as read", t.name, source, uid)
	t.answerCallbackQuery(cb.ID, "✅ 已标记为已读")
}

func (t *Notifier) answerCallbackQuery(id, text string) {
	body, _ := json.Marshal(map[string]any{
		"callback_query_id": id,
		"text":              text,
	})
	url := fmt.Sprintf("%s/bot%s/answerCallbackQuery", t.apiBase, t.token)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		log.Printf("[%s] answerCallbackQuery: %v", t.name, err)
		return
	}
	resp.Body.Close()
}

// readCallbackData encodes a source+uid pair into a button callback id. It is
// empty when the message carries no uid ref (non-IMAP sources), in which case
// the button is omitted entirely.
func readCallbackData(msg core.Message) string {
	uid := msg.Ref["uid"]
	if uid == "" || msg.Source == "" {
		return ""
	}
	return fmt.Sprintf("read:%s:%s", msg.Source, uid)
}

// parseReadCallback decodes a callback id back into source+uid.
func parseReadCallback(data string) (string, uint64, bool) {
	const prefix = "read:"
	rest, ok := strings.CutPrefix(data, prefix)
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

// capText limits s to at most n runes, appending "…" when cut. Platform text
// limits (Telegram 4096) are defined in runes/chars.
func capText(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return ""
	}
	return strings.TrimSpace(string(r[:n-1])) + "…"
}

func escapeMarkdown(s string) string {
	replacer := strings.NewReplacer(
		"_", "\\_",
		"*", "\\*",
		"`", "\\`",
		"[", "\\[",
		"]", "\\]",
		"(", "\\(",
		")", "\\)",
		"~", "\\~",
		">", "\\>",
		"#", "\\#",
		"+", "\\+",
		"-", "\\-",
		"=", "\\=",
		"|", "\\|",
		"{", "\\{",
		"}", "\\}",
		".", "\\.",
		"!", "\\!",
	)
	return replacer.Replace(s)
}

var mdLinkRe = regexp.MustCompile(`\[([^\]]*)\]\(([^)]*)\)`)

func escapeMarkdownPreserveLinks(s string) string {
	type link struct {
		placeholder string
		original    string
	}
	var links []link
	i := 0
	result := mdLinkRe.ReplaceAllStringFunc(s, func(match string) string {
		ph := fmt.Sprintf("\x00L%d\x00", i)
		links = append(links, link{placeholder: ph, original: match})
		i++
		return ph
	})
	result = escapeMarkdown(result)
	for _, l := range links {
		result = strings.Replace(result, l.placeholder, l.original, 1)
	}
	return result
}

// validateAPIBase rejects a Telegram API base that would leak the bot token:
// the token is interpolated into the URL path, so a plain http scheme would
// put the credential on the wire in clear text, and a base with a path or
// query would silently mangle every request URL.
func validateAPIBase(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("api_base %q is not a valid URL: %w", raw, err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return fmt.Errorf("api_base must use https (got %q): the bot token is sent in the request path", u.Scheme)
	}
	if u.Scheme != "https" && !isLoopback(u.Host) {
		return fmt.Errorf("api_base must use https (got %q): the bot token is sent in the request path", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("api_base %q has no host", raw)
	}
	if p := strings.Trim(u.Path, "/"); p != "" {
		return fmt.Errorf("api_base %q must not have a path (got %q)", raw, u.Path)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("api_base %q must not have a query or fragment", raw)
	}
	return nil
}

// isLoopback tolerates plain http for a local Bot API server, where the
// credential never leaves the machine.
func isLoopback(host string) bool {
	host = strings.SplitN(host, ":", 2)[0]
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}
