# Documentation site

VitePress site for the project. It hosts the user guide and, more importantly,
the two pages Google requires before an OAuth consent screen can be published:

- `/privacy` — Privacy Policy
- `/terms` — Terms of Service

Both must be reachable at a **public, unauthenticated URL** for the consent
screen's "Application home page", "Privacy policy" and "Terms of service"
fields.

## Local development

```bash
make docs-install   # npm ci
cd docs && npm run dev
```

The dev server prints its URL (normally `http://localhost:5173`).

## Build

```bash
make docs
```

Output goes to `docs/.vitepress/dist` and is gitignored. CI builds it on every
push and fails if `privacy.html` or `terms.html` is missing.

## Deploying

The site is plain static files, so any static host works. Two common choices:

### Cloudflare Pages

| Setting | Value |
| --- | --- |
| Build command | `cd docs && npm ci && npm run build` |
| Output directory | `docs/.vitepress/dist` |

### GitHub Pages

Publish `docs/.vitepress/dist` from a workflow. If the site is served from a
project subpath (`https://user.github.io/mailer/`), set `base: '/mailer/'` in
`docs/.vitepress/config.mjs` — links and assets all derive from it. A custom
domain served from the root needs no change.

Serve over HTTPS. Google rejects consent-screen URLs that do not resolve, and a
legal page behind a redirect to a login will be refused.

## What Google asks for

When publishing the OAuth consent screen, the fields map onto this site as
follows:

| Console field | Value |
| --- | --- |
| Application home page | the site root, e.g. `https://mailer.example.com/` |
| Application privacy policy link | `https://mailer.example.com/privacy` |
| Application terms of service link | `https://mailer.example.com/terms` |
| Authorized domain | the domain above, without scheme or path |

The domain must be verified as owned in Search Console before it can be added as
an authorized domain.

## Editing the legal pages

`docs/privacy.md` and `docs/terms.md` describe what the software actually does.
If behaviour changes — a new notifier, a new field persisted in `state.db`, a
different scope requested — update the policy in the same change. A privacy
policy that no longer matches the code is worse than none at all: it is both a
verification risk and a misrepresentation to users.
