# WASM mail processors

Mailer can load optional WASM processors from this directory. Set `plugin_dir`
in `config.yaml` to enable them.

Each processor has a YAML manifest and a WASM entrypoint:

```yaml
name: otp
version: 1.0.0
api_version: mailer.plugin.v1
entrypoint: otp.wasm
priority: 100
limits:
  timeout: 100ms
  max_input: 524288
  max_output: 16384
```

The WASM module must export:

- `memory`
- `alloc(size) -> pointer`
- `process(pointer, length) -> packed pointer/length` where the low 32 bits
  are the result pointer and the high 32 bits are the result length. The
  packed value is returned as one `i64` result.

The input and output are UTF-8 JSON. The input follows `internal/plugin.Mail`;
the output follows `internal/plugin.Result`.

`Mail` also carries `attachments`, a metadata-only list (`filename`,
`content_type`, `size`, `content_id`, `inline`). The bytes are never included —
the daemon only parses IMAP BODYSTRUCTURE, so a plugin can react to *what* was
attached without the host ever downloading it.

The repository includes `sample-mail.json` as an input fixture. A compiled
plugin can be inspected and tested with:

```bash
mailer plugins list --dir ./plugins
mailer plugins validate --dir ./plugins
mailer plugins test --dir ./plugins --input ./plugins/sample-mail.json
```

Plugins are tried by descending priority and the first result with
`matched: true` wins. WASI, filesystem, network, and notification APIs are not
provided by the host.

## Declarative regex patterns

Because TinyGo's `regexp` cannot run inside a wazero reactor module (see
*Runtime sandbox*), the host evaluates regular expressions for you. Add a
`patterns` list to the manifest and the captures appear in `result.fields`,
which templates address as `{{.Fields.<name>}}`:

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

Semantics:

- Patterns use Go's RE2 syntax and run against the decoded mail text, before
  the WASM module, so they work even for a plugin that does no parsing at all.
- A field the plugin already returned wins over a pattern of the same name;
  plugins know more context than a generic pattern.
- Invalid regexps, unknown `source` values, and out-of-range `group` numbers
  are rejected at load time, so `mailer plugins validate` catches them.
- `required: true` turns a miss into a plugin error (logged, message still
  delivered with the default template).

## Result fields the host consumes

| Field | Effect |
|---|---|
| `matched` | `false` moves to the next plugin; `true` stops the search |
| `fields` | Available to templates as `{{.Fields.key}}` |
| `title` / `text` | Override the rendered notification |
| `notifiers` | Narrow delivery to these channel names. Names that are not configured are dropped; if none remain the account default is used so mail is never silently lost |

`fields`, `title`, `text`, and `notifiers` all survive redelivery: they are
persisted with the retry queue, so a transient notifier failure does not
downgrade the message to the default template.

## Runtime sandbox

The host runs each plugin in wazero's interpreter with
`WithCloseOnContextDone` enabled. Even so, a guest that never reaches an
interrupt check cannot be preempted: the runtime abandons the goroutine on
timeout and the plugin is treated as failed. Keep processors small and
bounded so they finish well inside `limits.timeout`.

Not every toolchain produces workable modules:

- **TinyGo `regexp` is unusable here.** `regexp` (and `fmt`, `time`, and
  `encoding/json` via `reflect`) either pulls in `syscall/js` imports the
  host does not provide, or crashes inside `setupDeferFrame` with a
  `runtime.nilPanic`. Avoid all four in plugin code.
- **`encoding/json` cannot be used to parse the input.** The bundled `otp`
  plugin scans the flat JSON fields by hand instead.

## Building plugins

WASM output is **not committed**. A checked-in binary cannot be reviewed and
drifts from its source, so `*.wasm` under `plugins/` is git-ignored. Build it
locally before running `mailer` (or `mailer plugins test`):

```bash
make plugins          # builds every plugins/*/go.mod into <dir>/<name>.wasm
make clean-plugins    # removes the built artifacts
```

`make plugins` skips directories without a `go.mod`, fails loudly when TinyGo
is missing, and can be pointed at a specific toolchain with
`make plugins TINYGO=/path/to/tinygo`.

CI (`.github/workflows/test.yml`) builds and validates every plugin on each
push and uploads the wasm files as the `wasm-plugins` artifact. The host test
suite does **not** need TinyGo: `internal/plugin/testdata` holds tiny
hand-assembled modules so `go test ./...` works on a bare Go install.

### `plugins/otp`

`plugins/otp/main.go` is deliberately written without `regexp`, `fmt`,
`encoding/json`, or `time` so it can be built with TinyGo for `wasip1` as a
reactor module. To build just this one:

```bash
./scripts/build-plugin.sh plugins/otp
```

The reactor module exports `_initialize`, which the host calls before
`alloc`/`process`. See `internal/plugin/testdata/README.md` for the fixtures
used by the host test suite.
