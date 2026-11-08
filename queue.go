package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"strings"
	"text/tabwriter"
	"time"

	"github.com/elysola/mailer/internal/config"
	"github.com/elysola/mailer/internal/state"
)

// runQueue implements `mailer queue {ls|drop}`: an operator view into the
// retry queue.
//
// Without it, the only way to answer "why did I not get notified?" was to open
// the SQLite file by hand — and the answer (a row stuck with a shrinking
// notifier set, or one retried twenty times) is exactly what an operator needs
// while the daemon is running.
func runQueue(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: mailer queue {ls|drop} [flags]")
		return 2
	}

	switch args[0] {
	case "ls":
		return runQueueLs(args[1:])
	case "drop":
		return runQueueDrop(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown queue command %q (want ls or drop)\n", args[0])
		return 2
	}
}

func runQueueLs(args []string) int {
	fs := flag.NewFlagSet("queue ls", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "path to the YAML config file")
	account := fs.String("account", "", "only show this account")
	limit := fs.Int("limit", 50, "maximum rows to show")
	asJSON := fs.Bool("json", false, "emit JSON instead of a table")
	fs.Parse(args)

	st, err := openStateForInspection(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	defer st.store.Close()

	var rows []state.Pending
	if *account != "" {
		rows, err = st.store.PendingRows(*account, *limit)
	} else {
		rows, err = st.store.AllPendingRows(*limit)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "queue: %v\n", err)
		return 1
	}

	if *asJSON {
		if rows == nil {
			rows = []state.Pending{}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rows); err != nil {
			fmt.Fprintf(os.Stderr, "encode: %v\n", err)
			return 1
		}
		return 0
	}

	if len(rows) == 0 {
		fmt.Println("queue is empty")
		return 0
	}

	now := time.Now()
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ACCOUNT\tUID\tATTEMPTS\tCREATED\tNEXT TRY\tFAILED\tSUBJECT")
	for _, p := range rows {
		failed := strings.Join(p.Failed, ",")
		if failed == "" {
			failed = "-"
		}
		fmt.Fprintf(w, "%s\t%d\t%d\t%s\t%s\t%s\t%s\n",
			p.Account, p.UID, p.Attempts,
			humanAge(now.Sub(p.Created)),
			humanUntil(p.NextTry.Sub(now)),
			failed, truncate(p.Subject, 48))
	}
	w.Flush()

	fmt.Printf("\n%d row(s)\n", len(rows))
	// An empty retry set on a queued row is worth calling out: it means the row
	// will never be retried successfully and will sit there until it ages out.
	stuck := 0
	for _, p := range rows {
		if len(p.Failed) == 0 {
			stuck++
		}
	}
	if stuck > 0 {
		fmt.Printf("%d row(s) have no notifier left to try — they will be dropped when they age out\n", stuck)
	}
	return 0
}

func runQueueDrop(args []string) int {
	fs := flag.NewFlagSet("queue drop", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "path to the YAML config file")
	account := fs.String("account", "", "drop this account's queue")
	all := fs.Bool("all", false, "drop every account's queue")
	yes := fs.Bool("yes", false, "skip the confirmation prompt")
	fs.Parse(args)

	if *account == "" && !*all {
		fmt.Fprintln(os.Stderr, "refusing to drop: pass -account NAME or -all")
		return 2
	}
	if *account != "" && *all {
		fmt.Fprintln(os.Stderr, "pass either -account or -all, not both")
		return 2
	}

	st, err := openStateForInspection(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	defer st.store.Close()

	target := *account
	if *all {
		target = ""
	}

	// Show what is about to be destroyed before destroying it: dropping the
	// queue discards undelivered mail notifications, which is not recoverable.
	rows, err := func() ([]state.Pending, error) {
		if target == "" {
			return st.store.AllPendingRows(1000)
		}
		return st.store.PendingRows(target, 1000)
	}()
	if err != nil {
		fmt.Fprintf(os.Stderr, "queue: %v\n", err)
		return 1
	}
	if len(rows) == 0 {
		fmt.Println("nothing to drop")
		return 0
	}
	if !*yes {
		fmt.Printf("about to drop %d queued notification(s):\n", len(rows))
		for _, p := range rows {
			fmt.Printf("  %s uid %d: %s\n", p.Account, p.UID, truncate(p.Subject, 60))
		}
		fmt.Print("\nThese notifications will never be delivered. Re-run with -yes to proceed.\n")
		return 1
	}

	accounts := map[string]bool{}
	for _, p := range rows {
		accounts[p.Account] = true
	}
	if target != "" {
		accounts = map[string]bool{target: true}
	}

	dropped := 0
	for acc := range accounts {
		n, err := st.store.DeletePendingForAccount(acc)
		if err != nil {
			fmt.Fprintf(os.Stderr, "drop %s: %v\n", acc, err)
			return 1
		}
		dropped += n
	}
	fmt.Printf("dropped %d queued notification(s)\n", dropped)
	return 0
}

// runDoctor implements `mailer doctor`: a read-only health report that works
// whether or not the daemon is running.
//
// `mailer test` answers "can I reach the server right now?" and sends real
// messages; `doctor` answers "is this deployment healthy?" without touching the
// network, which is the question you actually have when something is wrong.
func runDoctor(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "path to the YAML config file")
	asJSON := fs.Bool("json", false, "emit JSON instead of a report")
	fs.Parse(args)

	report := struct {
		Status   string       `json:"status"`
		Problems []string     `json:"problems"`
		Warnings []string     `json:"warnings"`
		Info     []string     `json:"info"`
		Accounts []doctorAcct `json:"accounts"`
	}{Status: "ok", Problems: []string{}, Warnings: []string{}, Info: []string{}}

	problem := func(format string, a ...any) {
		report.Problems = append(report.Problems, fmt.Sprintf(format, a...))
	}
	warn := func(format string, a ...any) {
		report.Warnings = append(report.Warnings, fmt.Sprintf(format, a...))
	}
	info := func(format string, a ...any) {
		report.Info = append(report.Info, fmt.Sprintf(format, a...))
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		report.Status = "error"
		problem("config: %v", err)
		return finishDoctor(report, *asJSON)
	}
	info("config %s loaded: %d account(s)", *configPath, len(cfg.Accounts))

	if !cfg.Telegram.Enabled && !cfg.Discord.Enabled {
		problem("no notifier is enabled — mail would be fetched and discarded")
	}
	if cfg.Telegram.Enabled {
		info("notifier telegram: enabled")
	}
	if cfg.Discord.Enabled {
		// Report only which transport, never the token or channel id: the
		// output is meant to be pasteable into an issue.
		switch {
		case cfg.Discord.BotToken != "" || cfg.Discord.BotTokenFile != "":
			info("notifier discord: enabled (bot API, channel %s)", orUnset(cfg.Discord.ChannelID))
		default:
			info("notifier discord: enabled (webhook)")
		}
	}
	if port, enabled := cfg.HealthListenPort(); enabled {
		info("health server: port %d", port)
	} else {
		warn("health server disabled (health_port: 0) — no /health, /status or /metrics")
	}
	info("poll interval: %s", cfg.PollInterval)

	st, err := openStateForInspection(*configPath)
	if err != nil {
		report.Status = "error"
		problem("%v", err)
		return finishDoctor(report, *asJSON)
	}
	defer st.store.Close()
	info("state file: %s", st.path)

	now := time.Now()
	for _, acc := range cfg.Accounts {
		a := doctorAcct{Name: acc.Name}

		saved, found, err := st.store.Get(acc.Name)
		switch {
		case err != nil:
			problem("[%s] read state: %v", acc.Name, err)
		case !found:
			a.Note = "never polled"
			warn("[%s] no progress recorded yet — the daemon has not completed a poll", acc.Name)
		default:
			a.LastUID = saved.LastUID
			a.UIDValidity = saved.UIDValidity
		}

		pending, err := st.store.PendingRows(acc.Name, 1000)
		if err != nil {
			problem("[%s] read queue: %v", acc.Name, err)
		}
		a.Pending = len(pending)
		var oldest time.Duration
		stuck := 0
		for _, p := range pending {
			if age := now.Sub(p.Created); age > oldest {
				oldest = age
			}
			if len(p.Failed) == 0 {
				stuck++
			}
		}
		if len(pending) > 0 {
			a.OldestPending = oldest.Round(time.Second).String()
			warn("[%s] %d notification(s) queued, oldest %s",
				acc.Name, len(pending), humanAge(oldest))
		}
		if stuck > 0 {
			// A queued row with no notifier left can never succeed: it is
			// dead weight that occupies a queue slot until it ages out.
			problem("[%s] %d queued row(s) have no notifier left to try", acc.Name, stuck)
		}
		if limit := cfg.MaxPendingPerAccount; limit > 0 && len(pending) >= limit {
			problem("[%s] queue is at the max_pending_per_account cap (%d) — new failures will be dropped",
				acc.Name, limit)
		}

		seen, err := st.store.SeenCount(acc.Name)
		if err == nil {
			a.Seen = seen
		}

		if acc.Idle {
			a.Mode = "idle"
		} else {
			a.Mode = "poll"
		}
		report.Accounts = append(report.Accounts, a)
	}

	if len(report.Problems) > 0 {
		report.Status = "error"
	} else if len(report.Warnings) > 0 {
		report.Status = "warn"
	}
	return finishDoctor(report, *asJSON)
}

type doctorAcct struct {
	Name          string `json:"name"`
	Mode          string `json:"mode"`
	LastUID       uint32 `json:"last_uid"`
	UIDValidity   uint32 `json:"uid_validity"`
	Pending       int    `json:"pending"`
	OldestPending string `json:"oldest_pending,omitempty"`
	Seen          int    `json:"seen_records"`
	Note          string `json:"note,omitempty"`
}

func finishDoctor(report struct {
	Status   string       `json:"status"`
	Problems []string     `json:"problems"`
	Warnings []string     `json:"warnings"`
	Info     []string     `json:"info"`
	Accounts []doctorAcct `json:"accounts"`
}, asJSON bool) int {
	code := 0
	switch report.Status {
	case "error":
		code = 1
	case "warn":
		code = 0 // warnings are not failures: a queue draining normally is fine
	}

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(report)
		return code
	}

	mark := map[string]string{"ok": "✓", "warn": "⚠", "error": "✗"}[report.Status]
	fmt.Printf("%s overall: %s\n\n", mark, report.Status)

	for _, s := range report.Problems {
		fmt.Printf("  ✗ %s\n", s)
	}
	for _, s := range report.Warnings {
		fmt.Printf("  ⚠ %s\n", s)
	}
	if len(report.Problems) > 0 || len(report.Warnings) > 0 {
		fmt.Println()
	}
	for _, s := range report.Info {
		fmt.Printf("  · %s\n", s)
	}

	if len(report.Accounts) > 0 {
		fmt.Println()
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ACCOUNT\tMODE\tLAST UID\tUIDVALIDITY\tPENDING\tSEEN")
		for _, a := range report.Accounts {
			lastUID := fmt.Sprint(a.LastUID)
			if a.Note != "" {
				lastUID = a.Note
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%d\n",
				a.Name, a.Mode, lastUID, a.UIDValidity, a.Pending, a.Seen)
		}
		w.Flush()
	}
	return code
}

// inspectedStore is an opened state database plus the path it came from, so a
// report can name the file.
type inspectedStore struct {
	store *state.Store
	path  string
}

// openStateForInspection opens the configured state database read-write but
// never starts the daemon, so it works alongside a running instance.
func openStateForInspection(configPath string) (*inspectedStore, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	st, err := state.Load(cfg.StateFile)
	if err != nil {
		return nil, fmt.Errorf("state: %w", err)
	}
	return &inspectedStore{store: st, path: cfg.StateFile}, nil
}

func humanAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func humanUntil(d time.Duration) string {
	if d <= 0 {
		return "now"
	}
	return "in " + humanAge(d)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// orUnset renders an empty string as "(unset)" so a report never shows a bare
// colon with nothing after it.
func orUnset(s string) string {
	if s == "" {
		return "(unset)"
	}
	return s
}
