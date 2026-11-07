package mail

import (
	"fmt"
	"strings"
	"text/template"
	"time"
)

// Attachment is the metadata of one attached part. The content itself is
// intentionally absent.
type Attachment struct {
	// Filename is the decoded file name, empty when the sender omitted one.
	Filename string
	// ContentType is the lowercased MIME type, e.g. "application/pdf".
	ContentType string
	// Size is the encoded part size in bytes, as reported by IMAP.
	Size int64
	// ContentID is the HTML content id (without angle brackets), set for
	// parts referenced inline by the HTML body.
	ContentID string
	// Inline reports a part meant to be displayed within the body rather than
	// listed as a separate attachment.
	Inline bool
}

type Message struct {
	Account       string
	UID           uint32
	From          string
	To            []string
	Cc            []string
	Subject       string
	Date          time.Time
	Preview       string
	BodyText      string
	HTML          string
	Headers       map[string]string
	MessageID     string
	TitleTmpl     string
	TextTmpl      string
	TitleOverride string
	TextOverride  string

	// Attachments describes the message's attachments by metadata only. The
	// bytes are never downloaded: the daemon's job is to tell you the mail
	// arrived, and pulling multi-megabyte payloads over a polling loop would
	// cost far more than the notification is worth.
	Attachments []Attachment

	// Fields holds structured values a WASM plugin extracted (for example a
	// "code" from a verification mail). Templates address them as
	// {{.Fields.code}}, so a plugin can feed a user-written template instead
	// of hard-coding its own wording. Nil when no plugin matched.
	Fields map[string]string
}

func (m Message) Title() string {
	if m.TitleOverride != "" {
		return m.TitleOverride
	}
	if m.TitleTmpl != "" {
		return renderTmpl(m.TitleTmpl, m)
	}
	subject := strings.TrimSpace(m.Subject)
	if subject == "" {
		subject = "(no subject)"
	}
	return subject
}

func (m Message) Text() string {
	if m.TextOverride != "" {
		return m.TextOverride
	}
	if m.TextTmpl != "" {
		return renderTmpl(m.TextTmpl, m)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "📬 New mail on %s\n", m.Account)
	fmt.Fprintf(&b, "From: %s\n", orDash(m.From))
	fmt.Fprintf(&b, "Subject: %s\n", orDash(m.Subject))
	if !m.Date.IsZero() {
		fmt.Fprintf(&b, "Date: %s\n", m.Date.Local().Format("2006-01-02 15:04:05"))
	}
	if s := m.AttachmentSummary(); s != "" {
		fmt.Fprintf(&b, "Attachments: %s\n", s)
	}
	if p := strings.TrimSpace(m.Preview); p != "" {
		fmt.Fprintf(&b, "\n%s", p)
	}
	return strings.TrimRight(b.String(), "\n")
}

// AttachmentSummary renders the attachment list as "name (size)" pairs, one
// per line, or "" when the message has none. Names are truncated so a hostile
// 300-character filename cannot push the actual mail out of the notification.
func (m Message) AttachmentSummary() string {
	if len(m.Attachments) == 0 {
		return ""
	}
	const maxNameRunes = 60
	parts := make([]string, 0, len(m.Attachments))
	for _, a := range m.Attachments {
		name := a.Filename
		if name == "" {
			name = a.ContentType
		}
		if r := []rune(name); len(r) > maxNameRunes {
			name = string(r[:maxNameRunes-1]) + "…"
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", name, HumanSize(a.Size)))
	}
	return strings.Join(parts, ", ")
}

// HumanSize formats a byte count the way a mail client would.
func HumanSize(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KB", "MB", "GB"}
	v := float64(n) / 1024
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if v < 10 {
		return fmt.Sprintf("%.1f %s", v, units[i])
	}
	return fmt.Sprintf("%.0f %s", v, units[i])
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func renderTmpl(tmpl string, data any) string {
	t, err := template.New("").Parse(tmpl)
	if err != nil {
		return tmpl
	}
	var buf strings.Builder
	if err := t.Execute(&buf, data); err != nil {
		return tmpl
	}
	return buf.String()
}
