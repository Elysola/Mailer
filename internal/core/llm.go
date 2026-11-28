package core

import "context"

// LLM is an AI model service component. The interface is deliberately
// minimal: single-turn completion covers summarization, extraction and
// classification. Multi-turn chat, streaming and tool-use are over-design for
// mail notifications; if ever needed, they are handled inside a provider, not
// in the shared port.
type LLM interface {
	Component
	// Complete runs a single-turn completion. Processors trim input to
	// MaxRunes before calling; providers should additionally enforce
	// MaxTokens server-side.
	Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error)
	// Model returns the model actually in use, for metric labels and cache keys.
	Model() string
}

type CompletionRequest struct {
	System string
	Prompt string
	// JSONSchema, when non-nil, asks for structured output (OpenAI
	// response_format, or a tool-use emulation). Providers that cannot do
	// structured output fall back to embedding the schema in the prompt and
	// extracting JSON from the response text.
	JSONSchema map[string]any
	MaxTokens  int
}

type CompletionResponse struct {
	Text         string
	InputTokens  int
	OutputTokens int
}
