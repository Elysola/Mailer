# Notifiers

Notifiers deliver the rendered notification. All of them share three
behaviors:

- **Rate-limit aware** — when a platform answers 429, its `Retry-After` window
  propagates into the pipeline, which pauses that notifier instead of retrying
  blindly into a ban.
- **Construction-time validation** — a malformed token, missing chat ID or
  broken URL fails at startup, not at 3 AM when mail arrives.
- **Secret indirection** — every credential accepts the `*_file` form.

## telegram

The most capable integration: long messages split automatically, and a bot
token enables the interactive **标记已读 / mark-as-read** button.

```yaml
notifiers:
  - name: tg-main
    type: telegram
    bot_token_file: /run/secrets/tg_token   # or bot_token: "123:abc"
    chat_ids: ["123456789"]                 # multiple chats just work
    interactive: true                       # the mark-read button
```

Get a token from [@BotFather](https://t.me/BotFather); your chat ID is revealed
by messaging the bot and visiting
`https://api.telegram.org/bot<TOKEN>/getUpdates`.

## discord

Webhook-only delivery, or full bot integration for interactive buttons and
message edits.

```yaml
  - name: dc-alerts
    type: discord
    webhook_url_file: /run/secrets/dc_webhook
    # bot_token + channel_id enable interactive buttons; a plain webhook
    # is send-only — the platform's design, not mailer's choice.
    # bot_token_file: /run/secrets/dc_bot
    # channel_id: "1234567890"
```

## slack

Incoming-webhook integration:

```yaml
  - name: slack-ops
    type: slack
    webhook_url_file: /run/secrets/slack_webhook
    channel: "#alerts"          # optional override of the webhook's default
    username: mailer
    icon_emoji: ":mailbox:"
```

## ntfy

Push to [ntfy.sh](https://ntfy.sh) or your self-hosted instance:

```yaml
  - name: ntfy-alerts
    type: ntfy
    server: https://ntfy.sh
    topic_file: /run/secrets/ntfy_topic   # the topic IS the credential
    priority: 4                           # 1 min .. 5 max
    tags: [email, warning]
    # token_file: /run/secrets/ntfy_token   # bearer auth, or user/password
```

On ntfy anyone who knows the topic can publish to it — treat the topic name
itself as a secret.

## gotify

Self-hosted push with a minimal API:

```yaml
  - name: gotify-home
    type: gotify
    url: https://push.example.com
    token_file: /run/secrets/gotify_token
    priority: 5                           # 0..10
```

## webhook

The generic escape hatch — covers home automation, ticket systems, custom
bots, everything else:

```yaml
  - name: homeassistant
    type: webhook
    url: https://ha.example.com/api/webhook/mail-arrived
    method: POST                          # default
    headers:
      Authorization: Bearer ${HA_TOKEN}
    body: '{"text": {{.Message.Subject | json}}}'
    # success_status: 200                 # when the endpoint answers non-2xx
```

Without `body`, the entire envelope is posted as JSON. With it, the template
renders per message — use the `json` helper for interpolated strings, which
escapes quotes and backslashes so the output stays valid JSON.

Plain `http://` URLs are rejected unless they point at loopback — webhook URLs
usually embed credentials, and a local relay never leaves the machine.

## Multiple instances

Instances are independent: `tg-personal` and `tg-work` can be two bots, or one
bot with different chats, routed by [rules](/guide/routing). Interactive
capabilities are per-instance, so a send-only alert channel and an interactive
personal channel can share one bot token or not — your call.
