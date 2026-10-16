package mail

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/elysola/mailer/internal/config"
	"github.com/emersion/go-imap/v2/imapclient"
)

// Pool keeps two connections per account:
//
//   - watch: long-lived, dedicated to background traffic (NOOP keep-alive and,
//     from v1.2, IDLE). It is never handed out, so interactive work cannot
//     interrupt it.
//   - work: borrowed exclusively by one operation at a time (UID FETCH, STORE
//     \Seen). While checked out it is excluded from keep-alive.
//
// Both connections are lazily dialled and validated (NOOP) before use.
type Pool struct {
	noopInterval time.Duration

	mu    sync.Mutex
	conns map[string]*accountConns
	done  chan struct{}
}

type accountConns struct {
	mu       sync.Mutex
	watch    *imapclient.Client
	watchErr error // set when watch dial failed; cleared on success
	work     *imapclient.Client
	workBusy bool
	nooping  bool
}

// WorkConn is a checked-out work connection. Release returns it to the pool.
type WorkConn struct {
	pool    *Pool
	account string
	client  *imapclient.Client
}

// Client returns the underlying IMAP client.
func (w *WorkConn) Client() *imapclient.Client { return w.client }

// Release returns the connection to the pool. It is safe to call twice.
func (w *WorkConn) Release() {
	w.pool.releaseWork(w.account, w.client)
	w.client = nil
}

func NewPool(noopInterval time.Duration) *Pool {
	p := &Pool{
		noopInterval: noopInterval,
		conns:        make(map[string]*accountConns),
		done:         make(chan struct{}),
	}
	if noopInterval > 0 {
		go p.keepAlive()
	}
	return p
}

func (p *Pool) account(name string) *accountConns {
	p.mu.Lock()
	defer p.mu.Unlock()
	ac, ok := p.conns[name]
	if !ok {
		ac = &accountConns{}
		p.conns[name] = ac
	}
	return ac
}

// AcquireWork borrows the account's work connection, blocking until the
// previous borrower releases it or ctx is done.
func (p *Pool) AcquireWork(ctx context.Context, acc config.Account) (*WorkConn, error) {
	ac := p.account(acc.Name)
	for {
		ac.mu.Lock()
		if !ac.workBusy {
			ac.workBusy = true
			client := ac.work
			ac.work = nil
			ac.mu.Unlock()

			if client != nil && client.Noop().Wait() == nil {
				return &WorkConn{pool: p, account: acc.Name, client: client}, nil
			}
			if client != nil {
				closeQuietly(client)
			}
			dialed, err := dial(ctx, acc)
			if err != nil {
				ac.mu.Lock()
				ac.workBusy = false
				ac.mu.Unlock()
				return nil, err
			}
			return &WorkConn{pool: p, account: acc.Name, client: dialed}, nil
		}
		ac.mu.Unlock()

		// Busy: wait for a release signal (polled via a small channel).
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("acquire work: %w", ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (p *Pool) releaseWork(account string, client *imapclient.Client) {
	if client == nil {
		return
	}
	ac := p.account(account)
	ac.mu.Lock()
	defer ac.mu.Unlock()
	if ac.work != nil && ac.work != client {
		closeQuietly(ac.work)
	}
	ac.work = client
	ac.workBusy = false
}

// Watch returns the account's dedicated background connection, dialling it if
// necessary. The caller must NOT store flags or fetch on it; it is for NOOP
// (and later IDLE) only. Returns nil on dial failure; the error is logged and
// surfaced via health metrics rather than returned.
func (p *Pool) Watch(acc config.Account) *imapclient.Client {
	ac := p.account(acc.Name)
	ac.mu.Lock()
	defer ac.mu.Unlock()

	if ac.watch != nil {
		if ac.watch.Noop().Wait() == nil {
			return ac.watch
		}
		closeQuietly(ac.watch)
		ac.watch = nil
	}
	client, err := dial(context.Background(), acc)
	if err != nil {
		log.Printf("[pool] %s: watch dial: %v", acc.Name, err)
		return nil
	}
	ac.watch = client
	return client
}

func (p *Pool) keepAlive() {
	ticker := time.NewTicker(p.noopInterval)
	defer ticker.Stop()
	for {
		select {
		case <-p.done:
			return
		case <-ticker.C:
			p.noopAll()
		}
	}
}

// noopAll pings every account's *watch* connection. The work connection is
// only pinged while idle (not checked out), to avoid interleaving commands
// with an in-flight FETCH/STORE.
func (p *Pool) noopAll() {
	p.mu.Lock()
	snapshot := make(map[string]*accountConns, len(p.conns))
	for k, v := range p.conns {
		snapshot[k] = v
	}
	p.mu.Unlock()

	for name, ac := range snapshot {
		ac.mu.Lock()
		watch, work, nooping := ac.watch, ac.work, ac.nooping
		if nooping || (watch == nil && (work == nil || ac.workBusy)) {
			ac.mu.Unlock()
			continue
		}
		if watch == nil && work != nil && !ac.workBusy {
			// No watch connection yet: promote the idle work client.
			ac.watch, ac.work = work, nil
			watch, work = ac.watch, nil
		}
		ac.nooping = true
		ac.mu.Unlock()

		var deadWatch *imapclient.Client
		if watch != nil && ping(watch) != nil {
			deadWatch = watch
		}
		var deadWork *imapclient.Client
		if work != nil && ping(work) != nil {
			deadWork = work
		}

		ac.mu.Lock()
		ac.nooping = false
		if deadWatch != nil && ac.watch == deadWatch {
			closeQuietly(ac.watch)
			ac.watch = nil
		}
		if deadWork != nil && ac.work == deadWork && !ac.workBusy {
			closeQuietly(ac.work)
			ac.work = nil
		}
		ac.mu.Unlock()

		if deadWatch != nil {
			log.Printf("[pool] %s: watch connection dead, will redial", name)
		}
	}
}

func ping(client *imapclient.Client) error {
	if client == nil {
		return fmt.Errorf("nil")
	}
	return client.Noop().Wait()
}

// Close logs out and closes every pooled connection.
func (p *Pool) Close() {
	select {
	case <-p.done:
		// already closed
		return
	default:
		close(p.done)
	}

	p.mu.Lock()
	snapshot := make(map[string]*accountConns, len(p.conns))
	for k, v := range p.conns {
		snapshot[k] = v
	}
	p.mu.Unlock()

	for _, ac := range snapshot {
		ac.mu.Lock()
		if ac.watch != nil {
			logoutQuietly(ac.watch)
			ac.watch = nil
		}
		if ac.work != nil {
			logoutQuietly(ac.work)
			ac.work = nil
		}
		ac.mu.Unlock()
	}
}

// closeQuietly closes a client without logout chatter, swallowing errors.
func closeQuietly(client *imapclient.Client) {
	if client == nil {
		return
	}
	_ = client.Close()
}

// logoutQuietly asks the server to log the session out, then closes. Errors
// are ignored on shutdown.
func logoutQuietly(client *imapclient.Client) {
	if client == nil {
		return
	}
	cmd := client.Logout()
	_ = cmd.Wait()
	_ = client.Close()
}
