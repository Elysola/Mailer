// Package notify delivers mail notifications to chat platforms.
package notify

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/elysola/mailer/internal/mail"
)

// Notifier delivers a single mail notification to a destination.
type Notifier interface {
	// Name identifies the notifier in logs.
	Name() string
	// Send delivers the message. It should be safe to call concurrently.
	Send(ctx context.Context, msg mail.Message) error
}

// MarkReadFunc marks a mail message as read on its IMAP server.
type MarkReadFunc func(ctx context.Context, account string, uid uint32) error

// Closer stops background listeners (e.g. interactive buttons) of a Notifier.
type Closer interface {
	Close()
}

// readCallbackData encodes an account+UID pair into a button callback id.
func readCallbackData(account string, uid uint32) string {
	return fmt.Sprintf("read:%s:%d", account, uid)
}

// cap limits s to at most n runes, appending "…" when cut. Platform text
// limits (Telegram 4096, Discord embed fields) are defined in runes/chars.
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

// parseReadCallback decodes a callback id back into account+UID.
func parseReadCallback(data string) (string, uint32, bool) {
	const prefix = "read:"
	rest, ok := strings.CutPrefix(data, prefix)
	if !ok {
		return "", 0, false
	}
	idx := strings.LastIndex(rest, ":")
	if idx <= 0 {
		return "", 0, false
	}
	uid, err := strconv.ParseUint(rest[idx+1:], 10, 32)
	if err != nil {
		return "", 0, false
	}
	return rest[:idx], uint32(uid), true
}
