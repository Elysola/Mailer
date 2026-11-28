package maildir

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/elysola/mailer/internal/component"
	"github.com/elysola/mailer/internal/core"
	"github.com/elysola/mailer/internal/store/sqlite"
)

// setup builds a scratch maildir plus a sqlite store for the cursor.
func setup(t *testing.T) (dir string, store core.Store) {
	t.Helper()
	root := t.TempDir()
	dir = filepath.Join(root, "mail")
	for _, sub := range []string{"new", "cur", "tmp"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	st, err := sqlite.Load(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return dir, st
}

const sampleMail = `From: Alice <alice@example.com>
To: bob@example.com
Subject: Hello =?UTF-8?Q?=E4=B8=96=E7=95=8C?=
Date: Mon, 02 Jan 2024 15:04:05 +0000
Message-Id: <abc123@example.com>

This is the body text.
`

func writeMail(t *testing.T, dir, name, content string) {
	t.Helper()
	// Real delivery writes to tmp/ then links into new/ — replicate the
	// atomic-publish shape so the scan sees complete files.
	tmp := filepath.Join(dir, "tmp", name)
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, "new", name)); err != nil {
		t.Fatal(err)
	}
}

func newSource(t *testing.T, name string, cfg map[string]any, store core.Store) *Source {
	t.Helper()
	src, err := New(name, cfg, component.Deps{Store: store})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return src.(*Source)
}

func TestScanEmitsNewMail(t *testing.T) {
	dir, store := setup(t)
	writeMail(t, dir, "m1", sampleMail)

	s := newSource(t, "md", map[string]any{
		"dir":             dir,
		"notify_existing": true,
		"poll_interval":   "100ms",
	}, store)

	ctx := context.Background()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Stop(context.Background()) }()

	select {
	case ev := <-s.Events():
		if ev.Message.Subject != "Hello 世界" {
			t.Errorf("subject = %q (encoded-word decode failed?)", ev.Message.Subject)
		}
		if ev.Message.From != "Alice <alice@example.com>" {
			t.Errorf("from = %q", ev.Message.From)
		}
		if !strings.Contains(ev.Message.Text, "This is the body text.") {
			t.Errorf("text = %q", ev.Message.Text)
		}
		if ev.Message.Ref["uid"] != "m1" {
			t.Errorf("uid ref = %q", ev.Message.Ref["uid"])
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no event within 3s")
	}
}

func TestBaselineDoesNotNotifyExisting(t *testing.T) {
	dir, store := setup(t)
	writeMail(t, dir, "old1", sampleMail)

	s := newSource(t, "md", map[string]any{
		"dir":           dir,
		"poll_interval": "100ms",
	}, store)
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Stop(context.Background()) }()

	// Baseline file must be silent: no event within several poll cycles.
	select {
	case ev := <-s.Events():
		t.Fatalf("baseline mail must not notify, got %q", ev.Message.Subject)
	case <-time.After(400 * time.Millisecond):
	}

	// But mail arriving AFTER the baseline must notify.
	writeMail(t, dir, "new1", sampleMail)
	select {
	case <-s.Events():
	case <-time.After(3 * time.Second):
		t.Fatal("post-baseline mail must notify")
	}
}

func TestAckMovesToCur(t *testing.T) {
	dir, store := setup(t)
	writeMail(t, dir, "m1", sampleMail)

	s := newSource(t, "md", map[string]any{
		"dir": dir, "notify_existing": true, "poll_interval": "100ms", "mark_read": true,
	}, store)
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Stop(context.Background()) }()

	var ev core.Event
	select {
	case ev = <-s.Events():
	case <-time.After(3 * time.Second):
		t.Fatal("no event")
	}
	if err := s.Ack(context.Background(), ev.Message); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "cur", "m1:2,S")); err != nil {
		t.Errorf("expected file in cur/: %v", err)
	}
	// Ack is idempotent — second call must not error even though new/m1 is gone.
	if err := s.Ack(context.Background(), ev.Message); err != nil {
		t.Errorf("idempotent Ack failed: %v", err)
	}
}

func TestCommandMarkRead(t *testing.T) {
	dir, store := setup(t)
	writeMail(t, dir, "m1", sampleMail)

	s := newSource(t, "md", map[string]any{
		"dir": dir, "notify_existing": true, "poll_interval": "100ms", "mark_read": true,
	}, store)
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Stop(context.Background()) }()

	select {
	case <-s.Events():
	case <-time.After(3 * time.Second):
		t.Fatal("no event")
	}
	err := s.Command(context.Background(), core.Command{
		Kind: "mark_read", Args: map[string]string{"uid": "m1"},
	})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "cur", "m1:2,S")); err != nil {
		t.Errorf("expected file in cur/: %v", err)
	}
}

func TestCursorSurvivesRestart(t *testing.T) {
	dir, store := setup(t)
	writeMail(t, dir, "m1", sampleMail)

	cfg := map[string]any{"dir": dir, "notify_existing": true, "poll_interval": "100ms"}

	s1 := newSource(t, "md", cfg, store)
	if err := s1.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s1.Events():
	case <-time.After(3 * time.Second):
		t.Fatal("first run: no event")
	}
	_ = s1.Stop(context.Background())

	// Restart with the same store: the cursor must suppress m1 even though
	// notify_existing is true.
	s2 := newSource(t, "md", cfg, store)
	if err := s2.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Stop(context.Background()) }()
	select {
	case ev := <-s2.Events():
		t.Fatalf("restart must not re-notify m1, got subject %q", ev.Message.Subject)
	case <-time.After(400 * time.Millisecond):
	}
}

func TestMultipartExtraction(t *testing.T) {
	dir, store := setup(t)
	multipart := `From: a@b.c
Subject: multi
Content-Type: multipart/mixed; boundary=xyz
Message-Id: <m@x>

--xyz
Content-Type: text/plain

plain body here
--xyz
Content-Type: text/html

<html><p>html body</p></html>
--xyz
Content-Type: application/pdf; name="invoice.pdf"
Content-Disposition: attachment; filename="invoice.pdf"

fake-pdf-bytes
--xyz--
`
	writeMail(t, dir, "m1", multipart)

	s := newSource(t, "md", map[string]any{"dir": dir, "notify_existing": true, "poll_interval": "100ms"}, store)
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Stop(context.Background()) }()

	select {
	case ev := <-s.Events():
		if !strings.Contains(ev.Message.Text, "plain body here") {
			t.Errorf("plain = %q", ev.Message.Text)
		}
		if !strings.Contains(ev.Message.HTML, "html body") {
			t.Errorf("html = %q", ev.Message.HTML)
		}
		if len(ev.Message.Attachments) != 1 || ev.Message.Attachments[0].Filename != "invoice.pdf" {
			t.Errorf("attachments = %+v", ev.Message.Attachments)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no event")
	}
}

func TestRejectsPathologicalFilenames(t *testing.T) {
	dir, store := setup(t)
	s := newSource(t, "md", map[string]any{"dir": dir, "mark_read": true}, store)
	if err := s.moveToCur("../escape"); err == nil {
		t.Fatal("path traversal filename must be refused")
	}
	if err := s.moveToCur(""); err != nil {
		t.Errorf("empty filename must be a silent no-op: %v", err)
	}
}

func TestInvalidDirRejected(t *testing.T) {
	_, store := setup(t)
	if _, err := New("md", map[string]any{"dir": "/nonexistent-maildir"}, component.Deps{Store: store}); err == nil {
		t.Fatal("missing maildir must fail at construction")
	}
}
