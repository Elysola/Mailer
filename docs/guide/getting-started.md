# Getting started

Mailer is a single static binary that watches mailboxes and forwards new mail
to the chat services you already use. Everything runs on a host you control;
the architecture is a small set of composable components — **sources** produce
mail, **processors** transform it, **notifiers** deliver it, and **routes**
decide what goes where.

## Requirements

- An IMAP mailbox reachable over TLS (implicit TLS on 993, or STARTTLS on 143) —
  or a local **maildir** if your mail never leaves the machine.
- At least one delivery target: a Telegram bot token, a Discord webhook, or any
  of the Slack / ntfy / Gotify / webhook endpoints.
- For Gmail or Google Workspace accounts, an OAuth2 client — password login is
  no longer accepted by Google. See [Gmail over OAuth2](/guide/gmail-oauth2).

## Install

Download a release binary, or build from source:

```bash
git clone https://github.com/elysola/mailer
cd mailer
go build -o mailer ./cmd/mailer
```

Docker images and a compose file are included in the repository.

## Configure

Copy `config.example.yaml` to `config.yaml`. A minimal working setup is one
source, one notifier, and the required catch-all route:

```yaml
version: 2

sources:
  - name: inbox
    type: imap
    host: imap.example.com
    port: 993
    tls: true
    username: me@example.com
    password_file: /run/secrets/imap_password   # or ${IMAP_PASSWORD}
    mailbox: INBOX

notifiers:
  - name: tg
    type: telegram
    bot_token_file: /run/secrets/telegram_token
    chat_ids: ["123456789"]

routes:
  - name: catch-all
    to: [tg]
```

Credentials can be written inline, read from a file (`*_file` fields), or
expanded from the environment (`${VAR}`). File indirection is recommended —
it keeps secrets out of backups and version control.

## Verify

Before running the daemon, check the whole chain end to end:

```bash
./mailer test        # IMAP login + notifier delivery probe
./mailer doctor      # config validity, queue state, polling progress
```

`mailer test` connects to every configured source and sends a real test
notification through every notifier — the fastest way to catch a typo in a
token or a firewall rule before they surprise you in production.

## Run

```bash
./mailer             # reads ./config.yaml by default
./mailer -config /etc/mailer/config.yaml
```

The daemon exposes a health and metrics endpoint (default port `9100`) and
persists all state in a single SQLite file — cursor positions, deduplication
memory and the retry queue all survive restarts.

## Migrating from v1

Existing v1 configs are converted mechanically:

```bash
./mailer migrate-config -config config.yaml
```

The converter writes `config.v2.yaml` next to the original; review it, then
rename. Your existing state database is migrated in place on first start —
cursors, dedup memory and pending retries carry over.

## Next steps

- [Configuration reference](/guide/configuration) — every section of the file
- [Processors](/guide/processors) — extraction, throttling, templating, AI
- [Routing & selectors](/guide/routing) — the rules language that ties it together
