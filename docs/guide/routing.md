# Routing & selectors

Routes are the single source of truth for delivery policy: which processors
run, which notifiers receive the message, which template renders it. The
selector language is the glue — it gates routes, processors, and filter rules
with one syntax.

## The route table

```yaml
routes:
  - name: otp-fast-path
    when: 'has(fields.code)'
    to: [tg-personal]
    stop: true

  - name: work-mail
    when: 'source == "work"'
    processors: [noise-filter, list-headers]
    to: [dc-alerts]

  - name: catch-all              # required: every message must match something
    processors: [noise-filter]
    to: [tg-main]
```

Evaluation semantics, precisely:

1. **Rows evaluate in order.** Every matching row contributes its `to` and
   `processors` to a union (deduplicated) — matching rows accumulate rather
   than compete.
2. **`stop: true` terminates evaluation.** Nothing after a matched stop row
   contributes — the fast path above shadows the catch-all for OTP mail.
3. A row **without `when` always matches**.
4. **Exactly one catch-all is required.** The loader rejects configs whose
   routes cannot match every message — a message with nowhere to go is a
   configuration bug, not a runtime surprise.

Per-row options:

| Field | Effect |
|---|---|
| `when` | Selector gate; absent means always-match |
| `to` | Notifier instance names receiving the message |
| `processors` | Processor instance names, applied in order |
| `template` | Inline `{title, body}` rendering override |
| `stop` | Terminate evaluation when this row matches |

## The selector language

Selectors are boolean expressions over the envelope:

```
subject contains "invoice" and not (account == "spam-trap")
fields.class == "bill" or has(fields.code)
headers["X-Spam-Flag"] == "YES"
subject ~= "^\[list-dev\]"
```

### Operands

| Operand | Meaning |
|---|---|
| `subject` | Message subject |
| `from` | Sender address |
| `preview` | Body preview text |
| `account` / `source` | Source instance name |
| `fields.name` | Extracted field (regex, LLM, set, header…) |
| `headers["Name"]` | Raw message header, case-insensitive |
| `"literal"` | String literal |

### Operators

| Operator | Meaning |
|---|---|
| `==` `!=` | Equality |
| `contains` | Substring match |
| `~= ` | Regular expression match |
| `has(fields.x)` | Field exists and is non-empty |
| `and` `or` `not` `( )` | Boolean composition |

### Design properties worth knowing

- **Validation at load time.** A broken selector fails startup with a
  line-level error — it never silently disables a route.
- **Absent fields don't error.** `fields.x == "y"` is simply false when `x`
  was never extracted, so ordering mistakes degrade gracefully.
- **Zero cost.** Evaluation is pure string matching — the reason `when` gates
  belong in front of every LLM stage.

## A complete example

Notification flow for a two-mailbox setup with AI triage:

```yaml
routes:
  # Verification codes: fast path to the personal device, nothing else.
  - name: otp-fast-path
    when: 'has(fields.code)'
    to: [tg-personal]
    stop: true

  # Bills get AI extraction on the way to the finance channel.
  - name: bills
    when: 'fields.class == "bill"'
    processors: [classify-first-noop, extract-invoice]   # already-classified mail
    to: [dc-finance]

  # Mailing lists route on List-Id, throttled, to a quiet channel.
  - name: lists
    when: 'has(fields.list)'
    processors: [alert-throttle]
    to: [ntfy-lists]

  # Everything else: noise-filtered, classified, delivered to the main chat.
  - name: catch-all
    processors: [noise-filter, otp-extract, list-headers, classify]
    to: [tg-main]
```

Note how the catch-all's processor chain populates the very fields the earlier
rows match on: routes evaluate their `when` against the envelope *after* the
union of all matching rows' processors has run. Ordering rows is about policy,
not data flow.
