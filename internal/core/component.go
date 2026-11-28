package core

import "context"

// Component is the common lifecycle every registered component satisfies.
// The daemon starts all components after wiring and stops them in reverse
// order on shutdown.
type Component interface {
	// Name is the instance name from the config ("tg-ops"), unique within
	// its component kind. It appears in logs, metrics labels and routes.
	Name() string
	// Type is the factory type string ("imap", "telegram", "llmsummarize").
	Type() string
	// Start launches background work. It must not block; anything long-lived
	// runs in goroutines the component owns. Implementations must tolerate
	// being called exactly once.
	Start(ctx context.Context) error
	// Stop releases resources and waits for owned goroutines to wind down.
	// It must be safe to call after a failed Start.
	Stop(ctx context.Context) error
}

// Command is a source-specific write-back operation. The pipeline never
// interprets Kind or Args; only the originating source does.
type Command struct {
	Kind string            `json:"kind"` // e.g. "mark_read"
	Args map[string]string `json:"args,omitempty"`
	// MessageID identifies the target message (the Envelope's Message.ID);
	// the source correlates it with its own Ref data.
	MessageID string `json:"message_id,omitempty"`
}
