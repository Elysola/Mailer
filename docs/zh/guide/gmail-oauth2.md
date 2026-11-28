# Gmail OAuth2 配置

Google 已不再接受 IMAP 的用户名密码登录。Gmail 和 Google Workspace
账号需要用 **OAuth2（XOAUTH2）** 认证。

本页覆盖凭据创建、邮箱授权，以及两个在生产环境中让人措手不及的限制。

## 为什么是 OAuth2 而不是应用专用密码

应用专用密码对很多个人 `@gmail.com` 账号仍可用，但 Google Workspace
账号不可用——基于密码的"低安全性应用"访问已于 2025 年 3 月关闭。
如果你账号的安全设置里找不到应用专用密码页面，说明你的组织已禁用
它们，OAuth2 是唯一剩下的选择。

OAuth2 还有第二个优势：凭据是可吊销的、限定 scope 的令牌而非密码，
吊销它不会惊动你的账号密码。

## 1. 创建 OAuth 客户端

在 [Google Cloud Console](https://console.cloud.google.com/apis/credentials)：

1. 创建或选择一个项目，启用 **Gmail API**。
2. 配置 OAuth 同意屏。**外部**用户类型适合个人使用；**内部**在
   Workspace 上可用，且能避开下文所述的测试限制。
3. 创建凭据 → **OAuth 客户端 ID** → 应用类型选**桌面应用**。
4. 复制客户端 ID 和客户端密钥。

::: warning 用"桌面应用"，不要"Web 应用"
桌面应用客户端无需注册重定向 URI，Google 会接受 **任意**端口的
`http://127.0.0.1`——这正是 Mailer 回环流程所用的。Web 应用客户端
要求精确预注册，回环流程会以 `redirect_uri_mismatch` 失败。
:::

同意屏处于**测试中**状态时，只有列为**测试用户**的账号可以授权。
在*测试用户*下添加邮箱地址，否则会得到 `Error 403: access_denied`。

## 2. 授权邮箱

在有浏览器的机器上运行：

```bash
./mailer auth gmail \
  --account me@gmail.com \
  --client-id 1234-abc.apps.googleusercontent.com \
  --client-secret-file ./gmail_client_secret \
  --output gmail_token.json
```

`--client-secret-file` 需要的是密钥本身，不是 Console 下载的 JSON
文件。提取它：

```bash
jq -r '.installed.client_secret' client_secret_*.json > gmail_client_secret
chmod 600 gmail_client_secret
```

命令会打开浏览器并在 `127.0.0.1` 捕获回调，然后以 `0600` 权限写出
`gmail_token.json`。

### 在无头服务器上

加 `--no-browser`。它会打印同意 URL 并等待你在批准后粘贴完整的重定向
URL。页面本身会加载失败——没有东西在监听——复制地址栏的内容即可：

```bash
./mailer auth gmail --account me@gmail.com \
  --client-id ... --client-secret-file ... --no-browser --output gmail_token.json
```

或者在你的笔记本上授权，再把 token 文件复制到服务器。刷新令牌绑定
的是 OAuth 客户端和 Google 账号，**不是**机器或 IP 地址，所以移动它
是合法的，也避免在服务器上运行浏览器。

## 3. 配置源

用 `oauth2` 块替代 `password`：

```yaml
sources:
  - name: gmail
    type: imap
    host: imap.gmail.com
    port: 993
    username: me@gmail.com
    tls: true
    oauth2:
      client_id: 1234-abc.apps.googleusercontent.com
      client_secret_file: /run/secrets/gmail_client_secret
      token_file: /var/lib/mailer/gmail_token.json
```

可选键：`scope`（默认 `https://mail.google.com/`）和 `token_url`
（默认 Google 端点；测试替身或代理时覆盖）。

## 刷新与过期

访问令牌自动刷新并在内存中缓存。刷新令牌永远不会写回 `config.yaml`。
如果刷新令牌失效，守护进程向账号的通知器发送**一次**告警并在
`/status` 中设置 `auth_failed`，然后持续重试。重新运行
`mailer auth gmail` 并重新部署 token 文件即可恢复。

## 部署前值得了解的两个限制

### 测试模式的同意屏让刷新令牌 7 天后过期

OAuth 同意屏处于**测试中**状态时，Google 将外部用户的刷新令牌限制为
7 天；之后每次刷新都以 `invalid_grant` 失败。长期运行的守护进程因此
需要每周重新授权。

避免方法，二选一：

- 发布应用，或
- 使用**内部**用户类型（Workspace 账号可用）。内部应用不受 7 天刷新
  令牌限制。

### `https://mail.google.com/` 是受限 scope

通过 IMAP 读邮件需要这个 scope，而它授予邮箱的完整读写权限。它是
*受限* scope，让应用对公众可用需要 Google 的受限 scope 验证和第三方
安全评估。

只用于自己的邮箱时，保持应用未验证并把自己加为测试用户即可。公开
分发是另一套更重的流程。

## 故障排查

| 错误 | 原因 |
| --- | --- |
| `redirect_uri_mismatch` | OAuth 客户端是 **Web 应用**；重建为**桌面应用** |
| `Error 403: access_denied` | 账号不是测试用户，或浏览器登录的是另一个 Google 账号 |
| 登录时 `AUTHENTICATIONFAILED` | 对 Workspace 账号用了密码登录，或账号未启用 IMAP |
| 刷新时 `invalid_grant` | 刷新令牌被吊销，或测试模式令牌过了 7 天限制 |

## 相关

- [隐私政策](/zh/privacy) —— 令牌和邮件内容如何被处理
