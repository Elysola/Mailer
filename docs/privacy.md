# Privacy Policy

> Last updated: 2026-11-08

This policy covers the Mailer software ("the Software") and this documentation
site. It explains what the Software handles, where that data goes, and what
control you have over it.

## Summary

Mailer is **self-hosted software**, not a hosted service. The project does not
operate any server that receives, stores, or processes your mail. You deploy the
Software on a machine you control, and your data stays there — except for the
notification content you explicitly configure it to deliver to Telegram or
Discord.

There is no account to create with us, no telemetry, and no analytics.

## Who is responsible

When you self-host Mailer, **you are the data controller** for the mail it
processes, and Google's or your provider's terms govern the mailbox itself. The
Mailer project has no access to your mailbox, your credentials, or the content
of your notifications.

If you are evaluating the Software on behalf of an organisation, that
organisation is the controller, and deploying it is their decision, not ours.

## What the Software processes

Mailer connects to an IMAP mailbox that you specify and reads mail from it. To
decide whether to notify and what to send, it processes:

| Data | Why | Where it goes |
| --- | --- | --- |
| Mailbox credentials — password, or OAuth2 client credentials and refresh token | Authenticating to the IMAP server | Stored only on your host, in the file or environment variable you choose |
| Message headers — sender, recipients, subject, date, `Message-ID` | Deciding whether a message is new and formatting the notification | Kept in local state; subject and sender are forwarded in the notification |
| Message body and attachments | Running your parsing rules or plugins to extract fields such as a one-time code | Used in memory; only extracted fields reach a notification |
| OAuth2 access and refresh tokens | Maintaining the IMAP session without a stored password | Written to the `token_file` you configure, mode `0600` |

### Local state

The Software keeps a SQLite database (by default `state.db`) on your host. It
holds the last-seen mailbox UID, a set of `Message-ID` values used to avoid
announcing the same message twice, and a queue of notifications that could not
be delivered yet. That queue necessarily contains the not-yet-sent notification
subject, sender, body text, and any extracted fields. This file never leaves
your host except as the notifications you configured.

### Notification delivery

When a new message matches, the Software sends a notification — containing the
sender, subject, a preview or the processed body, and any extracted fields — to
the destination you configured:

- **Telegram**, via `https://api.telegram.org`
- **Discord**, via `https://discord.com/api/v10/...`

Those messages then travel through and are stored by Telegram or Discord, under
their respective privacy policies. **Choosing a destination is the act that
discloses data to a third party.** If the content of your mail is sensitive,
choose the destination accordingly, or run the Software without a notifier.

### Google user data

If you connect a Gmail or Google Workspace mailbox using OAuth2, the Software
requests the `https://mail.google.com/` scope. That scope is required to read a
mailbox over IMAP, and it grants full read and write access to the mailbox.

- The access and refresh tokens obtained are stored **only** in the token file
  on your host, with permissions restricted to your user.
- The Software uses these tokens **only** to authenticate to Google's IMAP
  server so it can read the mailbox you authorised. It does not use them for any
  other Google API, and it does not transmit them anywhere else.
- You can revoke access at any time at
  <https://myaccount.google.com/permissions>; the next refresh will then fail and
  the Software will alert you.

Mailer's use of information received from Google APIs adheres to the
[Google API Services User Data Policy](https://developers.google.com/terms/api-services-user-data-policy),
including the Limited Use requirements.

## What the Software does not do

- It does not send telemetry, metrics, crash reports, or usage statistics.
- It does not contain analytics or advertising code.
- It does not read mailboxes or files other than those you configure.
- It does not share data with the Mailer project or any party other than the
  notification destination you configure.
- WebAssembly plugins run sandboxed, with no network, filesystem, or environment
  access. A plugin receives the message and returns extracted fields; it cannot
  exfiltrate anything.

## Security

Credentials should be supplied through `password_file`, `client_secret_file`, or
environment variables rather than written into `config.yaml`, so that the config
file can be shared or committed without leaking secrets. Token files are written
atomically with mode `0600`, and the Software refuses to read a token file that
is group- or world-readable.

The OAuth2 authorisation flow binds its callback to `127.0.0.1` and validates the
`state` parameter to prevent an unrelated local process or a forged callback from
injecting an authorisation code.

No system is perfectly secure. You are responsible for the security of the host
you deploy on, including disk encryption of the token and state files.

## Retention and deletion

You control retention entirely:

- Delete the token file to remove stored OAuth2 credentials (revoke them at
  Google as well, so they cannot be reused).
- Delete `state.db` to remove the UID cursor, deduplication set, and retry queue.
  The Software will then re-announce existing mail.
- Messages already delivered to Telegram or Discord are subject to those
  services' retention policies, not this one.

Uninstalling the Software and deleting its configuration, token, and state files
removes everything it stored on your host.

## Children

The Software is a system administration tool and is not directed to children.

## Changes to this policy

This policy may be updated as the Software changes. The "last updated" date above
reflects the most recent revision, and the history is available in this
repository.

## Contact

Questions about this policy can be raised at
<https://github.com/elysola/mailer/issues>.

---

See also the [Terms of Service](/terms).
