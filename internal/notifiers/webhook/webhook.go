// Package webhook is the generic outbound webhook notifier. It POSTs the
// envelope as JSON (or a user-templated body) to any HTTP endpoint, covering
// the long tail of services that accept webhooks — home automation, ticket
// systems, custom bots — without a per-service component.
package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"text/template"
	"time"

	"github.com/elysola/mailer/internal/component"
	"github.com/elysola/mailer/internal/core"
)

func init() {
	component.RegisterNotifier("webhook", New)
}

type Config struct {
	URL     string `yaml:"url"`
	URLFile string `yaml:"url_file"`
	// Method defaults to POST. GET makes little sense for notifications but
	// some IoT endpoints insist on PUT.
	Method string `yaml:"method"`
	// Headers are static HTTP headers (Authorization tokens, API keys).
	// Values support ${ENV} expansion via the shared secret machinery.
	Headers map[string]string `yaml:"headers"`
	// Body is an optional Go template producing the request body. When empty,
	// the envelope is marshalled as JSON.
	Body string `yaml:"body"`
	// ContentType defaults to application/json. Set it when body is a custom
	// template producing something else.
	ContentType string `yaml:"content_type"`
	// SuccessStatus is the range of HTTP statuses considered delivered.
	// Defaults to 2xx. Some endpoints answer 204, some 200, some 302 — a
	// single int redefines the expectation entirely.
	SuccessStatus int `yaml:"success_status"`
}

type Notifier struct {
	name    string
	url     string
	method  string
	headers map[string]string
	tmpl    *template.Template
	body    string
	ctype   string
	success int
	client  *http.Client
}

// New is the component factory for `type: webhook`.
func New(name string, raw map[string]any, deps component.Deps) (core.Notifier, error) {
	var cfg Config
	if err := component.Decode(component.ExpandMap(raw), &cfg); err != nil {
		return nil, fmt.Errorf("webhook notifier %q: %w", name, err)
	}
	u, err := component.Secret(cfg.URL, cfg.URLFile)
	if err != nil {
		return nil, fmt.Errorf("webhook notifier %q: %w", name, err)
	}
	if u == "" {
		return nil, fmt.Errorf("webhook notifier %q: url is required", name)
	}
	// Webhook URLs embed credentials more often than not (discord.com/api/
	// webhooks/..., hooks.slack.com/...); requiring https protects them in
	// transit. Plain http is allowed only for loopback targets, where the
	// traffic never leaves the machine.
	if strings.HasPrefix(u, "http://") && !isLoopback(u) {
		return nil, fmt.Errorf("webhook notifier %q: plain http is only allowed for loopback targets (use https)", name)
	}
	method := strings.ToUpper(cfg.Method)
	if method == "" {
		method = http.MethodPost
	}
	n := &Notifier{
		name:    name,
		url:     u,
		method:  method,
		headers: cfg.Headers,
		body:    cfg.Body,
		ctype:   cfg.ContentType,
		success: cfg.SuccessStatus,
		client:  &http.Client{Timeout: 20 * time.Second},
	}
	if cfg.Body != "" {
		// The template feeds an HTTP body, not a terminal: text/template is
		// correct here (html/template would entity-escape JSON into garbage).
		// The `json` helper is the escape hatch — use it for every
		// interpolated string inside a JSON body so quotes and newlines in a
		// mail subject cannot corrupt the payload or inject fields:
		//
		//   body: '{"text": {{.Envelope.Message.Subject | json}}}'
		funcs := template.FuncMap{
			"json": func(v any) (string, error) {
				b, err := json.Marshal(v)
				return string(b), err
			},
		}
		t, err := template.New("body").Funcs(funcs).Parse(cfg.Body)
		if err != nil {
			return nil, fmt.Errorf("webhook notifier %q: bad body template: %w", name, err)
		}
		n.tmpl = t
	}
	if n.ctype == "" {
		n.ctype = "application/json"
	}
	return n, nil
}

func isLoopback(u string) bool {
	return strings.Contains(u, "//127.") || strings.Contains(u, "//localhost") ||
		strings.Contains(u, "//[::1]")
}

func (n *Notifier) Name() string { return n.name }
func (n *Notifier) Type() string { return "webhook" }

func (n *Notifier) Capabilities() core.Capabilities {
	// Webhooks are fire-and-forget: no callbacks, no buttons.
	return core.Capabilities{MaxBodyRunes: 0}
}

func (n *Notifier) Start(context.Context) error { return nil }
func (n *Notifier) Stop(context.Context) error  { return nil }

func (n *Notifier) Send(ctx context.Context, env core.Envelope) error {
	var payload []byte
	var err error
	if n.tmpl != nil {
		var buf bytes.Buffer
		if err = n.tmpl.Execute(&buf, env); err != nil {
			return fmt.Errorf("body template: %w", err)
		}
		payload = buf.Bytes()
	} else {
		payload, err = json.Marshal(struct {
			Title    string        `json:"title"`
			Body     string        `json:"body"`
			Envelope core.Envelope `json:"envelope"`
		}{Title: env.Title, Body: env.Body, Envelope: env})
		if err != nil {
			return fmt.Errorf("marshal envelope: %w", err)
		}
	}

	req, err := http.NewRequestWithContext(ctx, n.method, n.url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", n.ctype)
	for k, v := range n.headers {
		req.Header.Set(k, v)
	}

	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return core.NewRateLimitError(n.Name(), resp, 0)
	}
	ok := resp.StatusCode >= 200 && resp.StatusCode < 300
	if n.success != 0 {
		ok = resp.StatusCode == n.success
	}
	if !ok {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("webhook: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
