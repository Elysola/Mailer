# Mailer

*[English](README.md) | [简体中文](README.zh-CN.md)*

A small Go daemon that periodically checks one or more mailboxes for new email
and pushes a notification to **Telegram** and/or **Discord** bots.

## Features

- Polls multiple IMAP accounts concurrently on a configurable interval.
- Implicit TLS (port 993) or STARTTLS (port 143).
- **IMAP connection pool** with NOOP keepalive — per account the pool keeps two
  connections (a watch connection for liveness checks and a work connection
  borrowed exclusively for fetch/store), instead of reconnecting every poll.
- De-duplicates by tracking the last processed UID per account in a **SQLite**
  database, so mail is never notified twice — without altering your mailbox by
  default. The Message-ID dedup table prunes itself (`seen_retention`,
  default 90 days).
- **Message-ID de-duplication** — the database also remembers which
  `Message-ID` values have been notified, catching duplicates across mailbox
  moves or UID changes.
- Handles `UIDVALIDITY` changes (server-side renumbering).
- Notification **retry with exponential backoff** (configurable attempts & delay),
  plus a **persistent pending queue**: notifications that still fail after the
  inline retries are stored in SQLite and re-attempted on later poll cycles
  (backoff 30 s → 1 h, dropped after 7 days with a counter). A temporary
  network/API outage never costs you a notification.
- Optional: mark notified mail as `\Seen`.
- **Optional "mark as read" button** — notifications carry an interactive button
  that flags the mail as read on the IMAP server on click (Telegram needs
  `bot_token`; Discord needs `bot_token`, see below).
- By default only notifies mail that arrives *after* startup (configurable).
- Notifications include sender, subject, date, and a plain-text body preview
  (MIME/charset decoded). The preview is **cleaned**: template/blank-line runs
  collapsed, non-breaking/zero-width spaces removed, 1×1 tracking pixels
  skipped — no more notification walls of empty lines. Length is configurable
  via `preview_len` (global and per-account, default 400 characters).
- **Platform-safe delivery** — text is truncated to the Telegram (4096) and
  Discord (embed limits) caps *after* escaping, so long mail never fails to
  deliver with an opaque API error.
- **Customizable message templates** — use Go `text/template` to format title
  and body globally or per-account.
- Telegram (Bot API, **MarkdownV2** mode) and Discord (webhook or bot token)
  delivery.
- **Per-account notifier routing** — choose which notifiers (Telegram, Discord,
  or both) apply to each account.
- **Per-account Discord routing** to a specific channel (频道) or thread (子区).
- **Secrets via file path or environment variables** — `password_file`,
  `bot_token_file`, `webhook_url_file`; all secret fields support
  `${VAR}` / `$VAR` expansion.
- **Health check (`/health`), account status (`/status`) + Prometheus metrics
  (`/metrics`)** on a configurable HTTP port.
- **`mailer test` subcommand** — verifies IMAP login, mailbox `SELECT`,
  capabilities (incl. an IDLE probe) and a test notification per channel,
  without starting the daemon.
- CGO-free build → tiny multi-arch (amd64/arm64) Docker image.
- Graceful shutdown on SIGINT/SIGTERM.

## Multiple accounts

The `accounts:` list supports any number of mailboxes — just add more entries.
They are polled **concurrently** and each keeps its own de-duplication state
(keyed by `name`, so give every account a unique `name` — duplicates are
rejected at startup):

```yaml
accounts:
  - name: primary
    host: imap.example.com
    username: me@example.com
    password: "app-password"
    tls: true

  - name: work
    host: outlook.office365.com
    username: me@work.com
    password: "app-password"
    tls: true

  - name: gmail
    host: imap.gmail.com
    username: me@gmail.com
    password: "app-password"   # Gmail needs an App Password
    tls: true
```

## Build

```bash
go build -o mailer .
```

## Configure

Copy the example and edit it:

```bash
cp config.example.yaml config.yaml
```

Key fields are documented inline in `config.example.yaml`.

### Telegram setup
1. Create a bot via [@BotFather](https://t.me/BotFather) and copy the token.
2. Send a message to your bot, then read
   `https://api.telegram.org/bot<TOKEN>/getUpdates` to find your numeric
   `chat_id`.
3. Set `telegram.enabled: true`, `bot_token`, and `chat_ids`.

### Discord setup
- **Webhook (simplest):** Channel settings → Integrations → Webhooks → New
  Webhook → copy URL into `discord.webhook_url`.
- **Bot:** create an application/bot, invite it with `Send Messages`, then set
  `discord.bot_token` and `discord.channel_id`.

#### Per-account routing: channel (频道) vs thread (子区)

Each account can override the global Discord destination so different mailboxes
post to different channels or threads. Add a `discord:` block under the account:

```yaml
discord:                     # global defaults
  enabled: true
  bot_token: "your-bot-token"
  channel_id: "111111111111" # fallback for accounts without a route

accounts:
  - name: primary
    # ...imap fields...
    discord:
      mode: channel          # 频道
      channel_id: "222222222222"

  - name: work
    # ...imap fields...
    discord:
      mode: thread           # 子区
      thread_id: "333333333333"
```

- `mode: channel` posts to `channel_id`.
- `mode: thread` posts to `thread_id` (a thread is itself a channel for bots;
  for webhooks the message is sent with `?thread_id=`).
- With a **webhook**, a per-account `webhook_url` can also be supplied; a
  webhook is bound to its own channel, so use a different webhook (or the bot
  transport) to target a different channel.

### Per-account notifier selection

By default all enabled notifiers receive every notification. To restrict which
notifiers fire for a particular account, list them under `notifiers`:

```yaml
telegram:
  enabled: true
discord:
  enabled: true

accounts:
  - name: quiet
    # ...imap fields...
    notifiers: [discord]        # only Discord, no Telegram
```

## Read button (`read_button`)

Optionally attach an interactive **mark as read** button to each notification.
Clicking it marks the mail as `\Seen` on the IMAP server directly, without
opening a mail client.

```yaml
read_button: true
```

Requirements:

- **Telegram:** needs `telegram.bot_token`. The app receives button presses via
  `getUpdates` long polling and confirms with `answerCallbackQuery`.
- **Discord:** needs `discord.bot_token` — button interactions must be handled by
  the bot's Gateway connection. In plain **webhook** mode buttons are inert (the
  app logs a warning at startup). Messages sent via the bot REST API
  (`channel_id`) always work; if you send via webhook, the webhook must have been
  created by the bot application so that clicks are dispatched to it.

> Note: this is an on-demand action — the IMAP connection is acquired from the
> pool at click time to run `STORE +FLAGS \Seen`. It does not affect the existing
> `mark_seen` (auto-seen after notify) behavior.

## Connection pool

Each account holds **two** persistent IMAP connections:

- a **watch** connection, used only for background liveness checks (NOOP);
- a **work** connection, borrowed exclusively (`AcquireWork` → use → `Release`)
  by whichever operation needs to talk to the server right now — a poll cycle
  or a "mark as read" button click.

Because poll and read-button traffic no longer share the same socket, a slow
fetch can no longer interleave with a `STORE` from another goroutine. If the
work connection happens to be idle, it is promoted to answer the NOOP, so a
live connection is never pinged needlessly; if the watch connection dies, the
next `Watch`/`AcquireWork` redials lazily.

Connections are kept alive with NOOP commands on a configurable
`noop_interval` (default 30 s). A NOOP that times out (10 s) closes the
connection, which is then redialed on demand.

## Notification retry

Retries happen at **two levels**.

**1. Inline, per notifier (same poll cycle).** Each failing notifier is retried
up to `retry_attempts` times with an exponential delay:

```yaml
retry_attempts: 2    # extra tries beyond the first (default: 2)
retry_delay: 5s      # base delay, doubled each attempt (default: 5 s)
```

**2. Persistent pending queue (across restarts).** If a message still has
failing notifiers after the inline retries, it is **not** dropped: it is
recorded in the `pending_notifications` table and re-attempted from now on
*only against the notifiers that failed* — notifiers that already delivered
are never sent the same mail twice. On every poll cycle the queue is drained
first, before new mail:

```yaml
max_pending_per_account: 200   # due queue items retried per account per poll cycle (default: 200)
```

- Backoff per message: 30 s → 60 s → 120 s → … capped at **1 hour**.
- Delivery order follows the message UID, so old mail is not overtaken.
- After **7 days** a still-undeliverable notification is dropped and counted in
  `mailer_notify_dropped_total`.
- Pending rows survive a restart; `mailer_pending_notifications` reports the
  current queue depth and `/status` shows it per account.

This closes the old failure mode where a Telegram API outage during a poll
would advance the UID cursor and lose the notification forever.

## Preview text

The body preview is cleaned up before it is sent, so templated "verification
code" emails (long runs of blank lines, spacer images, invisible whitespace)
do not blow up the notification:

- runs of blank lines collapse to a single blank line;
- non-breaking (`U+00A0`) and ideographic (`U+3000`) spaces become ordinary
  spaces; zero-width characters (`U+200B`, `U+FEFF`, `U+2007`) are removed;
- 1×1/2×2 spacer and `data:`-URI tracking-pixel images are skipped;
- the result is truncated to `preview_len` **characters** (runes, so CJK is
  not cut mid-glyph), default 400, configurable globally and per account.

On top of that, each notifier enforces its own platform limit *after*
formatting/escaping (Telegram 4096 characters; Discord 4000 for the embed
description, 256 for the title).

## Message de-duplication (Message-ID)

In addition to UID-based tracking, the `seen_messages` table records every
successfully delivered `Message-ID`. This catches duplicates that occur when a
message moves between folders (new UID) or the server renumbers (UID validity
change). No extra configuration is needed.

Rows older than `seen_retention` (default **2160 h = 90 days**) are pruned once
a day, so the table cannot grow without bound on a long-running install:

```yaml
seen_retention: 2160h
```

## Customizable message templates

Use Go [`text/template`](https://pkg.go.dev/text/template) to customise
notification format:

```yaml
message_template:
  title: "[{{.Subject}}]"
  text: |
    **From:** {{.From}}
    **Date:** {{.Date}}
    {{.Preview}}...
    {{"\n"}}`{{.MessageID}}`
```

The global template applies to every account; an account-level
`message_template` overrides it for that account alone.

Available fields: `{{.From}}`, `{{.Subject}}`, `{{.Date}}`, `{{.Preview}}`,
`{{.MessageID}}`, `{{.Text}}` (full body), `{{.Account}}` (config account name).

## Health check, status & Prometheus metrics

A built-in HTTP server (default port **9100**) exposes three endpoints:

| Endpoint    | Description                           |
|-------------|---------------------------------------|
| `GET /health` | Pure liveness probe: returns `{"status":"ok"}` as soon as the HTTP server is up — ideal for container health checks. It deliberately does **not** reflect IMAP/API failures, so one flaky notifier never gets you restarted into an outage. |
| `GET /status` | Per-account operational state as JSON: last successful poll (Unix time), consecutive poll failures, and pending (unsent) notifications. |
| `GET /metrics` | Prometheus text format — poll counts, notification counts, errors, queue depth. |

```console
$ curl -s localhost:9100/status
{"status":"ok","accounts":{"primary":{"last_poll_success_unix":1755561600,"consecutive_failures":0,"pending_notifications":2,"last_poll_duration_sec":0.41}}}
```

`/status` is intentionally limited to non-sensitive numbers: it exposes **no**
hostnames, usernames, message content, or raw error strings, so it can be
scraped or exposed behind a reverse proxy without leaking credentials.

Configure the port with `health_port` (set to `0` to disable the server).

Useful metrics:

| Metric | Meaning |
|--------|---------|
| `mailer_poll_failures_total{account}` | Poll cycles that ended with an error. |
| `mailer_last_poll_success_timestamp{account}` | Unix time of the last successful poll — alert when it goes stale. |
| `mailer_last_poll_duration_seconds{account}` | Duration of the most recent poll (gauge with last-value semantics). |
| `mailer_pending_notifications{account}` | Notifications queued for redelivery right now. |
| `mailer_notify_dropped_total{account}` | Notifications abandoned after 7 days. |
| `mailer_messages_fetched_total` / `mailer_messages_delivered_total` | Mail read from IMAP / delivered to a notifier. |

## `mailer test` — connection & notification check

`test` runs the same IMAP handshake the daemon uses, then tries a real
notification through every enabled notifier, and exits `1` if anything
failed:

```bash
./mailer -config config.yaml test
```

```console
$ ./mailer -config config.yaml test
IMAP primary (imap.example.com:993 tls=true mailbox=INBOX)
  ✓ select INBOX: 142 message(s), UIDNEXT 317, UIDVALIDITY 1
  ✓ capabilities: AUTH=PLAIN IDLE IMAP4rev2 LOG-IN-STARTTLS NAMESPACE
  ✓ IDLE available (real-time mode lands in v1.2)
IMAP work (imap.example.org:993 tls=true mailbox=INBOX)
  ✗ select INBOX: LOGIN failed: [AUTHENTICATIONFAILED] Invalid credentials
notifier telegram: ✓ test message sent
notifier discord: ✓ test message sent

1 check(s) failed
```

For each account it reports:

- **select** — credentials valid (login happens as part of dialing) and the
  configured mailbox exists, with its message count, `UIDNEXT` and
  `UIDVALIDITY`;
- **capabilities / IDLE probe** — server capabilities, and whether `IDLE` (or
  `IMAP4rev2`, which requires it) is available;
- **per-notifier test send** — one real message through every enabled
  notifier, so you see immediately whether the token/chat target works.

Note that this *does* send test messages to your chats/channels.

## Secrets management

All credential fields support two sources (evaluated in order):

1. **File path** — e.g. `password_file: /run/secrets/imap_password` (read from
   file, whitespace trimmed).
2. **Environment variable expansion** — e.g. `password: ${IMAP_PASSWORD}` via
   `os.ExpandEnv`.

In Docker you can use secrets mounted as files:

```yaml
password_file: /run/secrets/imap_pass
```

Or environment variables (e.g. in docker-compose):

```yaml
password: ${IMAP_PASSWORD}
```

The same applies to `bot_token` / `bot_token_file` (Telegram & Discord) and
`webhook_url` / `webhook_url_file` (Discord).

## Run

```bash
./mailer -config config.yaml
```

## Run with Docker

A multi-stage `Dockerfile` produces a tiny static image (Alpine + CA certs),
running as a non-root user. The build is CGO-free, so images are published for
both `linux/amd64` and `linux/arm64`.

### Pull the prebuilt image (GHCR)

Every push to the default branch and every `v*` tag builds and publishes an
image to the GitHub Container Registry via the workflow in
`.github/workflows/docker.yml`:

```bash
docker pull ghcr.io/elysola/mailer:latest
```

### Build & run directly

```bash
docker build -t mailer .

docker run -d --name mailer --restart unless-stopped \
  -e TZ=Asia/Shanghai \
  -v "$PWD/config.yaml:/app/config.yaml:ro" \
  -v "$PWD/data:/app/data" \
  mailer
```

### docker compose (recommended)

```bash
mkdir -p data                 # writable state directory
cp config.example.yaml config.yaml
# In config.yaml set:  state_file: /app/data/state.db
docker compose up -d
docker compose logs -f
```

> The container runs as UID `10001`. Make sure the mounted `./data` directory
> is writable by it, e.g. `sudo chown -R 10001:10001 data`, and set
> `state_file: /app/data/state.db` so de-duplication state survives restarts.

## Run as a systemd service

```ini
# /etc/systemd/system/mailer.service
[Unit]
Description=Mailer IMAP -> Telegram/Discord notifier
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/opt/mailer/mailer -config /opt/mailer/config.yaml
WorkingDirectory=/opt/mailer
Restart=always
RestartSec=10
User=mailer

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl enable --now mailer
```

## How de-duplication works

Two layers prevent duplicate notifications:

1. **UID tracking** — For each account the highest seen IMAP UID is stored in
   the `account_state` table. Each poll searches for `UID > last_uid`, so only
   genuinely new mail is fetched. On first run the current newest UID is
   recorded as the baseline (unless `notify_existing: true`), so you are not
   flooded with your entire inbox history.

2. **Message-ID dedup** — The `seen_messages` table records every successfully
   delivered `Message-ID`. When a message is moved between folders or the
   server renumbers (UID validity change), its `Message-ID` is still recognised
   and skipped.

## Project layout

```
main.go                       entrypoint, flag parsing, signal handling,
                              health/status & metrics HTTP server
subcmd.go                     `mailer test` subcommand
internal/config               YAML config loading, defaults, validation
internal/mail                 IMAP fetch, MIME preview extraction + cleanup,
                              mark-seen
internal/mail/pool.go         dual (watch/work) IMAP connection pool with
                              NOOP keepalive
internal/notify               Telegram (+MarkdownV2) + Discord notifiers,
                              platform-limit truncation
internal/state                SQLite: UID progress, Message-ID dedup,
                              pending-notification queue
internal/app                  scheduler tying fetch + notify + retry queue +
                              status together
internal/app/metrics.go       Prometheus metric counters
.github/workflows             CI: build & push the Docker image to GHCR;
                              vet + race-enabled tests
```

