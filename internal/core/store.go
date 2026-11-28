package core

import (
	"encoding/json"
	"time"
)

// Store is the persistence port. It is deliberately split into four focused
// concerns; an implementation may provide all of them (SQLite does) or a
// subset, and the pipeline wires what it finds.
type Store interface {
	CursorStore
	DedupStore
	QueueStore
	KVStore
	Close() error
}

// CursorStore persists per-source cursors. The value is opaque to the store:
// a JSON blob the source interprets (IMAP: last UID + UIDVALIDITY).
type CursorStore interface {
	GetCursor(source string) (data []byte, found bool, err error)
	SetCursor(source string, data []byte) error
}

// DedupStore remembers which message IDs were already delivered, so an
// at-least-once pipeline does not re-notify after restarts or cursor resets.
type DedupStore interface {
	IsDuplicate(source, messageID string) (bool, error)
	MarkDelivered(source, messageID string) error
	CleanSeen(before time.Time) error
	SeenCount(source string) (int, error)
}

// QueuedItem is one notification waiting in the delivery queue. It replaces
// the v1 pending row: the envelope travels as a serialized unit, so queue
// entries no longer need a schema migration every time a field is added.
type QueuedItem struct {
	Source string
	// Key is the queue identity inside the source (IMAP: the UID as decimal).
	Key      string
	Envelope Envelope
	// Kind distinguishes "retry" (delivery failed, backoff applies) from
	// "defer" (a processor consciously parked the message).
	Kind string
	// Failed lists the notifier names still owed a delivery; empty means all.
	Failed   []string
	Attempts int
	NextTry  time.Time
	Created  time.Time
}

const (
	QueueKindRetry = "retry"
	QueueKindDefer = "defer"
)

// EncodeEnvelope serializes an envelope for queue storage.
func EncodeEnvelope(e Envelope) ([]byte, error) { return json.Marshal(e) }

// DecodeEnvelope reverses EncodeEnvelope.
func DecodeEnvelope(data []byte) (Envelope, error) {
	var e Envelope
	err := json.Unmarshal(data, &e)
	return e, err
}

type QueueStore interface {
	Enqueue(item QueuedItem) error
	// Due returns items whose backoff/deferral has elapsed, oldest first.
	Due(source string, now time.Time, limit int) ([]QueuedItem, error)
	// AllDue is Due across every source — the retry worker is daemon-global.
	AllDue(now time.Time, limit int) ([]QueuedItem, error)
	UpdateRetry(source, key string, failed []string, attempts int, nextTry time.Time) error
	Delete(source, key string) error
	Count(source string) (int, error)
	// Rows/AllRows power the `mailer queue` operator commands.
	Rows(source string, limit int) ([]QueuedItem, error)
	AllRows(limit int) ([]QueuedItem, error)
	DeleteForSource(source string) (int, error)
}

// KVStore is a namespaced key-value store. Processor instances (in particular
// stateful WASM plugins and the LLM result cache) each get their own
// namespace, so they can neither collide with nor read each other's state.
type KVStore interface {
	KVGet(ns, key string) (value []byte, found bool, err error)
	KVSet(ns, key string, value []byte, ttl time.Duration) error
	KVDelete(ns, key string) error
	// CleanKV drops expired entries; the daemon calls it periodically.
	CleanKV(now time.Time) error
}
