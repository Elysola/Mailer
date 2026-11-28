// Package pipeline is the per-message conveyor belt: dedup, process, render,
// route, deliver, ack. It is deliberately in-order per source — the cursor
// semantics of every known source (IMAP UIDs in particular) are simpler when
// the events of one source never overtake each other, and notifications are
// not a throughput-sensitive workload.
package pipeline

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/elysola/mailer/internal/core"
	"github.com/elysola/mailer/internal/selector"
	"github.com/elysola/mailer/internal/render"
)

const (
	// pendingInitialDelay is the first retry-queue wait. Short enough that a
	// brief outage resolves quickly, long enough not to hammer the platform.
	pendingInitialDelay = 30 * time.Second
	// pendingMaxDelay caps the exponential backoff.
	pendingMaxDelay = 5 * time.Minute
	// pendingAgeLimit is the maximum time a row may sit in the retry queue
	// before the notification is dropped as undeliverable.
	pendingAgeLimit = 7 * 24 * time.Hour
	// maxInlineRateLimitWait is the longest platform-imposed wait dispatch
	// will absorb inline. A brief burst is worth waiting for; beyond this
	// the retry queue does a better job.
	maxInlineRateLimitWait = 5 * time.Second
)

// Route is one row of the ordered routing table.
type Route struct {
	When       string // selector expression; empty matches everything
	Processors []string
	Notifiers  []string
	Templates  string
	Template   RouteTemplate // inline template, overrides the named reference
	Stop       bool          // a terminal hit: no further rows are evaluated
}

// RouteTemplate carries inline title/body Go templates on a route.
type RouteTemplate struct {
	Title string
	Body  string
}

// Templates is the named-template registry a route can reference.
type Templates map[string]RouteTemplate

// Metrics is the minimal instrumentation the pipeline needs; *metricsx.Metrics
// satisfies it.
type Metrics interface {
	Inc(name string, labels map[string]string)
	Add(name string, labels map[string]string, delta int64)
	Observe(name string, labels map[string]string, value float64)
}

// Deps wires the pipeline to its surroundings.
type Deps struct {
	Store      core.Store
	Metrics    Metrics
	Notifiers  map[string]core.Notifier
	Processors map[string]core.Processor
	Routes     []Route
	Templates  Templates

	// Commander dispatches write-back commands (e.g. "delivered") to the
	// message's source.
	Commander func(ctx context.Context, source string, cmd core.Command) error

	RetryAttempts int
	RetryDelay    time.Duration
}

// Pipeline processes the event stream of exactly one source.
type Pipeline struct {
	source core.Source
	deps   Deps

	mu        sync.Mutex
	throttles map[string]time.Time

	wg sync.WaitGroup
}

// New builds the pipeline feeding off src.
func New(src core.Source, deps Deps) *Pipeline {
	if deps.RetryDelay <= 0 {
		deps.RetryDelay = time.Second
	}
	return &Pipeline{source: src, deps: deps, throttles: make(map[string]time.Time)}
}

// Run consumes the source's event stream until it closes or ctx is done.
// It blocks; run it in a goroutine per source.
func (p *Pipeline) Run(ctx context.Context) {
	// A failing dedup lookup cannot drop mail; duplication is the cheaper
	// failure mode. Track lapses so they appear in the log exactly once per
	// pipeline run instead of once per message.
	dedupWarned := false
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-p.source.Events():
			if !ok {
				return
			}
			dup, err := p.deps.Store.IsDuplicate(p.source.Name(), ev.Message.ID)
			if err != nil && !dedupWarned {
				log.Printf("[%s] dedup check broken (%v); duplicates may arrive until the state db recovers", p.source.Name(), err)
				dedupWarned = true
			} else if err == nil {
				dedupWarned = false
			}
			if dup {
				log.Printf("[%s] skip duplicate: %s", p.source.Name(), ev.Message.ID)
				p.ack(ctx, ev.Message)
				continue
			}
			p.handle(ctx, ev)
		}
	}
}

// handle carries one message through process → render → route → deliver → ack.
func (p *Pipeline) handle(ctx context.Context, ev core.Event) {
	src := p.source.Name()
	env := core.NewEnvelope(ev.Message)
	if ev.FirstRun {
		env.Meta["first_run"] = "true"
	}

	route := p.matchRoute(env)
	titleTmpl, bodyTmpl := p.templatesFor(route)

	// --- processing stage -------------------------------------------------
	var pluginNotifiers []string
	for _, name := range route.Processors {
		proc, ok := p.deps.Processors[name]
		if !ok {
			// A typo in the route table must not silently kill the stage:
			// treat it as a no-op but make it loud.
			log.Printf("[%s] route references unknown processor %q; skipped", src, name)
			continue
		}
		v, err := proc.Process(ctx, env)
		if err != nil {
			// A broken processor must never disturb the cursor or the queue.
			log.Printf("[%s] processor %s: %v", src, name, err)
			p.deps.Metrics.Inc("mailer_processor_errors_total", map[string]string{
				"source": src, "processor": name,
			})
			continue
		}
		switch v.Action {
		case core.ActionDrop:
			log.Printf("[%s] dropped by processor %s", src, name)
			p.deps.Metrics.Inc("mailer_messages_dropped_total", map[string]string{
				"source": src, "processor": name,
			})
			p.ack(ctx, env.Message)
			return
		case core.ActionDefer:
			next := time.Now().Add(pendingInitialDelay)
			if !v.DeferUntil.IsZero() {
				next = v.DeferUntil
			}
			p.deps.Metrics.Inc("mailer_messages_deferred_total", map[string]string{
				"source": src, "processor": name,
			})
			if err := p.enqueue(ctx, env, nil, core.QueueKindDefer, next); err != nil {
				log.Printf("[%s] defer enqueue: %v", src, err)
			}
			p.ack(ctx, env.Message)
			return
		case core.ActionFail:
			// The processor declared its output indispensable: park the whole
			// envelope for a full retry rather than continuing degraded.
			log.Printf("[%s] processor %s requested retry (on_error: fail)", src, name)
			p.deps.Metrics.Inc("mailer_processor_failures_total", map[string]string{
				"source": src, "processor": name,
			})
			if err := p.enqueue(ctx, env, nil, core.QueueKindRetry, time.Now().Add(pendingInitialDelay)); err != nil {
				log.Printf("[%s] fail enqueue: %v", src, err)
			}
			p.ack(ctx, env.Message)
			return
		}
		// ActionContinue: apply the mutations the verdict carries.
		if v.Title != "" {
			env.Title = v.Title
		}
		if v.Body != "" {
			env.Body = v.Body
		}
		if len(v.Fields) > 0 {
			for k, val := range v.Fields {
				if _, exists := env.Fields[k]; exists && env.Fields[k] != val {
					p.deps.Metrics.Inc("mailer_field_conflicts_total", map[string]string{
						"source": src, "processor": name, "field": k,
					})
				}
				env.Fields[k] = val
			}
		}
		if len(v.Route) > 0 {
			pluginNotifiers = v.Route
		}
	}

	// --- render ------------------------------------------------------------
	render.Render(&env, titleTmpl, bodyTmpl)

	// --- routing / delivery -------------------------------------------------
	targets := p.resolveNotifiers(env, route, pluginNotifiers)
	if len(targets) == 0 {
		// An empty resolution is a config error, not a delivery failure:
		// retrying forever cannot fix it. Log loudly, ack, move on.
		log.Printf("[%s] no notifiers resolved for route; message delivered nowhere", src)
		p.ack(ctx, env.Message)
		return
	}

	failed := p.dispatch(ctx, env, targets)
	if len(failed) == 0 {
		if err := p.deps.Store.MarkDelivered(p.source.Name(), env.Message.ID); err != nil {
			log.Printf("[%s] mark delivered: %v", src, err)
		}
		p.delivered(ctx, env.Message)
		p.ack(ctx, env.Message)
		return
	}

	// Undelivered for at least one notifier: queue for retry. The cursor
	// still advances — the queue owns redelivery, nothing is lost.
	if err := p.enqueue(ctx, env, failed, core.QueueKindRetry, time.Now().Add(pendingInitialDelay)); err != nil {
		log.Printf("[%s] enqueue pending: %v", src, err)
	}
	p.ack(ctx, env.Message)
}

// matchRoute walks the ordered table and merges every matching row until a
// row with stop=true terminates evaluation. Merging is union-style: notifier
// and processor lists concatenate (deduped, first occurrence wins), while the
// first row to set a template reference or inline template wins. A config
// with a single catch-all row therefore behaves exactly like v1, and layered
// rules (otp fast path + work default) compose without repetition.
func (p *Pipeline) matchRoute(env core.Envelope) Route {
	var merged Route
	matched := false
	seenNotifier := map[string]bool{}
	seenProcessor := map[string]bool{}
	for _, r := range p.deps.Routes {
		if r.When != "" && !selector.MatchSelector(r.When, env) {
			continue
		}
		matched = true
		for _, n := range r.Notifiers {
			if !seenNotifier[n] {
				seenNotifier[n] = true
				merged.Notifiers = append(merged.Notifiers, n)
			}
		}
		for _, pr := range r.Processors {
			if !seenProcessor[pr] {
				seenProcessor[pr] = true
				merged.Processors = append(merged.Processors, pr)
			}
		}
		if merged.Templates == "" {
			merged.Templates = r.Templates
		}
		if merged.Template.Title == "" && merged.Template.Body == "" {
			merged.Template = r.Template
		}
		if r.Stop {
			break
		}
	}
	if !matched {
		return Route{}
	}
	return merged
}

func (p *Pipeline) templatesFor(route Route) (title, body string) {
	if route.Template.Title != "" || route.Template.Body != "" {
		return route.Template.Title, route.Template.Body
	}
	if t, ok := p.deps.Templates[route.Templates]; ok {
		return t.Title, t.Body
	}
	return "", ""
}

// resolveNotifiers computes the final target set: the route's list, narrowed
// by a processor's explicit Notifiers verdict when it names at least one
// configured notifier. Unknown names are dropped; a processor that names
// nothing valid falls back to the route defaults so the mail is not lost.
func (p *Pipeline) resolveNotifiers(env core.Envelope, route Route, pluginNotifiers []string) []core.Notifier {
	base := make([]core.Notifier, 0, len(route.Notifiers))
	for _, n := range route.Notifiers {
		if nt, ok := p.deps.Notifiers[n]; ok {
			base = append(base, nt)
		} else {
			log.Printf("[%s] route references unknown notifier %q", p.source.Name(), n)
		}
	}
	if len(pluginNotifiers) == 0 {
		return base
	}
	want := make(map[string]bool, len(pluginNotifiers))
	for _, n := range pluginNotifiers {
		want[n] = true
	}
	var routed []core.Notifier
	for _, nt := range base {
		if want[nt.Name()] {
			routed = append(routed, nt)
		}
	}
	if len(routed) == 0 {
		log.Printf("[%s] processor requested notifiers %v, none in route; using route defaults",
			p.source.Name(), pluginNotifiers)
		return base
	}
	return routed
}

// dispatch sends env to every notifier, retrying inline per config. It
// returns the names of notifiers that still failed after inline retries
// (empty slice == full success).
func (p *Pipeline) dispatch(ctx context.Context, env core.Envelope, notifiers []core.Notifier) []string {
	var failed []string
	for _, n := range notifiers {
		// Honour an active platform rate limit instead of burning retries
		// against it.
		if wait := p.throttledFor(n.Name()); wait > 0 {
			log.Printf("[%s] %s still rate limited for %s; deferring to retry queue",
				env.Message.Source, n.Name(), wait.Round(time.Second))
			failed = append(failed, n.Name())
			continue
		}

		ok := false
	attempts:
		for attempt := 0; attempt <= p.deps.RetryAttempts; attempt++ {
			if attempt > 0 {
				delay := p.deps.RetryDelay * (1 << (attempt - 1))
				select {
				case <-ctx.Done():
					log.Printf("[%s] %s notify aborted: %v", env.Message.Source, n.Name(), ctx.Err())
					break attempts
				case <-time.After(delay):
				}
			}
			sendStart := time.Now()
			err := n.Send(ctx, env)
			p.deps.Metrics.Observe("mailer_notify_duration_seconds",
				map[string]string{"source": env.Message.Source, "notifier": n.Name()},
				time.Since(sendStart).Seconds(),
			)
			if err == nil {
				p.deps.Metrics.Inc("mailer_messages_delivered_total",
					map[string]string{"source": env.Message.Source, "notifier": n.Name()},
				)
				ok = true
				break
			}

			// A rate limit is not a delivery bug: the payload is fine and the
			// platform will take it later. Record the window and decide whether
			// it is short enough to absorb inline.
			var rl *core.RateLimitError
			if errors.As(err, &rl) {
				p.noteThrottle(rl.Notifier, rl.RetryAfter)
				p.deps.Metrics.Inc("mailer_notify_rate_limited_total",
					map[string]string{"source": env.Message.Source, "notifier": n.Name()},
				)
				log.Printf("[%s] %s rate limited, retry after %s: %v",
					env.Message.Source, n.Name(), rl.RetryAfter.Round(time.Millisecond), err)

				if rl.RetryAfter > maxInlineRateLimitWait || !sleepCtx(ctx, rl.RetryAfter) {
					break attempts
				}
				// Waited out a short window; retry without consuming an attempt,
				// since the previous call never reached the platform properly.
				attempt--
				continue
			}

			if attempt < p.deps.RetryAttempts {
				log.Printf("[%s] %s notify error (retry %d/%d): %v",
					env.Message.Source, n.Name(), attempt+1, p.deps.RetryAttempts, err)
			} else {
				log.Printf("[%s] %s notify error (failed after %d attempts): %v",
					env.Message.Source, n.Name(), p.deps.RetryAttempts+1, err)
			}
		}
		if !ok {
			failed = append(failed, n.Name())
		}
	}
	return failed
}

// enqueue stores env in the retry/defer queue. The queue key is the message's
// source cursor reference (uid for IMAP), falling back to the message id, so
// operator-facing keys stay meaningful.
func (p *Pipeline) enqueue(ctx context.Context, env core.Envelope, failed []string, kind string, nextTry time.Time) error {
	key := env.Message.Ref["uid"]
	if key == "" {
		key = env.Message.ID
	}
	if key == "" {
		key = fmt_KeyFromTime()
	}
	return p.deps.Store.Enqueue(core.QueuedItem{
		Source:   env.Message.Source,
		Key:      key,
		Kind:     kind,
		Envelope: env.Copy(),
		Failed:   failed,
		NextTry:  nextTry,
		Created:  time.Now(),
	})
}

func fmt_KeyFromTime() string {
	return "msg-" + time.Now().UTC().Format("20060102150405.000000000")
}

// RetryDue processes due queue rows for this pipeline's source. It is called
// periodically by the daemon's retry worker.
func (p *Pipeline) RetryDue(ctx context.Context) {
	due, err := p.deps.Store.Due(p.source.Name(), time.Now(), 200)
	if err != nil {
		log.Printf("[%s] pending queue read: %v", p.source.Name(), err)
		return
	}
	if len(due) == 0 {
		return
	}

	now := time.Now()
	for _, item := range due {
		if ctx.Err() != nil {
			return
		}
		if now.Sub(item.Created) > pendingAgeLimit {
			if err := p.deps.Store.Delete(item.Source, item.Key); err == nil {
				p.deps.Metrics.Inc("mailer_notify_dropped_total", map[string]string{"source": item.Source})
				log.Printf("[%s] %s: dropped after %s in queue (%d attempt(s))",
					item.Source, item.Key, pendingAgeLimit, item.Attempts)
			}
			continue
		}

		if item.Kind == core.QueueKindDefer {
			// A deferred message re-enters the pipeline as a fresh event: the
			// processors see it again and decide whether the condition now
			// holds. Re-processing also re-evaluates drop, so a defer loop
			// cannot wedge the queue — the age limit above bounds it.
			p.deps.Store.Delete(item.Source, item.Key)
			p.handle(ctx, core.Event{Message: item.Envelope.Message})
			continue
		}

		// Retry row: redeliver only to the notifiers that still owe delivery.
		var targets []core.Notifier
		for _, name := range item.Failed {
			if nt, ok := p.deps.Notifiers[name]; ok {
				targets = append(targets, nt)
			}
		}
		if len(targets) == 0 {
			// All owed notifiers are gone from config; nothing left to do.
			_ = p.deps.Store.Delete(item.Source, item.Key)
			continue
		}

		failed := p.dispatch(ctx, item.Envelope, targets)
		if len(failed) == 0 {
			if err := p.deps.Store.Delete(item.Source, item.Key); err != nil {
				log.Printf("[%s] delete pending %s: %v", item.Source, item.Key, err)
			}
			if err := p.deps.Store.MarkDelivered(item.Source, item.Envelope.Message.ID); err != nil {
				log.Printf("[%s] mark delivered (retry): %v", item.Source, err)
			}
			log.Printf("[%s] %s: retried successfully after %d failed round(s)",
				item.Source, item.Key, item.Attempts)
			p.delivered(ctx, item.Envelope.Message)
			continue
		}

		attempts := item.Attempts + 1
		delay := pendingInitialDelay << min(attempts-1, 20)
		if delay > pendingMaxDelay {
			delay = pendingMaxDelay
		}
		if err := p.deps.Store.UpdateRetry(item.Source, item.Key, failed, attempts, now.Add(delay)); err != nil {
			log.Printf("[%s] update pending %s: %v", item.Source, item.Key, err)
		}
	}
	p.observePending()
}

func (p *Pipeline) observePending() {
	n, err := p.deps.Store.Count(p.source.Name())
	if err != nil {
		return
	}
	p.deps.Metrics.Observe("mailer_pending_notifications", map[string]string{"source": p.source.Name()}, float64(n))
}

func (p *Pipeline) ack(ctx context.Context, msg core.Message) {
	if err := p.source.Ack(ctx, msg); err != nil {
		log.Printf("[%s] ack: %v", p.source.Name(), err)
	}
}

// delivered informs the source the message reached its targets, so it can
// apply configured write-backs (mark_seen on IMAP).
func (p *Pipeline) delivered(ctx context.Context, msg core.Message) {
	if p.deps.Commander == nil {
		return
	}
	args := map[string]string{}
	for k, v := range msg.Ref {
		args[k] = v
	}
	if err := p.deps.Commander(ctx, msg.Source, core.Command{Kind: "delivered", Args: args}); err != nil {
		log.Printf("[%s] delivered write-back: %v", msg.Source, err)
	}
}

// ---- throttling -------------------------------------------------------------

func (p *Pipeline) noteThrottle(notifier string, wait time.Duration) {
	until := time.Now().Add(wait)
	p.mu.Lock()
	defer p.mu.Unlock()
	if cur, ok := p.throttles[notifier]; !ok || until.After(cur) {
		p.throttles[notifier] = until
	}
}

func (p *Pipeline) throttledFor(notifier string) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	until, ok := p.throttles[notifier]
	if !ok {
		return 0
	}
	remaining := time.Until(until)
	if remaining <= 0 {
		delete(p.throttles, notifier)
		return 0
	}
	return remaining
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
