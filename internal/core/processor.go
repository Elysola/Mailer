package core

import (
	"context"
	"time"
)

// Processor is a pipeline middle stage. WASM plugins, built-in filters,
// external commands and AI processors all implement this one interface and
// can be freely interleaved in the processor chain.
type Processor interface {
	Component
	Process(ctx context.Context, env Envelope) (Verdict, error)
}

// Action is the disposal a Verdict asks the pipeline to perform.
type Action string

const (
	// ActionContinue passes the envelope to the next stage. The zero value,
	// so a Verdict that only carries fields/title/body needs no explicit action.
	ActionContinue Action = ""
	// ActionDrop discards the message entirely (spam, noise). The pipeline
	// acks it — the message was seen and consciously rejected.
	ActionDrop Action = "drop"
	// ActionDefer parks the message in the queue for later delivery. The
	// pipeline acks it; the queue owns redelivery at DeferUntil.
	ActionDefer Action = "defer"
	// ActionFail treats processing as a delivery failure: the pipeline
	// enqueues the envelope for a full retry, because downstream behaviour
	// depends on the processor's output. This is the semantics behind
	// `on_error: fail` — use it only when continuing without the processor's
	// result would be wrong, never merely when the result is missing.
	ActionFail Action = "fail"
)

// Verdict is a processor's output: a disposal plus optional mutations the
// pipeline applies to the envelope. Replacing the v1 "rewrite the text"
// single-purpose result with an explicit disposal is what turns plugins from
// text rewriters into real pipeline participants.
type Verdict struct {
	Action Action `json:"action,omitempty"`

	// Title and Body override the rendered notification text.
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`

	// Fields merge into the envelope's Fields map. Later processors win on
	// key conflicts (and the pipeline counts conflicts as a metric).
	Fields map[string]string `json:"fields,omitempty"`

	// Route is the processor's routing suggestion: notifier instance names.
	// It is only a signal — route rules decide whether to honour it
	// (via adopt_processor_route), keeping the config the single source of
	// truth for delivery policy.
	Route []string `json:"route,omitempty"`

	// DeferUntil sets the redelivery time when Action is defer.
	DeferUntil time.Time `json:"defer_until,omitempty"`
}
