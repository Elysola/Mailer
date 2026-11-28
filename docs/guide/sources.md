# Sources: IMAP & maildir

Sources produce messages. Each one owns its cursor — the answer to "what has
been processed" — because that differs per protocol: an IMAP UID, a maildir
filename set. Cursors live in the state database, so restarts resume exactly
where processing stopped.

## imap

One instance watches one mailbox of one account.

```yaml
sources:
  - name: inbox
    type: imap
    host: imap.example.com
    port: 993                  # 993 = implicit TLS, 143 = STARTTLS
    tls: true                  # true = implicit TLS; false = STARTTLS
    username: me@example.com
    password_file: /run/secrets/imap_pass
    mailbox: INBOX
    mark_seen: false           # true = mark mail \Seen after delivery
    notify_existing: false     # false = only mail arriving after startup notifies
    idle: true                 # real-time push, with automatic polling fallback
    preview_len: 400           # max runes of body preview
    # noop_interval: 25m       # keepalive for the IDLE connection
```

### IDLE and polling

With `idle: true` the source pushes new mail in real time. IDLE is fragile by
protocol — servers drop the connection on a schedule — so the implementation
re-establishes it continuously and falls back to plain polling whenever push
is unavailable. Both paths share the cursor: a mail that arrives during a
reconnect window is picked up by the next poll, never lost.

### OAuth2 (Gmail)

```yaml
  - name: gmail
    type: imap
    host: imap.gmail.com
    tls: true
    username: you@gmail.com
    oauth2:
      client_id: ${GMAIL_CLIENT_ID}
      client_secret_file: gmail_client_secret.json
      token_file: gmail_token.json
```

See [Gmail over OAuth2](/guide/gmail-oauth2) for the complete setup including
the consent-screen gotchas. Access tokens refresh themselves; when a refresh
token is revoked the daemon alerts once per boot through your notifiers rather
than logging silently into the void.

### Watching several mailboxes

One instance per mailbox — two accounts, or two folders of one account, are
just two entries in the list. `mailbox` accepts any folder the server exposes.

## maildir

A maildir source watches the `new/` directory of a standard maildir. It is the
integration point for local mail stacks: postfix/dovecot delivery, fetchmail
or getmail retrieval, procmail sorting — anything that drops RFC 5322 files
becomes a notification source.

```yaml
  - name: local
    type: maildir
    dir: /var/mail/me
    poll_interval: 30s
    mark_read: true            # move handled mail to cur/ with the \Seen flag
    notify_existing: false
    preview_len: 200
```

Because maildir is a filesystem protocol, "handled" has a physical meaning:
acking a message moves it from `new/` to `cur/` with the `\Seen` flag, so
other mail clients reading the same maildir see the state mailer produced.
Set `mark_read: false` when another tool owns state transitions.

The scanner follows maildir's atomic-publish semantics — writers deliver into
`tmp/` and then link into `new/` — so it never parses a partially-written file.
Encoded-word subjects (`=?UTF-8?Q?...?=`), nested multipart bodies and
attachments are all extracted the same way the IMAP source does.

## Interactive commands

Both sources support the **mark-as-read button** Telegram and Discord can
attach to notifications. Pressing it routes a command back to the source:
IMAP sets the `\Seen` flag, maildir performs the same move-to-cur as acking.
The button appears only when the notifier is configured with bot credentials —
plain webhooks are send-only by platform design.
