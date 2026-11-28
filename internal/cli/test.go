package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/elysola/mailer/internal/component"
	"github.com/elysola/mailer/internal/config"
	"github.com/elysola/mailer/internal/core"
	"github.com/elysola/mailer/internal/metricsx"
	"github.com/emersion/go-imap/v2"

	// Register the built-in component types so the config can instantiate
	// them. This is the single import site — the daemon imports it too, which
	// keeps the wiring symmetrical between the daemon and the test command.
	_ "github.com/elysola/mailer/internal/notifiers/discord"
	_ "github.com/elysola/mailer/internal/notifiers/telegram"
	_ "github.com/elysola/mailer/internal/processors/filter"
	_ "github.com/elysola/mailer/internal/sources/imap"
)

// RunTest implements `mailer test`: instantiate every configured component,
// connect each imap source once and send one test notification through every
// notifier. Exits non-zero if anything fails.
func RunTest(args []string) int {
	fs := flag.NewFlagSet("test", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "path to the YAML config file")
	fs.Parse(args)

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	failures := 0
	deps := component.Deps{Metrics: metricsx.NewMetrics()}

	// --- Sources -----------------------------------------------------------
	for _, inst := range cfg.Sources {
		fmt.Printf("source %s (type %s)\n", inst.Name, inst.Type)
		src, err := component.BuildSource(inst.Type, inst.Name, inst.Raw, deps)
		if err != nil {
			fmt.Printf("  ✗ build: %v\n", err)
			failures++
			continue
		}
		probe, ok := src.(interface {
			Probe(ctx context.Context) (caps []string, messages uint32, err error)
		})
		if !ok {
			fmt.Printf("  ⚠ probe unsupported for type %s; build ok\n", inst.Type)
			continue
		}
		caps, messages, err := probe.Probe(ctx)
		if err != nil {
			fmt.Printf("  ✗ probe: %v\n", err)
			failures++
			continue
		}
		fmt.Printf("  ✓ connected: %d message(s), capabilities: %s\n", messages, strings.Join(caps, " "))
		if hasCap(caps, "IDLE") {
			fmt.Printf("  ✓ IDLE available\n")
		} else {
			fmt.Printf("  ⚠ IDLE not advertised — poll mode will be used\n")
		}
	}

	// --- Notifiers ---------------------------------------------------------
	testEnv := core.Envelope{
		Message: core.Message{
			Source:  "test",
			Account: "test",
			From:    "Mailer Test <no-reply@example.com>",
			Subject: "mailer test notification",
			Date:    time.Now(),
			Preview: "This is a test message from `mailer test`. If you can see it, the notifier works.",
		},
	}

	for _, inst := range cfg.Notifiers {
		nt, err := component.BuildNotifier(inst.Type, inst.Name, inst.Raw, deps)
		if err != nil {
			fmt.Printf("notifier %s: ✗ build: %v\n", inst.Name, err)
			failures++
			continue
		}
		nctx, ncancel := context.WithTimeout(ctx, 30*time.Second)
		err = nt.Send(nctx, testEnv)
		ncancel()
		if err != nil {
			fmt.Printf("notifier %s: ✗ %v\n", inst.Name, err)
			failures++
			continue
		}
		fmt.Printf("notifier %s: ✓ test message sent\n", inst.Name)
	}

	if failures > 0 {
		fmt.Fprintf(os.Stderr, "\n%d check(s) failed\n", failures)
		return 1
	}
	fmt.Println("\nall checks passed")
	return 0
}

func hasCap(caps []string, want string) bool {
	for _, c := range caps {
		if strings.EqualFold(c, want) {
			return true
		}
	}
	return false
}

// capsString renders an imap.CapSet as a sorted, space-separated list.
func capsString(caps imap.CapSet) string {
	names := make([]string, 0, len(caps))
	for c := range caps {
		names = append(names, string(c))
	}
	sort.Strings(names)
	return strings.Join(names, " ")
}
