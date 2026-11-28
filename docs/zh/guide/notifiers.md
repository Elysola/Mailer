# 通知渠道

通知器投递渲染后的通知。它们共享三个行为：

- **限速感知** —— 平台返回 429 时，其 `Retry-After` 窗口传播进流水线，
  流水线暂停该通知器而不是盲目重试直到被封。
- **构造时校验** —— 畸形 token、缺失 chat ID、损坏的 URL 在启动时失败，
  而不是在凌晨三点邮件到达时。
- **秘密间接** —— 每个凭据都接受 `*_file` 形式。

## telegram

能力最强的集成：长消息自动分割，bot token 启用交互式**标记已读**按钮。

```yaml
notifiers:
  - name: tg-main
    type: telegram
    bot_token_file: /run/secrets/tg_token   # 或 bot_token: "123:abc"
    chat_ids: ["123456789"]                 # 多聊天直接支持
    interactive: true                       # 标记已读按钮
```

从 [@BotFather](https://t.me/BotFather) 获取 token；给 bot 发消息后访问
`https://api.telegram.org/bot<TOKEN>/getUpdates` 即可看到你的 chat ID。

## discord

纯 webhook 投递，或完整 bot 集成以获得交互按钮和消息编辑。

```yaml
  - name: dc-alerts
    type: discord
    webhook_url_file: /run/secrets/dc_webhook
    # bot_token + channel_id 启用交互按钮；纯 webhook 只能发送——
    # 这是平台的设计，不是 mailer 的选择。
    # bot_token_file: /run/secrets/dc_bot
    # channel_id: "1234567890"
```

## slack

Incoming webhook 集成：

```yaml
  - name: slack-ops
    type: slack
    webhook_url_file: /run/secrets/slack_webhook
    channel: "#alerts"          # 可选覆盖 webhook 默认频道
    username: mailer
    icon_emoji: ":mailbox:"
```

## ntfy

推送到 [ntfy.sh](https://ntfy.sh) 或你的自建实例：

```yaml
  - name: ntfy-alerts
    type: ntfy
    server: https://ntfy.sh
    topic_file: /run/secrets/ntfy_topic   # topic 本身就是凭据
    priority: 4                           # 1 最小 .. 5 最大
    tags: [email, warning]
    # token_file: /run/secrets/ntfy_token   # bearer 认证，或 user/password
```

在 ntfy 上任何知道 topic 的人都能向其发布——把 topic 名本身当作秘密对待。

## gotify

API 极简的自托管推送：

```yaml
  - name: gotify-home
    type: gotify
    url: https://push.example.com
    token_file: /run/secrets/gotify_token
    priority: 5                           # 0..10
```

## webhook

通用逃生舱——覆盖智能家居、工单系统、自建 bot 和其他一切：

```yaml
  - name: homeassistant
    type: webhook
    url: https://ha.example.com/api/webhook/mail-arrived
    method: POST                          # 默认值
    headers:
      Authorization: Bearer ${HA_TOKEN}
    body: '{"text": {{.Message.Subject | json}}}'
    # success_status: 200                 # 端点返回非 2xx 时
```

不带 `body` 时，整个 envelope 以 JSON  POST。带 `body` 时模板逐消息
渲染——插值字符串用 `json` 助手，它会转义引号和反斜杠，保证输出仍是
合法 JSON。

纯 `http://` URL 会被拒绝，除非指向 loopback——webhook URL 通常内嵌
凭据，而本地中继从不离开本机。

## 多实例

实例相互独立：`tg-personal` 和 `tg-work` 可以是两个 bot，或一个 bot
的不同聊天，由[路由规则](/zh/guide/routing)分配。交互能力按实例计算，
所以一个只发的告警频道和一个可交互的个人频道可以共享一个 bot token，
也可以不——你说了算。
