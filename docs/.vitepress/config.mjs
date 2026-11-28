import { defineConfig } from 'vitepress'

// Documentation site for Mailer.
//
// Two of these pages are not ordinary docs: /privacy and /terms are the public
// pages Google requires before an OAuth consent screen can be published. They
// must stay reachable at stable, unauthenticated URLs, so they live as their own
// top-level routes and are linked from the nav and footer. The zh-CN locale
// carries translated copies at /zh/privacy and /zh/terms for readers, while the
// English pages remain the authoritative versions Google's reviewers see.
//
// `base` is '/' because the site is served from a domain root (GitHub Pages with
// a custom domain, or Cloudflare Pages). If it is ever served from a subpath,
// change this to e.g. '/mailer/' — links and asset URLs all derive from it.

const guideSidebarEn = [
  { text: 'Getting started', link: '/guide/getting-started' },
  { text: 'Configuration', link: '/guide/configuration' },
  { text: 'Sources: IMAP & maildir', link: '/guide/sources' },
  { text: 'Notifiers', link: '/guide/notifiers' },
  { text: 'Processors', link: '/guide/processors' },
  { text: 'Routing & selectors', link: '/guide/routing' },
  { text: 'Gmail over OAuth2', link: '/guide/gmail-oauth2' },
]

const guideSidebarZh = [
  { text: '快速上手', link: '/zh/guide/getting-started' },
  { text: '配置详解', link: '/zh/guide/configuration' },
  { text: '邮件源：IMAP 与 maildir', link: '/zh/guide/sources' },
  { text: '通知渠道', link: '/zh/guide/notifiers' },
  { text: '处理器', link: '/zh/guide/processors' },
  { text: '路由与选择器', link: '/zh/guide/routing' },
  { text: 'Gmail OAuth2 配置', link: '/zh/guide/gmail-oauth2' },
]

export default defineConfig({
  title: 'Mailer',
  description:
    'A self-hosted mail notification daemon: watches IMAP mailboxes and maildirs, processes mail through a composable pipeline, and delivers to Telegram, Discord, Slack, ntfy, Gotify or any webhook.',
  base: '/',

  // Google's review team must be able to read the policy pages, and the app is
  // not hostile to indexing, so clean URLs are enabled and the site is indexable.
  cleanUrls: true,
  lastUpdated: true,

  // Internal material must never reach the public site: design drafts are
  // working documents, and the repo README is contributor-facing. node_modules
  // is excluded by default but listed for clarity.
  srcExclude: ['design/**', 'README.md', 'node_modules/**'],

  head: [
    ['link', { rel: 'icon', href: '/favicon-32.png', type: 'image/png', sizes: '32x32' }],
    ['link', { rel: 'icon', href: '/favicon-16.png', type: 'image/png', sizes: '16x16' }],
    ['link', { rel: 'apple-touch-icon', href: '/apple-touch-icon.png', sizes: '256x256' }],
    ['meta', { name: 'theme-color', content: '#4E82DF' }],
  ],

  locales: {
    root: {
      label: 'English',
      lang: 'en-US',
      themeConfig: {
        nav: [
          { text: 'Guide', link: '/guide/getting-started' },
          {
            text: 'Legal',
            items: [
              { text: 'Privacy Policy', link: '/privacy' },
              { text: 'Terms of Service', link: '/terms' },
            ],
          },
        ],
        sidebar: [
          { text: 'Guide', items: guideSidebarEn },
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
      },
    },

    zh: {
      label: '简体中文',
      lang: 'zh-CN',
      link: '/zh/',
      themeConfig: {
        nav: [
          { text: '指南', link: '/zh/guide/getting-started' },
          {
            text: '法律条款',
            items: [
              { text: '隐私政策', link: '/zh/privacy' },
              { text: '服务条款', link: '/zh/terms' },
            ],
          },
        ],
        sidebar: [
          { text: '指南', items: guideSidebarZh },
          {
            text: '法律条款',
            items: [
              { text: '隐私政策', link: '/zh/privacy' },
              { text: '服务条款', link: '/zh/terms' },
            ],
          },
        ],
        footer: {
          message: '基于 AGPL-3.0 许可证发布。英文版隐私政策与服务条款为权威版本。',
          copyright: 'Copyright © Mailer contributors',
        },
        docFooter: { prev: '上一页', next: '下一页' },
        outline: { label: '本页内容', level: [2, 3] },
        returnToTopLabel: '回到顶部',
        darkModeSwitchLabel: '外观',
        sidebarMenuLabel: '菜单',
        lastUpdatedText: '最后更新',
      },
    },
  },
})
