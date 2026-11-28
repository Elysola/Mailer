// Package core defines the domain types and ports every mailer component is
// built against. It imports nothing outside the standard library: sources,
// notifiers, processors and stores depend on core, never on each other.
package core

import (
	"fmt"
	"strings"
	"time"
)

// Attachment is the metadata of one attached part. The content itself is
// intentionally absent: the daemon's job is to tell you the mail arrived, and
// pulling multi-megabyte payloads over a polling loop would cost far more than
// the notification is worth.
type Attachment struct {
	// Filename is the decoded file name, empty when the sender omitted one.
	Filename string `json:"filename,omitempty"`
	// ContentType is the lowercased MIME type, e.g. "application/pdf".
	ContentType string `json:"content_type,omitempty"`
	// Size is the encoded part size in bytes, as reported by the source.
	Size int64 `json:"size,omitempty"`
	// ContentID is the HTML content id (without angle brackets), set for
	// parts referenced inline by the HTML body.
	ContentID string `json:"content_id,omitempty"`
	// Inline reports a part meant to be displayed within the body rather than
	// listed as a separate attachment.
	Inline bool `json:"inline,omitempty"`
}

// Message is the immutable domain object a source produces. Processors never
// modify it; they return a Verdict the pipeline applies to the Envelope, so
// every pipeline stage has independently observable input and output.
type Message struct {
	// ID is the stable message identifier inside the source (IMAP: the
	// Message-ID header). It keys de-duplication.
	ID string `json:"id"`
	// Source is the name of the source instance that produced the message.
	Source string `json:"source"`
	// Account is the logical account name (for IMAP the account label);
	// usually identical to Source, but a multi-mailbox source may differ.
	Account string `json:"account"`

	From    string            `json:"from"`
	To      []string          `json:"to,omitempty"`
	Cc      []string          `json:"cc,omitempty"`
	Subject string            `json:"subject"`
	Date    time.Time         `json:"date"`
	Text    string            `json:"text,omitempty"`
	HTML    string            `json:"html,omitempty"`
	Preview string            `json:"preview,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`

	Attachments []Attachment `json:"attachments,omitempty"`

	// Ref carries source-specific references the pipeline does not
	// understand. IMAP uses it for the UID and the mailbox, so that Ack and
	// MarkRead commands can be routed back to the right connection. Keys are
	// owned by the producing source.
	Ref map[string]string `json:"ref,omitempty"`
}

// Envelope carries a Message through the pipeline, accumulating the products
// of each stage. Unlike Message it is mutable, but only the pipeline mutates
// it — processors contribute through Verdicts.
type Envelope struct {
	Message Message `json:"message"`

	// Fields holds structured values extracted by processors (an OTP code, a
	// classified category, a parsed amount). Templates address them as
	// {{.Fields.code}}; route rules can match on them.
	Fields map[string]string `json:"fields,omitempty"`

	// Title and Body are the rendered notification text. Empty means "render
	// from templates at delivery time"; a processor or the render stage fills
	// them in.
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`

	// Meta holds pipeline-internal annotations: which processors matched,
	// whether the message arrived on the first run, and so on. It is never
	// rendered into user-visible output directly.
	Meta map[string]string `json:"meta,omitempty"`
}

// NewEnvelope wraps a message in a fresh envelope.
func NewEnvelope(msg Message) Envelope {
	// Fields starts non-nil: processor verdicts merge into it blindly and a
	// nil map would panic the whole pipeline on the first write.
	return Envelope{Message: msg, Fields: map[string]string{}, Meta: map[string]string{}}
}

// Copy returns a deep-enough copy for queue round-trips: the map fields are
// cloned so a mutation after enqueue cannot retroactively change a queued row.
func (e Envelope) Copy() Envelope {
	out := e
	if e.Fields != nil {
		out.Fields = make(map[string]string, len(e.Fields))
		for k, v := range e.Fields {
			out.Fields[k] = v
		}
	}
	if e.Meta != nil {
		out.Meta = make(map[string]string, len(e.Meta))
		for k, v := range e.Meta {
			out.Meta[k] = v
		}
	}
	if e.Message.Ref != nil {
		ref := make(map[string]string, len(e.Message.Ref))
		for k, v := range e.Message.Ref {
			ref[k] = v
		}
		out.Message.Ref = ref
	}
	if e.Message.Headers != nil {
		h := make(map[string]string, len(e.Message.Headers))
		for k, v := range e.Message.Headers {
			h[k] = v
		}
		out.Message.Headers = h
	}
	return out
}

// Field reads an envelope field, treating an absent map and an absent key alike.
func (e Envelope) Field(key string) (string, bool) {
	if e.Fields == nil {
		return "", false
	}
	v, ok := e.Fields[key]
	return v, ok
}

// AttachmentSummary renders the message's attachment list; see the package
// function for details.
func (m Message) AttachmentSummary() string {
	return AttachmentSummary(m.Attachments)
}

// AttachmentSummary renders the attachment list as "name (size)" pairs, or ""
// when the message has none. Names are truncated so a hostile 300-character
// filename cannot push the actual mail out of the notification.
func AttachmentSummary(atts []Attachment) string {
	if len(atts) == 0 {
		return ""
	}
	const maxNameRunes = 60
	parts := make([]string, 0, len(atts))
	for _, a := range atts {
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
