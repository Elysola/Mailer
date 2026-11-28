# 路由与选择器

路由是投递策略的唯一权威：运行哪些处理器、哪些通知器接收消息、用哪个
模板渲染。选择器语言是胶水——用同一语法门控路由、处理器和过滤规则。

## 路由表

```yaml
routes:
  - name: otp-fast-path
    when: 'has(fields.code)'
    to: [tg-personal]
    stop: true

  - name: work-mail
    when: 'source == "work"'
    processors: [noise-filter, list-headers]
    to: [dc-alerts]

  - name: catch-all              # 必需：每封消息必须匹配到某个路由
    processors: [noise-filter]
    to: [tg-main]
```

精确的求值语义：

1. **行按序求值。** 每个匹配行将其 `to` 和 `processors` 贡献进并集
   （去重）——匹配行之间是累积而非竞争。
2. **`stop: true` 终止求值。** 匹配的 stop 行之后没有任何行再贡献——
   上面的快速路径让验证码邮件绕过兜底路由。
3. **没有 `when` 的行永远匹配。**
4. **必须恰有一条兜底。** 加载器拒绝无法匹配所有消息的配置——无处可
   去的消息是配置错误，不该是运行时惊喜。

行级选项：

| 字段 | 效果 |
|---|---|
| `when` | 选择器门控；缺省即永远匹配 |
| `to` | 接收消息的通知器实例名 |
| `processors` | 处理器实例名，按序应用 |
| `template` | 内联 `{title, body}` 渲染覆盖 |
| `stop` | 匹配时终止求值 |

## 选择器语言

选择器是作用于 envelope 的布尔表达式：

```
subject contains "invoice" and not (account == "spam-trap")
fields.class == "bill" or has(fields.code)
headers["X-Spam-Flag"] == "YES"
subject ~= "^\[list-dev\]"
```

### 操作数

| 操作数 | 含义 |
|---|---|
| `subject` | 邮件主题 |
| `from` | 发件人地址 |
| `preview` | 正文预览文本 |
| `account` / `source` | 源实例名 |
| `fields.name` | 提取的字段（regex、LLM、set、header……） |
| `headers["Name"]` | 原始邮件头，大小写不敏感 |
| `"literal"` | 字符串字面量 |

### 运算符

| 运算符 | 含义 |
|---|---|
| `==` `!=` | 相等比较 |
| `contains` | 子串匹配 |
| `~= ` | 正则匹配 |
| `has(fields.x)` | 字段存在且非空 |
| `and` `or` `not` `( )` | 布尔组合 |

### 值得了解的设计属性

- **加载时校验。** 损坏的选择器以行级错误让启动失败——永远不会静默
  禁用一条路由。
- **缺失字段不报错。** `x` 从未被提取时 `fields.x == "y"` 只是为假，
  所以顺序错误会优雅降级。
- **零成本。** 求值是纯字符串匹配——这就是每个 LLM 阶段前面都应该
  有 `when` 门控的原因。

## 一个完整例子

双邮箱加 AI 分拣的通知流：

```yaml
routes:
  # 验证码：快速路径直达个人设备，不走任何其他渠道。
  - name: otp-fast-path
    when: 'has(fields.code)'
    to: [tg-personal]
    stop: true

  # 账单在送往财务频道的路上做 AI 提取。
  - name: bills
    when: 'fields.class == "bill"'
    processors: [extract-invoice]
    to: [dc-finance]

  # 邮件列表按 List-Id 路由，经抑制后进入安静频道。
  - name: lists
    when: 'has(fields.list)'
    processors: [alert-throttle]
    to: [ntfy-lists]

  # 其余一切：过滤噪音、分类、投递到主聊天。
  - name: catch-all
    processors: [noise-filter, otp-extract, list-headers, classify]
    to: [tg-main]
```

注意兜底路由的处理器链恰好填充了前面各行所匹配的字段：路由的 `when`
是对所有匹配行的处理器**运行之后**的 envelope 求值的。行的排序关乎
策略，而非数据流。
