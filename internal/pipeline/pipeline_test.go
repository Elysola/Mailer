package pipeline

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/elysola/mailer/internal/core"
	"github.com/elysola/mailer/internal/metricsx"
	"github.com/elysola/mailer/internal/store/sqlite"
)

// ---- test doubles -----------------------------------------------------------

type fakeSource struct {
	name   string
	events chan core.Event
	acked  []core.Message
	mu     sync.Mutex
}

func newFakeSource(name string) *fakeSource {
	return &fakeSource{name: name, events: make(chan core.Event, 16)}
}

func (f *fakeSource) Name() string                { return f.name }
func (f *fakeSource) Type() string                { return "fake" }
func (f *fakeSource) Start(context.Context) error { return nil }
func (f *fakeSource) Stop(context.Context) error  { close(f.events); return nil }
func (f *fakeSource) Events() <-chan core.Event   { return f.events }
func (f *fakeSource) Ack(ctx context.Context, msg core.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acked = append(f.acked, msg)
	return nil
}
func (f *fakeSource) Command(context.Context, core.Command) error { return nil }

func (f *fakeSource) ackedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.acked)
}

type fakeNotifier struct {
	name  string
	sends int
	mu    sync.Mutex
	err   error // returned on every send
	envs  []core.Envelope
}

func (f *fakeNotifier) Name() string                { return f.name }
func (f *fakeNotifier) Type() string                { return "fake" }
func (f *fakeNotifier) Start(context.Context) error { return nil }
func (f *fakeNotifier) Stop(context.Context) error  { return nil }
func (f *fakeNotifier) Capabilities() core.Capabilities {
	return core.Capabilities{}
}
func (f *fakeNotifier) Send(ctx context.Context, env core.Envelope) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sends++
	f.envs = append(f.envs, env)
	return f.err
}
func (f *fakeNotifier) sendCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sends
}

type fakeProcessor struct {
	name    string
	verdict core.Verdict
	err     error
	seen    int
	mu      sync.Mutex
}

func (f *fakeProcessor) Name() string                { return f.name }
func (f *fakeProcessor) Type() string                { return "fake" }
func (f *fakeProcessor) Start(context.Context) error { return nil }
func (f *fakeProcessor) Stop(context.Context) error  { return nil }
func (f *fakeProcessor) Process(ctx context.Context, env core.Envelope) (core.Verdict, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen++
	return f.verdict, f.err
}
func (f *fakeProcessor) processCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seen
}

// ---- harness ---------------------------------------------------------------

type harness struct {
	src    *fakeSource
	store  *sqlite.Store
	pl     *Pipeline
	nt     *fakeNotifier
	cancel context.CancelFunc
	done   chan struct{}
}

func newHarness(t *testing.T, mutate func(*Deps)) *harness {
	t.Helper()
	st, err := sqlite.Load(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	src := newFakeSource("test-src")
	nt := &fakeNotifier{name: "nt"}
	deps := Deps{
		Store:    st,
		Metrics:  metricsx.NewMetrics(),
		Routes:   []Route{{Notifiers: []string{"nt"}}},
		Commander: func(context.Context, string, core.Command) error { return nil },
		Notifiers: map[string]core.Notifier{"nt": nt},
	}
	if mutate != nil {
		mutate(&deps)
	}
	pl := New(src, deps)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		pl.Run(ctx)
		close(done)
	}()
	h := &harness{src: src, store: st, pl: pl, nt: nt, cancel: cancel, done: done}
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return h
}

func (h *harness) inject(uid, msgID string) {
	msg := core.Message{
		ID:      msgID,
		Source:  "test-src",
		Subject: "s" + uid,
		Ref:     map[string]string{"uid": uid},
	}
	h.src.events <- core.Event{Message: msg}
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// ---- tests ------------------------------------------------------------------

func TestDeliversAndAcks(t *testing.T) {
	h := newHarness(t, nil)
	h.inject("1", "<m1@x>")
	waitFor(t, "delivery", func() bool { return h.nt.sendCount() == 1 })
	waitFor(t, "ack", func() bool { return h.src.ackedCount() == 1 })

	// Delivered mail is remembered: re-injecting the same message ID must not
	// produce a second notification.
	h.inject("2", "<m1@x>")
	time.Sleep(150 * time.Millisecond)
	if h.nt.sendCount() != 1 {
		t.Errorf("duplicate delivered, sends = %d", h.nt.sendCount())
	}
}

func TestDropAcksWithoutDelivering(t *testing.T) {
	h := newHarness(t, func(d *Deps) {
		d.Processors = map[string]core.Processor{
			"dropper": &fakeProcessor{name: "dropper", verdict: core.Verdict{Action: core.ActionDrop}},
		}
		d.Routes[0].Processors = []string{"dropper"}
	})
	h.inject("1", "<m1@x>")
	waitFor(t, "ack", func() bool { return h.src.ackedCount() == 1 })
	if h.nt.sendCount() != 0 {
		t.Error("dropped message must never be delivered")
	}
}

func TestDeferEnqueuesAndAcks(t *testing.T) {
	h := newHarness(t, func(d *Deps) {
		d.Processors = map[string]core.Processor{
			"deferer": &fakeProcessor{name: "deferer", verdict: core.Verdict{
				Action:     core.ActionDefer,
				DeferUntil: time.Now().Add(time.Hour),
			}},
		}
		d.Routes[0].Processors = []string{"deferer"}
	})
	h.inject("1", "<m1@x>")
	waitFor(t, "ack", func() bool { return h.src.ackedCount() == 1 })
	waitFor(t, "queue row", func() bool {
		rows, err := h.store.Rows("test-src", 10)
		return err == nil && len(rows) == 1
	})
	rows, _ := h.store.Rows("test-src", 10)
	if rows[0].Kind != core.QueueKindDefer {
		t.Errorf("kind = %q, want defer", rows[0].Kind)
	}
	if h.nt.sendCount() != 0 {
		t.Error("deferred message must not be delivered inline")
	}
}

func TestFailActionEnqueuesRetry(t *testing.T) {
	h := newHarness(t, func(d *Deps) {
		d.Processors = map[string]core.Processor{
			"failer": &fakeProcessor{name: "failer", verdict: core.Verdict{Action: core.ActionFail}},
		}
		d.Routes[0].Processors = []string{"failer"}
	})
	h.inject("1", "<m1@x>")
	waitFor(t, "queue row", func() bool {
		rows, err := h.store.Rows("test-src", 10)
		return err == nil && len(rows) == 1 && rows[0].Kind == core.QueueKindRetry
	})
	if h.nt.sendCount() != 0 {
		t.Error("fail action must not deliver inline")
	}
}

func TestProcessorErrorDoesNotDisturb(t *testing.T) {
	// A crashing processor must neither drop the mail nor block delivery.
	proc := &fakeProcessor{name: "broken", err: errors.New("boom")}
	h := newHarness(t, func(d *Deps) {
		d.Processors = map[string]core.Processor{"broken": proc}
		d.Routes[0].Processors = []string{"broken"}
	})
	h.inject("1", "<m1@x>")
	waitFor(t, "delivery", func() bool { return h.nt.sendCount() == 1 })
	waitFor(t, "ack", func() bool { return h.src.ackedCount() == 1 })
}

func TestFailedDeliveryEnqueuesWithBackoff(t *testing.T) {
	nt := &fakeNotifier{name: "nt", err: errors.New("connection refused")}
	h := newHarness(t, func(d *Deps) {
		d.Notifiers = map[string]core.Notifier{"nt": nt}
		d.RetryAttempts = 1
		d.RetryDelay = time.Millisecond
	})
	h.nt = nt
	h.inject("1", "<m1@x>")
	waitFor(t, "queue row", func() bool {
		rows, err := h.store.Rows("test-src", 10)
		return err == nil && len(rows) == 1
	})
	rows, _ := h.store.Rows("test-src", 10)
	if len(rows[0].Failed) != 1 || rows[0].Failed[0] != "nt" {
		t.Errorf("failed notifiers = %v", rows[0].Failed)
	}
	if rows[0].NextTry.Before(time.Now()) {
		t.Error("retry must be scheduled in the future")
	}
	// Ack happens immediately: the source cursor must not wait for retries.
	waitFor(t, "ack", func() bool { return h.src.ackedCount() == 1 })
}

func TestRateLimitThrottlesAndQueues(t *testing.T) {
	nt := &fakeNotifier{name: "nt", err: &core.RateLimitError{Notifier: "nt", RetryAfter: time.Hour, StatusCode: 429}}
	h := newHarness(t, func(d *Deps) {
		d.Notifiers = map[string]core.Notifier{"nt": nt}
		d.RetryAttempts = 0
		d.RetryDelay = time.Millisecond
	})
	h.nt = nt
	h.inject("1", "<m1@x>")
	waitFor(t, "queue row", func() bool {
		rows, err := h.store.Rows("test-src", 10)
		return err == nil && len(rows) == 1
	})
	// A second message must not even attempt a send while the throttle window
	// is active — hammering a rate-limited platform makes things worse.
	sendsBefore := nt.sendCount()
	h.inject("2", "<m2@x>")
	time.Sleep(150 * time.Millisecond)
	if nt.sendCount() != sendsBefore {
		t.Errorf("throttled notifier still called: %d -> %d", sendsBefore, nt.sendCount())
	}
}

func TestRouteTableUnionSemantics(t *testing.T) {
	second := &fakeNotifier{name: "second"}
	h := newHarness(t, func(d *Deps) {
		d.Notifiers["second"] = second
		d.Routes = []Route{
			// No stop: the catch-all still contributes, so matching mail
			// reaches the union of both rows' notifiers.
			{When: `subject contains "otp"`, Notifiers: []string{"second"}},
			{Notifiers: []string{"nt"}},
		}
	})
	h.src.events <- core.Event{Message: core.Message{
		ID: "<otp@x>", Source: "test-src", Subject: "otp inside",
		Ref: map[string]string{"uid": "1"},
	}}
	waitFor(t, "otp delivered to both", func() bool {
		return h.nt.sendCount() == 1 && second.sendCount() == 1
	})

	// Ordinary mail: only the catch-all.
	h.src.events <- core.Event{Message: core.Message{
		ID: "<plain@x>", Source: "test-src", Subject: "hello",
		Ref: map[string]string{"uid": "2"},
	}}
	waitFor(t, "plain delivered", func() bool { return h.nt.sendCount() == 2 })
	if second.sendCount() != 1 {
		t.Errorf("second notifier must not receive plain mail: %d", second.sendCount())
	}
}

func TestStopShadowsCatchAll(t *testing.T) {
	// With stop, a matching fast-path row is terminal: the catch-all does
	// not contribute its notifiers. This is how an operator expresses
	// "OTP mail goes to tg-personal ONLY".
	second := &fakeNotifier{name: "second"}
	h := newHarness(t, func(d *Deps) {
		d.Notifiers["second"] = second
		d.Routes = []Route{
			{When: `subject contains "otp"`, Notifiers: []string{"second"}, Stop: true},
			{Notifiers: []string{"nt"}},
		}
	})
	h.src.events <- core.Event{Message: core.Message{
		ID: "<otp@x>", Source: "test-src", Subject: "otp inside",
		Ref: map[string]string{"uid": "1"},
	}}
	waitFor(t, "otp delivered to second", func() bool { return second.sendCount() == 1 })
	time.Sleep(150 * time.Millisecond)
	if h.nt.sendCount() != 0 {
		t.Errorf("stop must shadow the catch-all, nt got %d sends", h.nt.sendCount())
	}
}

func TestStopPreventsFallthrough(t *testing.T) {
	// A stop row that matches must shadow the catch-all entirely when it is
	// the only row the operator wants — union semantics still apply to the
	// rows before it.
	h := newHarness(t, func(d *Deps) {
		d.Routes = []Route{
			{When: `source == "nope"`, Notifiers: []string{"nt"}, Stop: true},
			{Notifiers: []string{"nt"}},
		}
	})
	h.inject("1", "<m1@x>")
	waitFor(t, "delivery", func() bool { return h.nt.sendCount() == 1 })
}

func TestVerdictFieldsReachNotifier(t *testing.T) {
	h := newHarness(t, func(d *Deps) {
		d.Processors = map[string]core.Processor{
			"tagger": &fakeProcessor{name: "tagger", verdict: core.Verdict{
				Fields: map[string]string{"class": "bill"},
			}},
		}
		d.Routes[0].Processors = []string{"tagger"}
	})
	h.inject("1", "<m1@x>")
	waitFor(t, "delivery", func() bool { return h.nt.sendCount() == 1 })
	h.nt.mu.Lock()
	defer h.nt.mu.Unlock()
	if h.nt.envs[0].Fields["class"] != "bill" {
		t.Errorf("fields lost in delivery: %v", h.nt.envs[0].Fields)
	}
}
