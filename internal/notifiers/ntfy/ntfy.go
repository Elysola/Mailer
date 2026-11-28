// Package ntfy is the ntfy.sh notifier component. ntfy is the natural fit
// for self-hosters: one binary or the public hosted service, topics instead
// of bot tokens, and first-class support for priorities, tags and action
// buttons over plain HTTP.
package ntfy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/elysola/mailer/internal/component"
	"github.com/elysola/mailer/internal/core"
)

func init() {
	component.RegisterNotifier("ntfy", New)
}

type Config struct {
	// Server defaults to https://ntfy.sh; self-hosted instances set their own.
	Server string `yaml:"server"`
	// Topic is the ntfy topic — effectively the credential, since anyone who
	// knows it can subscribe. Treat it like a secret.
	Topic     string `yaml:"topic"`
	TopicFile string `yaml:"topic_file"`
	// Token is the bearer token for protected topics on ntfy.sh or a
	// self-hosted server with auth enabled.
	Token     string `yaml:"token"`
	TokenFile string `yaml:"token_file"`
	// User/Password enable basic auth instead of a bearer token.
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	// Priority 1-5, ntfy semantics: 1 min, 3 default, 5 max (bypasses mute
	// on many clients).
	Priority int `yaml:"priority"`
	// Tags add emoji/keywords to the notification, e.g. ["email", "warning"].
	Tags []string `yaml:"tags"`
}

type Notifier struct {
	name     string
	server   string
	topic    string
	token    string
	user     string
	password string
	priority int
	tags     []string
	client   *http.Client
}

// New is the component factory for `type: ntfy`.
func New(name string, raw map[string]any, deps component.Deps) (core.Notifier, error) {
	var cfg Config
	if err := component.Decode(component.ExpandMap(raw), &cfg); err != nil {
		return nil, fmt.Errorf("ntfy notifier %q: %w", name, err)
	}
	topic, err := component.Secret(cfg.Topic, cfg.TopicFile)
	if err != nil {
		return nil, fmt.Errorf("ntfy notifier %q: %w", name, err)
	}
	if topic == "" {
		return nil, fmt.Errorf("ntfy notifier %q: topic is required", name)
	}
	token, err := component.Secret(cfg.Token, cfg.TokenFile)
	if err != nil {
		return nil, fmt.Errorf("ntfy notifier %q: %w", name, err)
	}
	server := strings.TrimRight(cfg.Server, "/")
	if server == "" {
		server = "https://ntfy.sh"
	}
	if strings.HasPrefix(server, "http://") && !strings.Contains(server, "//127.") &&
		!strings.Contains(server, "//localhost") {
		return nil, fmt.Errorf("ntfy notifier %q: plain http servers leak the topic and body; use https (loopback excepted)", name)
	}
	prio := cfg.Priority
	if prio != 0 && (prio < 1 || prio > 5) {
		return nil, fmt.Errorf("ntfy notifier %q: priority must be 1-5, got %d", name, prio)
	}
	return &Notifier{
		name:     name,
		server:   server,
		topic:    topic,
		token:    token,
		user:     cfg.User,
		password: cfg.Password,
		priority: prio,
		tags:     cfg.Tags,
		client:   &http.Client{Timeout: 20 * time.Second},
	}, nil
}

func (n *Notifier) Name() string { return n.name }
func (n *Notifier) Type() string { return "ntfy" }

func (n *Notifier) Capabilities() core.Capabilities {
	return core.Capabilities{MaxBodyRunes: 4096}
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

	u := fmt.Sprintf("%s/%s", n.server, url.PathEscape(n.topic))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	// ntfy headers are ASCII-only; titles come from mail and can contain
	// anything, so non-ASCII bytes are percent-encoded per RFC 2047-ish
	// practice of ntfy clients.
	req.Header.Set("Title", encodeHeader(title))
	if n.priority != 0 {
		req.Header.Set("Priority", fmt.Sprintf("%d", n.priority))
	}
	if len(n.tags) > 0 {
		req.Header.Set("Tags", strings.Join(n.tags, ","))
	}
	if n.token != "" {
		req.Header.Set("Authorization", "Bearer "+n.token)
	} else if n.user != "" {
		req.SetBasicAuth(n.user, n.password)
	}

	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("ntfy request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return core.NewRateLimitError(n.Name(), resp, 0)
	}
	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("ntfy: status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return nil
}

// encodeHeader makes an arbitrary Unicode string safe for an HTTP header
// value: ntfy expects UTF-8 in headers but Go's http transport rejects some
// control bytes, and servers differ in tolerance. Percent-encoding non-ASCII
// runes keeps every byte in the ASCII printable range.
func encodeHeader(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 0x20 && r < 0x7f {
			b.WriteRune(r)
		} else {
			for _, c := range []byte(string(r)) {
				fmt.Fprintf(&b, "%%%02X", c)
			}
		}
	}
	return b.String()
}
