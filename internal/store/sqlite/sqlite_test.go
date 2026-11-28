package sqlite

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/elysola/mailer/internal/core"
)

func testEnvelope(source, uid string) core.Envelope {
	msg := core.Message{
		ID:      "<m" + uid + "@x>",
		Source:  source,
		Account: source,
		From:    "a@b.c",
		Subject: "subj" + uid,
		Preview: "preview",
		Ref:     map[string]string{"uid": uid},
	}
	return core.NewEnvelope(msg)
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Load(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestCursorRoundTrip(t *testing.T) {
	st := openTestStore(t)
	if _, ok, _ := st.GetCursor("a"); ok {
		t.Fatal("fresh database should have no cursor")
	}
	data := []byte(`{"last_uid":42,"uid_validity":7}`)
	if err := st.SetCursor("a", data); err != nil {
		t.Fatalf("SetCursor: %v", err)
	}
	got, ok, err := st.GetCursor("a")
	if err != nil || !ok {
		t.Fatalf("GetCursor ok=%v err=%v", ok, err)
	}
	if string(got) != string(data) {
		t.Errorf("cursor = %s, want %s", got, data)
	}
}

func TestDedupAndCleanSeen(t *testing.T) {
	st := openTestStore(t)
	dup, err := st.IsDuplicate("a", "<m1@x>")
	if err != nil || dup {
		t.Fatalf("new message must not be duplicate: %v %v", dup, err)
	}
	if err := st.MarkDelivered("a", "<m1@x>"); err != nil {
		t.Fatalf("MarkDelivered: %v", err)
	}
	dup, _ = st.IsDuplicate("a", "<m1@x>")
	if !dup {
		t.Fatal("delivered message must be duplicate")
	}
	// Per-source isolation: the same message ID under another source is new.
	dup, _ = st.IsDuplicate("b", "<m1@x>")
	if dup {
		t.Fatal("dedup must be per-source")
	}
	if n, _ := st.SeenCount("a"); n != 1 {
		t.Fatalf("SeenCount = %d, want 1", n)
	}
	// Retention cleanup drops rows older than the cutoff only.
	if err := st.CleanSeen(time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("CleanSeen: %v", err)
	}
	if n, _ := st.SeenCount("a"); n != 1 {
		t.Fatal("fresh row must survive cleanup")
	}
	if err := st.CleanSeen(time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("CleanSeen: %v", err)
	}
	if n, _ := st.SeenCount("a"); n != 0 {
		t.Fatal("old row must be cleaned")
	}
}

func TestQueueLifecycle(t *testing.T) {
	st := openTestStore(t)
	item := core.QueuedItem{
		Source:   "a",
		Key:      "1",
		Envelope: testEnvelope("a", "1"),
		Failed:   []string{"tg"},
		Attempts: 1,
		NextTry:  time.Now().Add(-time.Minute),
		Kind:     core.QueueKindRetry,
	}
	if err := st.Enqueue(item); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	due, err := st.Due("a", time.Now(), 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("Due = %d err=%v, want 1", len(due), err)
	}
	if due[0].Envelope.Message.Subject != "subj1" {
		t.Errorf("payload round-trip broken: %q", due[0].Envelope.Message.Subject)
	}
	if len(due[0].Failed) != 1 || due[0].Failed[0] != "tg" {
		t.Errorf("failed notifiers lost: %v", due[0].Failed)
	}

	// Not-yet-due rows are invisible.
	if err := st.Enqueue(core.QueuedItem{
		Source:   "a",
		Key:      "2",
		Envelope: testEnvelope("a", "2"),
		NextTry:  time.Now().Add(time.Hour),
		Kind:     core.QueueKindRetry,
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	due, _ = st.Due("a", time.Now(), 10)
	if len(due) != 1 {
		t.Fatalf("Due should exclude future rows, got %d", len(due))
	}

	// Enqueue merge: re-enqueueing the same key unions the failed sets.
	if err := st.Enqueue(core.QueuedItem{
		Source:   "a",
		Key:      "1",
		Envelope: testEnvelope("a", "1"),
		Failed:   []string{"dc"},
		NextTry:  time.Now(),
		Kind:     core.QueueKindRetry,
	}); err != nil {
		t.Fatalf("Enqueue merge: %v", err)
	}
	rows, _ := st.Rows("a", 10)
	var merged core.QueuedItem
	for _, r := range rows {
		if r.Envelope.Message.Ref["uid"] == "1" {
			merged = r
		}
	}
	if len(merged.Failed) != 2 {
		t.Errorf("merge should union failed sets, got %v", merged.Failed)
	}

	if err := st.Delete("a", merged.Envelope.Message.Ref["uid"]); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	rows, _ = st.Rows("a", 10)
	if len(rows) != 1 {
		t.Fatalf("Rows after delete = %d, want 1", len(rows))
	}
}

func TestQueueKeySelection(t *testing.T) {
	// uid ref wins, then message ID, then timestamp — see store docs.
	st := openTestStore(t)
	item := core.QueuedItem{
		Source:   "a",
		Key:      "9",
		Envelope: testEnvelope("a", "9"),
		NextTry:  time.Now(),
		Kind:     core.QueueKindDefer,
	}
	if err := st.Enqueue(item); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	due, _ := st.Due("a", time.Now(), 1)
	if len(due) != 1 || due[0].Kind != core.QueueKindDefer {
		t.Fatalf("kind must round-trip: %+v", due)
	}
}

func TestKV(t *testing.T) {
	st := openTestStore(t)
	if _, ok, _ := st.KVGet("llmcache", "k"); ok {
		t.Fatal("empty KV must miss")
	}
	if err := st.KVSet("llmcache", "k", []byte("v"), time.Hour); err != nil {
		t.Fatalf("KVSet: %v", err)
	}
	got, ok, err := st.KVGet("llmcache", "k")
	if err != nil || !ok || string(got) != "v" {
		t.Fatalf("KVGet = %q ok=%v err=%v", got, ok, err)
	}
	// Expired entries read as missing even before CleanKV runs. Insert one
	// directly because KVSet treats non-positive TTLs as "never expires".
	if _, err := st.db.Exec(`INSERT INTO kv (ns, key, value, expires_at) VALUES ('llmcache', 'old', 'x', ?)`, time.Now().Add(-time.Hour).Unix()); err != nil {
		t.Fatalf("insert expired kv: %v", err)
	}
	if _, ok, _ := st.KVGet("llmcache", "old"); ok {
		t.Fatal("expired entry must read as missing")
	}
	if err := st.CleanKV(time.Now()); err != nil {
		t.Fatalf("CleanKV: %v", err)
	}
}

// TestMigrateV1 builds a v1-shaped database by hand, then loads it through
// the v2 store and verifies cursor/dedup/queue data survived the migration.
func TestMigrateV1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_, err = db.Exec(`
CREATE TABLE account_state (
    account TEXT PRIMARY KEY,
    last_uid INTEGER NOT NULL DEFAULT 0,
    uid_validity INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE seen_messages (
    message_id TEXT NOT NULL,
    account TEXT NOT NULL,
    created_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
    PRIMARY KEY (message_id, account)
);
CREATE TABLE pending_notifications (
    account TEXT NOT NULL,
    uid INTEGER NOT NULL,
    message_id TEXT,
    sender TEXT,
    subject TEXT,
    date_unix INTEGER,
    preview TEXT,
    failed TEXT,
    attempts INTEGER NOT NULL DEFAULT 0,
    next_try_unix INTEGER NOT NULL DEFAULT 0,
    created_unix INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (account, uid)
);
INSERT INTO account_state VALUES ('work', 123, 456);
INSERT INTO seen_messages (message_id, account) VALUES ('<old@x>', 'work');
INSERT INTO pending_notifications
    (account, uid, message_id, sender, subject, date_unix, preview, failed, attempts, next_try_unix, created_unix)
VALUES ('work', 77, '<p@x>', 'a@b.c', 'pending mail', 1700000000, 'pv', 'tg', 2, 1700000100, 1700000000);
`)
	if err != nil {
		t.Fatalf("seed v1: %v", err)
	}
	db.Close()

	st, err := Load(path)
	if err != nil {
		t.Fatalf("Load with migration: %v", err)
	}
	defer st.Close()

	// Cursor migrated into the opaque-blob format.
	cur, ok, err := st.GetCursor("work")
	if err != nil || !ok {
		t.Fatalf("cursor lost in migration: ok=%v err=%v", ok, err)
	}
	var decoded struct {
		LastUID     uint32 `json:"last_uid"`
		UIDValidity uint32 `json:"uid_validity"`
	}
	if err := json.Unmarshal(cur, &decoded); err != nil {
		t.Fatalf("cursor blob not the expected JSON shape: %v", err)
	}
	if decoded.LastUID != 123 || decoded.UIDValidity != 456 {
		t.Errorf("cursor = %+v, want uid 123 validity 456", decoded)
	}

	// Dedup survived.
	dup, _ := st.IsDuplicate("work", "<old@x>")
	if !dup {
		t.Error("seen row lost in migration")
	}

	// Pending row became a queue entry with the original timestamps.
	rows, err := st.Rows("work", 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("queue rows = %d err=%v, want 1", len(rows), err)
	}
	row := rows[0]
	if row.Envelope.Message.Subject != "pending mail" {
		t.Errorf("subject = %q", row.Envelope.Message.Subject)
	}
	if row.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", row.Attempts)
	}

	// Legacy tables are gone.
	for _, tbl := range []string{"account_state", "pending_notifications"} {
		var n int
		if err := st.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&n); err != nil {
			t.Fatalf("inspect schema: %v", err)
		}
		if n != 0 {
			t.Errorf("legacy table %s survived migration", tbl)
		}
	}

	// The migrated database must remain loadable (idempotent migration).
	st.Close()
	st2, err := Load(path)
	if err != nil {
		t.Fatalf("reload migrated database: %v", err)
	}
	st2.Close()
}
