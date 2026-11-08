# Gmail over OAuth2

Google no longer accepts a username and password for IMAP. For Gmail and Google
Workspace accounts, Mailer authenticates with **OAuth2 (XOAUTH2)** instead.

This page covers creating the credentials, authorising a mailbox, and the two
limits that surprise people in production.

## Why OAuth2 and not an App Password

App Passwords still work for many personal `@gmail.com` accounts, but they are
not available to Google Workspace accounts, where password-based access to less
secure apps was switched off in March 2025. If the App Passwords page does not
appear under your account's security settings, your organisation has disabled
them and OAuth2 is the only remaining option.

OAuth2 has a second advantage: the credential is a revocable, scope-limited token
rather than a password, and revoking it does not disturb your account password.

## 1. Create an OAuth client

In the [Google Cloud Console](https://console.cloud.google.com/apis/credentials):

1. Create or select a project and enable the **Gmail API**.
2. Configure the OAuth consent screen. **External** user type works for personal
   use; **Internal** is available on Workspace and avoids the testing limits
   described below.
3. Create credentials → **OAuth client ID** → application type **Desktop app**.
4. Copy the client ID and client secret.

::: warning Use "Desktop app", not "Web application"
A Desktop app client needs no registered redirect URI, and Google then accepts
`http://127.0.0.1` on **any** port — which is what Mailer's loopback flow uses.
A Web application client requires exact pre-registration, so the loopback flow
fails with `redirect_uri_mismatch`.
:::

While the consent screen is in **Testing** status, only accounts listed as
**test users** can authorise. Add the mailbox address under *Test users*, or you
will get `Error 403: access_denied`.

## 2. Authorise the mailbox

Run this on a machine with a browser:

```bash
./mailer auth gmail \
  --account me@gmail.com \
  --client-id 1234-abc.apps.googleusercontent.com \
  --client-secret-file ./gmail_client_secret \
  --output gmail_token.json
```

`--client-secret-file` expects the secret itself, not the JSON file Console
downloads. Extract it:

```bash
jq -r '.installed.client_secret' client_secret_*.json > gmail_client_secret
chmod 600 gmail_client_secret
```

The command opens a browser and captures the callback on `127.0.0.1`, then writes
`gmail_token.json` with mode `0600`.

### On a headless server

Add `--no-browser`. It prints the consent URL and waits for you to paste the full
redirect URL back after approving. The page itself will fail to load — nothing is
listening — so copy the contents of the address bar:

```bash
./mailer auth gmail --account me@gmail.com \
  --client-id ... --client-secret-file ... --no-browser --output gmail_token.json
```

Alternatively, authorise on your laptop and copy the token file to the server. A
refresh token is bound to the OAuth client and the Google account, **not** to a
machine or an IP address, so moving it is legitimate and avoids running a browser
on the server.

## 3. Configure the account

Replace `password` with an `oauth2` block:

```yaml
accounts:
  - name: gmail
    host: imap.gmail.com
    port: 993
    username: me@gmail.com
    tls: true
    oauth2:
      client_id: 1234-abc.apps.googleusercontent.com
      client_secret_file: /run/secrets/gmail_client_secret
      token_file: /var/lib/mailer/gmail_token.json
```

Optional keys: `scope` (default `https://mail.google.com/`) and `token_url`
(default Google's endpoint; override for a test double or proxy).

## Refreshing and expiry

The access token is refreshed automatically and cached in memory. The refresh
token is never written back to `config.yaml`. If the refresh token stops working,
the daemon sends **one** alert to the account's notifiers and sets `auth_failed`
in `/status`, then keeps retrying. Re-run `mailer auth gmail` and redeploy the
token file to recover.

## Two limits worth knowing before you deploy

### Testing-mode consent screens expire refresh tokens after 7 days

While the OAuth consent screen is in **Testing** status, Google limits refresh
tokens for external users to 7 days; after that every refresh fails with
`invalid_grant`. A long-running daemon will therefore need re-authorisation every
week.

To avoid this, either:

- publish the app, or
- use an **Internal** user type, available to Workspace accounts. Internal apps
  are not subject to the 7-day refresh-token limit.

### `https://mail.google.com/` is a restricted scope

Reading mail over IMAP requires this scope, and it grants full read and write
access to the mailbox. It is a *restricted* scope, so making the app generally
available requires Google's restricted-scope verification and a third-party
security assessment.

For your own mailbox, keep the app unverified and add yourself as a test user.
Distributing it publicly is a separate, heavier process.

## Troubleshooting

| Error | Cause |
| --- | --- |
| `redirect_uri_mismatch` | The OAuth client is a **Web application**; recreate it as a **Desktop app** |
| `Error 403: access_denied` | The account is not a test user, or a different Google account is signed in to the browser |
| `AUTHENTICATIONFAILED` on login | Password login was used for a Workspace account, or IMAP is disabled for the account |
| `invalid_grant` on refresh | The refresh token was revoked, or a Testing-mode token passed its 7-day limit |

## Related

- [Privacy Policy](/privacy) — how tokens and mail content are handled
- [Terms of Service](/terms)
