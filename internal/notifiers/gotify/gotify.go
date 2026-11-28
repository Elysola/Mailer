// Package gotify is the notifier for Gotify, the self-hosted push service.
// Gotify's API is deliberately minimal — one POST per message — which makes
// this the simplest notifier in the tree and a good reference implementation
// for the contract: construction-time validation, rate-limit surfacing,
// credential secrecy.
package gotify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/elysola/mailer/internal/component"
	"github.com/elysola/mailer/internal/core"
)

func init() {
	component.RegisterNotifier("gotify", New)
}

type Config struct {
	// URL is the server base, e.g. https://push.example.com.
	URL string `yaml:"url"`
	// Token is the application token — the credential, so the file
	// indirection is the recommended form.
	Token     string `yaml:"token"`
	TokenFile string `yaml:"token_file"`
	// Priority follows Gotify's 0-10 scale; the app defaults control what
	// buzzes and what stays silent.
	Priority int `yaml:"priority"`
	// Timeout caps each send attempt.
	Timeout time.Duration `yaml:"timeout"`
}

type Notifier struct {
	name     string
	url      string
	token    string
	priority int
	client   *http.Client
}

// New is the component factory for `type: gotify`.
func New(name string, raw map[string]any, deps component.Deps) (core.Notifier, error) {
	var cfg Config
	if err := component.Decode(component.ExpandMap(raw), &cfg); err != nil {
		return nil, fmt.Errorf("gotify notifier %q: %w", name, err)
	}
	tok, err := component.Secret(cfg.Token, cfg.TokenFile)
	if err != nil {
		return nil, fmt.Errorf("gotify notifier %q: %w", name, err)
	}
	cfg.Token = tok
	if cfg.URL == "" {
		return nil, fmt.Errorf("gotify notifier %q: url is required", name)
	}
	if cfg.Token == "" {
		return nil, fmt.Errorf("gotify notifier %q: token or token_file is required", name)
	}
	if !strings.HasPrefix(cfg.URL, "https://") && !isLoopback(cfg.URL) {
		return nil, fmt.Errorf("gotify notifier %q: url must be https — the token travels as a query parameter (plain http allowed only for loopback)", name)
	}
	if cfg.Priority < 0 || cfg.Priority > 10 {
		return nil, fmt.Errorf("gotify notifier %q: priority must be 0..10", name)
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	return &Notifier{
		name:     name,
		url:      strings.TrimRight(cfg.URL, "/"),
		token:    cfg.Token,
		priority: cfg.Priority,
		client:   &http.Client{Timeout: cfg.Timeout},
	}, nil
}

func isLoopback(u string) bool {
	return strings.Contains(u, "//127.") || strings.Contains(u, "//localhost") ||
		strings.Contains(u, "//[::1]")
}

func (n *Notifier) Name() string                { return n.name }
func (n *Notifier) Type() string                { return "gotify" }
func (n *Notifier) Start(context.Context) error { return nil }
func (n *Notifier) Stop(context.Context) error  { return nil }

func (n *Notifier) Capabilities() core.Capabilities {
	return core.Capabilities{} // send-only: no buttons, no edits
}

func (n *Notifier) Send(ctx context.Context, env core.Envelope) error {
	title := env.Title
	if title == "" {
		title = env.Message.Subject
	}
	body, _ := json.Marshal(map[string]any{
		"title":    title,
		"message":  env.Body,
		"priority": n.priority,
	})
	url := fmt.Sprintf("%s/message?token=%s", n.url, n.token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return core.NewRateLimitError(n.Name(), resp, 0)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("gotify auth failed (status %d) — check the application token", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("gotify send failed: status %d", resp.StatusCode)
	}
	return nil
}
