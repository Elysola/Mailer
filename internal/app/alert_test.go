package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/elysola/mailer/internal/config"
	"github.com/elysola/mailer/internal/oauth"
)

// resetFakeNotifier clears recorded deliveries so an assertion can target only
// the alert that follows.
func resetFakeNotifier(f *fakeNotifier) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = f.sent[:0]
}

// waitForSends polls until the notifier has recorded at least n deliveries, or
// the deadline passes. The alert is dispatched on a goroutine by design (a
// failing poll must not block on a notifier), so the test cannot assume it has
// completed when alertAuthFailure returns.
func waitForSends(t *testing.T, f *fakeNotifier, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if f.sentCount() >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d delivery(ies); got %d", n, f.sentCount())
}

// deadCredentialError produces a real error from the refresh path against a
// token endpoint that rejects the grant.
//
// Using the production code rather than a hand-built sentinel keeps the test
// honest: if the refresh layer stops classifying invalid_grant as permanent,
// this test fails instead of silently passing against a stub.
func deadCredentialError(t *testing.T) error {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`))
	}))
	t.Cleanup(srv.Close)

	_, err := oauth.Refresh(context.Background(), srv.Client(), srv.URL, "cid", "secret", "1//revoked")
	if err == nil {
		t.Fatal("test precondition failed: expected a refresh error")
	}
	return fmt.Errorf("refresh access token: %w", err)
}

func TestAlertAuthFailureNotifiesOncePerOutage(t *testing.T) {
	store := openStore(t)
	f := newFake("telegram", false)
	p := newTestPoller(t, testCfg(config.Account{Name: testAcc}), store, f)
	resetFakeNotifier(f)

	err := deadCredentialError(t)
	if !oauth.IsAuthError(err) {
		t.Fatalf("test precondition failed: IsAuthError(%v) = false", err)
	}

	p.alertAuthFailure(context.Background(), testAcc, err)
	waitForSends(t, f, 1)

	if got := f.sentCount(); got != 1 {
		t.Fatalf("sent = %d, want 1", got)
	}
	msg := f.sent[0]
	if msg.Subject == "" || msg.TextOverride == "" {
		t.Errorf("alert must carry a title and body, got %+v", msg)
	}
	// The operator needs the fix in the message, not just the symptom.
	if !contains(msg.TextOverride, "mailer auth gmail") {
		t.Errorf("alert body should name the fix, got %q", msg.TextOverride)
	}

	// Every subsequent poll fails the same way; that must not re-alert, or the
	// one message that matters drowns in its own repeats.
	for i := 0; i < 5; i++ {
		p.alertAuthFailure(context.Background(), testAcc, err)
	}
	time.Sleep(50 * time.Millisecond)
	if got := f.sentCount(); got != 1 {
		t.Errorf("sent = %d after repeated failures, want 1 (alert must be de-duplicated)", got)
	}

	// /status must expose the condition so a scrape can alert independently.
	if !p.status[testAcc].AuthFailed {
		t.Error("status should report auth_failed")
	}
}

func TestAlertAuthFailureIgnoresTransientErrors(t *testing.T) {
	store := openStore(t)
	f := newFake("telegram", false)
	p := newTestPoller(t, testCfg(config.Account{Name: testAcc}), store, f)
	resetFakeNotifier(f)

	// A network blip must stay an ordinary poll failure: alerting on it would
	// train the operator to ignore the alert that matters.
	p.alertAuthFailure(context.Background(), testAcc, errors.New("dial tcp: connection refused"))
	p.alertAuthFailure(context.Background(), testAcc, nil)

	time.Sleep(50 * time.Millisecond)
	if got := f.sentCount(); got != 0 {
		t.Errorf("sent = %d for transient errors, want 0", got)
	}
	if p.status[testAcc].AuthFailed {
		t.Error("transient failure must not set auth_failed")
	}
}

func TestAlertAuthFailureRearmsAfterRecovery(t *testing.T) {
	store := openStore(t)
	f := newFake("telegram", false)
	p := newTestPoller(t, testCfg(config.Account{Name: testAcc}), store, f)
	resetFakeNotifier(f)

	authErr := deadCredentialError(t)

	p.alertAuthFailure(context.Background(), testAcc, authErr)
	waitForSends(t, f, 1)

	// A successful poll proves the credential works again, so a later break is
	// a new outage and deserves its own alert.
	p.notePollSuccess(testAcc, time.Second)
	if p.status[testAcc].AuthFailed {
		t.Error("successful poll should clear auth_failed")
	}

	p.alertAuthFailure(context.Background(), testAcc, authErr)
	waitForSends(t, f, 2)

	if got := f.sentCount(); got != 2 {
		t.Errorf("sent = %d, want 2 (a second outage must alert again)", got)
	}
}

// contains is a local helper to keep assertions readable.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
