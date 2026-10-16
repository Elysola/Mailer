package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/elysola/mailer/internal/config"
	"github.com/elysola/mailer/internal/mail"
)

const telegramMaxLen = 4096

// maxCallbackData is the Telegram Bot API limit for inline keyboard
// callback_data (bytes). Accounts with long names could overflow it.
const maxCallbackData = 64

type Telegram struct {
	token    string
	chatIDs  []string
	apiBase  string
	client   *http.Client
	readBtn  bool
	markRead MarkReadFunc

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func NewTelegram(cfg config.Telegram, readBtn bool, markRead MarkReadFunc) *Telegram {
	base := strings.TrimRight(cfg.APIBase, "/")
	if base == "" {
		base = "https://api.telegram.org"
	}
	t := &Telegram{
		token:    cfg.BotToken,
		chatIDs:  cfg.ChatIDs,
		apiBase:  base,
		client:   &http.Client{Timeout: 20 * time.Second},
		readBtn:  readBtn,
		markRead: markRead,
	}
	if readBtn {
		t.ctx, t.cancel = context.WithCancel(context.Background())
		t.wg.Add(1)
		go t.updatesLoop()
	}
	return t
}

func (t *Telegram) Close() {
	if t.cancel != nil {
		t.cancel()
		t.wg.Wait()
	}
}

func (t *Telegram) Name() string { return "telegram" }

func (t *Telegram) Send(ctx context.Context, msg mail.Message) error {
	text := fmt.Sprintf("*%s*\n%s", escapeMarkdown(msg.Title()), escapeMarkdownPreserveLinks(msg.Text()))
	// Telegram counts message length after entity escaping; cap the final
	// payload at the API limit (4096).
	text = capText(text, telegramMaxLen)

	var firstErr error
	for _, chatID := range t.chatIDs {
		if err := t.sendOne(ctx, chatID, text, msg); err != nil {
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func (t *Telegram) sendOne(ctx context.Context, chatID, text string, msg mail.Message) error {
	payload := map[string]any{
		"chat_id":                  chatID,
		"text":                     text,
		"parse_mode":               "MarkdownV2",
		"disable_web_page_preview": true,
	}
	if t.readBtn {
		if cb := readCallbackData(msg.Account, msg.UID); len(cb) <= maxCallbackData {
			payload["reply_markup"] = map[string]any{
				"inline_keyboard": [][]map[string]string{{
					{"text": "标记已读", "callback_data": cb},
				}},
			}
		} else {
			log.Printf("[telegram] account %q: callback_data too long (%d bytes), read button omitted", msg.Account, len(cb))
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

func (t *Telegram) updatesLoop() {
	defer t.wg.Done()
	client := &http.Client{Timeout: 45 * time.Second}
	offset := 0
	for {
		updates, err := t.getUpdates(client, offset)
		if err != nil {
			if t.ctx.Err() != nil {
				return
			}
			log.Printf("[telegram] getUpdates: %v (retry in 5s)", err)
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

func (t *Telegram) getUpdates(client *http.Client, offset int) ([]tgUpdate, error) {
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

func (t *Telegram) handleCallback(cb *tgCallbackQuery) {
	account, uid, ok := parseReadCallback(cb.Data)
	if !ok || t.markRead == nil {
		t.answerCallbackQuery(cb.ID, "未知操作")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := t.markRead(ctx, account, uid); err != nil {
		log.Printf("[telegram] mark read %s:%d: %v", account, uid, err)
		t.answerCallbackQuery(cb.ID, "❌ 标记失败，请稍后重试")
		return
	}
	log.Printf("[telegram] marked %s:%d as read", account, uid)
	t.answerCallbackQuery(cb.ID, "✅ 已标记为已读")
}

func (t *Telegram) answerCallbackQuery(id, text string) {
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
		log.Printf("[telegram] answerCallbackQuery: %v", err)
		return
	}
	resp.Body.Close()
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
