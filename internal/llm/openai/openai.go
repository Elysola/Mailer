// Package openai implements the core.LLM service against any OpenAI-compatible
// chat completions endpoint: OpenAI itself, DeepSeek, Qwen, Zhipu, Ollama and
// vLLM all speak this dialect. Provider-level concerns (concurrency gate,
// timeout, token metrics) live here so individual AI processors never
// reimplement them.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/elysola/mailer/internal/component"
	"github.com/elysola/mailer/internal/core"
	"github.com/elysola/mailer/internal/metricsx"
)

func init() {
	component.RegisterLLM("openai", New)
}

type Config struct {
	// BaseURL is the endpoint root; /chat/completions is appended.
	BaseURL string        `yaml:"base_url"`
	APIKey  string        `yaml:"api_key"`
	Model   string        `yaml:"model"`
	Timeout time.Duration `yaml:"timeout"`
	// MaxConcurrency caps simultaneous in-flight requests so a burst of
	// incoming mail cannot stampede the API quota. Default 2.
	MaxConcurrency int `yaml:"max_concurrency"`
}

type Provider struct {
	name    string
	cfg     Config
	client  *http.Client
	gate    chan struct{}
	metrics *metricsx.Metrics
}

func New(name string, raw map[string]any, deps component.Deps) (core.LLM, error) {
	var cfg Config
	if err := component.Decode(component.ExpandMap(raw), &cfg); err != nil {
		return nil, fmt.Errorf("llm %q: %w", name, err)
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("llm %q: model is required", name)
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.openai.com/v1"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 20 * time.Second
	}
	if cfg.MaxConcurrency <= 0 {
		cfg.MaxConcurrency = 2
	}
	if deps.Store == nil {
		// LLMs do not need the store themselves; processors use it for caching.
		// Nothing to wire here — noted for clarity.
	}
	return &Provider{
		name:    name,
		cfg:     cfg,
		client:  &http.Client{Timeout: cfg.Timeout},
		gate:    make(chan struct{}, cfg.MaxConcurrency),
		metrics: deps.Metrics,
	}, nil
}

func (p *Provider) Name() string                    { return p.name }
func (p *Provider) Type() string                    { return "openai" }
func (p *Provider) Model() string                   { return p.cfg.Model }
func (p *Provider) Start(ctx context.Context) error { return nil }
func (p *Provider) Stop(ctx context.Context) error  { return nil }

// chatRequest is the OpenAI wire format. Only the fields we use are modelled.
type chatRequest struct {
	Model          string          `json:"model"`
	Messages       []chatMessage   `json:"messages"`
	MaxTokens      int             `json:"max_tokens,omitempty"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// responseFormat requests structured output. `json_schema` is OpenAI's strict
// mode; endpoints that only understand `json_object` get that fallback when
// no schema is given.
type responseFormat struct {
	Type       string          `json:"type"`
	JSONSchema json.RawMessage `json:"json_schema,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (p *Provider) Complete(ctx context.Context, req core.CompletionRequest) (core.CompletionResponse, error) {
	// The concurrency gate is acquired inside Complete rather than by the
	// caller so queuing time is bounded by the caller's context, not the
	// HTTP timeout.
	select {
	case p.gate <- struct{}{}:
		defer func() { <-p.gate }()
	case <-ctx.Done():
		return core.CompletionResponse{}, ctx.Err()
	}

	messages := make([]chatMessage, 0, 2)
	if req.System != "" {
		messages = append(messages, chatMessage{Role: "system", Content: req.System})
	}
	messages = append(messages, chatMessage{Role: "user", Content: req.Prompt})

	wire := chatRequest{
		Model:     p.cfg.Model,
		Messages:  messages,
		MaxTokens: req.MaxTokens,
	}
	if len(req.JSONSchema) > 0 {
		// OpenAI strict structured output wraps the schema in a named object.
		wrapped, err := json.Marshal(map[string]any{
			"name":   "mailer_extraction",
			"strict": true,
			"schema": req.JSONSchema,
		})
		if err != nil {
			return core.CompletionResponse{}, fmt.Errorf("wrap json schema: %w", err)
		}
		wire.ResponseFormat = &responseFormat{Type: "json_schema", JSONSchema: wrapped}
	}

	body, err := json.Marshal(wire)
	if err != nil {
		return core.CompletionResponse{}, fmt.Errorf("marshal request: %w", err)
	}

	endpoint := fmt.Sprintf("%s/chat/completions", trimTrailingSlash(p.cfg.BaseURL))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return core.CompletionResponse{}, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	}

	start := time.Now()
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return core.CompletionResponse{}, fmt.Errorf("llm %s: %w", p.name, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return core.CompletionResponse{}, fmt.Errorf("llm %s: read response: %w", p.name, err)
	}

	if p.metrics != nil {
		p.metrics.Observe("mailer_llm_duration_seconds",
			map[string]string{"provider": p.name, "model": p.cfg.Model}, time.Since(start).Seconds())
	}

	if resp.StatusCode != http.StatusOK {
		return core.CompletionResponse{}, fmt.Errorf("llm %s: HTTP %d: %s", p.name, resp.StatusCode, truncate(string(respBody), 300))
	}

	var decoded chatResponse
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return core.CompletionResponse{}, fmt.Errorf("llm %s: decode response: %w", p.name, err)
	}
	if decoded.Error != nil {
		return core.CompletionResponse{}, fmt.Errorf("llm %s: %s", p.name, decoded.Error.Message)
	}
	if len(decoded.Choices) == 0 {
		return core.CompletionResponse{}, fmt.Errorf("llm %s: no completion choices", p.name)
	}

	if p.metrics != nil {
		labels := map[string]string{"provider": p.name, "model": p.cfg.Model}
		p.metrics.Add("mailer_llm_input_tokens_total", labels, int64(decoded.Usage.PromptTokens))
		p.metrics.Add("mailer_llm_output_tokens_total", labels, int64(decoded.Usage.CompletionTokens))
	}

	return core.CompletionResponse{
		Text:         decoded.Choices[0].Message.Content,
		InputTokens:  decoded.Usage.PromptTokens,
		OutputTokens: decoded.Usage.CompletionTokens,
	}, nil
}

func trimTrailingSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
