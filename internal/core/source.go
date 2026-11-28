package core

import "context"

// Source produces messages. Implementations own their cursor persistence
// (through a CursorStore), because "what has been processed" differs per
// protocol: an IMAP UID, a Gmail historyId, a POP3 UIDL.
type Source interface {
	Component

	// Events returns the stream of message events. Poll-style sources emit
	// in batches after each tick; push-style sources (IDLE, webhooks) emit in
	// real time. The channel closes when the source is stopped.
	Events() <-chan Event

	// Ack confirms a message has been taken care of — delivered, queued for
	// retry, or deliberately dropped. The source advances its cursor past
	// the acked message, using the Ref data it originally attached. Ack must
	// be idempotent: delivery is at-least-once, so de-duplication downstream
	// is the norm, not the exception.
	Ack(ctx context.Context, msg Message) error

	// Command executes a write-back operation originating from a notifier's
	// interactive surface (e.g. a "mark as read" button). Sources that do not
	// support a command kind return ErrUnsupportedCommand.
	Command(ctx context.Context, cmd Command) error
}

// Event is one arrival of a message.
type Event struct {
	Message Message
	// FirstRun is true when the message was seen during the source's first
	// baseline scan. Sources use it to implement notify_existing=false; the
	// pipeline also exposes it to processors via envelope Meta.
	FirstRun bool
}

// ErrUnsupportedCommand is returned by sources that cannot perform a command.
type errUnsupported struct{ kind string }

func (e *errUnsupported) Error() string { return "unsupported command: " + e.kind }

// ErrUnsupportedCommandf formats an unsupported-command error.
func ErrUnsupportedCommandf(kind string) error { return &errUnsupported{kind: kind} }
