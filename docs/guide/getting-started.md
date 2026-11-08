# Getting started

Mailer is a single static binary that watches one or more IMAP mailboxes and
forwards new mail to Telegram or Discord. Everything runs on a host you control.

## Requirements

- An IMAP mailbox reachable over TLS (implicit TLS on 993, or STARTTLS on 143).
- Either a bot token (Telegram) or a webhook URL (Discord) for delivery.
- For Gmail or Google Workspace accounts, an OAuth2 client — password login is
  no longer accepted by Google. See [Gmail over OAuth2](/guide/gmail-oauth2).

## Install

Download a release binary, or build from source:

```bash
git clone https://github.com/elysola/mailer
cd mailer
go build -o mailer .
```

## Configure

Copy `config.example.yaml` to `config.yaml` and fill in an account and a
notifier. Credentials can come from the file, from a separate file, or from
environment variables:

```yaml
accounts:
  - name: inbox
    host: imap.example.com
    port: 993
    username: me@example.com
    password_file: /run/secrets/imap_password   # or ${IMAP_PASSWORD}
    tls: true
    mailbox: INBOX

telegram:
  bot_token_file: /run/secrets/telegram_token
  chat_id: "12345678"
```

`password_file` and the `${VAR}` form both keep the secret out of the config
file, which matters if the file is ever committed or copied.

## Verify the connection

Before leaving the daemon running, confirm both sides work:

```bash
./mailer test -config ./config.yaml
```

This logs in, selects the mailbox, and sends one test message to each notifier.
A failure here is almost always credentials, a disabled IMAP setting on the
provider side, or a wrong port/TLS combination.

## Run

```bash
./mailer -config ./config.yaml
```

State — the UID cursor, the Message-ID deduplication set, and the retry queue —
lives in a SQLite file (`state.db` by default) and survives restarts. Deleting
it re-announces existing mail, so keep it.

## Next steps

- [Gmail over OAuth2](/guide/gmail-oauth2) — required for Google accounts
- [Privacy Policy](/privacy) — what the software handles
- [Terms of Service](/terms) — terms of use
