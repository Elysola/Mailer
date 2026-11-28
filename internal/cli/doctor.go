package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/elysola/mailer/internal/config"
)

// RunDoctor implements `mailer doctor`: a read-only health report that works
// whether or not the daemon is running. `mailer test` answers "can I reach the
// server right now?" by touching the network; doctor answers "is this
// deployment healthy?" from local state alone.
func RunDoctor(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "path to the YAML config file")
	asJSON := fs.Bool("json", false, "emit JSON instead of a report")
	fs.Parse(args)

	report := doctorReport{
		Status:   "ok",
		Problems: []string{},
		Warnings: []string{},
		Info:     []string{},
	}
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
	info("config %s loaded: %d source(s), %d notifier(s), %d processor(s)",
		*configPath, len(cfg.Sources), len(cfg.Notifiers), len(cfg.Processors))

	if len(cfg.Notifiers) == 0 {
		problem("no notifier configured — mail would be fetched and discarded")
	}
	for _, n := range cfg.Notifiers {
		info("notifier %s: type %s", n.Name, n.Type)
	}
	if port, enabled := cfg.HealthListenPort(); enabled {
		info("health server: port %d", port)
	} else {
		warn("health server disabled (health_port: 0) — no /health, /status or /metrics")
	}
	info("poll interval: %s", cfg.Daemon.PollInterval)

	store, err := openState(*configPath)
	if err != nil {
		report.Status = "error"
		problem("%v", err)
		return finishDoctor(report, *asJSON)
	}
	defer store.Close()
	info("state file: %s", cfg.State.File)

	now := time.Now()
	for _, src := range cfg.Sources {
		a := doctorSource{Name: src.Name, Type: src.Type}

		if _, found, err := store.GetCursor(src.Name); err != nil {
			problem("[%s] read cursor: %v", src.Name, err)
		} else if !found {
			a.Note = "never polled"
			warn("[%s] no progress recorded yet — the daemon has not completed a poll", src.Name)
		}

		rows, err := store.QueueRows(src.Name, 1000)
		if err != nil {
			problem("[%s] read queue: %v", src.Name, err)
		}
		a.Pending = len(rows)
		var oldest time.Duration
		stuck := 0
		for _, p := range rows {
			if age := now.Sub(p.Created); age > oldest {
				oldest = age
			}
			if p.Kind == "retry" && len(p.Failed) == 0 {
				stuck++
			}
		}
		if len(rows) > 0 {
			a.OldestPending = oldest.Round(time.Second).String()
			warn("[%s] %d notification(s) queued, oldest %s",
				src.Name, len(rows), humanAge(oldest))
		}
		if stuck > 0 {
			problem("[%s] %d queued row(s) have no notifier left to try", src.Name, stuck)
		}
		if limit := cfg.Daemon.MaxPendingPerSource; limit > 0 && len(rows) >= limit {
			problem("[%s] queue is at the max_pending_per_source cap (%d) — new failures will be dropped",
				src.Name, limit)
		}

		if seen, err := store.SeenCount(src.Name); err == nil {
			a.Seen = seen
		}
		report.Sources = append(report.Sources, a)
	}

	if len(report.Problems) > 0 {
		report.Status = "error"
	} else if len(report.Warnings) > 0 {
		report.Status = "warn"
	}
	return finishDoctor(report, *asJSON)
}

type doctorSource struct {
	Name          string `json:"name"`
	Type          string `json:"type"`
	Pending       int    `json:"pending"`
	OldestPending string `json:"oldest_pending,omitempty"`
	Seen          int    `json:"seen_records"`
	Note          string `json:"note,omitempty"`
}

type doctorReport struct {
	Status   string         `json:"status"`
	Problems []string       `json:"problems"`
	Warnings []string       `json:"warnings"`
	Info     []string       `json:"info"`
	Sources  []doctorSource `json:"sources"`
}

func finishDoctor(report doctorReport, asJSON bool) int {
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

	status := map[string]string{"ok": "✓ healthy", "warn": "⚠ warnings", "error": "✗ problems"}[report.Status]
	fmt.Printf("status: %s\n", status)
	for _, i := range report.Info {
		fmt.Printf("  info:    %s\n", i)
	}
	for _, w := range report.Warnings {
		fmt.Printf("  warning: %s\n", w)
	}
	for _, p := range report.Problems {
		fmt.Printf("  problem: %s\n", p)
	}
	for _, s := range report.Sources {
		line := fmt.Sprintf("  source %s (%s): %d pending, %d seen", s.Name, s.Type, s.Pending, s.Seen)
		if s.Note != "" {
			line += " — " + s.Note
		}
		fmt.Println(line)
	}
	return code
}
