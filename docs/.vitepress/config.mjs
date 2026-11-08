import { defineConfig } from 'vitepress'

// Documentation site for Mailer.
//
// Two of these pages are not ordinary docs: /privacy and /terms are the public
// pages Google requires before an OAuth consent screen can be published. They
// must stay reachable at stable, unauthenticated URLs, so they live as their own
// top-level routes and are linked from the nav and footer.
//
// `base` is '/' because the site is served from a domain root (GitHub Pages with
// a custom domain, or Cloudflare Pages). If it is ever served from a subpath,
// change this to e.g. '/mailer/' — links and asset URLs all derive from it.
export default defineConfig({
  lang: 'en-US',
  title: 'Mailer',
  description:
    'A self-hosted mail notification daemon: watches IMAP mailboxes over IDLE and forwards new mail to Telegram or Discord.',
  base: '/',

  // Google's review team must be able to read the policy pages, and the app is
  // not hostile to indexing, so clean URLs are enabled and the site is indexable.
  cleanUrls: true,
  lastUpdated: true,

  head: [
    ['link', { rel: 'icon', href: '/favicon.svg', type: 'image/svg+xml' }],
    ['meta', { name: 'theme-color', content: '#0b7285' }],
  ],

  themeConfig: {
    nav: [
      { text: 'Home', link: '/' },
      { text: 'Guide', link: '/guide/getting-started' },
      { text: 'Gmail OAuth2', link: '/guide/gmail-oauth2' },
      {
        text: 'Legal',
        items: [
          { text: 'Privacy Policy', link: '/privacy' },
          { text: 'Terms of Service', link: '/terms' },
        ],
      },
    ],

    sidebar: [
      {
        text: 'Guide',
        items: [
          { text: 'Getting started', link: '/guide/getting-started' },
          { text: 'Gmail over OAuth2', link: '/guide/gmail-oauth2' },
        ],
      },
      {
        text: 'Legal',
        items: [
          { text: 'Privacy Policy', link: '/privacy' },
          { text: 'Terms of Service', link: '/terms' },
        ],
      },
    ],

    // The footer links matter beyond aesthetics: Google's reviewers look for a
    // reachable privacy policy from every page.
    footer: {
      message: 'Released under the AGPL-3.0 License.',
      copyright: 'Copyright © Mailer contributors',
    },

    docFooter: { prev: 'Previous', next: 'Next' },
    outline: { label: 'On this page', level: [2, 3] },
    returnToTopLabel: 'Back to top',
    darkModeSwitchLabel: 'Appearance',
    sidebarMenuLabel: 'Menu',
  },
})
