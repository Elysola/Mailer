---
layout: home

hero:
  name: Mailer
  text: Self-hosted mail notifications
  tagline: Watches your mailboxes, runs new mail through a composable processing pipeline, and delivers notifications to Telegram, Discord, Slack, ntfy, Gotify — or anything with a webhook. No third-party server in the middle.
  actions:
    - theme: brand
      text: Get started
      link: /guide/getting-started
    - theme: alt
      text: 中文文档
      link: /zh/

features:
  - title: Runs on your own machine
    details: A single static binary. Your mailbox credentials and message contents stay on the host you control; there is no hosted service operated by this project.
  - title: Instant delivery over IMAP IDLE
    details: Connects with IDLE so new mail is forwarded as it arrives, with automatic fallback to polling. Local maildirs are supported too — postfix, fetchmail and getmail all become notification sources.
  - title: A composable pipeline
    details: Regex extraction, field tagging, templating, repeat throttling, WASM plugins, external commands and LLM stages all implement one interface and interleave freely.
  - title: Deliver anywhere
    details: Telegram and Discord with interactive mark-read buttons, plus Slack, ntfy, Gotify and a generic webhook for everything else — multiple instances of each, routed by rule.
  - title: AI optional, not required
    details: OpenAI-compatible LLMs can classify, summarize and extract — gated by zero-cost selectors so tokens are only spent on mail that matters. Local models through Ollama work too.
  - title: No duplicate notifications
    details: Message-ID deduplication, repeat throttling and a persistent retry queue survive restarts, so nothing is announced twice and nothing is silently dropped.
---

## What this site is

This is the documentation site for the Mailer project. It also hosts the two
pages Google requires before an OAuth consent screen can be published:

- [Privacy Policy](/privacy) — what the software handles, and what leaves your machine
- [Terms of Service](/terms) — the terms under which the software is provided

Both apply to the self-hosted software itself. Mailer is not a hosted service:
there is no account to create, and the project does not operate a server that
receives your mail.
