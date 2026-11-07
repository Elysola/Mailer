package mail

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/elysola/mailer/internal/config"
)

// idleMode caps how long a single IDLE sits before the connection is cycled.
//
// RFC 2177 lets a server drop an IDLE that it considers idle-stale (Gmail
// does at ~29 min), and a dropped connection can go unnoticed until the next
// poll. Restarting well inside that window keeps a dead watch connection a
// matter of minutes rather than an hour.
//
// It is also the safety net for the whole feature: even if every NOTIFY never
// arrives, the watcher resyncs at this interval, so IDLE can only ever make
// delivery faster, never less reliable than plain polling.
var idleMode = 20 * time.Minute

// ErrWatchClosed reports that the IDLE connection dropped on its own, so the
// caller should redial with backoff instead of trusting the session.
var ErrWatchClosed = errors.New("imap: watch connection closed")

// IdleWatcher holds one IDLE-capable watch connection per account and reports
// mailbox growth.
//
// Only *growth* is reported. An IMAP NOTIFY carries EXISTS (message count) and
// EXPUNGE but no UIDs, so the watcher cannot know which mail is new: it hands
// the poller a "something arrived" nudge and lets the normal UID fetch do the
// work. That keeps one code path authoritative for de-duplication and the UID
// cursor, and means a missed or spurious event costs latency at worst.
type IdleWatcher struct {
	mu     sync.Mutex
	states map[string]*idleState
	events chan string // account name
	done   chan struct{}
	once   sync.Once
}

type idleState struct {
	cancel context.CancelFunc
}

// NewIdleWatcher returns a watcher with no accounts registered.
func NewIdleWatcher() *IdleWatcher {
	return &IdleWatcher{
		states: make(map[string]*idleState),
		events: make(chan string, 64),
		done:   make(chan struct{}),
	}
}

// Events reports the account name of a mailbox that just grew.
func (w *IdleWatcher) Events() <-chan string { return w.events }

// Watch starts (or restarts) IDLE for acc. It is safe to call repeatedly: a
// duplicate call for an already-watched account is a no-op, and a call after
// Stop restarts cleanly.
func (w *IdleWatcher) Watch(acc config.Account) {
	w.mu.Lock()
	defer w.mu.Unlock()

	select {
	case <-w.done:
		return
	default:
	}
	if _, ok := w.states[acc.Name]; ok {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	w.states[acc.Name] = &idleState{cancel: cancel}
	go w.loop(ctx, acc)
}

// Stop terminates every watcher and closes the events channel once all
// goroutines have returned.
func (w *IdleWatcher) Stop() {
	w.once.Do(func() {
		w.mu.Lock()
		for _, st := range w.states {
			st.cancel()
		}
		w.states = make(map[string]*idleState)
		close(w.done)
		w.mu.Unlock()
	})
}

// forget removes an account so a later Watch starts a fresh connection (used
// after an unrecoverable IDLE failure).
func (w *IdleWatcher) forget(account string) {
	w.mu.Lock()
	if st, ok := w.states[account]; ok {
		st.cancel()
		delete(w.states, account)
	}
	w.mu.Unlock()
}

// loop keeps an IDLE session alive for one account until ctx is cancelled.
func (w *IdleWatcher) loop(ctx context.Context, acc config.Account) {
	defer w.forget(acc.Name)

	backoff := 5 * time.Second
	for {
		if ctx.Err() != nil {
			return
		}

		if _, err := w.session(ctx, acc); err != nil {
			// Never let a broken IDLE be fatal: the poller keeps polling
			// regardless, so we only lose latency while retrying.
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 5*time.Minute {
				backoff *= 2
			}
			continue
		}
		backoff = 5 * time.Second
	}
}

// session runs one dial+SELECT+IDLE cycle. It returns nil only when the cycle
// ended normally (idleMode elapsed, or ctx cancelled), so the caller can skip
// the backoff.
//
// The handler blocks the client, as go-imap requires, so it only does a
// non-blocking send and drops the event if the poller is behind. Losing an
// event is harmless: the periodic resync covers it.
func (w *IdleWatcher) session(ctx context.Context, acc config.Account) (bool, error) {
	notify := make(chan struct{}, 1)
	var lastCount uint32

	options := dialOptions(ctx)
	options.UnilateralDataHandler = &imapclient.UnilateralDataHandler{
		Mailbox: func(data *imapclient.UnilateralDataMailbox) {
			if data.NumMessages == nil {
				return
			}
			if *data.NumMessages <= lastCount {
				// EXPUNGE or a re-announcement; not growth.
				lastCount = *data.NumMessages
				return
			}
			lastCount = *data.NumMessages
			select {
			case notify <- struct{}{}:
			default:
			}
		},
	}

	addr := imapAddress(acc)
	var (
		client *imapclient.Client
		err    error
	)
	if acc.TLS {
		client, err = imapclient.DialTLS(addr, options)
	} else {
		client, err = imapclient.DialStartTLS(addr, options)
	}
	if err != nil {
		log.Printf("[idle:%s] connect: %v", acc.Name, err)
		return false, err
	}
	defer func() {
		_ = client.Logout().Wait()
		_ = client.Close()
	}()

	if err := authenticate(ctx, client, acc); err != nil {
		log.Printf("[idle:%s] %v", acc.Name, err)
		return false, err
	}

	caps := client.Caps()
	if !caps.Has(imap.CapIdle) && !caps.Has(imap.CapIMAP4rev2) {
		log.Printf("[idle:%s] server does not advertise IDLE; falling back to polling", acc.Name)
		return true, nil
	}

	sel, err := client.Select(acc.Mailbox, nil).Wait()
	if err != nil {
		log.Printf("[idle:%s] select %s: %v", acc.Name, acc.Mailbox, err)
		return false, err
	}
	lastCount = sel.NumMessages

	idle, err := client.Idle()
	if err != nil {
		log.Printf("[idle:%s] IDLE: %v", acc.Name, err)
		return false, err
	}
	log.Printf("[idle:%s] watching %s (%d message(s))", acc.Name, acc.Mailbox, sel.NumMessages)

	timer := time.NewTimer(idleMode)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			_ = idle.Close()
			return true, nil
		case <-timer.C:
			_ = idle.Close()
			return true, nil
		case <-notify:
			// Stop IDLE and let the poller issue the real fetch; keeping the
			// session up would race the work connection's SELECT.
			if err := idle.Close(); err != nil {
				return false, err
			}
			if err := idle.Wait(); err != nil {
				return false, err
			}
			select {
			case w.events <- acc.Name:
			case <-ctx.Done():
				return true, nil
			}
			return true, nil
		case <-client.Closed():
			return false, ErrWatchClosed
		}
	}
}
