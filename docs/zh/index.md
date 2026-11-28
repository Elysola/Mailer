---
layout: home

hero:
  name: Mailer
  text: 自托管邮件通知
  tagline: 监视你的邮箱，让新邮件经过可组合的处理流水线，送达 Telegram、Discord、Slack、ntfy、Gotify——或任何带 webhook 的服务。中间没有任何第三方服务器。
  actions:
    - theme: brand
      text: 快速上手
      link: /zh/guide/getting-started
    - theme: alt
      text: English docs
      link: /

features:
  - title: 运行在你自己的机器上
    details: 单个静态二进制。你的邮箱凭据和邮件内容留在你控制的主机上；本项目不运营任何托管服务。
  - title: IMAP IDLE 即时投递
    details: 通过 IDLE 推送，新邮件到达即转发，不可用时自动回退轮询。同时支持本地 maildir——postfix、fetchmail、getmail 都能成为通知源。
  - title: 可组合的流水线
    details: 正则提取、字段标记、模板渲染、重复抑制、WASM 插件、外部命令和 LLM 阶段实现同一接口，自由交错组合。
  - title: 送达任何地方
    details: Telegram 和 Discord 带交互式已读按钮，另有 Slack、ntfy、Gotify 和通用 webhook 兜底——每种可多实例，按规则路由。
  - title: AI 可选而非必需
    details: OpenAI 兼容 LLM 可分类、总结、提取——由零成本选择器门控，只为重要邮件消耗 token。Ollama 本地模型亦可。
  - title: 绝不重复通知
    details: Message-ID 去重、重复抑制和持久化重试队列在重启后存活，没有邮件会被通知两次，也没有邮件会被静默丢弃。
---

## 关于本站

这是 Mailer 项目的文档站点。同时托管 Google 发布 OAuth 同意屏前要求的两
个页面：

- [隐私政策](/zh/privacy) —— 软件处理什么数据，以及什么会离开你的机器
- [服务条款](/zh/terms) —— 软件的提供条款

两者均适用于自托管软件本身。Mailer 不是托管服务：没有账号可注册，项目
方也不运营接收你邮件的服务器。英文版为权威版本。
