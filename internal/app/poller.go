package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/elysola/mailer/internal/config"
	"github.com/elysola/mailer/internal/mail"
	"github.com/elysola/mailer/internal/notify"
	"github.com/elysola/mailer/internal/oauth"
	"github.com/elysola/mailer/internal/plugin"
	"github.com/elysola/mailer/internal/state"
)

const (
	pollTimeout = 30 * time.Second

	// pendingAgeLimit is how long an undelivered notification may sit in the
	// retry queue before it is dropped and counted.
	pendingAgeLimit = 7 * 24 * time.Hour
	// pendingInitialDelay is the backoff after the first failure; each
	// subsequent round doubles it up to pendingMaxDelay.
	pendingInitialDelay = 30 * time.Second
	pendingMaxDelay     = time.Hour
	// seenCleanInterval is how often the seen_messages table is pruned.
	seenCleanInterval = 24 * time.Hour
)

// AccountStatus is the non-sensitive health summary for one account.
type AccountStatus struct {
	LastPollSuccessUnix  int64   `json:"last_poll_success_unix"`
	ConsecutiveFailures  int64   `json:"consecutive_failures"`
	PendingNotifications int     `json:"pending_notifications"`
	LastPollDurationSec  float64 `json:"last_poll_duration_seconds"`
	// AuthFailed reports credentials the server rejected. Exposed because it
	// is the one failure the daemon cannot recover from on its own.
	AuthFailed bool `json:"auth_failed"`

	// AuthAlerted is internal bookkeeping: the alert for the current failure
	// was already sent. Never serialised.
	AuthAlerted bool `json:"-"`
}

type Poller struct {
	cfg           *config.Config
	store         *state.Store
	pool          *mail.Pool
	notifiers     []notify.Notifier
	plugins       *plugin.Engine
	accNotifs     map[string][]string
	firstRun      map[string]bool
	mu            sync.Mutex
	metrics       *Metrics
	status        map[string]*AccountStatus
	lastSeenClean time.Time
}

func New(cfg *config.Config, store *state.Store) (*Poller, error) {
	pool := mail.NewPool(cfg.NoopInterval)

	notifMap := make(map[string][]string)
	for _, a := range cfg.Accounts {
		if len(a.Notifiers) == 0 {
			continue
		}
		notifMap[a.Name] = a.Notifiers
	}

	var notifiers []notify.Notifier

	markRead := notify.MarkReadFunc(func(ctx context.Context, account string, uid uint32) error {
		acc, ok := findAccount(cfg.Accounts, account)
		if !ok {
			return fmt.Errorf("unknown account %q", account)
		}
		wc, err := pool.AcquireWork(ctx, acc)
		if err != nil {
			return err
		}
		defer wc.Release()
		return mail.MarkSeen(ctx, wc.Client(), acc, []uint32{uid})
	})

	if cfg.Telegram.Enabled {
		notifiers = append(notifiers, notify.NewTelegram(cfg.Telegram, cfg.ReadButton, markRead))
	}
	if cfg.Discord.Enabled {
		discord, err := notify.NewDiscord(cfg.Discord, cfg.Accounts, cfg.ReadButton, markRead)
		if err != nil {
			for _, n := range notifiers {
				if c, ok := n.(notify.Closer); ok {
					c.Close()
				}
			}
			pool.Close()
			return nil, err
		}
		notifiers = append(notifiers, discord)
	}

	plugins, err := plugin.LoadDir(cfg.PluginDir, plugin.Limits{
		Timeout: cfg.PluginTimeout, MaxInput: cfg.PluginMaxInput, MaxOutput: cfg.PluginMaxOutput,
	})
	if err != nil {
		for _, n := range notifiers {
			if c, ok := n.(notify.Closer); ok {
				c.Close()
			}
		}
		pool.Close()
		return nil, err
	}

	firstRun := make(map[string]bool, len(cfg.Accounts))
	status := make(map[string]*AccountStatus, len(cfg.Accounts))
	for _, a := range cfg.Accounts {
		firstRun[a.Name] = true
		status[a.Name] = &AccountStatus{}
	}

	return &Poller{
		cfg:       cfg,
		store:     store,
		pool:      pool,
		notifiers: notifiers,
		plugins:   plugins,
		accNotifs: notifMap,
		firstRun:  firstRun,
		metrics:   NewMetrics(),
		status:    status,
	}, nil
}

func (p *Poller) Run(ctx context.Context) {
	log.Printf("mailer started: %d account(s), interval %s, notifiers %v",
		len(p.cfg.Accounts), p.cfg.PollInterval, p.notifierNames())

	p.pollAll(ctx)
	p.cleanSeenIfNeeded()

	ticker := time.NewTicker(p.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Print("shutting down")
			return
		case <-ticker.C:
			p.pollAll(ctx)
			p.cleanSeenIfNeeded()
		}
	}
}

func (p *Poller) Metrics() *Metrics {
	return p.metrics
}

// Status returns a copy of the per-account health summary for /status.
func (p *Poller) Status() map[string]AccountStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]AccountStatus, len(p.status))
	for name, st := range p.status {
		row := *st
		if n, err := p.store.PendingCount(name); err == nil {
			row.PendingNotifications = n
		}
		out[name] = row
	}
	return out
}

func (p *Poller) cleanSeenIfNeeded() {
	p.mu.Lock()
	due := time.Since(p.lastSeenClean) >= seenCleanInterval
	if due {
		p.lastSeenClean = time.Now()
	}
	p.mu.Unlock()
	if !due {
		return
	}
	cutoff := time.Now().Add(-p.cfg.SeenRetention)
	if err := p.store.CleanSeen(cutoff); err != nil {
		log.Printf("[state] clean seen: %v", err)
		return
	}
	log.Printf("[state] pruned seen_messages older than %s", p.cfg.SeenRetention)
}

func (p *Poller) Close() {
	for _, n := range p.notifiers {
		if c, ok := n.(notify.Closer); ok {
			c.Close()
		}
	}
	p.pool.Close()
}

func findAccount(accounts []config.Account, name string) (config.Account, bool) {
	for _, a := range accounts {
		if a.Name == name {
			return a, true
		}
	}
	return config.Account{}, false
}

func (p *Poller) pollAll(ctx context.Context) {
	var wg sync.WaitGroup
	for _, acc := range p.cfg.Accounts {
		wg.Add(1)
		go func(acc config.Account) {
			defer wg.Done()
			pollCtx, cancel := context.WithTimeout(ctx, pollTimeout)
			defer cancel()
			start := time.Now()
			if err := p.pollAccount(pollCtx, acc); err != nil {
				log.Printf("[%s] poll error: %v", acc.Name, err)
				p.notePollFailure(acc.Name)
				p.alertAuthFailure(ctx, acc.Name, err)
				return
			}
			p.notePollSuccess(acc.Name, time.Since(start))
		}(acc)
	}
	wg.Wait()
}

func (p *Poller) notePollSuccess(account string, dur time.Duration) {
	p.mu.Lock()
	st := p.status[account]
	if st != nil {
		st.LastPollSuccessUnix = time.Now().Unix()
		st.ConsecutiveFailures = 0
		st.LastPollDurationSec = dur.Seconds()
		// Clear the auth state so a later credential failure can alert
		// again: the operator fixed it once and deserves to hear about a
		// second breakage.
		st.AuthAlerted = false
		st.AuthFailed = false
	}
	p.mu.Unlock()
	p.metrics.Observe("mailer_last_poll_duration_seconds", map[string]string{"account": account}, dur.Seconds())
	p.metrics.Observe("mailer_last_poll_success_timestamp", map[string]string{"account": account}, float64(time.Now().Unix()))
}

func (p *Poller) notePollFailure(account string) {
	p.mu.Lock()
	if st := p.status[account]; st != nil {
		st.ConsecutiveFailures++
	}
	p.mu.Unlock()
	p.metrics.Inc("mailer_poll_failures_total", map[string]string{"account": account})
}

// alertAuthFailure surfaces a dead credential once, on the transition into the
// failed state.
//
// A revoked or expired refresh token makes every poll fail with the same
// permanent error, so without an alert the daemon silently stops delivering
// notifications and nothing points at the cause. The de-duplication matters as
// much as the alert: the poll interval would otherwise fire the same message
// every minute and bury the one that counts.
func (p *Poller) alertAuthFailure(ctx context.Context, account string, err error) {
	if err == nil || !oauth.IsAuthError(err) {
		return
	}

	p.mu.Lock()
	st := p.status[account]
	if st == nil || st.AuthAlerted {
		p.mu.Unlock()
		return
	}
	st.AuthAlerted = true
	st.AuthFailed = true
	p.mu.Unlock()

	log.Printf("[%s] credentials rejected; re-run `mailer auth gmail` and redeploy the token: %v", account, err)
	p.metrics.Inc("mailer_auth_failures_total", map[string]string{"account": account})

	acc, ok := findAccount(p.cfg.Accounts, account)
	if !ok {
		return
	}

	msg := mail.Message{
		Account: account,
		Subject: fmt.Sprintf("[mailer] %s: mail authorization expired", account),
		From:    "mailer",
	}
	body := fmt.Sprintf(
		"Mail polling for %s stopped because the mail server rejected the stored credentials.\n\n"+
			"Cause: %v\n\n"+
			"Fix: re-run `mailer auth gmail`, copy the new token file to oauth2.token_file, then restart the daemon.\n"+
			"Until then this account delivers no notifications.",
		account, err,
	)
	msg.TitleOverride = msg.Subject
	msg.TextOverride = body

	go func() {
		alertCtx, cancel := context.WithTimeout(ctx, pollTimeout)
		defer cancel()
		if sent := p.dispatch(alertCtx, msg, p.notifiersFor(acc)); len(sent) == 0 {
			log.Printf("[%s] no notifier accepted the credential alert", account)
		}
	}()
}

func (p *Poller) pollAccount(ctx context.Context, acc config.Account) error {
	// Step 1: flush any notifications that failed earlier (oldest UID first).
	p.retryPending(ctx, acc)

	prev, _, err := p.store.Get(acc.Name)
	if err != nil {
		// Propagate: treating a DB hiccup as "first run" would reset the
		// baseline and re-notify the entire mailbox.
		return err
	}

	p.mu.Lock()
	first := p.firstRun[acc.Name]
	p.firstRun[acc.Name] = false
	p.mu.Unlock()

	wc, err := p.pool.AcquireWork(ctx, acc)
	if err != nil {
		return err
	}
	defer wc.Release()

	res, err := mail.Fetch(ctx, wc.Client(), acc, prev.LastUID, first, prev.UIDValidity)
	if err != nil {
		var changed *mail.UIDValidityChanged
		if errors.As(err, &changed) {
			log.Printf("[%s] %v, resetting baseline", acc.Name, changed)
			return p.store.Set(acc.Name, state.AccountState{UIDValidity: changed.Current})
		}
		return err
	}

	p.metrics.Add("mailer_messages_fetched_total",
		map[string]string{"account": acc.Name},
		int64(len(res.Messages)),
	)

	newState := state.AccountState{LastUID: res.HighestUID, UIDValidity: res.UIDValidity}

	if len(res.Messages) == 0 {
		if newState != prev {
			return p.store.Set(acc.Name, newState)
		}
		return nil
	}

	log.Printf("[%s] %d new message(s)", acc.Name, len(res.Messages))

	notifiers := p.notifiersFor(acc)
	titleTmpl := p.templateFor("title", acc)
	textTmpl := p.templateFor("text", acc)

	var fullyDelivered []uint32
	for i := range res.Messages {
		msg := &res.Messages[i]
		wantNotifiers, pluginMatched := p.applyPlugin(ctx, msg)
		msg.TitleTmpl = titleTmpl
		msg.TextTmpl = textTmpl

		// A plugin may narrow delivery to specific channels (for example send
		// OTP codes to Telegram only). Unknown names are dropped; if the plugin
		// names only channels that are not configured, fall back to the
		// account default so the mail is not silently lost.
		targets := notifiers
		if pluginMatched && len(wantNotifiers) > 0 {
			routed := filterNotifiers(notifiers, wantNotifiers)
			if len(routed) == 0 {
				log.Printf("[%s] plugin requested notifiers %v, none configured; using defaults",
					acc.Name, wantNotifiers)
			} else {
				targets = routed
			}
		}

		dup, err := p.store.IsDuplicate(acc.Name, msg.MessageID)
		if err != nil {
			log.Printf("[%s] dedup check: %v", acc.Name, err)
		} else if dup {
			log.Printf("[%s] skip duplicate: %s", acc.Name, msg.MessageID)
			continue
		}

		failed := p.dispatch(ctx, *msg, targets)
		if len(failed) == 0 {
			fullyDelivered = append(fullyDelivered, msg.UID)
			if err := p.store.MarkDelivered(acc.Name, msg.MessageID); err != nil {
				log.Printf("[%s] mark delivered: %v", acc.Name, err)
			}
			continue
		}
		// Undelivered for at least one notifier: queue for retry. LastUID
		// still advances, but nothing is lost — the queue owns redelivery.
		if err := p.store.EnqueuePending(state.Pending{
			Account:       acc.Name,
			UID:           msg.UID,
			MessageID:     msg.MessageID,
			From:          msg.From,
			Subject:       msg.Subject,
			Date:          msg.Date,
			Preview:       msg.Preview,
			TitleOverride: msg.TitleOverride,
			TextOverride:  msg.TextOverride,
			Fields:        msg.Fields,
			Failed:        failed,
		}, time.Now().Add(pendingInitialDelay)); err != nil {
			log.Printf("[%s] enqueue pending: %v", acc.Name, err)
		}
	}

	// Reflect the new queue depth regardless of outcome.
	p.observePending(acc.Name)

	if err := p.store.Set(acc.Name, newState); err != nil {
		return err
	}

	if acc.MarkSeen && len(fullyDelivered) > 0 {
		if err := mail.MarkSeen(ctx, wc.Client(), acc, fullyDelivered); err != nil {
			log.Printf("[%s] mark seen: %v", acc.Name, err)
		}
	}
	return nil
}

// retryPending re-sends due notifications from the queue, oldest UID first.
func (p *Poller) retryPending(ctx context.Context, acc config.Account) {
	due, err := p.store.DuePending(acc.Name, time.Now(), p.cfg.MaxPendingPerAccount)
	if err != nil {
		log.Printf("[%s] pending queue read: %v", acc.Name, err)
		return
	}
	if len(due) == 0 {
		return
	}

	titleTmpl := p.templateFor("title", acc)
	textTmpl := p.templateFor("text", acc)
	all := p.notifiersFor(acc)
	now := time.Now()

	for _, item := range due {
		if ctx.Err() != nil {
			return
		}
		if now.Sub(item.Created) > pendingAgeLimit {
			if err := p.store.DeletePending(acc.Name, item.UID); err == nil {
				p.metrics.Inc("mailer_notify_dropped_total", map[string]string{"account": acc.Name})
				log.Printf("[%s] uid %d: dropped after %s in retry queue (%d attempt(s))",
					acc.Name, item.UID, pendingAgeLimit, item.Attempts)
			}
			continue
		}

		// Only retry the notifiers that still owe delivery.
		targets := filterNotifiers(all, item.Failed)
		if len(targets) == 0 {
			// All owed notifiers are gone from config; nothing left to do.
			_ = p.store.DeletePending(acc.Name, item.UID)
			continue
		}

		msg := mail.Message{
			Account:       item.Account,
			UID:           item.UID,
			MessageID:     item.MessageID,
			From:          item.From,
			Subject:       item.Subject,
			Date:          item.Date,
			Preview:       item.Preview,
			TitleOverride: item.TitleOverride,
			TextOverride:  item.TextOverride,
			Fields:        item.Fields,
			TitleTmpl:     titleTmpl,
			TextTmpl:      textTmpl,
		}
		failed := p.dispatch(ctx, msg, targets)
		if len(failed) == 0 {
			if err := p.store.DeletePending(acc.Name, item.UID); err != nil {
				log.Printf("[%s] delete pending %d: %v", acc.Name, item.UID, err)
			}
			if err := p.store.MarkDelivered(acc.Name, item.MessageID); err != nil {
				log.Printf("[%s] mark delivered (retry): %v", acc.Name, err)
			}
			log.Printf("[%s] uid %d: retried successfully after %d failed round(s)", acc.Name, item.UID, item.Attempts)
			if acc.MarkSeen {
				if wc, err := p.pool.AcquireWork(ctx, acc); err == nil {
					_ = mail.MarkSeen(ctx, wc.Client(), acc, []uint32{item.UID})
					wc.Release()
				}
			}
			continue
		}

		attempts := item.Attempts + 1
		delay := pendingInitialDelay << min(attempts-1, 20)
		if delay > pendingMaxDelay {
			delay = pendingMaxDelay
		}
		if err := p.store.UpdatePendingRetry(acc.Name, item.UID, failed, attempts, now.Add(delay)); err != nil {
			log.Printf("[%s] update pending %d: %v", acc.Name, item.UID, err)
		}
	}
	p.observePending(acc.Name)
}

func (p *Poller) observePending(account string) {
	n, err := p.store.PendingCount(account)
	if err != nil {
		return
	}
	p.metrics.Observe("mailer_pending_notifications", map[string]string{"account": account}, float64(n))
}

func filterNotifiers(all []notify.Notifier, names []string) []notify.Notifier {
	if len(names) == 0 {
		return all
	}
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	var out []notify.Notifier
	for _, n := range all {
		if want[n.Name()] {
			out = append(out, n)
		}
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (p *Poller) notifiersFor(acc config.Account) []notify.Notifier {
	if names, ok := p.accNotifs[acc.Name]; ok {
		filtered := make([]notify.Notifier, 0, len(names))
		for _, n := range p.notifiers {
			for _, name := range names {
				if n.Name() == name {
					filtered = append(filtered, n)
					break
				}
			}
		}
		return filtered
	}
	return p.notifiers
}

// applyPlugin lets a WASM processor rewrite the message. It returns the
// notifier names the plugin wants to target (empty means "no opinion") and
// whether a plugin actually matched. Errors are logged and swallowed: a broken
// plugin must never disturb the UID cursor or the retry queue.
func (p *Poller) applyPlugin(ctx context.Context, msg *mail.Message) ([]string, bool) {
	if p.plugins == nil || msg == nil {
		return nil, false
	}
	result, err := p.plugins.Process(ctx, plugin.Mail{
		Account: msg.Account, UID: msg.UID, MessageID: msg.MessageID,
		From: msg.From, To: msg.To, Cc: msg.Cc, Subject: msg.Subject,
		Date: msg.Date, Text: msg.BodyText, HTML: msg.HTML,
		Preview: msg.Preview, Headers: msg.Headers,
	})
	if err != nil {
		log.Printf("[%s] plugin processing: %v", msg.Account, err)
		return nil, false
	}
	if !result.Matched {
		return nil, false
	}
	msg.TitleOverride = result.Title
	msg.TextOverride = result.Text
	msg.Fields = result.Fields
	return result.Notifiers, true
}

func (p *Poller) templateFor(field string, acc config.Account) string {
	if acc.MessageTemplate != nil {
		switch field {
		case "title":
			return acc.MessageTemplate.Title
		case "text":
			return acc.MessageTemplate.Text
		}
	}
	if p.cfg.MessageTemplate != nil {
		switch field {
		case "title":
			return p.cfg.MessageTemplate.Title
		case "text":
			return p.cfg.MessageTemplate.Text
		}
	}
	return ""
}

// dispatch sends msg to every notifier, retrying inline per config. It
// returns the names of notifiers that still failed after inline retries
// (empty slice == full success).
func (p *Poller) dispatch(ctx context.Context, msg mail.Message, notifiers []notify.Notifier) []string {
	var failed []string
	for _, n := range notifiers {
		ok := false
		for attempt := 0; attempt <= p.cfg.RetryAttempts; attempt++ {
			if attempt > 0 {
				delay := p.cfg.RetryDelay * (1 << (attempt - 1))
				select {
				case <-ctx.Done():
					break
				case <-time.After(delay):
				}
			}
			if err := n.Send(ctx, msg); err != nil {
				if attempt < p.cfg.RetryAttempts {
					log.Printf("[%s] %s notify error (retry %d/%d): %v",
						msg.Account, n.Name(), attempt+1, p.cfg.RetryAttempts, err)
				} else {
					log.Printf("[%s] %s notify error (failed after %d attempts): %v",
						msg.Account, n.Name(), p.cfg.RetryAttempts+1, err)
				}
				continue
			}
			p.metrics.Inc("mailer_messages_delivered_total",
				map[string]string{"account": msg.Account, "notifier": n.Name()},
			)
			ok = true
			break
		}
		if !ok {
			failed = append(failed, n.Name())
		}
	}
	return failed
}

func (p *Poller) notifierNames() []string {
	names := make([]string, 0, len(p.notifiers))
	for _, n := range p.notifiers {
		names = append(names, n.Name())
	}
	return names
}
