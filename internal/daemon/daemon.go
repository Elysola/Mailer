// Package daemon assembles the configured components and runs them: sources
// feeding pipelines, a central retry worker, the periodic seen-table cleanup
// and the health HTTP server. It replaces the v1 god-object poller — every
// concern it used to conflate is now an explicit, separately testable piece.
package daemon

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/elysola/mailer/internal/component"
	"github.com/elysola/mailer/internal/config"
	"github.com/elysola/mailer/internal/core"
	"github.com/elysola/mailer/internal/metricsx"
	"github.com/elysola/mailer/internal/pipeline"
	"github.com/elysola/mailer/internal/store/sqlite"
)

// Daemon is the assembled, runnable application.
type Daemon struct {
	cfg        *config.Config
	store      *sqlite.Store
	metrics    *metricsx.Metrics
	sources    []core.Source
	notifiers  map[string]core.Notifier
	processors map[string]core.Processor
	llms       map[string]core.LLM
	pipelines  []*pipeline.Pipeline

	wg sync.WaitGroup
}

// Assemble builds every configured component. Assembly order matters: LLMs
// first (processors reference them), then processors and notifiers, then
// sources, then the pipelines binding them together. Any failure tears down
// what was already built.
func Assemble(cfg *config.Config) (*Daemon, error) {
	store, err := sqlite.Load(cfg.State.File)
	if err != nil {
		return nil, fmt.Errorf("state: %w", err)
	}
	d := &Daemon{
		cfg:        cfg,
		store:      store,
		metrics:    metricsx.NewMetrics(),
		notifiers:  make(map[string]core.Notifier),
		processors: make(map[string]core.Processor),
		llms:       make(map[string]core.LLM),
	}
	if err := d.build(); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

func (d *Daemon) build() error {
	deps := component.Deps{
		Store:     d.store,
		Metrics:   d.metrics,
		Commander: d,
		LLMs:      d.llms,
	}

	for _, inst := range d.cfg.LLMs {
		llm, err := component.BuildLLM(inst.Type, inst.Name, inst.Raw, deps)
		if err != nil {
			return err
		}
		d.llms[inst.Name] = llm
	}
	for _, inst := range d.cfg.Processors {
		proc, err := component.BuildProcessor(inst.Type, inst.Name, inst.Raw, deps)
		if err != nil {
			return err
		}
		d.processors[inst.Name] = proc
	}
	for _, inst := range d.cfg.Notifiers {
		nt, err := component.BuildNotifier(inst.Type, inst.Name, inst.Raw, deps)
		if err != nil {
			return err
		}
		d.notifiers[inst.Name] = nt
	}
	for _, inst := range d.cfg.Sources {
		src, err := component.BuildSource(inst.Type, inst.Name, inst.Raw, deps)
		if err != nil {
			return err
		}
		d.sources = append(d.sources, src)
	}
	return nil
}

// Command implements component.Commander: it routes a write-back command to
// the named source instance.
func (d *Daemon) Command(ctx context.Context, source string, cmd core.Command) error {
	for _, s := range d.sources {
		if s.Name() == source {
			return s.Command(ctx, cmd)
		}
	}
	return fmt.Errorf("unknown source %q", source)
}

// Metrics exposes the instrumentation registry for the HTTP server.
func (d *Daemon) Metrics() *metricsx.Metrics { return d.metrics }

// Store exposes the state store for the queue CLI and doctor checks.
func (d *Daemon) Store() *sqlite.Store { return d.store }

// Run starts everything and blocks until ctx is cancelled.
func (d *Daemon) Run(ctx context.Context) error {
	// Start components in dependency order: notifiers and processors first
	// (they must be ready when the first event flows), sources last.
	for _, nt := range d.notifiers {
		if err := nt.Start(ctx); err != nil {
			return fmt.Errorf("start notifier %s: %w", nt.Name(), err)
		}
	}
	for _, p := range d.processors {
		if err := p.Start(ctx); err != nil {
			return fmt.Errorf("start processor %s: %w", p.Name(), err)
		}
	}

	routes := make([]pipeline.Route, 0, len(d.cfg.Routes))
	for _, r := range d.cfg.Routes {
		routes = append(routes, pipeline.Route{
			When:       r.When,
			Processors: r.Processors,
			Notifiers:  r.To,
			Templates:  r.Templates,
			Template:   pipeline.RouteTemplate{Title: r.Template.Title, Body: r.Template.Body},
			Stop:       r.Stop,
		})
	}
	templates := make(pipeline.Templates, len(d.cfg.Templates))
	for name, t := range d.cfg.Templates {
		templates[name] = pipeline.RouteTemplate{Title: t.Title, Body: t.Body}
	}

	pdeps := pipeline.Deps{
		Store:         d.store,
		Metrics:       d.metrics,
		Notifiers:     d.notifiers,
		Processors:    d.processors,
		Routes:        routes,
		Templates:     templates,
		Commander:     d.Command,
		RetryAttempts: d.cfg.Daemon.RetryAttempts,
		RetryDelay:    d.cfg.Daemon.RetryDelay,
	}

	for _, src := range d.sources {
		if err := src.Start(ctx); err != nil {
			return fmt.Errorf("start source %s: %w", src.Name(), err)
		}
		pl := pipeline.New(src, pdeps)
		d.pipelines = append(d.pipelines, pl)
		d.wg.Add(1)
		go func(pl *pipeline.Pipeline) {
			defer d.wg.Done()
			pl.Run(ctx)
		}(pl)
	}

	// Central retry worker: one timer scans due queue rows across all
	// sources, replacing the per-account retry cycle of v1.
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		d.retryLoop(ctx)
	}()

	// Periodic seen-table and KV cleanup.
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		d.cleanLoop(ctx)
	}()

	// Auth-failure alerting: once per failure episode per source.
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		d.authWatchLoop(ctx)
	}()

	log.Printf("daemon running: %d source(s), %d notifier(s), %d processor(s)",
		len(d.sources), len(d.notifiers), len(d.processors))

	<-ctx.Done()
	log.Print("shutting down…")

	// Stop sources first so no new events arrive, then wait for pipelines to
	// drain in-flight work, then stop the rest.
	stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, src := range d.sources {
		if err := src.Stop(stopCtx); err != nil {
			log.Printf("stop source %s: %v", src.Name(), err)
		}
	}
	d.wg.Wait()
	for _, p := range d.processors {
		_ = p.Stop(stopCtx)
	}
	for _, nt := range d.notifiers {
		_ = nt.Stop(stopCtx)
	}
	for _, l := range d.llms {
		_ = l.Stop(stopCtx)
	}
	return nil
}

// Close releases resources held by assembly (used when Run is never called).
func (d *Daemon) Close() {
	if d.store != nil {
		d.store.Close()
	}
}

func (d *Daemon) retryLoop(ctx context.Context) {
	interval := d.cfg.Daemon.RetryScanInterval
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, pl := range d.pipelines {
				pl.RetryDue(ctx)
			}
		}
	}
}

func (d *Daemon) cleanLoop(ctx context.Context) {
	// Clean the seen table and expired KV entries once per day, first pass
	// shortly after startup so an upgrade does not carry bloat for a day.
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			before := time.Now().Add(-d.cfg.Daemon.SeenRetention)
			if err := d.store.CleanSeen(before); err != nil {
				log.Printf("clean seen: %v", err)
			}
			if err := d.store.CleanKV(time.Now()); err != nil {
				log.Printf("clean kv: %v", err)
			}
			timer.Reset(24 * time.Hour)
		}
	}
}

// authWatchLoop polls each source for a one-shot auth-failure signal and
// alerts the operator through the catch-all route's notifiers. The alert is
// an ordinary envelope travelling the normal delivery path — no special
// channel needed.
func (d *Daemon) authWatchLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, src := range d.sources {
				as, ok := src.(interface{ AuthFailureConsumed() (bool, error) })
				if !ok {
					continue
				}
				fire, err := as.AuthFailureConsumed()
				if err != nil || !fire {
					continue
				}
				d.alertAuthFailure(ctx, src.Name())
			}
		}
	}
}

func (d *Daemon) alertAuthFailure(ctx context.Context, sourceName string) {
	env := core.Envelope{
		Message: core.Message{
			Source:  sourceName,
			Account: sourceName,
			From:    "mailer",
			Subject: fmt.Sprintf("⚠️ %s: authentication failing", sourceName),
			Date:    time.Now(),
			Preview: fmt.Sprintf("mailer can no longer log in to %s. "+
				"The credential was rejected; check the password or re-run `mailer auth`. "+
				"The daemon keeps retrying in the background.", sourceName),
		},
	}
	// Deliver through the first route that matches everything (or the last
	// route, which is conventionally the catch-all). The alert must reach
	// the operator even when the failing source has its own narrow routes.
	for i := len(d.cfg.Routes) - 1; i >= 0; i-- {
		r := d.cfg.Routes[i]
		if r.When != "" {
			continue
		}
		for _, name := range r.To {
			nt, ok := d.notifiers[name]
			if !ok {
				continue
			}
			nctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			if err := nt.Send(nctx, env); err != nil {
				log.Printf("[%s] auth alert via %s: %v", sourceName, name, err)
			}
			cancel()
		}
		return
	}
	log.Printf("[%s] auth failure alert could not be delivered: no catch-all route", sourceName)
}

// Status reports per-source health for the /status endpoint.
type SourceStatus struct {
	LastPollSuccessUnix int64   `json:"last_poll_success_unix"`
	ConsecutiveFailures int64   `json:"consecutive_failures"`
	LastPollDurationSec float64 `json:"last_poll_duration_seconds"`
	AuthFailed          bool    `json:"auth_failed"`
	Pending             int     `json:"pending_notifications"`
}

func (d *Daemon) Status() map[string]SourceStatus {
	out := make(map[string]SourceStatus, len(d.sources))
	for _, src := range d.sources {
		st := SourceStatus{}
		if ss, ok := src.(interface {
			Status() struct {
				LastPollSuccessUnix int64   `json:"last_poll_success_unix"`
				ConsecutiveFailures int64   `json:"consecutive_failures"`
				LastPollDurationSec float64 `json:"last_poll_duration_seconds"`
				AuthFailed          bool    `json:"auth_failed"`
			}
		}); ok {
			s := ss.Status()
			st.LastPollSuccessUnix = s.LastPollSuccessUnix
			st.ConsecutiveFailures = s.ConsecutiveFailures
			st.LastPollDurationSec = s.LastPollDurationSec
			st.AuthFailed = s.AuthFailed
		}
		if n, err := d.store.Count(src.Name()); err == nil {
			st.Pending = n
		}
		out[src.Name()] = st
	}
	return out
}
