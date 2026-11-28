# 配置详解

Mailer 用单个 YAML 文件配置。`sources`、`notifiers`、`processors` 和
`llms` 下的每个条目都是一个**组件实例**：`name` 命名它，`type` 选择
工厂，其余所有字段属于组件本身。上游新增组件类型永远不会改变这个文件
的 schema。

```yaml
version: 2
```

文件必须声明 `version: 2`。v1 文件用 `mailer migrate-config` 转换。

## daemon —— 进程级行为

```yaml
daemon:
  poll_interval: 60s          # 源的轮询频率（IDLE 源以此作为回退）
  health_port: 9100           # HTTP 健康/指标端点；0 为禁用
  retry_attempts: 2           # 投递重试次数（不含首次）
  retry_delay: 5s             # 基础退避延迟，逐次翻倍
  seen_retention: 2160h       # 已投递 Message-ID 的记忆时长（默认 90 天）
  max_pending_per_source: 200 # 每次扫描重试的队列条数上限
```

## state —— 持久化

```yaml
state:
  driver: sqlite
  file: state.db              # Docker 中指向挂载卷
```

一个 SQLite 文件承载一切：源游标、去重记忆、重试/延迟队列和处理器的
键值暂存空间。v1 数据库在首次启动时就地迁移。

## sources、notifiers、processors、llms

每个都是实例列表：

```yaml
sources:
  - name: inbox               # 被路由、日志、队列命令引用
    type: imap                # 选择组件工厂
    host: imap.example.com
    # ...类型专有字段

notifiers:
  - name: tg
    type: telegram
    bot_token_file: /run/secrets/tg_token
    chat_ids: ["123456789"]
```

同类型多实例是常态——工作和个人两个 IMAP 账号、两个 Telegram bot、
三个 webhook。名字在同类内唯一，路由通过名字引用实例。

组件参考页面：

- [邮件源](/zh/guide/sources) —— `imap`、`maildir`
- [通知渠道](/zh/guide/notifiers) —— `telegram`、`discord`、`slack`、`ntfy`、`gotify`、`webhook`
- [处理器](/zh/guide/processors) —— `filter`、`regex`、`set`、`header`、`template`、`throttle`、`exec`、`wasm`、`llmsummarize`、`llmclassify`、`llmextract`

## 凭据与秘密

三种可互换形式，按此顺序检查：

```yaml
password_file: /run/secrets/imap_pass   # 1. 文件内容（首个非空行）
password: ${IMAP_PASSWORD}              # 2. 环境变量展开（${VAR} 可用于任意位置）
password: "literal"                     # 3. 内联值
```

两者并存时 `*_file` 字段优先。环境变量展开在解析前对整个文件进行，
所以 `${VAR}` 在任何字符串中都有效。缺失的变量展开为空——这通常会
在启动时响亮地验证失败，而这正是你想要的位置。

## templates —— 命名渲染对

```yaml
templates:
  default:
    title: "[{{.Source}}] {{.Subject}}"
    body: |
      {{.Preview}}
      —— {{.From}}
```

模板是作用于消息的 Go `text/template` 字符串。路由可以引用命名模板或
携带内联模板；否则应用 `default` 模板。通知文本在处理器运行**之后**
计算，所以处理器提取的字段都可用。

## routes —— 投递策略

路由是每封邮件命运的唯一权威：运行哪些处理器、哪些通知器接收、用哪个
模板渲染。它强大到值得[单独一页](/zh/guide/routing)。

```yaml
routes:
  - name: otp-fast-path
    when: 'has(fields.otp_code)'
    to: [tg-personal]
    stop: true

  - name: catch-all          # 必须恰有一条匹配一切的路由
    processors: [noise-filter]
    to: [tg]
```

路由行按序求值；每个匹配行贡献其通知器和处理器（并集去重），直到
`stop: true` 的行终止求值。没有 `when` 的行永远匹配。加载器拒绝无法
匹配所有消息的配置。

## 校验哲学

加载器刻意严格：未知类型、损坏的选择器、无法解析的正则和模板、指向
不存在实例的悬空引用——全部在启动时以行级错误失败，而不是在运行时
静默失效。`mailer doctor` 离线重跑同一套校验。
