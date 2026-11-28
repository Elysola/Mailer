// Package render turns a processed envelope into the final title and body
// text, applying the route's Go templates when present. Rendering is a pure
// pipeline stage: it reads the envelope and writes Title/Body, nothing else.
package render

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"

	"github.com/elysola/mailer/internal/core"
)

// Data is the view templates address. It flattens message fields so a
// template can say {{.From}} or {{.Fields.code}} without knowing which side
// the value came from. Processor-written fields live under .Fields exactly as
// they did in v1, so existing user templates keep working.
type Data struct {
	ID          string
	Source      string
	Account     string
	From        string
	To          []string
	Cc          []string
	Subject     string
	Date        string // preformatted, empty when unset
	Preview     string
	Text        string
	HTML        string
	Attachments string // pre-rendered summary
	Fields      map[string]string
}

func view(env core.Envelope) Data {
	m := env.Message
	var date string
	if !m.Date.IsZero() {
		date = m.Date.Local().Format("2006-01-02 15:04:05")
	}
	return Data{
		ID:          m.ID,
		Source:      m.Source,
		Account:     m.Account,
		From:        m.From,
		To:          m.To,
		Cc:          m.Cc,
		Subject:     m.Subject,
		Date:        date,
		Preview:     m.Preview,
		Text:        m.Text,
		HTML:        m.HTML,
		Attachments: m.AttachmentSummary(),
		Fields:      env.Fields,
	}
}

func renderTmpl(tmpl string, data Data) string {
	t, err := template.New("").Parse(tmpl)
	if err != nil {
		return fmt.Sprintf("(template error: %v)", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return fmt.Sprintf("(template error: %v)", err)
	}
	return strings.TrimSpace(buf.String())
}

// Render computes env.Title and env.Body in place. Overrides already on the
// envelope (written by a processor) win over templates; templates win over
// the defaults.
//
// The defaults reproduce the v1 output exactly: the subject (or "(no
// subject)") as title and the "📬 New mail on <account>" block as body.
func Render(env *core.Envelope, titleTmpl, bodyTmpl string) {
	data := view(*env)

	if env.Title == "" {
		switch {
		case titleTmpl != "":
			env.Title = renderTmpl(titleTmpl, data)
		default:
			env.Title = strings.TrimSpace(env.Message.Subject)
			if env.Title == "" {
				env.Title = "(no subject)"
			}
		}
	}

	if env.Body == "" {
		switch {
		case bodyTmpl != "":
			env.Body = renderTmpl(bodyTmpl, data)
		default:
			env.Body = defaultBody(data)
		}
	}
}

func defaultBody(d Data) string {
	var b strings.Builder
	fmt.Fprintf(&b, "📬 New mail on %s\n", d.Account)
	fmt.Fprintf(&b, "From: %s\n", orDash(d.From))
	fmt.Fprintf(&b, "Subject: %s\n", orDash(d.Subject))
	if d.Date != "" {
		fmt.Fprintf(&b, "Date: %s\n", d.Date)
	}
	if d.Attachments != "" {
		fmt.Fprintf(&b, "Attachments: %s\n", d.Attachments)
	}
	if p := strings.TrimSpace(d.Preview); p != "" {
		fmt.Fprintf(&b, "\n%s", p)
	}
	return strings.TrimRight(b.String(), "\n")
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}
