---
layout: home

hero:
  name: Mailer
  text: Self-hosted mail notifications
  tagline: Watches your IMAP mailboxes over IDLE and forwards new mail to Telegram or Discord — with your own parsing rules and no third-party server in the middle.
  actions:
    - theme: brand
      text: Get started
      link: /guide/getting-started
    - theme: alt
      text: Gmail OAuth2 setup
      link: /guide/gmail-oauth2

features:
  - title: Runs on your own machine
    details: A single static binary. Your mailbox credentials and message contents stay on the host you control; there is no hosted service operated by this project.
  - title: Instant delivery over IMAP IDLE
    details: Connects with IDLE so new mail is forwarded as it arrives, rather than on a polling interval.
  - title: Pluggable parsing
    details: Extract one-time codes or specific fields with host-side regular expressions, or with sandboxed WebAssembly plugins that have no file, network, or environment access.
  - title: No duplicate notifications
    details: Message-ID deduplication and a persistent retry queue survive restarts, so nothing is announced twice and nothing is silently dropped.
---

## What this site is

This is the documentation site for the Mailer project. It also hosts the two
pages Google requires before an OAuth consent screen can be published:

- [Privacy Policy](/privacy) — what the software handles, and what leaves your machine
- [Terms of Service](/terms) — the terms under which the software is provided

Both apply to the self-hosted software itself. Mailer is not a hosted service:
there is no account to create, and the project does not operate a server that
receives your mail.
