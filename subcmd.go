package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/elysola/mailer/internal/config"
	"github.com/elysola/mailer/internal/mail"
	"github.com/elysola/mailer/internal/notify"
)

// runTest implements `mailer test -config ...`: for every configured account
// it dials, logs in, SELECTs the mailbox and reports capabilities (including
// an IDLE probe); then it sends one test notification through every enabled
// notifier. Exits non-zero if anything fails.
func runTest(args []string) int {
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

	// --- IMAP accounts -------------------------------------------------
	for _, acc := range cfg.Accounts {
		fmt.Printf("IMAP %s (%s:%d tls=%v mailbox=%s)\n", acc.Name, acc.Host, acc.Port, acc.TLS, acc.Mailbox)
		client, err := mail.Dial(ctx, acc)
		if err != nil {
			fmt.Printf("  ✗ connect/login: %v\n", err)
			failures++
			continue
		}

		sel, err := client.Select(acc.Mailbox, nil).Wait()
		if err != nil {
			fmt.Printf("  ✗ select %s: %v\n", acc.Mailbox, err)
			failures++
		} else {
			fmt.Printf("  ✓ select %s: %d message(s), UIDNEXT %d, UIDVALIDITY %d\n",
				acc.Mailbox, sel.NumMessages, sel.UIDNext, sel.UIDValidity)
		}

		caps, capErr := client.Capability().Wait()
		if capErr != nil {
			fmt.Printf("  ✗ capability: %v\n", capErr)
			failures++
		} else {
			fmt.Printf("  ✓ capabilities: %s\n", capsString(caps))
			if caps.Has(imap.CapIdle) || caps.Has(imap.CapIMAP4rev2) {
				fmt.Printf("  ✓ IDLE available (real-time mode lands in v1.2)\n")
			} else {
				fmt.Printf("  ⚠ IDLE not advertised — poll mode will be used\n")
			}
		}

		client.Logout().Wait()
		client.Close()
	}

	// --- Notifiers -----------------------------------------------------
	testMsg := mail.Message{
		Account: "test",
		UID:     0,
		From:    "Mailer Test <no-reply@example.com>",
		Subject: "mailer test notification",
		Date:    time.Now(),
		Preview: "This is a test message from `mailer test`. If you can see it, the notifier works.",
	}

	var notifiers []notify.Notifier
	if cfg.Telegram.Enabled {
		notifiers = append(notifiers, notify.NewTelegram(cfg.Telegram, false, nil))
	}
	if cfg.Discord.Enabled {
		d, err := notify.NewDiscord(cfg.Discord, cfg.Accounts, false, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "discord init: %v\n", err)
			failures++
		} else {
			notifiers = append(notifiers, d)
		}
	}
	for _, n := range notifiers {
		if c, ok := n.(notify.Closer); ok {
			defer c.Close()
		}
	}

	for _, n := range notifiers {
		nctx, ncancel := context.WithTimeout(ctx, 30*time.Second)
		err := n.Send(nctx, testMsg)
		ncancel()
		if err != nil {
			fmt.Printf("notifier %s: ✗ %v\n", n.Name(), err)
			failures++
			continue
		}
		fmt.Printf("notifier %s: ✓ test message sent\n", n.Name())
	}

	if failures > 0 {
		fmt.Fprintf(os.Stderr, "\n%d check(s) failed\n", failures)
		return 1
	}
	fmt.Println("\nall checks passed")
	return 0
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

// usage prints the top-level help for the mailer binary.
func usage() {
	fmt.Fprint(os.Stderr, `mailer - IMAP to Telegram/Discord notification daemon

Usage:
  mailer [flags]          run the daemon (default)
  mailer test [-config f] check IMAP login + notifier delivery, then exit

Flags:
  -config string   path to the YAML config file (default "config.yaml")
`)
}
