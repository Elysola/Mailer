# Configuration

Mailer is configured with a single YAML file. Every entry under `sources`,
`notifiers`, `processors` and `llms` is a **component instance**: `name` labels
it, `type` selects its factory, and every other field belongs to the component
itself. Adding a new component type upstream never changes the schema of this
file.

```yaml
version: 2
```

The file must declare `version: 2`. v1 files are converted with
`mailer migrate-config`.

## daemon — process-wide behavior

```yaml
daemon:
  poll_interval: 60s          # how often sources poll (IDLE sources use this as fallback)
  health_port: 9100           # HTTP health/metrics endpoint; 0 disables
  retry_attempts: 2           # delivery retries, excluding the first try
  retry_delay: 5s             # base backoff delay, doubling per attempt
  seen_retention: 2160h       # how long delivered Message-IDs are remembered (90d default)
  max_pending_per_source: 200 # cap on queued retries re-attempted per scan
```

## state — persistence

```yaml
state:
  driver: sqlite
  file: state.db              # in Docker, point at the mounted volume
```

One SQLite file carries everything: source cursors, deduplication memory,
the retry/defer queue and key-value scratch space for processors. v1 databases
are migrated in place on first start.

## sources, notifiers, processors, llms

Each is a list of instances:

```yaml
sources:
  - name: inbox               # referenced by routes, logs, queue commands
    type: imap                # selects the component factory
    host: imap.example.com
    # ...type-specific fields

notifiers:
  - name: tg
    type: telegram
    bot_token_file: /run/secrets/tg_token
    chat_ids: ["123456789"]
```

Multiple instances of the same type are normal — a work and a personal IMAP
account, two Telegram bots, three webhooks. Names are unique within their
kind and are how routes refer to them.

Component reference pages:

- [Sources](/guide/sources) — `imap`, `maildir`
- [Notifiers](/guide/notifiers) — `telegram`, `discord`, `slack`, `ntfy`, `gotify`, `webhook`
- [Processors](/guide/processors) — `filter`, `regex`, `set`, `header`, `template`, `throttle`, `exec`, `wasm`, `llmsummarize`, `llmclassify`, `llmextract`

## Credentials and secrets

Three interchangeable forms, checked in this order:

```yaml
password_file: /run/secrets/imap_pass   # 1. file contents (first non-empty line)
password: ${IMAP_PASSWORD}              # 2. environment expansion (${VAR} anywhere)
password: "literal"                     # 3. inline value
```

`*_file` fields win over inline values when both are present. Environment
expansion runs over the entire file before parsing, so `${VAR}` works in any
string. Missing variables expand to empty — which usually fails validation
loudly at startup, exactly where you want it.

## templates — named rendering pairs

```yaml
templates:
  default:
    title: "[{{.Source}}] {{.Subject}}"
    body: |
      {{.Preview}}
      — {{.From}}
```

Templates are Go `text/template` strings over the message. Routes can adopt a
named template or carry an inline one; the `default` template applies
otherwise. The notification text is computed *after* processors run, so fields
they extracted are available.

## routes — delivery policy

Routes are the single source of truth for what happens to each message:
which processors run, which notifiers receive it, which template renders it.
They are powerful enough to deserve [their own page](/guide/routing).

```yaml
routes:
  - name: otp-fast-path
    when: 'has(fields.otp_code)'
    to: [tg-personal]
    stop: true

  - name: catch-all          # exactly one route must match everything
    processors: [noise-filter]
    to: [tg]
```

Rows evaluate in order; every matching row contributes its notifiers and
processors (union, deduplicated) until a row with `stop: true` terminates
evaluation. A row without `when` always matches. The loader rejects a config
whose routes cannot match every message.

## Validation philosophy

The loader is strict on purpose: unknown types, broken selectors, unparseable
regexes and templates, dangling references to nonexistent instances — all fail
at startup with a line-level error, rather than silently disabling themselves
at runtime. `mailer doctor` re-runs the same validation offline.
