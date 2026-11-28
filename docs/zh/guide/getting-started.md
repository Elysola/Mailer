# 快速上手

Mailer 是单个静态二进制，监视邮箱并将新邮件转发到你已在用的聊天服务。
一切运行在你控制的主机上；架构是一小组可组合组件——**源（sources）**
产出邮件，**处理器（processors）** 加工邮件，**通知器（notifiers）**
投递通知，**路由（routes）** 决定什么去哪里。

## 前置要求

- 一个可通过 TLS 访问的 IMAP 邮箱（993 隐式 TLS，或 143 STARTTLS）——
  或者本地 **maildir**（邮件不离开本机的场景）。
- 至少一个投递目标：Telegram bot token、Discord webhook，或 Slack /
  ntfy / Gotify / webhook 端点中的任意一个。
- Gmail 或 Google Workspace 账号需要 OAuth2 客户端——Google 已不再接受
  密码登录。参见 [Gmail OAuth2 配置](/zh/guide/gmail-oauth2)。

## 安装

下载 release 二进制，或从源码构建：

```bash
git clone https://github.com/elysola/mailer
cd mailer
go build -o mailer ./cmd/mailer
```

仓库内含 Docker 镜像与 compose 文件。

## 配置

复制 `config.example.yaml` 为 `config.yaml`。最小可用配置是一个源、一个
通知器，加上必需的兜底路由：

```yaml
version: 2

sources:
  - name: inbox
    type: imap
    host: imap.example.com
    port: 993
    tls: true
    username: me@example.com
    password_file: /run/secrets/imap_password   # 或 ${IMAP_PASSWORD}
    mailbox: INBOX

notifiers:
  - name: tg
    type: telegram
    bot_token_file: /run/secrets/telegram_token
    chat_ids: ["123456789"]

routes:
  - name: catch-all
    to: [tg]
```

凭据可以内联书写、从文件读取（`*_file` 字段）、或从环境变量展开
（`${VAR}`）。推荐文件间接方式——让秘密远离备份和版本控制。

## 验证

运行守护进程前，端到端检查整条链路：

```bash
./mailer test        # IMAP 登录 + 通知投递探测
./mailer doctor      # 配置有效性、队列状态、轮询进度
```

`mailer test` 连接每个配置的源，并通过每个通知器发送真实测试通知——
这是在生产环境给你惊喜之前，最快发现 token 笔误或防火墙规则的方式。

## 运行

```bash
./mailer             # 默认读取 ./config.yaml
./mailer -config /etc/mailer/config.yaml
```

守护进程暴露健康与指标端点（默认端口 `9100`），并将全部状态持久化在
单个 SQLite 文件中——游标位置、去重记忆和重试队列都能在重启后存活。

## 从 v1 迁移

现有 v1 配置可机械转换：

```bash
./mailer migrate-config -config config.yaml
```

转换器在原文件旁写出 `config.v2.yaml`；检查后重命名即可。现有状态数据
库在首次启动时就地迁移——游标、去重记忆和待重试通知全部保留。

## 下一步

- [配置详解](/zh/guide/configuration) —— 配置文件的每个部分
- [处理器](/zh/guide/processors) —— 提取、抑制、模板、AI
- [路由与选择器](/zh/guide/routing) —— 把一切串联起来的规则语言
