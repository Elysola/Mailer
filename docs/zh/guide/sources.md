# 邮件源：IMAP 与 maildir

源产出消息。每个源拥有自己的游标——"什么已被处理"的答案——因为这因
协议而异：IMAP 是 UID，maildir 是文件名集合。游标存在状态数据库中，
重启后从处理停止的地方精确恢复。

## imap

一个实例监视一个账号的一个邮箱。

```yaml
sources:
  - name: inbox
    type: imap
    host: imap.example.com
    port: 993                  # 993 = 隐式 TLS，143 = STARTTLS
    tls: true                  # true = 隐式 TLS；false = STARTTLS
    username: me@example.com
    password_file: /run/secrets/imap_pass
    mailbox: INBOX
    mark_seen: false           # true = 投递后标记邮件 \Seen
    notify_existing: false     # false = 只通知启动后到达的邮件
    idle: true                 # 实时推送，不可用时自动回退轮询
    preview_len: 400           # 正文预览的最大字符数
    # noop_interval: 25m       # IDLE 连接的保活间隔
```

### IDLE 与轮询

`idle: true` 时源实时推送新邮件。IDLE 协议天然脆弱——服务器会定期
断开连接——所以实现会持续重建连接，并在推送不可用时回退到普通轮询。
两条路径共享游标：重连窗口内到达的邮件会被下一次轮询捕获，绝不丢失。

### OAuth2（Gmail）

```yaml
  - name: gmail
    type: imap
    host: imap.gmail.com
    tls: true
    username: you@gmail.com
    oauth2:
      client_id: ${GMAIL_CLIENT_ID}
      client_secret_file: gmail_client_secret.json
      token_file: gmail_token.json
```

完整设置（含同意屏的各种坑）见 [Gmail OAuth2 配置](/zh/guide/gmail-oauth2)。
访问令牌自动刷新；刷新令牌被吊销时，守护进程每次启动通过你的通知器
告警一次，而不是对着虚空静默记录日志。

### 监视多个邮箱

一个实例对应一个邮箱——两个账号、或一个账号的两个文件夹，都只是列表
中的两个条目。`mailbox` 接受服务器暴露的任何文件夹。

## maildir

maildir 源监视标准 maildir 的 `new/` 目录。它是本地邮件栈的集成点：
postfix/dovecot 投递、fetchmail 或 getmail 拉取、procmail 分拣——
任何投放 RFC 5322 文件的工具都成为通知源。

```yaml
  - name: local
    type: maildir
    dir: /var/mail/me
    poll_interval: 30s
    mark_read: true            # 已处理邮件移入 cur/ 并带 \Seen 标志
    notify_existing: false
    preview_len: 200
```

因为 maildir 是文件系统协议，"已处理"有物理含义：确认消息时将其从
`new/` 移入 `cur/` 并带 `\Seen` 标志，所以读取同一 maildir 的其他邮件
客户端能看到 mailer 产生的状态。当其他工具拥有状态转换时设
`mark_read: false`。

扫描器遵循 maildir 的原子发布语义——写入方先投递到 `tmp/` 再链接进
`new/`——所以永远不会解析写了一半的文件。编码词主题
（`=?UTF-8?Q?...?=`）、嵌套 multipart 正文和附件都以 IMAP 源相同的
方式提取。

## 交互命令

两种源都支持 Telegram 和 Discord 可附加到通知上的**标记已读按钮**。
按下时命令路由回源：IMAP 设置 `\Seen` 标志，maildir 执行与确认相同的
移入 cur 操作。按钮只在通知器配置了 bot 凭据时出现——纯 webhook 按
平台设计只能发送。
