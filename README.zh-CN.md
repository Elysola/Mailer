# Mailer

*[English](README.md) | [简体中文](README.zh-CN.md)*

一个小巧的 Go 守护进程，定期检查一个或多个邮箱是否有新邮件，并将通知推送到
**Telegram** 和/或 **Discord** 机器人。

## 功能特性

- 按可配置的时间间隔并发轮询多个 IMAP 账户。
- 支持隐式 TLS（993 端口）或 STARTTLS（143 端口）。
- **IMAP 连接池**与 NOOP 保活 — 每个账户维护两条持久连接（watch 连接负责
  后台探活，work 连接由 fetch/store 操作独占借用），无需每次轮询重新建立连接。
- 通过在 **SQLite** 数据库中记录每个账户已处理的最大 UID 来去重，
  邮件绝不会被重复通知——并且默认不会改动你的邮箱。
- **Message-ID 去重** — 数据库同时记录已投递的 `Message-ID`，可在邮件移动或
  UID 变更时捕获重复。去重表按 `seen_retention`（默认 90 天）自动清理过期记录。
- 处理 `UIDVALIDITY` 变化（服务端重新编号）。
- 通知**指数退避重试**（可配置重试次数和延迟），外加**持久补发队列**：
  即时重试用尽仍失败的通知会写入 SQLite，在后续轮询中继续补发
  （退避 30 秒 → 1 小时，7 天后放弃并计入指标）。网络/API 短暂故障
  不会再永远丢失通知。
- 可选：将已通知的邮件标记为 `\Seen`（已读）。
- **可选「标记已读」按钮** — 通知中附带交互按钮，点击即可在 IMAP 服务器上
  标记邮件为已读（Telegram 需要 bot_token；Discord 需要 bot_token，见下文）。
- 默认只通知程序启动*之后*到达的邮件（可配置）。
- 通知内容包含发件人、主题、日期以及纯文本正文预览（已做 MIME/字符集解码）。
  预览会做**清理**：折叠连续空行、替换不间断/零宽空格、跳过 1×1 追踪像素图——
  再也不会出现满屏无意义空行。长度由 `preview_len` 控制（全局+账户级，默认
  400 字符）。
- **平台上限安全投递** — 转义/排版后的文本按 Telegram（4096）与 Discord
  （embed 限制）上限截断，长邮件不会再以看不懂的 API 错误收场。
- **可自定义消息模板** — 使用 Go `text/template` 自定义标题和正文格式，支持
  全局和每账户覆盖。
- 支持 Telegram（Bot API，**MarkdownV2** 模式）与 Discord（Webhook 或 Bot Token）投递。
- **每账户通知器路由** — 选择每个账户使用哪些通知渠道（Telegram、Discord 或两者）。
- **按账户分别路由 Discord**，可发送到指定的频道（channel）或子区（thread）。
- **通过文件路径或环境变量管理密钥** — `password_file`、`bot_token_file`、
  `webhook_url_file`；所有密钥字段支持 `${VAR}` / `$VAR` 展开。
- **健康检查（`/health`）、账户状态（`/status`）+ Prometheus 指标（`/metrics`）**
  — 可配置 HTTP 端口。
- **`mailer test` 子命令** — 不启动守护进程即可验证 IMAP 登录、邮箱 `SELECT`、
  服务器能力（含 IDLE 探测），并向每个启用的通知渠道试发一条消息。
- 无 CGO 构建 → 生成体积小巧的多架构（amd64/arm64）Docker 镜像。
- 收到 SIGINT/SIGTERM 时优雅退出。

## 多账户

`accounts:` 列表支持任意数量的邮箱——直接添加更多条目即可。它们会被**并发**
轮询，并各自维护独立的去重状态（以 `name` 作为键，因此请为每个账户设置唯一的
`name`——重名会在启动时报错拒绝）：

```yaml
accounts:
  - name: primary
    host: imap.example.com
    username: me@example.com
    password: "app-password"
    tls: true

  - name: work
    host: outlook.office365.com
    username: me@work.com
    password: "app-password"
    tls: true

  - name: gmail
    host: imap.gmail.com
    username: me@gmail.com
    password: "app-password"   # Gmail 需要使用「应用专用密码」
    tls: true
```

## 构建

```bash
go build -o mailer .
```

## 配置

复制示例文件并修改：

```bash
cp config.example.yaml config.yaml
```

主要字段在 `config.example.yaml` 中都有内联注释说明。

### Telegram 配置
1. 通过 [@BotFather](https://t.me/BotFather) 创建机器人并复制 token。
2. 给你的机器人发一条消息，然后访问
   `https://api.telegram.org/bot<TOKEN>/getUpdates` 查找你的数字 `chat_id`。
3. 设置 `telegram.enabled: true`、`bot_token` 和 `chat_ids`。

### Discord 配置
- **Webhook（最简单）：** 频道设置 → 整合 → Webhook → 新建 Webhook →
  将 URL 复制到 `discord.webhook_url`。
- **Bot：** 创建应用/机器人，以 `Send Messages` 权限邀请它，然后设置
  `discord.bot_token` 和 `discord.channel_id`。

#### 按账户路由：频道（channel）与子区（thread）

每个账户都可以覆盖全局的 Discord 目标，让不同邮箱把通知发送到不同的频道或子区。
在账户下添加一个 `discord:` 块即可：

```yaml
discord:                     # 全局默认值
  enabled: true
  bot_token: "your-bot-token"
  channel_id: "111111111111" # 未单独设置路由的账户使用此默认频道

accounts:
  - name: primary
    # ...IMAP 相关字段...
    discord:
      mode: channel          # 频道
      channel_id: "222222222222"

  - name: work
    # ...IMAP 相关字段...
    discord:
      mode: thread           # 子区
      thread_id: "333333333333"
```

- `mode: channel` 发送到 `channel_id`。
- `mode: thread` 发送到 `thread_id`（对 Bot 而言子区本身就是一个频道；
   对 Webhook 则通过 `?thread_id=` 参数发送）。
- 使用 **Webhook** 时，也可以为账户单独提供 `webhook_url`；由于一个 Webhook
   绑定到它自己的频道，若要发往不同频道，请使用不同的 Webhook（或改用 Bot 方式）。

### 每账户通知器选择

默认所有启用的通知器都会收到每条通知。要限制某个账户使用哪些通知器，请在
账户下设置 `notifiers`：

```yaml
telegram:
  enabled: true
discord:
  enabled: true

accounts:
  - name: quiet
    # ...IMAP 相关字段...
    notifiers: [discord]        # 仅 Discord，不含 Telegram
```

## 已读按钮（read_button）

可选开启交互式「标记已读」按钮，直接点击通知即可将对应邮件在 IMAP 服务器上
标记为 `\Seen`，无需登录邮箱客户端。

```yaml
read_button: true
```

要求：

- **Telegram：** 需要 `telegram.bot_token`。程序通过 `getUpdates` 长轮询接收
  按钮点击，并调用 `answerCallbackQuery` 反馈结果。
- **Discord：** 需要 `discord.bot_token`（按钮交互必须由 Bot 的 Gateway 处理）。
  纯 **Webhook** 模式无法接收按钮点击，按钮会处于不可用状态——程序会在启动时
  打印警告。此外，用 Bot 的 REST API（`channel_id`）发送的消息按钮必然可用；
  若用 Webhook 发送，则该 Webhook 必须由 Bot 应用创建，点击事件才会派发给 Bot。

> 注意：按钮触发的是**即时**标记操作，仅在点击时通过连接池的 IMAP 连接执行
> `STORE +FLAGS \Seen`，不会影响 `mark_seen`（通知后自动已读）等现有行为。

## 连接池

每个账户维护**两条**持久 IMAP 连接：

- **watch 连接**：只用于后台探活（NOOP）；
- **work 连接**：由当前需要与服务器对话的操作**独占借用**（`AcquireWork` →
  使用 → `Release`）——轮询或「标记已读」按钮点击。

由于轮询与按钮操作不再共用同一条 socket，慢速 fetch 不可能再与另一个
goroutine 的 `STORE` 交错。若 work 连接恰好空闲，会被提升去应答 NOOP，避免
对活跃连接做无谓 ping；watch 连接断开后，下一次 `Watch`/`AcquireWork` 会
惰性重连。

连接通过 NOOP 在可配置的 `noop_interval`（默认 30 秒）间隔下保活。NOOP
超时（10 秒）会关闭该连接，之后按需重连。

## 通知重试

重试分为**两层**。

**1. 即时重试（同一轮询周期内、按通知器）。** 每个失败的通知器最多重试
`retry_attempts` 次，间隔指数增长：

```yaml
retry_attempts: 2    # 首次之外的额外尝试数（默认：2）
retry_delay: 5s      # 基础延迟，每次尝试加倍（默认：5 秒）
```

**2. 持久补发队列（跨重启）。** 若某条消息即时重试用尽后仍有通知器失败，
它**不会**被丢弃，而是写入 `pending_notifications` 表；此后每轮 poll 会
**先补发队列、再拉新邮件**，且只重发给**失败过的那些通知器**——已成功
投递的通知器绝不会收到重复：

```yaml
max_pending_per_account: 200   # 每账户每轮补发处理的上限（默认：200）
```

- 单条消息退避：30 秒 → 60 秒 → 120 秒 → … 封顶 **1 小时**。
- 补发顺序按消息 UID 升序，老邮件不会被插队。
- 超过 **7 天**仍发不出去的通知会被放弃，并计入 `mailer_notify_dropped_total`。
- 队列跨重启保留；`mailer_pending_notifications` 指标与 `/status` 都能看到
  当前队列深度。

这堵上了旧版的漏洞：Telegram API 故障期间轮询到的邮件，UID 游标照常推进，
通知永远丢失。

## 预览文本

正文预览在发送前会做清理，因此模板化的「验证码」类邮件（大段空行、占位图、
不可见空白）不会把通知撑成花屏：

- 连续空行折叠为一个空行；
- 不间断空格（`U+00A0`）与全角空格（`U+3000`）替换为普通空格；零宽字符
  （`U+200B`、`U+FEFF`、`U+2007`）直接删除；
- 1×1/2×2 的占位图与 `data:` URI 追踪像素被跳过；
- 结果按 `preview_len` 个**字符**（按 rune 计，中文不会被截半）截断，
  默认 400，支持全局与每账户配置。

此外每个通知器在排版/转义**之后**还会按平台上限再截断一次（Telegram
4096 字符；Discord embed 描述 4000、标题 256）。

## 邮件去重（Message-ID）

除基于 UID 的追踪外，`seen_messages` 表记录了每个成功投递的 `Message-ID`。
这能捕获邮件在文件夹间移动（新 UID）或服务端重新编号（UID validity 变更）时
产生的重复。无需额外配置。

超过 `seen_retention`（默认 **2160h = 90 天**）的记录每天自动清理一次，
长期运行的实例上该表不会无限膨胀：

```yaml
seen_retention: 2160h
```

## 自定义消息模板

使用 Go [`text/template`](https://pkg.go.dev/text/template) 自定义通知格式：

```yaml
message_template:
  title: "[{{.Subject}}]"
  text: |
    **发件人：** {{.From}}
    **日期：** {{.Date}}
    {{.Preview}}...
    {{"\n"}}`{{.MessageID}}`
```

全局模板适用于所有账户；账户级的 `message_template` 可覆盖全局设置。

可用字段：`{{.From}}`、`{{.Subject}}`、`{{.Date}}`、`{{.Preview}}`、
`{{.MessageID}}`、`{{.Text}}`（完整正文）、`{{.Account}}`（配置中的账户名称）。

## 健康检查、状态与 Prometheus 指标

内置 HTTP 服务器（默认端口 **9100**）提供三个端点：

| 端点          | 说明                                        |
|---------------|---------------------------------------------|
| `GET /health` | 纯存活（liveness）探针：HTTP 服务器一启动就返回 `{"status":"ok"}`——适合容器健康检查。它**故意不**反映 IMAP/API 故障，以免某个通知器抖动导致容器被无意义重启。 |
| `GET /status` | JSON 格式的每账户运行状态：最近一次成功轮询（Unix 时间）、连续失败次数、待补发通知数。 |
| `GET /metrics` | Prometheus 文本格式 — 轮询次数、通知次数、错误数、队列深度。 |

```console
$ curl -s localhost:9100/status
{"status":"ok","accounts":{"primary":{"last_poll_success_unix":1755561600,"consecutive_failures":0,"pending_notifications":2,"last_poll_duration_sec":0.41}}}
```

`/status` 刻意只暴露非敏感数字：**没有**主机名、用户名、邮件内容或原始错误
字符串，因此可以安全地被采集或放在反向代理之后。

通过 `health_port` 配置端口（设为 `0` 可禁用服务器）。

常用指标：

| 指标 | 含义 |
|------|------|
| `mailer_poll_failures_total{account}` | 以错误结束的轮询周期数。 |
| `mailer_last_poll_success_timestamp{account}` | 最近一次成功轮询的 Unix 时间——变陈旧即可告警。 |
| `mailer_last_poll_duration_seconds{account}` | 最近一次轮询的耗时（last-value 语义的 gauge）。 |
| `mailer_pending_notifications{account}` | 当前排队待补发的通知数。 |
| `mailer_notify_dropped_total{account}` | 7 天后被放弃的通知数。 |
| `mailer_messages_fetched_total` / `mailer_messages_delivered_total` | 从 IMAP 拉取 / 投递给通知器的邮件数。 |

## `mailer test` — 连接与通知自检

`test` 用与守护进程完全相同的方式走一遍 IMAP 握手，然后通过每个启用的
通知器真实发送一条测试通知；任何一步失败都会以退出码 `1` 结束：

```bash
./mailer -config config.yaml test
```

```console
$ ./mailer -config config.yaml test
IMAP primary (imap.example.com:993 tls=true mailbox=INBOX)
  ✓ select INBOX: 142 message(s), UIDNEXT 317, UIDVALIDITY 1
  ✓ capabilities: AUTH=PLAIN IDLE IMAP4rev2 LOG-IN-STARTTLS NAMESPACE
  ✓ IDLE available (real-time mode lands in v1.2)
IMAP work (imap.example.org:993 tls=true mailbox=INBOX)
  ✗ select INBOX: LOGIN failed: [AUTHENTICATIONFAILED] Invalid credentials
notifier telegram: ✓ test message sent
notifier discord: ✓ test message sent

1 check(s) failed
```

每个账户报告会：

- **select** — 凭证有效（登录在拨号阶段完成）且配置的邮箱存在，并显示邮件数、
  `UIDNEXT` 与 `UIDVALIDITY`；
- **capabilities / IDLE 探测** — 服务器能力列表，以及 `IDLE`（或要求其
  的 `IMAP4rev2`）是否可用；
- **每个通知器的试发** — 向所有启用渠道真实发送一条消息，token/目标配置
  是否正确立刻可见。

注意：这会**真的**向你的聊天/频道发出测试消息。

## 密钥管理

所有凭证字段支持两种来源（按顺序处理）：

1. **文件路径** — 例如 `password_file: /run/secrets/imap_password`（从文件读取，
   去除首尾空白）。
2. **环境变量展开** — 例如 `password: ${IMAP_PASSWORD}`，通过 `os.ExpandEnv` 实现。

在 Docker 中可以使用挂载的文件密钥：

```yaml
password_file: /run/secrets/imap_pass
```

或者环境变量（例如在 docker-compose 中）：

```yaml
password: ${IMAP_PASSWORD}
```

`bot_token` / `bot_token_file`（Telegram 和 Discord）以及 `webhook_url` /
`webhook_url_file`（Discord）同样适用。

## 运行

```bash
./mailer -config config.yaml
```

## 使用 Docker 运行

多阶段 `Dockerfile` 会生成一个极小的静态镜像（Alpine + CA 证书），以非 root
用户运行。构建过程无 CGO，因此同时发布 `linux/amd64` 和 `linux/arm64` 镜像。

### 拉取预构建镜像（GHCR）

每次推送到默认分支以及每个 `v*` 标签，都会通过 `.github/workflows/docker.yml`
中的工作流构建并发布镜像到 GitHub Container Registry：

```bash
docker pull ghcr.io/elysola/mailer:latest
```

### 直接构建并运行

```bash
docker build -t mailer .

docker run -d --name mailer --restart unless-stopped \
  -e TZ=Asia/Shanghai \
  -v "$PWD/config.yaml:/app/config.yaml:ro" \
  -v "$PWD/data:/app/data" \
  mailer
```

### docker compose（推荐）

```bash
mkdir -p data                 # 可写的状态目录
cp config.example.yaml config.yaml
# 在 config.yaml 中设置：  state_file: /app/data/state.db
docker compose up -d
docker compose logs -f
```

> 容器以 UID `10001` 运行。请确保挂载的 `./data` 目录对其可写，例如
> `sudo chown -R 10001:10001 data`，并设置 `state_file: /app/data/state.db`，
> 使去重状态能在重启后保留。

## 作为 systemd 服务运行

```ini
# /etc/systemd/system/mailer.service
[Unit]
Description=Mailer IMAP -> Telegram/Discord notifier
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/opt/mailer/mailer -config /opt/mailer/config.yaml
WorkingDirectory=/opt/mailer
Restart=always
RestartSec=10
User=mailer

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl enable --now mailer
```

## 去重原理

两层机制防止重复通知：

1. **UID 追踪** — 每个账户的最新 IMAP UID 存储在 `account_state` 表中。每次
   轮询搜索 `UID > last_uid`，因此只会拉取真正的新邮件。首次运行时会把当前
   最新的 UID 记录为基准（除非设置了 `notify_existing: true`），这样你就不会
   被整个收件箱的历史邮件刷屏。

2. **Message-ID 去重** — `seen_messages` 表记录每个成功投递的 `Message-ID`。
   当邮件在文件夹间移动或服务端重新编号时，其 `Message-ID` 仍能被识别并跳过。

## 项目结构

```
main.go                       入口，命令行参数解析、信号处理，
                              健康检查/状态与指标 HTTP 服务器
subcmd.go                     `mailer test` 子命令
internal/config               YAML 配置加载、默认值、校验
internal/mail                 IMAP 拉取、MIME 预览提取与清理、标记已读
internal/mail/pool.go         双连接（watch/work）IMAP 连接池与 NOOP 保活
internal/notify               Telegram (+MarkdownV2) + Discord 通知器，
                              平台上限截断
internal/state                SQLite：UID 进度、Message-ID 去重、待补发队列
internal/app                  将拉取 + 通知 + 补发队列 + 状态串联起来的调度器
internal/app/metrics.go       Prometheus 指标计数器
.github/workflows             CI：构建并推送 Docker 镜像到 GHCR；
                              vet + 带 -race 的测试
```
