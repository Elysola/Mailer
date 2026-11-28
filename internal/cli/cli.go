// Package cli implements mailer's operator subcommands: test, plugins, auth,
// queue, doctor and migrate-config. The daemon itself lives in internal/daemon;
// everything here is offline tooling that reads the same config and state.
package cli

import (
	"fmt"
	"os"
)

// Usage prints the top-level help for the mailer binary.
func Usage() {
	fmt.Fprint(os.Stderr, `mailer - IMAP to Telegram/Discord notification daemon

Usage:
  mailer [flags]          run the daemon (default)
  mailer test [-config f] check IMAP login + notifier delivery, then exit
  mailer plugins list [-dir d] list loaded WASM plugins
  mailer plugins validate [-dir d] validate plugin manifests and binaries
  mailer plugins test [-dir d] -input mail.json run plugins against a JSON mail
  mailer auth gmail -account a@b -client-id ID -client-secret-file F
                          authorize a Gmail mailbox over OAuth2 and emit a token
                          file for oauth2.token_file (--no-browser for headless)
  mailer queue ls [-source s] [-json] [-limit n]
                          show the notification retry/defer queue
  mailer queue drop [-source s | -all] [-yes]
                          discard queued notifications (needs -yes)
  mailer doctor [-json]   read-only health report: config, queue, progress
  mailer migrate-config [-config f] [-out f]
                          convert a v1 config file to v2 format
  mailer version          print version, commit and build metadata

Flags:
  -config string   path to the YAML config file (default "config.yaml")
`)
}
