package imap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/elysola/mailer/internal/component"
	"github.com/elysola/mailer/internal/core"
	"github.com/elysola/mailer/internal/oauth"
	"github.com/emersion/go-imap/v2"
)

func init() {
	component.RegisterSource("imap", New)
}

// cursorData is the stable on-disk cursor format. The sqlite store's v1
// migration writes this same shape, so it must not change casually.
type cursorData struct {
	LastUID     uint32 `json:"last_uid"`
	UIDValidity uint32 `json:"uid_validity"`
}

// pollDeadline derives the per-poll budget from the poll interval: 80% of the
// interval so a sustained slow poll can never pile up on the next tick, but
// never less than a few seconds for the IMAP round-trips (or more than the
// historical 30s, already generous for a single mailbox).
func pollDeadline(interval time.Duration) time.Duration {
	const maxPollTimeout = 30 * time.Second
	d := interval * 8 / 10
	if d < 5*time.Second {
		return 5 * time.Second
	}
	if d > maxPollTimeout {
		return maxPollTimeout
	}
	return d
}

// Source is the IMAP source component: one instance owns one mailbox, its
// connection pool, its IDLE watcher and its UID cursor.
type Source struct {
	cfg   *Config
	store core.Store

	pool *Pool
	idle *IdleWatcher

	events chan core.Event

	mu             sync.Mutex
	cursor         cursorData
	cursorLoaded   bool
	firstRun       bool
	authFailed     bool
	authAlerted    bool
	failures       int64
	lastPollOKUnix int64
	lastPollDurSec float64

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New is the component factory for `type: imap`.
func New(name string, raw map[string]any, deps component.Deps) (core.Source, error) {
	var cfg Config
	if err := component.Decode(component.ExpandMap(raw), &cfg); err != nil {
		return nil, fmt.Errorf("imap source %q: %w", name, err)
	}
	// Secret file indirection: file wins over the inline value, and env
	// expansion has already run over everything.
	if cfg.PasswordFile != "" {
		v, err := component.Secret("", cfg.PasswordFile)
		if err != nil {
			return nil, fmt.Errorf("imap source %q: %w", name, err)
		}
		cfg.Password = v
	}
	if cfg.OAuth2 != nil && cfg.OAuth2.ClientSecretFile != "" {
		v, err := component.Secret("", cfg.OAuth2.ClientSecretFile)
		if err != nil {
			return nil, fmt.Errorf("imap source %q: %w", name, err)
		}
		cfg.OAuth2.ClientSecret = v
	}
	cfg.applyDefaults()
	if err := cfg.validate(name); err != nil {
		return nil, err
	}
	cfg.InstanceName = name

	if deps.Store == nil {
		return nil, fmt.Errorf("imap source %q: a state store is required", name)
	}

	return &Source{
		cfg:      &cfg,
		store:    deps.Store,
		pool:     NewPool(cfg.NoopInterval),
		idle:     NewIdleWatcher(),
		events:   make(chan core.Event, 16),
		firstRun: true,
	}, nil
}

func (s *Source) Name() string { return s.cfg.InstanceName }
func (s *Source) Type() string { return "imap" }

func (s *Source) Start(ctx context.Context) error {
	s.ctx, s.cancel = context.WithCancel(context.Background())

	// Load the persisted cursor. A database error here is fatal for the
	// source: mistaking it for "first run" would reset the baseline and
	// re-notify the entire mailbox.
	data, found, err := s.store.GetCursor(s.Name())
	if err != nil {
		return fmt.Errorf("imap source %q: load cursor: %w", s.Name(), err)
	}
	if found {
		if err := json.Unmarshal(data, &s.cursor); err != nil {
			return fmt.Errorf("imap source %q: corrupt cursor: %w", s.Name(), err)
		}
	}
	s.cursorLoaded = true
	s.firstRun = !found

	s.wg.Add(1)
	go s.loop()

	if s.cfg.Idle {
		s.idle.Watch(s.cfg)
		s.wg.Add(1)
		go s.idleEvents()
		log.Printf("[%s] IDLE enabled; polling stays on as a %s safety net", s.Name(), s.cfg.PollInterval)
	}
	return nil
}

func (s *Source) Stop(ctx context.Context) error {
	if s.cancel != nil {
		s.cancel()
	}
	s.idle.Stop()
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
	}
	s.pool.Close()
	close(s.events)
	return nil
}

func (s *Source) Events() <-chan core.Event { return s.events }

// loop is the periodic poll cycle.
func (s *Source) loop() {
	defer s.wg.Done()
	s.poll()

	ticker := time.NewTicker(s.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.poll()
		}
	}
}

// idleEvents forwards IDLE growth events into an immediate poll cycle. The
// periodic ticker stays as the safety net for anything IDLE misses.
func (s *Source) idleEvents() {
	defer s.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case _, ok := <-s.idle.Events():
			if !ok {
				return
			}
			s.poll()
		}
	}
}

// poll runs one fetch cycle: read new messages, emit them as events. The
// cursor does not advance here — it advances per-message in Ack, so a crash
// mid-batch replays at most the tail of the batch (and dedup catches the
// overlap).
func (s *Source) poll() {
	pollCtx, cancel := context.WithTimeout(s.ctx, pollDeadline(s.cfg.PollInterval))
	defer cancel()
	start := time.Now()
	if err := s.pollOnce(pollCtx); err != nil {
		log.Printf("[%s] poll error: %v", s.Name(), err)
		s.noteFailure(err)
		return
	}
	s.noteSuccess(time.Since(start))
}

func (s *Source) pollOnce(ctx context.Context) error {
	s.mu.Lock()
	first := s.firstRun
	s.firstRun = false
	prev := s.cursor
	s.mu.Unlock()

	wc, err := s.pool.AcquireWork(ctx, s.Name(), s.cfg)
	if err != nil {
		return err
	}
	defer wc.Release()

	res, err := Fetch(ctx, wc.Client(), s.cfg, prev.LastUID, first, prev.UIDValidity)
	if err != nil {
		var changed *UIDValidityChanged
		if errors.As(err, &changed) {
			log.Printf("[%s] %v, resetting baseline", s.Name(), changed)
			s.mu.Lock()
			s.cursor = cursorData{UIDValidity: changed.Current}
			cur := s.cursor
			s.mu.Unlock()
			return s.saveCursor(cur)
		}
		return err
	}

	// The UIDVALIDITY/highest-UID baseline moves forward even when nothing
	// arrived, so a quiet mailbox does not re-SELECT and re-scan the same
	// range forever.
	if len(res.Messages) == 0 {
		s.mu.Lock()
		changed := s.cursor.UIDValidity != res.UIDValidity || res.HighestUID > s.cursor.LastUID
		if changed {
			s.cursor.UIDValidity = res.UIDValidity
			if res.HighestUID > s.cursor.LastUID {
				s.cursor.LastUID = res.HighestUID
			}
		}
		cur := s.cursor
		s.mu.Unlock()
		if changed {
			return s.saveCursor(cur)
		}
		return nil
	}

	log.Printf("[%s] %d new message(s)", s.Name(), len(res.Messages))

	// Record the validity immediately; per-message UIDs advance in Ack.
	s.mu.Lock()
	s.cursor.UIDValidity = res.UIDValidity
	cur := s.cursor
	s.mu.Unlock()
	if err := s.saveCursor(cur); err != nil {
		return err
	}

	for _, msg := range res.Messages {
		msg.Source = s.Name()
		select {
		case s.events <- core.Event{Message: msg, FirstRun: first}:
		case <-s.ctx.Done():
			return s.ctx.Err()
		}
	}
	return nil
}

func (s *Source) saveCursor(cur cursorData) error {
	data, err := json.Marshal(cur)
	if err != nil {
		return err
	}
	return s.store.SetCursor(s.Name(), data)
}

// Ack advances the cursor past the acked message. It is idempotent and
// tolerant of out-of-order calls: the cursor only ever moves forward, so a
// late ack for an older message is a no-op.
func (s *Source) Ack(ctx context.Context, msg core.Message) error {
	uid, err := strconv.ParseUint(msg.Ref["uid"], 10, 32)
	if err != nil {
		// An unparseable ref must not wedge the pipeline: the message was
		// still fully processed, so acking it is correct; only cursor
		// advancement is impossible.
		log.Printf("[%s] ack: bad uid ref %q (cursor not advanced)", s.Name(), msg.Ref["uid"])
		return nil
	}
	s.mu.Lock()
	if uint32(uid) <= s.cursor.LastUID {
		s.mu.Unlock()
		return nil
	}
	s.cursor.LastUID = uint32(uid)
	cur := s.cursor
	s.mu.Unlock()
	return s.saveCursor(cur)
}

// Command executes write-back operations:
//
//	mark_read  — user pressed the read button; always honoured
//	delivered  — pipeline reports successful delivery; marks \Seen only when
//	             the instance has mark_seen configured
func (s *Source) Command(ctx context.Context, cmd core.Command) error {
	uid, err := strconv.ParseUint(cmd.Args["uid"], 10, 32)
	if err != nil {
		return fmt.Errorf("imap command %s: bad uid %q", cmd.Kind, cmd.Args["uid"])
	}
	switch cmd.Kind {
	case "mark_read":
		return s.markSeen(ctx, []uint32{uint32(uid)})
	case "delivered":
		if !s.cfg.MarkSeen {
			return nil
		}
		return s.markSeen(ctx, []uint32{uint32(uid)})
	default:
		return core.ErrUnsupportedCommandf(cmd.Kind)
	}
}

func (s *Source) markSeen(ctx context.Context, uids []uint32) error {
	wc, err := s.pool.AcquireWork(ctx, s.Name(), s.cfg)
	if err != nil {
		return err
	}
	defer wc.Release()
	return MarkSeen(ctx, wc.Client(), s.cfg, uids)
}

// Probe connects once and reports server capabilities plus the mailbox's
// message count. `mailer test` uses it as a connectivity check; the probe
// dials its own connection rather than touching the pool, so a probe can
// never interfere with the watch/work separation.
func (s *Source) Probe(ctx context.Context) ([]string, uint32, error) {
	client, err := Dial(ctx, s.cfg)
	if err != nil {
		return nil, 0, err
	}
	defer client.Close()

	caps := client.Caps()
	var list []string
	for cap := range caps {
		list = append(list, string(cap))
	}
	sort.Strings(list)

	status, err := client.Status(s.cfg.Mailbox, &imap.StatusOptions{NumMessages: true}).Wait()
	if err != nil {
		return list, 0, fmt.Errorf("status %s: %w", s.cfg.Mailbox, err)
	}
	var count uint32
	if status.NumMessages != nil {
		count = *status.NumMessages
	}
	client.Logout().Wait()
	return list, count, nil
}

// ---- health / status --------------------------------------------------------

// Status is the per-source health snapshot for the daemon's /status endpoint.
type Status struct {
	LastPollSuccessUnix int64   `json:"last_poll_success_unix"`
	ConsecutiveFailures int64   `json:"consecutive_failures"`
	LastPollDurationSec float64 `json:"last_poll_duration_seconds"`
	AuthFailed          bool    `json:"auth_failed"`
}

func (s *Source) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Status{
		LastPollSuccessUnix: s.lastPollOKUnix,
		ConsecutiveFailures: s.failures,
		LastPollDurationSec: s.lastPollDurSec,
		AuthFailed:          s.authFailed,
	}
}

// AuthFailureConsumed reports (once per failure episode) that credentials are
// being rejected, so the daemon can alert the operator exactly once per
// breakage instead of every poll interval.
func (s *Source) AuthFailureConsumed() (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.authFailed && !s.authAlerted {
		s.authAlerted = true
		return true, nil
	}
	return false, nil
}

func (s *Source) noteFailure(err error) {
	s.mu.Lock()
	s.failures++
	if err != nil && oauth.IsAuthError(err) {
		s.authFailed = true
	}
	s.mu.Unlock()
}

func (s *Source) noteSuccess(dur time.Duration) {
	s.mu.Lock()
	s.failures = 0
	s.lastPollOKUnix = time.Now().Unix()
	s.lastPollDurSec = dur.Seconds()
	// Clear the auth state so a later credential failure can alert again:
	// the operator fixed it once and deserves to hear about a second breakage.
	s.authFailed = false
	s.authAlerted = false
	s.mu.Unlock()
}
