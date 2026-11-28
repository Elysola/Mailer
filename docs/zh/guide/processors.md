# 处理器

处理器是流水线的中间阶段：接收 envelope、检查或修改它、返回判决——
继续、丢弃、延迟或失败。它们实现同一接口所以自由交错。大多数共享
`when` 选择器门控：无门控的处理器看到所有消息，有门控的只看到匹配的
——且启动时校验，损坏的选择器永远不会静默禁用某个阶段。

执行顺序遵循路由表：各行按序贡献其处理器列表并去重。把廉价的粉碎器
放前面，昂贵的 AI 放最后。

## filter —— 声明式丢弃规则

```yaml
processors:
  - name: noise-filter
    type: filter
    rules:
      - when: 'headers["X-Spam-Flag"] == "YES"'
        action: drop
      - when: 'subject ~= "^\\[spam\\]"'
        action: drop
```

最简单的粉碎器。规则按序求值，首个匹配生效。放在一切昂贵操作之前，
垃圾邮件就永远不会消耗 LLM token。

## regex —— 提取、重写、粉碎

```yaml
  - name: otp-extract
    type: regex
    when: 'subject contains "code" or subject contains "验证"'
    extract:
      code: '(?:code|验证码)[:：\s]*(\d{4,8})'
    # replace:                      # 正文重写，允许 $1 组引用
    #   '\d{16}': "[card]"
    # drop_on_match: '(?i)unsubscribe'
```

`extract` 对主题、预览和正文应用模式；**第一捕获组**获胜（无捕获组时
用整体匹配）并落入 `fields.code`，模板和路由可直接使用。验证码、金额、
订单号一次通过就变成可路由的事实。

## set —— 静态字段标记

```yaml
  - name: tag-work
    type: set
    when: 'account == "work"'
    fields: { mailbox_type: work, priority: high }
    # drop: true                   # 兼作最简单的丢弃过滤器
```

## header —— 复制头到字段

```yaml
  - name: list-headers
    type: header
    copy:
      List-Id: list
      X-Spam-Flag: spam
    # prefix: hdr_                 # 类似 llmextract 的命名空间
```

头默认不进入字段，因为大多数是噪音——这是显式选择加入。邮件列表是
经典案例：`List-Id` 是可靠的路由键，而主题前缀和发件人地址会撒谎。

## template —— 管道中段重渲染

```yaml
  - name: otp-format
    type: template
    when: 'has(fields.code)'
    title: "验证码 {{.Fields.code}}"
    body: "来自 {{.Message.From}} 的验证码：{{.Fields.code}}"
```

在提取填充字段后覆盖标题/正文——验证码通知只在确实存在验证码时才
用专用格式。

## throttle —— 重复抑制

```yaml
  - name: alert-throttle
    type: throttle
    window: 1h
    keys: [from, subject]           # 默认值；单独的 [subject] 也可以
    when: 'account == "monitoring"'
```

首封邮件通过；窗口期内相同键组合的重复被丢弃。与去重不同：去重键控
不可变的 Message-ID、让至少一次投递变安全，throttle 键控内容、保护
人类。监控系统和故障机器人不再每五分钟响一次铃。

## exec —— 外部命令

```yaml
  - name: webhook-out
    type: exec
    command: ["/usr/local/bin/notify-hook"]
    timeout: 5s
```

逐消息运行命令，envelope 以 JSON 从 stdin 传入；退出码和 stdout 决定
判决。是任何不值得写插件的需求的万能适配器。

## wasm —— 沙箱插件

```yaml
  - name: wasm-plugins
    type: wasm
    dir: ./plugins
    limits:                         # 兜底；插件清单优先
      timeout: 100ms
      max_input: 524288
      max_output: 16384
```

WebAssembly 插件没有文件、网络和环境访问——不受信任的逻辑无法外传
凭据或邮件内容。开发工具见 `mailer plugins list/validate/test`。

## LLM 阶段 —— 分类、总结、提取

```yaml
llms:
  - name: fast
    type: openai                    # OpenAI 兼容：DeepSeek/通义/智谱/Ollama
    base_url: https://api.deepseek.com
    api_key: ${DEEPSEEK_KEY}
    model: deepseek-chat
    timeout: 15s
    max_concurrency: 4

processors:
  - name: classify
    type: llmclassify
    llm: fast
    classes: [bill, notice, personal, newsletter, other]
    cache: true                     # 重复输入复用缓存判决

  - name: summarize
    type: llmsummarize
    llm: fast
    when: 'fields.class == "bill"'  # 零成本门控：不匹配就不调 API
    prompt: |
      用中文三句话以内总结这封邮件，保留金额、日期、行动要求。
    output: body                    # body | fields
    on_error: passthrough           # passthrough | drop | fail

  - name: extract-invoice
    type: llmextract
    llm: fast
    when: 'fields.class == "bill"'
    prefix: invoice_
    schema:
      type: object
      properties:
        amount:   { type: number }
        currency: { type: string }
        due_date: { type: string }
        vendor:   { type: string }
```

三个特性让 AI 在这里实用：

- **`when` 门控在本地零成本求值**——不匹配的消息不会调用 LLM，token
  只花在重要的邮件上。
- **缓存**以模型 + 提示词 + schema + 输入哈希为键，重复投递的消息不会
  计费两次。
- **`on_error` 显式声明**：`passthrough` 投递未处理的邮件，`drop` 丢弃，
  `fail` 入队完整重试——按阶段选择，因为"锦上添花"的总结和"路由依赖"
 的提取的正确答案不同。

`llmextract` 的输出带配置的前缀落入字段——`invoice_amount`、
`invoice_due_date`——与正则提取的任何字段一样立即可路由。通过 Ollama
的 OpenAI 兼容端点使用本地模型，内容可以完全不出你的机器。
