package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/elysola/mailer/internal/config"
	"github.com/elysola/mailer/internal/store/sqlite"
)

// RunQueue implements `mailer queue {ls|drop}`: an operator view into the
// retry/defer queue. Without it the only way to answer "why did I not get
// notified?" was to open the SQLite file by hand.
func RunQueue(args []string) int {
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

// openState opens the configured state database for offline inspection.
func openState(configPath string) (*sqlite.Store, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("config: %v", err)
	}
	st, err := sqlite.Load(cfg.State.File)
	if err != nil {
		return nil, fmt.Errorf("state: %v", err)
	}
	return st, nil
}

func runQueueLs(args []string) int {
	fs := flag.NewFlagSet("queue ls", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "path to the YAML config file")
	source := fs.String("source", "", "only show this source")
	limit := fs.Int("limit", 50, "maximum rows to show")
	asJSON := fs.Bool("json", false, "emit JSON instead of a table")
	fs.Parse(args)

	store, err := openState(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	defer store.Close()

	rows, err := store.QueueRows(*source, *limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "queue: %v\n", err)
		return 1
	}

	if *asJSON {
		if rows == nil {
			rows = []sqlite.QueueRow{}
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
	fmt.Fprintln(w, "SOURCE\tKIND\tKEY\tATTEMPTS\tCREATED\tNEXT TRY\tFAILED\tSUBJECT")
	for _, p := range rows {
		failed := strings.Join(p.Failed, ",")
		if failed == "" {
			failed = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\n",
			p.Source, p.Kind, p.Key, p.Attempts,
			humanAge(now.Sub(p.Created)),
			humanUntil(time.Until(p.NextTry)),
			failed, truncate(p.Subject, 48))
	}
	w.Flush()

	fmt.Printf("\n%d row(s)\n", len(rows))
	stuck := 0
	for _, p := range rows {
		if p.Kind == "retry" && len(p.Failed) == 0 {
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
	source := fs.String("source", "", "drop this source's queue")
	all := fs.Bool("all", false, "drop every source's queue")
	yes := fs.Bool("yes", false, "skip the confirmation prompt")
	fs.Parse(args)

	if *source == "" && !*all {
		fmt.Fprintln(os.Stderr, "refusing to drop: pass -source NAME or -all")
		return 2
	}
	if *source != "" && *all {
		fmt.Fprintln(os.Stderr, "pass either -source or -all, not both")
		return 2
	}

	store, err := openState(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	defer store.Close()

	rows, err := store.QueueRows(*source, 1000)
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
			fmt.Printf("  %s %s %s: %s\n", p.Source, p.Kind, p.Key, truncate(p.Subject, 60))
		}
		fmt.Print("\nThese notifications will never be delivered. Re-run with -yes to proceed.\n")
		return 1
	}

	target := *source
	if *all {
		target = ""
	}
	dropped, err := store.DeleteQueued(target)
	if err != nil {
		fmt.Fprintf(os.Stderr, "drop: %v\n", err)
		return 1
	}
	fmt.Printf("dropped %d queued notification(s)\n", dropped)
	return 0
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func humanAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func humanUntil(d time.Duration) string {
	if d <= 0 {
		return "now"
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("in %ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("in %dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("in %dh", int(d.Hours()))
	}
}
