# Mailer

*[English](README.md) | [简体中文](README.zh-CN.md)*

A small Go daemon that periodically checks one or more mailboxes for new email
and pushes a notification to **Telegram** and/or **Discord** bots.

## Features

- Polls multiple IMAP accounts concurrently on a configurable interval.
- **Optional real-time delivery (`idle: true`)** — keep an IMAP `IDLE` session
  open and poll an account the instant the server announces new mail, while the
  periodic poll stays on as a safety net. See "Real-time delivery (IDLE)".
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
- **WASM mail parsing plugins** — drop WASM processors into `plugin_dir` to
  parse the full body (extract codes, links, structured fields) and build a
  custom title and body. Plugins run in a wazero interpreter sandbox: no WASI,
  no filesystem, network or environment access, bounded by timeout and
  input/output size limits. A failing plugin never disturbs the UID cursor,
  Message-ID de-duplication or the retry queue. Ships with an `otp` example
  plugin that extracts 6-digit verification codes. See `plugins/README.md`.
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
make build          # CGO-free, version metadata injected from git
# or, without make:
go build -o mailer .
```

`mailer version` prints what the binary was built from. Release builds get the
real tag from GoReleaser; a local build reports `dev` plus the commit Go
records in its build info.

```console
$ ./mailer version
mailer v1.2.0 (9d1408b44ff5 linux/amd64, go1.26.10, 2026-11-07T13:48:28Z)
```

Tagged releases publish static linux/darwin binaries (amd64/arm64) with a
checksums file, and a multi-arch container image under
`ghcr.io/elysola/mailer`.

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

## Real-time delivery (IDLE)

Polling alone means a worst-case delay of one `poll_interval`. Set
`idle: true` on an account and the daemon instead keeps an `IDLE` session open
and polls *that* account the moment the server announces new mail:

```yaml
accounts:
  - name: primary
    host: imap.gmail.com
    username: me@gmail.com
    password: "app-password"
    tls: true
    idle: true          # sub-second delivery instead of "within a minute"
```

The design is deliberately conservative, because a notification daemon must
never become *less* reliable for being faster:

- **IDLE is an accelerator, not the source of truth.** An IMAP `EXISTS`
  notification carries a message count and no UIDs, so the watcher only nudges
  the poller; the ordinary UID-based fetch still does the work. De-duplication,
  the UID cursor and the retry queue keep exactly one code path.
- **Polling never stops.** The periodic tick remains as the safety net. A
  missed, dropped or never-sent event costs latency, never a notification.
- **Sessions recycle every 20 minutes**, well inside the ~29-minute
  server-side IDLE timeout, so a half-dead watch connection cannot hide for
  long.
- **Failures are non-fatal.** If IDLE cannot connect, cannot `SELECT`, or is
  not advertised at all, the account silently falls back to polling with
  exponential backoff. `mailer test` tells you which mode an account will use.

The IDLE session uses its own dedicated connection, so it never contends with
the connection pool.

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

## Rate limits

Both platforms throttle, and they answer a too-eager retry by *extending* the
ban — Telegram raises the `retry_after` value, Discord can escalate to a
**global** limit that silences every channel the bot can post to. So a `429` is
treated differently from an ordinary send failure:

- The platform's own wait is honoured. Telegram's `parameters.retry_after` and
  Discord's `Retry-After` header are both parsed (fractional seconds included);
  when neither is present a conservative 1 s floor is used rather than no delay
  at all. Absurd values are capped at 1 hour — the pending queue drops items
  after 7 days anyway, so a longer wait belongs to the normal backoff path.
- **Short waits (≤ 5 s) are absorbed inline**, because parking the mail in the
  queue would deliver it 30 s later for no reason.
- **Longer waits defer to the pending queue.** The notifier is marked failed, the
  window is remembered, and subsequent sends to that notifier are skipped until
  it expires — so a throttled channel never gets hammered by the rest of the
  mailbox.
- Throttled sends are counted in `mailer_notify_rate_limited_total`.

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
`{{.MessageID}}`, `{{.Text}}` (full body), `{{.Account}}` (config account name),
`{{.Attachments}}` (see below).

### Attachments

Attachment **metadata** is parsed from the message's IMAP BODYSTRUCTURE, so
nothing is downloaded — the daemon still fetches only the body text:

```yaml
message_template:
  text: |
    **From:** {{.From}}
    **Subject:** {{.Subject}}
    {{with .Attachments}}**Files:** {{len .}}{{end}}
    {{range .Attachments}}- {{.Filename}} ({{.ContentType}}, {{.Size}} B){{end}}
```

Each entry carries `Filename` (decoded from RFC 2047, e.g. `=?utf-8?B?...?=`),
`ContentType`, `Size` in bytes, `ContentID` and `Inline`. Body parts
(`text/plain`, `text/html`) are never reported; an unnamed inline part
referenced from the HTML body (a logo, a signature graphic) is skipped, while
a **named** inline part is kept, matching what mail clients display.

Because templates render `Size` as a raw byte count, `{{.AttachmentSummary}}`
is also available: it returns a ready-made `name (size)` list with human sizes
(`invoice.pdf (2.0 KB)`) and truncated names, or `""` when there are none. The
default template already includes it as an `Attachments:` line when non-empty.

Metadata also reaches plugins (`attachments` in the JSON payload) and survives
a failed delivery — a retried notification lists the same files.

## WASM parsing plugins

When templates are not enough (extracting verification codes, parsing
structured notifications), drop WASM processors into a directory. A plugin
receives the full mail as JSON (including `text`, `html` and `headers`) and
returns whether it matched plus an optional custom title and body:

```yaml
plugin_dir: ./plugins        # plugin directory; empty disables plugins
plugin_timeout: 100ms        # per-plugin execution budget
plugin_max_input: 524288     # max JSON bytes handed to the plugin
plugin_max_output: 16384     # max JSON bytes returned by the plugin
```

Each plugin is a `plugin.yaml` manifest plus a `.wasm` entrypoint (a flat
`plugins/otp.yaml` + `plugins/otp.wasm` layout also works). Plugins run in
descending `priority` order and the **first result with `matched: true`
wins**.

Plugins execute inside a wazero interpreter sandbox: WASI is not enabled, so
there is no filesystem, network or environment access, and no notification
API. Execution is bounded by the timeout and the input/output limits. A
plugin that errors or times out is logged and the default template is used;
the UID cursor, Message-ID de-duplication and the retry queue are untouched.

### Declarative regex patterns

TinyGo's `regexp` cannot run inside a wazero reactor module, so the host
evaluates regexes for you. List `patterns` in the manifest and their captures
land in `result.fields`, addressable from templates as `{{.Fields.<name>}}`:

```yaml
patterns:
  - name: repo
    regexp: '([\w.-]+/[\w.-]+)#(\d+)'
    source: subject   # text (default) | html | subject | preview | from
    group: 1          # 0 (default) = whole match
  - name: pr
    regexp: '([\w.-]+/[\w.-]+)#(\d+)'
    source: subject
    group: 2
  - name: code
    regexp: '\b(\d{6})\b'
    source: text
    required: true    # a miss is an error, not a silent empty field
```

- Patterns use Go RE2 syntax and run against the decoded mail text *before*
  the WASM module, so even a plugin that does no parsing gets regex power.
- A field the plugin already returned wins over a pattern of the same name;
  plugins know more context than a generic pattern.
- Invalid regexps, unknown `source` values, and out-of-range `group` numbers
  are rejected at load time, so `mailer plugins validate` catches them.
- `required: true` turns a miss into a plugin error (logged; the message is
  still delivered with the default template).

### Result fields the host consumes

| Field | Effect |
|---|---|
| `matched` | `false` moves on to the next plugin; `true` stops the search |
| `fields` | Available to templates as `{{.Fields.key}}` |
| `title` / `text` | Override the rendered notification |
| `notifiers` | Narrow delivery to these channel names. Names that are not configured are dropped; if none remain the account default is used so mail is never silently lost |

`fields`, `title`, `text` and `notifiers` all survive redelivery: they are
persisted with the retry queue, so a transient notifier failure does not
downgrade the message to the default template.

```bash
make plugins                                # build every plugin (needs TinyGo)
mailer plugins list     --dir ./plugins   # list loaded plugins
mailer plugins validate --dir ./plugins   # check manifest and entrypoint
mailer plugins test --dir ./plugins --input ./plugins/sample-mail.json
```

Plugin binaries are **not committed** (`plugins/*/*.wasm` is git-ignored): a
checked-in wasm cannot be reviewed and drifts from its source. CI builds and
validates every plugin on each push and uploads the artifacts. Host tests do
not need TinyGo at all — `internal/plugin/testdata` ships tiny hand-assembled
modules, so `go test ./...` works on a bare Go install.

Plugin authoring (manifest schema, JSON ABI, build instructions) lives in
[`plugins/README.md`](plugins/README.md). The bundled `otp` plugin extracts
6-digit verification codes from the mail body.


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

Configure the port with `health_port` (default `9100`; set it to `0` to run
without the HTTP server entirely — nothing is bound and no listener is
started).

Useful metrics:

| Metric | Type | Meaning |
|--------|------|---------|
| `mailer_poll_failures_total{account}` | counter | Poll cycles that ended with an error. |
| `mailer_last_poll_success_timestamp{account}` | gauge | Unix time of the last successful poll — alert when it goes stale. |
| `mailer_last_poll_duration_seconds{account}` | gauge | Duration of the most recent poll. |
| `mailer_poll_duration_seconds{account}` | histogram | Every poll cycle, for latency percentiles. |
| `mailer_notify_duration_seconds{account,notifier}` | histogram | Time per `Send` call, so a slow platform is visible. |
| `mailer_pending_notifications{account}` | gauge | Notifications queued for redelivery right now. |
| `mailer_notify_dropped_total{account}` | counter | Notifications abandoned after 7 days. |
| `mailer_notify_rate_limited_total{account,notifier}` | counter | Sends rejected by a platform rate limit (429). |
| `mailer_messages_fetched_total{account}` | counter | Mail read from IMAP. |
| `mailer_messages_delivered_total{account,notifier}` | counter | Notifications delivered. |
| `mailer_auth_failures_total{account}` | counter | IMAP authentication failures. |
| `mailer_uptime_seconds` | gauge | Seconds since process start. |

Runtime metrics are exposed alongside them for free, so an alert can tell
"mail stopped arriving" apart from "the process is out of memory":
`go_goroutines`, `go_memstats_alloc_bytes`, `go_memstats_heap_inuse_bytes`,
`go_memstats_heap_objects`, `go_memstats_sys_bytes`,
`go_memstats_gc_cpu_fraction`, `go_gc_cycles_total` and
`process_start_time_seconds`.

Example alert on latency and queue growth:

```yaml
- alert: MailerPollSlow
  expr: histogram_quantile(0.95,
          rate(mailer_poll_duration_seconds_bucket[15m])) > 10
  for: 10m
- alert: MailerQueueGrowing
  expr: mailer_pending_notifications > 50
  for: 15m
```

The exposition is hand-rendered rather than pulling in `client_golang`,
which keeps the dependency tree small; the output is validated against the
official Prometheus text parser in the test suite.

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
  ✓ IDLE available — real-time mode enabled for this account
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

## `mailer doctor` — read-only health report

`doctor` inspects a deployment without touching the network and without
sending anything. It answers "is this deployment healthy?" rather than "can I
reach the server right now?", which is the question you actually have when
something is wrong:

```bash
./mailer doctor -config config.yaml
./mailer doctor -config config.yaml -json   # machine-readable
```

```console
$ ./mailer doctor -config config.yaml
⚠ overall: warn

  ⚠ health server disabled (health_port: 0) — no /health, /status or /metrics
  ⚠ [primary] 2 notification(s) queued, oldest 4m

  · config config.yaml loaded: 1 account(s)
  · notifier telegram: enabled
  · poll interval: 5m0s
  · state file: ./state.db

ACCOUNT  MODE  LAST UID  UIDVALIDITY  PENDING  SEEN
primary  idle  312       1            2        140
```

It checks, per account:

- **progress** — whether a completed poll has been recorded at all, and the
  stored `LastUID` / `UIDVALIDITY`;
- **queue depth** — how many notifications are waiting and how old the oldest
  is, plus a hard error when a queued row has no notifier left to try (that
  row can never be delivered and only occupies a queue slot);
- **queue cap** — whether the account has reached
  `max_pending_per_account`, after which new failures are dropped;
- **de-duplication** — how many `Message-ID` records are retained;
- **notifier and health-server** configuration.

The exit code is `0` for `ok` and `warn`, `1` for `error` — so `doctor` is
safe to run from a cron/Timer check only if a queued-but-draining backlog
should not page anyone. `warn` deliberately does not fail: a queue that is
draining normally is not an outage.

## `mailer queue` — inspect and drop the retry queue

Undelivered notifications are retried from a queue in the state database.
`queue ls` shows what is in it, which is usually the fastest answer to "why
did I not get notified?":

```bash
./mailer queue ls -config config.yaml
./mailer queue ls -config config.yaml -account primary -json
```

```console
$ ./mailer queue ls -config config.yaml
ACCOUNT  UID  ATTEMPTS  CREATED  NEXT TRY  FAILED    SUBJECT
primary  311  3         4m       now       telegram  Your verification code
primary  312  1         1m       in 2m     -         Invoice for October

2 row(s)
1 row(s) have no notifier left to try — they will be dropped when they age out
```

Note that the `FAILED` column lists *notifiers*, not attempts: a row whose
attempt set has emptied out (for example because you disabled the last
working notifier) can never succeed.

`queue drop` discards queued notifications, which is not recoverable — hence
the preview and the mandatory `-yes`:

```bash
./mailer queue drop -config config.yaml -account primary   # preview only
./mailer queue drop -config config.yaml -account primary -yes
./mailer queue drop -config config.yaml -all -yes
```

Passing neither `-account` nor `-all` refuses with exit code `2`, so a typo
cannot silently destroy undelivered notifications.

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

### Endpoints that carry a credential must use https

Two settings are interpolated into a request URL in a way that makes the
credential part of the URL itself:

- `telegram.api_base` — the bot token is a path segment
  (`<api_base>/bot<token>/sendMessage`);
- `discord.webhook_url` — the webhook token *is* the URL.

Both are validated as absolute `https` URLs, so a plaintext `http://` value is
rejected at startup instead of quietly putting the token on the wire. A custom
`api_base` is still supported for a proxy or a self-hosted Bot API server, as
long as it is `https` and has no path or query:

```yaml
telegram:
  api_base: "https://tg-proxy.internal:8081"   # ok
  # api_base: "http://tg-proxy.internal:8081"  # rejected: would leak the token
```

## Gmail over OAuth2 (XOAUTH2)

Google switched off "less secure app" password login, so a Gmail account needs
either an App Password (2FA required) or OAuth2. OAuth2 is the stronger option:
the credential is a revocable, scope-limited token instead of a password, and it
keeps working when an organisation blocks App Passwords outright.

IMAP itself is not going away — Gmail still serves IMAP, it just requires
XOAUTH2 to authenticate. Nothing about the polling code changes.

### 1. Create the OAuth client

In [Google Cloud Console](https://console.cloud.google.com/apis/credentials):

1. Create (or pick) a project and enable the **Gmail API**.
2. Configure the OAuth consent screen. **User type: External** is fine for
   personal use; **Internal** is only available on Workspace.
3. Create credentials → **OAuth client ID** → application type **Desktop app**.
4. Note the client ID and client secret.

### 2. Authorize the mailbox

Run this **where a browser exists** — normally your laptop, not the server:

```bash
./mailer auth gmail \
  --account me@gmail.com \
  --client-id 1234-abc.apps.googleusercontent.com \
  --client-secret-file ~/.config/mailer/gmail_client_secret \
  --output gmail_token.json
```

This opens a browser and captures the callback on `127.0.0.1`. Approve the
consent screen and it writes `gmail_token.json` (mode `0600`) containing a
refresh token.

**On a headless machine**, add `--no-browser`. It prints the consent URL and
waits for you to paste back the full redirect URL after approving. The page
itself will fail to load — that is expected, since nothing is listening; copy
the address bar instead:

```bash
./mailer auth gmail --account me@gmail.com \
  --client-id ... --client-secret-file ... --no-browser --output gmail_token.json
```

You can also authorize on a laptop and copy the resulting token file to the
server. A refresh token is bound to the OAuth client and the Google account, not
to a machine or IP address, so moving it is legitimate and avoids running a
browser on the server.

### 3. Configure the account

Replace `password` with an `oauth2` block:

```yaml
accounts:
  - name: gmail
    host: imap.gmail.com
    port: 993
    username: me@gmail.com
    tls: true
    oauth2:
      client_id: 1234-abc.apps.googleusercontent.com
      client_secret_file: /run/secrets/gmail_client_secret
      token_file: /var/lib/mailer/gmail_token.json
```

`client_secret` (inline) and `client_secret_file` are both accepted; the file
form keeps the secret out of `config.yaml`. `token_file` must be readable by the
daemon and must not be group- or world-readable — the daemon refuses to use a
token file with looser permissions than `0600`.

Optional keys: `scope` (default `https://mail.google.com/`) and `token_url`
(default Google's endpoint; override for a test double or a proxy).

### Refreshing and expiry

The access token is refreshed automatically and cached in memory; the refresh
token is never written back to `config.yaml`. If the refresh token stops working,
the daemon sends **one** alert to the account's notifiers and sets
`auth_failed` in `/status`, then keeps retrying quietly. Re-run
`mailer auth gmail` and redeploy the token file to recover.

Two constraints worth knowing before you deploy:

- **Testing-mode consent screens expire refresh tokens after 7 days.** While the
  OAuth consent screen is in *Testing* status, Google limits refresh tokens for
  external users to 7 days; after that every refresh fails with `invalid_grant`.
  For a long-running daemon, publish the app (or use an Internal/Workspace app)
  so the token does not need weekly manual renewal.
- **`https://mail.google.com/` is a restricted scope.** It is required to read
  mail over IMAP. Using it in Testing mode with test users is fine, but
  distributing the app publicly requires Google's restricted-scope verification
  and a third-party security assessment. For your own mailbox, keep the app
  unverified and add yourself as a test user.

For the same reason, App Passwords remain a valid fallback for a single personal
account: they need 2FA, but no Cloud project, and they do not expire on a
7-day cycle.

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
                              platform cap truncation
internal/plugin               WASM plugin host (wazero sandbox, manifest
                              loading, JSON ABI, timeout & size limits)
plugins/                      example plugin and authoring docs (otp)
internal/state                SQLite: UID progress, Message-ID dedup,
                              pending-notification queue
internal/app                  scheduler tying fetch + notify + retry queue +
                              status together
internal/app/metrics.go       Prometheus registry: counters, gauges,
                              histograms, Go runtime stats
.github/workflows             CI: build & push the Docker image to GHCR;
                              vet + race-enabled tests + govulncheck
.github/dependabot.yml        monthly grouped dependency updates
```

