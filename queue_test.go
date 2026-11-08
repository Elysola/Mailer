package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/elysola/mailer/internal/state"
)

// writeConfig drops a minimal but valid config next to a fresh state file and
// returns the config path.
func writeConfig(t *testing.T, stateFile string, extra string) string {
	t.Helper()
	dir := filepath.Dir(stateFile)
	cfg := `poll_interval: 5m
state_file: ` + stateFile + `
telegram:
  enabled: true
  bot_token: "1:abc"
  chat_ids: ["42"]
` + extra + `
accounts:
  - name: primary
    host: imap.example.com
    port: 993
    username: u
    password: p
    mailbox: INBOX
`
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func seedPending(t *testing.T, stateFile string) {
	t.Helper()
	st, err := state.Load(stateFile)
	if err != nil {
		t.Fatalf("state.Load: %v", err)
	}
	defer st.Close()

	now := time.Now()
	rows := []state.Pending{
		{
			Account: "primary", UID: 10, MessageID: "<a@x>",
			Subject: "verification code", Date: now,
			Failed:      []string{"telegram"},
			Attachments: []state.PendingAttachment{{Filename: "a.pdf", Size: 10}},
		},
		{
			Account: "primary", UID: 11, MessageID: "<b@x>",
			Subject: "invoice", Date: now, Failed: []string{"telegram", "discord"},
		},
		// No failed notifier left: this row can never be delivered.
		{Account: "primary", UID: 12, MessageID: "<c@x>", Subject: "stuck", Date: now},
	}
	for _, p := range rows {
		if err := st.EnqueuePending(p, now.Add(-time.Minute)); err != nil {
			t.Fatalf("EnqueuePending: %v", err)
		}
	}
}

func newStateFile(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "state.db")
}

// TestQueueLsReportsStuckRows: a queued row with an empty notifier set is the
// single most confusing queue state, so `queue ls` calls it out explicitly.
func TestQueueLsReportsStuckRows(t *testing.T) {
	stateFile := newStateFile(t)
	cfg := writeConfig(t, stateFile, "")
	seedPending(t, stateFile)

	out := captureStdout(t, func() {
		if code := runQueueLs([]string{"-config", cfg}); code != 0 {
			t.Errorf("runQueueLs = %d, want 0", code)
		}
	})

	for _, want := range []string{
		"verification code", "invoice", "stuck",
		"3 row(s)",
		"1 row(s) have no notifier left to try",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestQueueLsEmpty(t *testing.T) {
	stateFile := newStateFile(t)
	cfg := writeConfig(t, stateFile, "")
	if _, err := state.Load(stateFile); err != nil {
		t.Fatalf("state.Load: %v", err)
	}

	out := captureStdout(t, func() {
		if code := runQueueLs([]string{"-config", cfg}); code != 0 {
			t.Errorf("runQueueLs = %d, want 0", code)
		}
	})
	if !strings.Contains(out, "queue is empty") {
		t.Errorf("output = %q, want the empty notice", out)
	}
}

// TestQueueLsJSONIsMachineReadable: the JSON path exists so a script can page on
// queue depth, so it must emit an array even when empty rather than null.
func TestQueueLsJSONIsMachineReadable(t *testing.T) {
	stateFile := newStateFile(t)
	cfg := writeConfig(t, stateFile, "")

	out := captureStdout(t, func() {
		runQueueLs([]string{"-config", cfg, "-json"})
	})
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("empty queue JSON = %q, want []", out)
	}

	seedPending(t, stateFile)
	out = captureStdout(t, func() {
		runQueueLs([]string{"-config", cfg, "-json"})
	})
	var rows []state.Pending
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if len(rows) != 3 {
		t.Fatalf("decoded %d rows, want 3", len(rows))
	}
	// The attachment list must survive: dropping it here would show a queue
	// that disagrees with what will actually be delivered.
	if len(rows[0].Attachments) != 1 {
		t.Errorf("attachments = %+v, want 1", rows[0].Attachments)
	}
}

func TestQueueLsFiltersByAccount(t *testing.T) {
	stateFile := newStateFile(t)
	cfg := writeConfig(t, stateFile, "")
	seedPending(t, stateFile)

	out := captureStdout(t, func() {
		runQueueLs([]string{"-config", cfg, "-account", "primary"})
	})
	if !strings.Contains(out, "3 row(s)") {
		t.Errorf("account filter lost rows:\n%s", out)
	}

	out = captureStdout(t, func() {
		runQueueLs([]string{"-config", cfg, "-account", "nope"})
	})
	if !strings.Contains(out, "queue is empty") {
		t.Errorf("unknown account should be empty:\n%s", out)
	}
}

// TestQueueDropRequiresTarget guards against the one command that destroys data
// silently: no -account/-all means refuse.
func TestQueueDropRequiresTarget(t *testing.T) {
	stateFile := newStateFile(t)
	cfg := writeConfig(t, stateFile, "")
	seedPending(t, stateFile)

	if code := runQueueDrop([]string{"-config", cfg, "-yes"}); code != 2 {
		t.Errorf("runQueueDrop without a target = %d, want 2", code)
	}
	if code := runQueueDrop([]string{"-config", cfg, "-account", "primary", "-all"}); code != 2 {
		t.Errorf("runQueueDrop with both targets = %d, want 2", code)
	}
	// Neither attempt may have deleted anything.
	st, err := state.Load(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	n, err := st.PendingCount("primary")
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("pending = %d, want 3 (a refused drop must not delete)", n)
	}
}

// TestQueueDropNeedsConfirmation: without -yes it previews and refuses.
func TestQueueDropNeedsConfirmation(t *testing.T) {
	stateFile := newStateFile(t)
	cfg := writeConfig(t, stateFile, "")
	seedPending(t, stateFile)

	out := captureStdout(t, func() {
		if code := runQueueDrop([]string{"-config", cfg, "-account", "primary"}); code != 1 {
			t.Errorf("runQueueDrop without -yes = %d, want 1", code)
		}
	})
	if !strings.Contains(out, "will never be delivered") {
		t.Errorf("preview should warn about data loss:\n%s", out)
	}

	st, err := state.Load(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if n, _ := st.PendingCount("primary"); n != 3 {
		t.Errorf("pending = %d, want 3 after a refused drop", n)
	}
}

func TestQueueDropWithConfirmation(t *testing.T) {
	stateFile := newStateFile(t)
	cfg := writeConfig(t, stateFile, "")
	seedPending(t, stateFile)

	if code := runQueueDrop([]string{"-config", cfg, "-account", "primary", "-yes"}); code != 0 {
		t.Errorf("runQueueDrop = %d, want 0", code)
	}
	st, err := state.Load(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if n, _ := st.PendingCount("primary"); n != 0 {
		t.Errorf("pending = %d, want 0 after the drop", n)
	}
}

func TestQueueDropNothingToDo(t *testing.T) {
	stateFile := newStateFile(t)
	cfg := writeConfig(t, stateFile, "")
	if _, err := state.Load(stateFile); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if code := runQueueDrop([]string{"-config", cfg, "-all", "-yes"}); code != 0 {
			t.Errorf("runQueueDrop on an empty queue = %d, want 0", code)
		}
	})
	if !strings.Contains(out, "nothing to drop") {
		t.Errorf("output = %q", out)
	}
}

// TestDoctorReportsProblems covers the states an operator needs told about.
func TestDoctorReportsProblems(t *testing.T) {
	stateFile := newStateFile(t)
	cfg := writeConfig(t, stateFile, "health_port: 0\n")
	seedPending(t, stateFile)

	out := captureStdout(t, func() {
		if code := runDoctor([]string{"-config", cfg}); code != 1 {
			t.Errorf("runDoctor = %d, want 1 (a stuck row is an error)", code)
		}
	})
	for _, want := range []string{
		"overall: error",
		"no notifier left to try",
		"health server disabled",
		"has not completed a poll",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q:\n%s", want, out)
		}
	}
}

func TestDoctorHealthyConfigIsWarnFree(t *testing.T) {
	stateFile := newStateFile(t)
	cfg := writeConfig(t, stateFile, "")

	// A recorded progress row is what a running daemon leaves behind.
	st, err := state.Load(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Set("primary", state.AccountState{LastUID: 100, UIDValidity: 7}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	st.Close()

	var code int
	out := captureStdout(t, func() {
		code = runDoctor([]string{"-config", cfg})
	})
	if code != 0 {
		t.Errorf("runDoctor = %d, want 0:\n%s", code, out)
	}
	if strings.Contains(out, "⚠") || strings.Contains(out, "✗") {
		t.Errorf("healthy config reported a problem:\n%s", out)
	}
	if !strings.Contains(out, "poll") {
		t.Errorf("report should name the account mode:\n%s", out)
	}
}

// TestDoctorJSONShape: the JSON form is the machine-readable contract.
func TestDoctorJSONShape(t *testing.T) {
	stateFile := newStateFile(t)
	cfg := writeConfig(t, stateFile, "")

	out := captureStdout(t, func() {
		runDoctor([]string{"-config", cfg, "-json"})
	})
	var report struct {
		Status   string `json:"status"`
		Problems []string
		Warnings []string
		Info     []string
		Accounts []struct {
			Name string `json:"name"`
			Mode string `json:"mode"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if report.Status == "" {
		t.Error("status is empty")
	}
	if len(report.Accounts) != 1 || report.Accounts[0].Name != "primary" {
		t.Errorf("accounts = %+v", report.Accounts)
	}
	if report.Accounts[0].Mode != "poll" {
		t.Errorf("mode = %q, want poll", report.Accounts[0].Mode)
	}
	// Arrays must be present, not null: a consumer indexing into them would
	// otherwise have to special-case the empty report.
	if report.Problems == nil || report.Warnings == nil || report.Info == nil {
		t.Error("report arrays must not be nil")
	}
}

// TestDoctorFlagsConfigError: a broken config is the most common failure and
// must be reported as an error rather than a panic.
func TestDoctorFlagsConfigError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.yaml")
	if err := os.WriteFile(path, []byte("accounts: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if code := runDoctor([]string{"-config", path}); code != 1 {
			t.Errorf("runDoctor = %d, want 1", code)
		}
	})
	if !strings.Contains(out, "config") {
		t.Errorf("output should mention the config:\n%s", out)
	}
}

func TestDoctorMissingStateFileIsCreated(t *testing.T) {
	// state.Load creates the file, so doctor must not fail on a first run.
	stateFile := newStateFile(t)
	cfg := writeConfig(t, stateFile, "")
	if code := runDoctor([]string{"-config", cfg}); code != 0 {
		t.Errorf("runDoctor on a fresh state file = %d, want 0", code)
	}
	if _, err := os.Stat(stateFile); err != nil {
		t.Errorf("state file was not created: %v", err)
	}
}

func TestHumanAge(t *testing.T) {
	cases := map[time.Duration]string{
		-5 * time.Second: "0s",
		30 * time.Second: "30s",
		5 * time.Minute:  "5m",
		3 * time.Hour:    "3h",
		72 * time.Hour:   "3d",
	}
	for in, want := range cases {
		if got := humanAge(in); got != want {
			t.Errorf("humanAge(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestHumanUntil(t *testing.T) {
	if got := humanUntil(-time.Second); got != "now" {
		t.Errorf("humanUntil(past) = %q, want now", got)
	}
	if got := humanUntil(2 * time.Minute); got != "in 2m" {
		t.Errorf("humanUntil = %q, want in 2m", got)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("short", 10); got != "short" {
		t.Errorf("truncate = %q", got)
	}
	got := truncate(strings.Repeat("a", 20), 10)
	if r := []rune(got); len(r) != 10 {
		t.Errorf("truncate length = %d, want 10: %q", len(r), got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncate = %q, want an ellipsis", got)
	}
	// Multi-byte titles must not be cut mid-rune.
	got = truncate(strings.Repeat("验", 20), 5)
	if r := []rune(got); len(r) != 5 {
		t.Errorf("rune truncate length = %d, want 5: %q", len(r), got)
	}
}

func TestOrUnset(t *testing.T) {
	if got := orUnset(""); got != "(unset)" {
		t.Errorf("orUnset(empty) = %q", got)
	}
	if got := orUnset("x"); got != "x" {
		t.Errorf("orUnset(x) = %q", got)
	}
}

// captureStdout runs fn with os.Stdout redirected, returning what it wrote.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		done <- b.String()
	}()

	fn()

	w.Close()
	os.Stdout = old
	return <-done
}
