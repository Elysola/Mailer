package state

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Load(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestGetSetRoundTrip(t *testing.T) {
	s := newStore(t)
	if _, found, err := s.Get("nope"); err != nil || found {
		t.Fatalf("missing row: found=%v err=%v, want false, nil", found, err)
	}
	if err := s.Set("a", AccountState{LastUID: 7, UIDValidity: 3}); err != nil {
		t.Fatal(err)
	}
	st, found, err := s.Get("a")
	if err != nil || !found {
		t.Fatalf("get: found=%v err=%v", found, err)
	}
	if st.LastUID != 7 || st.UIDValidity != 3 {
		t.Errorf("got %+v, want {7 3}", st)
	}
	// Upsert replaces.
	if err := s.Set("a", AccountState{LastUID: 9, UIDValidity: 3}); err != nil {
		t.Fatal(err)
	}
	st, _, _ = s.Get("a")
	if st.LastUID != 9 {
		t.Errorf("LastUID = %d, want 9", st.LastUID)
	}
}

func TestDuplicateLifecycle(t *testing.T) {
	s := newStore(t)
	dup, err := s.IsDuplicate("a", "<m@x>")
	if err != nil || dup {
		t.Fatalf("before mark: dup=%v err=%v", dup, err)
	}
	if err := s.MarkDelivered("a", "<m@x>"); err != nil {
		t.Fatal(err)
	}
	dup, _ = s.IsDuplicate("a", "<m@x>")
	if !dup {
		t.Error("expected duplicate after mark")
	}
	// Different account is independent.
	dup, _ = s.IsDuplicate("b", "<m@x>")
	if dup {
		t.Error("dedup leaked across accounts")
	}
	// Empty Message-ID never dedups and never stores.
	if err := s.MarkDelivered("a", ""); err != nil {
		t.Fatal(err)
	}
	if dup, _ := s.IsDuplicate("a", ""); dup {
		t.Error("empty Message-ID must not dedup")
	}
}

func TestCleanSeen(t *testing.T) {
	s := newStore(t)
	if err := s.MarkDelivered("a", "<old@x>"); err != nil {
		t.Fatal(err)
	}
	// Backdate the row, then prune with cutoff in the future and a
	// second prune that must keep fresh rows.
	if _, err := s.db.Exec(`UPDATE seen_messages SET created_at = 1 WHERE message_id = '<old@x>'`); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkDelivered("a", "<new@x>"); err != nil {
		t.Fatal(err)
	}
	if err := s.CleanSeen(time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if dup, _ := s.IsDuplicate("a", "<old@x>"); dup {
		t.Error("old row survived CleanSeen")
	}
	if dup, _ := s.IsDuplicate("a", "<new@x>"); !dup {
		t.Error("fresh row pruned too early")
	}
}

func TestPendingEnqueueUnionAndPreserve(t *testing.T) {
	s := newStore(t)
	p := Pending{Account: "a", UID: 5, MessageID: "<m@x>", From: "f", Subject: "s", Preview: "pv"}
	if err := s.EnqueuePending(p, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// Second failure on same (account, uid): failed sets union, created and
	// attempts preserved, metadata refreshed.
	first, found, err := s.getPending("a", 5)
	if err != nil || !found {
		t.Fatalf("getPending: %v %v", found, err)
	}
	p2 := p
	p2.Subject = "s2"
	p2.Failed = []string{"telegram"}
	if err := s.EnqueuePending(p2, time.Now().Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdatePendingRetry("a", 5, []string{"telegram"}, 3, time.Now().Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	p3 := p2
	p3.Failed = []string{"discord"}
	if err := s.EnqueuePending(p3, time.Now().Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, _, err := s.getPending("a", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Failed) != 2 || got.Failed[0] != "telegram" || got.Failed[1] != "discord" {
		t.Errorf("failed = %v, want [telegram discord] unioned", got.Failed)
	}
	if got.Attempts != 3 {
		t.Errorf("attempts = %d, want preserved 3", got.Attempts)
	}
	if !got.Created.Equal(first.Created.Truncate(time.Second)) && !got.Created.After(first.Created) {
		t.Errorf("created reset: %v vs %v", got.Created, first.Created)
	}
	if got.Subject != "s2" {
		t.Errorf("subject not refreshed: %q", got.Subject)
	}
	if got.Preview != "pv" {
		t.Errorf("preview lost on re-enqueue: %q", got.Preview)
	}
}

func TestDuePendingRespectsBackoffAndOrder(t *testing.T) {
	s := newStore(t)
	now := time.Now()
	// uid 3 is due; uid 1 is due; uid 2 hides behind future backoff.
	if err := s.EnqueuePending(Pending{Account: "a", UID: 3, MessageID: "<c@x>", Failed: []string{"t"}}, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueuePending(Pending{Account: "a", UID: 1, MessageID: "<a@x>", Failed: []string{"t"}}, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueuePending(Pending{Account: "a", UID: 2, MessageID: "<b@x>", Failed: []string{"t"}}, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// Other account must not leak into this account's queue.
	if err := s.EnqueuePending(Pending{Account: "z", UID: 9, MessageID: "<z@x>", Failed: []string{"t"}}, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}

	due, err := s.DuePending("a", now, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 2 {
		t.Fatalf("due = %d rows, want 2 (uid 2 not yet due)", len(due))
	}
	if due[0].UID != 1 || due[1].UID != 3 {
		t.Errorf("due order = %d,%d want 1,3 (UID ascending)", due[0].UID, due[1].UID)
	}
	if n, _ := s.PendingCount("a"); n != 3 {
		t.Errorf("PendingCount = %d, want 3", n)
	}
	// Limit truncates.
	limited, _ := s.DuePending("a", now, 1)
	if len(limited) != 1 {
		t.Errorf("limit=1 returned %d rows", len(limited))
	}
}

func TestDeletePending(t *testing.T) {
	s := newStore(t)
	if err := s.EnqueuePending(Pending{Account: "a", UID: 4, Failed: []string{"t"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.DeletePending("a", 4); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := s.getPending("a", 4); found {
		t.Error("row survived DeletePending")
	}
	if n, _ := s.PendingCount("a"); n != 0 {
		t.Errorf("count = %d after delete", n)
	}
	// Deleting a missing row is a no-op, not an error.
	if err := s.DeletePending("a", 404); err != nil {
		t.Errorf("delete missing: %v", err)
	}
}

func TestExplicitCreatedHonoredOnlyOnInsert(t *testing.T) {
	s := newStore(t)
	old := time.Now().Add(-10 * 24 * time.Hour)
	if err := s.EnqueuePending(Pending{Account: "a", UID: 1, Created: old, Failed: []string{"t"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.getPending("a", 1)
	if !got.Created.Before(time.Now().Add(-24 * time.Hour)) {
		t.Errorf("explicit Created not stored: %v", got.Created)
	}
	// Re-enqueue keeps the ORIGINAL (old) created.
	if err := s.EnqueuePending(Pending{Account: "a", UID: 1, Created: time.Now(), Failed: []string{"t"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.getPending("a", 1)
	if !got.Created.Equal(old.Truncate(time.Second)) && got.Created.After(time.Now().Add(-time.Hour)) {
		t.Errorf("created overwritten on re-enqueue: %v want ~%v", got.Created, old)
	}
}

func TestPendingPersistsPluginOverrides(t *testing.T) {
	s := newStore(t)
	p := Pending{
		Account:       "a",
		UID:           7,
		MessageID:     "<p@x>",
		Preview:       "pv",
		TitleOverride: "验证码",
		TextOverride:  "🔐 验证码：690831",
		Failed:        []string{"telegram"},
	}
	if err := s.EnqueuePending(p, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}

	got, found, err := s.getPending("a", 7)
	if err != nil || !found {
		t.Fatalf("getPending: found=%v err=%v", found, err)
	}
	if got.TitleOverride != p.TitleOverride {
		t.Errorf("title override = %q, want %q", got.TitleOverride, p.TitleOverride)
	}
	if got.TextOverride != p.TextOverride {
		t.Errorf("text override = %q, want %q", got.TextOverride, p.TextOverride)
	}

	// DuePending must carry them too: retryPending rebuilds the message from it.
	due, err := s.DuePending("a", time.Now(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("due = %d rows, want 1", len(due))
	}
	if due[0].TitleOverride != p.TitleOverride || due[0].TextOverride != p.TextOverride {
		t.Errorf("DuePending dropped overrides: %+v", due[0])
	}
}

func TestLoadMigratesLegacyPendingTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")

	// Build a database shaped like the pre-plugin schema.
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`
CREATE TABLE pending_notifications (
    account      TEXT    NOT NULL,
    uid          INTEGER NOT NULL,
    message_id   TEXT    NOT NULL DEFAULT '',
    sender       TEXT    NOT NULL DEFAULT '',
    subject      TEXT    NOT NULL DEFAULT '',
    date_unix    INTEGER NOT NULL DEFAULT 0,
    preview      TEXT    NOT NULL DEFAULT '',
    failed       TEXT    NOT NULL DEFAULT '',
    attempts     INTEGER NOT NULL DEFAULT 0,
    next_try_unix INTEGER NOT NULL DEFAULT 0,
    created_unix INTEGER NOT NULL DEFAULT (strftime('%s','now')),
    PRIMARY KEY (account, uid)
);
INSERT INTO pending_notifications (account, uid, message_id, preview, failed)
VALUES ('a', 1, '<old@x>', 'legacy preview', 'telegram');
`)
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load on legacy db: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	got, found, err := s.getPending("a", 1)
	if err != nil || !found {
		t.Fatalf("getPending: found=%v err=%v", found, err)
	}
	if got.Preview != "legacy preview" {
		t.Errorf("legacy row lost: %+v", got)
	}
	if got.TitleOverride != "" || got.TextOverride != "" {
		t.Errorf("new columns not defaulted: %+v", got)
	}

	// Re-running Load must be a no-op (idempotent migration).
	s2, err := Load(path)
	if err != nil {
		t.Fatalf("second Load: %v", err)
	}
	t.Cleanup(func() { s2.Close() })
}

// TestPendingAttachmentsRoundTrip guards the JSON column: a notification that
// queued with attachments must still list them when it is retried later.
func TestPendingAttachmentsRoundTrip(t *testing.T) {
	s := newStore(t)
	now := time.Now()
	p := Pending{
		Account: "a", UID: 1, MessageID: "<x@y>", Failed: []string{"telegram"},
		Attachments: []PendingAttachment{
			{Filename: "invoice.pdf", ContentType: "application/pdf", Size: 2048},
			{Filename: "photo.jpg", Size: 5 << 20, Inline: true, ContentID: "img1"},
		},
	}
	if err := s.EnqueuePending(p, now.Add(-time.Minute)); err != nil {
		t.Fatalf("EnqueuePending: %v", err)
	}

	due, err := s.DuePending("a", now, 10)
	if err != nil {
		t.Fatalf("DuePending: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("got %d due, want 1", len(due))
	}
	got := due[0].Attachments
	if len(got) != 2 {
		t.Fatalf("attachments = %v, want 2", got)
	}
	if got[0] != p.Attachments[0] {
		t.Errorf("attachment[0] = %+v, want %+v", got[0], p.Attachments[0])
	}
	if got[1] != p.Attachments[1] {
		t.Errorf("attachment[1] = %+v, want %+v", got[1], p.Attachments[1])
	}
}

// TestPendingWithoutAttachmentsStaysEmpty ensures the new column does not turn
// every queued notification into a non-nil empty slice.
func TestPendingWithoutAttachmentsStaysEmpty(t *testing.T) {
	s := newStore(t)
	now := time.Now()
	if err := s.EnqueuePending(Pending{Account: "a", UID: 2, Failed: []string{"t"}}, now.Add(-time.Minute)); err != nil {
		t.Fatalf("EnqueuePending: %v", err)
	}
	due, err := s.DuePending("a", now, 10)
	if err != nil {
		t.Fatalf("DuePending: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("got %d due, want 1", len(due))
	}
	if due[0].Attachments != nil {
		t.Errorf("Attachments = %v, want nil", due[0].Attachments)
	}
}

// TestDecodeAttachmentsToleratesCorruptJSON keeps a corrupt cache from
// blocking delivery: the mail must still go out, just without the file list.
func TestDecodeAttachmentsToleratesCorruptJSON(t *testing.T) {
	if got := decodeAttachments("{not json"); got != nil {
		t.Errorf("decodeAttachments(corrupt) = %v, want nil", got)
	}
	if got := decodeAttachments(""); got != nil {
		t.Errorf("decodeAttachments(empty) = %v, want nil", got)
	}
}

// TestPendingRowsAndDeleteForAccount covers the inspection helpers the
// `mailer queue` subcommand relies on.
func TestPendingRowsAndDeleteForAccount(t *testing.T) {
	s := newStore(t)
	now := time.Now()
	for _, p := range []Pending{
		{Account: "a", UID: 2, Subject: "second"},
		{Account: "a", UID: 1, Subject: "first"},
		{Account: "b", UID: 9, Subject: "other"},
	} {
		if err := s.EnqueuePending(p, now.Add(time.Hour)); err != nil {
			t.Fatalf("EnqueuePending: %v", err)
		}
	}

	// Unlike DuePending, PendingRows must return rows that are not due yet.
	rows, err := s.PendingRows("a", 10)
	if err != nil {
		t.Fatalf("PendingRows: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (not-yet-due rows must be listed)", len(rows))
	}

	all, err := s.AllPendingRows(10)
	if err != nil {
		t.Fatalf("AllPendingRows: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("all rows = %d, want 3", len(all))
	}

	n, err := s.DeletePendingForAccount("a")
	if err != nil {
		t.Fatalf("DeletePendingForAccount: %v", err)
	}
	if n != 2 {
		t.Errorf("deleted = %d, want 2", n)
	}
	if got, _ := s.PendingCount("a"); got != 0 {
		t.Errorf("pending after delete = %d, want 0", got)
	}
	if got, _ := s.PendingCount("b"); got != 1 {
		t.Errorf("other account was touched: pending = %d, want 1", got)
	}
}

func TestPendingRowsLimit(t *testing.T) {
	s := newStore(t)
	now := time.Now()
	for uid := uint32(1); uid <= 5; uid++ {
		if err := s.EnqueuePending(Pending{Account: "a", UID: uid}, now); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.PendingRows("a", 2)
	if err != nil {
		t.Fatalf("PendingRows: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("rows = %d, want the limit of 2", len(rows))
	}
}

func TestSeenCount(t *testing.T) {
	s := newStore(t)
	if n, err := s.SeenCount("a"); err != nil || n != 0 {
		t.Fatalf("SeenCount = %d, %v; want 0, nil", n, err)
	}
	for _, id := range []string{"<1@x>", "<2@x>"} {
		if err := s.MarkDelivered("a", id); err != nil {
			t.Fatalf("MarkDelivered: %v", err)
		}
	}
	if n, err := s.SeenCount("a"); err != nil || n != 2 {
		t.Errorf("SeenCount = %d, %v; want 2, nil", n, err)
	}
	if n, _ := s.SeenCount("b"); n != 0 {
		t.Errorf("SeenCount for another account = %d, want 0", n)
	}
}
