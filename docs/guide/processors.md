# Processors

Processors are the pipeline's middle stages: each receives the envelope,
inspects or mutates it, and returns a verdict — continue, drop, defer, or
fail. They interleave freely because they all implement one interface. Most
share a `when` selector gate; an ungated processor sees every message, a gated
one only the messages that match — with validation at startup, so a broken
selector never silently disables a stage.

Execution order follows the route table: rows contribute their processor
lists in order, deduplicated. Put cheap shredders first, expensive AI last.

## filter — declarative drop rules

```yaml
processors:
  - name: noise-filter
    type: filter
    rules:
      - when: 'headers["X-Spam-Flag"] == "YES"'
        action: drop
      - when: 'subject ~= "^\\[spam\\]"'
        action: drop
```

The simplest shredder. Rules evaluate in order; the first match wins. Runs
before anything expensive so junk never bills LLM tokens.

## regex — extraction, rewriting, shredding

```yaml
  - name: otp-extract
    type: regex
    when: 'subject contains "code" or subject contains "验证"'
    extract:
      code: '(?:code|验证码)[:：\s]*(\d{4,8})'
    # replace:                      # body rewrite, $1 group refs allowed
    #   '\d{16}': "[card]"
    # drop_on_match: '(?i)unsubscribe'
```

`extract` applies patterns against subject, preview and body; the **first
capture group** wins (the whole match when the pattern has none) and lands in
`fields.code`, where templates and routes can use it. Verification codes,
amounts and order numbers become routable facts in one pass.

## set — stamp static fields

```yaml
  - name: tag-work
    type: set
    when: 'account == "work"'
    fields: { mailbox_type: work, priority: high }
    # drop: true                   # doubles as the simplest drop filter
```

## header — copy headers into fields

```yaml
  - name: list-headers
    type: header
    copy:
      List-Id: list
      X-Spam-Flag: spam
    # prefix: hdr_                 # namespace like llmextract does
```

Headers stay out of fields by default because most are noise — this is the
explicit opt-in. Mailing lists are the classic case: `List-Id` is the reliable
routing key where subject prefixes and sender addresses lie.

## template — re-render mid-pipeline

```yaml
  - name: otp-format
    type: template
    when: 'has(fields.code)'
    title: "验证码 {{.Fields.code}}"
    body: "来自 {{.Message.From}} 的验证码：{{.Fields.code}}"
```

Overrides title/body once extraction has filled fields — a verification-code
notification shaped only when a code actually exists.

## throttle — repeat suppression

```yaml
  - name: alert-throttle
    type: throttle
    window: 1h
    keys: [from, subject]           # the default; [subject] alone works too
    when: 'account == "monitoring"'
```

The first mail passes; identical key combinations inside the window are
dropped. Distinct from dedup: dedup keys on the immutable Message-ID to make
at-least-once delivery safe, throttle keys on content to protect the human.
Monitoring systems and broken bots stop ringing the phone every five minutes.

## exec — external commands

```yaml
  - name: webhook-out
    type: exec
    command: ["/usr/local/bin/notify-hook"]
    timeout: 5s
```

Runs the command per message with the envelope as JSON on stdin; the exit
code and stdout shape the verdict. The universal adapter for anything that
does not deserve a plugin.

## wasm — sandboxed plugins

```yaml
  - name: wasm-plugins
    type: wasm
    dir: ./plugins
    limits:                         # fallback; per-plugin manifests win
      timeout: 100ms
      max_input: 524288
      max_output: 16384
```

WebAssembly plugins with no file, network or environment access — untrusted
logic cannot exfiltrate credentials or mail contents. See
`mailer plugins list/validate/test` for the development tooling.

## LLM stages — classification, summary, extraction

```yaml
llms:
  - name: fast
    type: openai                    # OpenAI-compatible: DeepSeek/Qwen/Zhipu/Ollama
    base_url: https://api.deepseek.com
    api_key: ${DEEPSEEK_KEY}
    model: deepseek-chat
    timeout: 15s
    max_concurrency: 4

processors:
  - name: classify
    type: llmclassify
    llm: fast
    classes: [bill, notice, personal, newsletter, other]
    cache: true                     # repeat input reuses the cached verdict

  - name: summarize
    type: llmsummarize
    llm: fast
    when: 'fields.class == "bill"'  # zero-cost gate: no match, no API call
    prompt: |
      Summarize this email in at most three sentences, preserving
      amounts, dates and required actions.
    output: body                    # body | fields
    on_error: passthrough           # passthrough | drop | fail

  - name: extract-invoice
    type: llmextract
    llm: fast
    when: 'fields.class == "bill"'
    prefix: invoice_
    schema:
      type: object
      properties:
        amount:   { type: number }
        currency: { type: string }
        due_date: { type: string }
        vendor:   { type: string }
```

Three properties make AI practical here:

- **The `when` gate is evaluated locally at zero cost** — the LLM is never
  called for messages that do not match, so tokens are spent only on mail
  that matters.
- **Caching** keys on model + prompt + schema + input hash, so a re-delivered
  message never bills twice.
- **`on_error` is explicit**: `passthrough` delivers the unprocessed mail,
  `drop` discards it, `fail` enqueues a full retry — choose per stage, because
  the right answer differs between a summary (nice to have) and an extraction
  the route depends on (required).

`llmextract` output lands in fields with the configured prefix —
`invoice_amount`, `invoice_due_date` — immediately routable like anything
regex extracted. Local models through Ollama's OpenAI-compatible endpoint
keep contents on your machine entirely.
