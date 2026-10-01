# Responses 客户端工具协议（核心）

本文描述核心对 Responses 客户端工具协议的处理：客户端执行的 `tool_search`
发现流程与 `custom` 工具。处理完全位于核心，无需任何插件或动态库。

## 背景

只有当所选模型声明 `supports_search_tool` 时，Codex 系客户端才会延迟下发工具
定义。未声明时，客户端会把整份工具面（顶层 `tools` 数组与
`input.additional_tools`）全量注入**每一轮的首个请求**。

2026-09-26 在 Codex 0.156.1 桌面端、非 OpenAI 路由上实测：这一回退使一个任务
的首轮请求达到约 120K input token，其中几乎全部是模型从未调用过的工具定义。
这笔开销每轮都要支付——延迟、首包前缀缓存失效、以及按请求计费——换来的却
是模型可能根本不会用到的工具。

## 问题

要压缩首包就必须有发现协议，而两种客户端执行的工具形态都无法干净地映射到
纯 `function` 工具列表：

- `tool_search`——模型发出搜索调用，客户端返回匹配到的工具定义，之后才允许
  调用这些被发现的工具。
- `custom` 工具——自由字符串输入，可选地受语法约束。

只转发已声明工具列表的代理会破坏这个闭环：延迟工具在客户端搜索之前就被展开，
被发现的工具名返回时被改名，模型的搜索调用与客户端的搜索结果不再匹配，
`custom` 调用则被只接受严格 function schema 的上游拒绝。能力判定也发生在错误
的位置——由模板和 provider 类型推断，而不是由实际选中的路由决定——于是模型
被宣称支持搜索，实际却无法闭合该闭环。

## 价值

当客户端执行的 `tool_search` 闭合整条往返链路后，延迟工具不再进入首包，只有
模型真正搜索时才被物化。这是本功能最高价值的结果，也正是下面这套协议契约
值得其复杂度的原因：搜索闭环必须能挺过改名、历史重放、流式传输与 provider
翻译，而不静默丢失任何工具。

## 目标与非目标

目标：

- 保持首包精简：延迟工具绝不物化进首个请求，激活它们的唯一路径是发现流程。
- 仅用核心功能完成客户端 `tool_search` 与 `custom` 桥接，无需安装动态库。
- 约定大于配置：未声明任何配置的路由同样按运行时已知信息得到正确策略。
- 按实际上游路由（选定 provider、认证类别、真实上游模型、实际协议、
  endpoint 限定）解析策略，不从客户端 alias 推断能力。
- 仅当公开模型 alias 背后全部可选路由都能完成客户端搜索闭环时，
  才声明 `supports_search_tool`。

非目标：Muse 专用 provider、工具执行服务器、第二套通用 JSON-RPC ABI、
自动模型回退器、跨会话工具内容数据库。

## 配置

`requests.responses-tools` 是唯一配置源。`models.json`、模型 alias、插件
YAML 中不存在第二套可覆盖策略。所有字段都是可选的：整块缺省即按约定生效。

```yaml
requests:
  responses-tools:
    enabled: true          # 紧急总闸，三态；缺省等同 true，仅显式 false 才整体关闭
    limits:
      max-active-tool-bytes: 262144
      max-active-attempts: 512
      max-state-bytes: 33554432
      max-attempt-bytes: 1048576
      max-schema-expansion-bytes: 65536
      max-schema-expansion-nodes: 10000
      max-depth: 64
    routes:                 # 可选，用于覆盖约定默认
      - match:
          provider: "xai"
          auth-kind: "api-key"
          upstream-model: "grok-4.5"
          upstream-format: "codex"
          base-url: "https://api.x.ai/v1"
        client-search: "bridge"
        custom-grammar: "reject"
        schema:
          complete-search-required: false
          local-refs: "preserve"
```

### 路由标识

`match` 各字段必须描述运行时**实际选中**的路由，而不是客户端输入的模型名。
provider 到格式的映射是固定的：

| `provider` | `upstream-format` |
|---|---|
| `codex`、`xai`、`meta` | `codex` |
| `claude` | `claude` |
| `gemini`、`vertex`、`aistudio` | `gemini` |
| `kimi` | `openai` |
| `antigravity` | `antigravity` |
| `openai-compatible-<name>` | `openai` |

这里的 `codex` 是 OpenAI Responses 方言的内部名称，不是厂商标识：`xai` 与
`meta` 说的是 Responses 线协议，因此复用同一个 translator。字段拼错或与实际
路由不符**不会报错**，只是该条规则不匹配，最终回落到约定。

## 默认行为

常规场景无需任何配置。有效策略由上游格式**与** provider 共同推导：

- 只有当上游格式为 `openai-response` 或 `codex`，**并且** provider 原样
  透传 `tool_search` 内建工具时，才是 `client-search: native`。目前只有
  `codex` 与 `meta` 满足。不做任何改写，保留原生透传路径，包括实时
  duplex steering。
- 其余上游一律 `client-search: bridge`，**包括**那些说着原生 Responses
  格式但会改写内建工具的 provider：`xai` executor 会把它剥掉，走原生透传
  会静默丢失 discovery。bridge 在客户端真正声明 `tool_search` 之前完全不
  生效，不带该声明的请求原样通过。
- 对能接受原生工具面的 provider，一律 `custom-tools: inherit`、
  `custom-grammar: reject`。核心从不自行推断 custom 处理方式。
- 所有路由一律 `schema.local-refs: preserve`、
  `schema.complete-search-required: false`。约定从不把其中任何一个改写为
  重写类取值。目前只有 `meta` 需要可移植工具面：它拒绝 `type: custom`，
  且要求每个属性都出现在 `required` 中，还拒绝递归 schema，因此约定会
  对它采用 `custom-tools: function`、`custom-grammar: describe`、
  `schema.complete-search-required: true` 与 `schema.local-refs: flatten`。
  `schema.local-refs: inline` 对所有 provider 都是手动开启，`meta` 也不
  例外：约定从不展开客户端自己写的 `$ref`；flatten 只把成环的引用替换
  为无约束 schema，非环 `$defs` 保持原样。

有损策略保持手动开启，因为它们要么丢失能力，要么改写用户 schema。唯一的
例外是上文所述的可移植工具面折叠——对 `meta` 而言这是约定而非可选项，
因为该上游不接受其他方案：

- `custom-tools: strip` 与 `custom-grammar: describe`
- `schema.complete-search-required: true` 与 `schema.local-refs: flatten`
- `client-search: disabled`，用于单条路由关闭搜索

`routes[]` 规则只覆盖它自己写出的策略；每一条未写出的策略会各自解析为
该调用实际所用路由的约定，因此规则改一条策略时仍保留该 provider 约定的
其余部分。写了一条 schema 策略并不会让该路由放弃另一条：
`schema.local-refs` 与 `schema.complete-search-required` 各自独立解析。

`enabled: false` 是紧急总闸：不删除任何其他配置即可整体关闭。

策略枚举：

- `client-search`：`inherit`（按该路由的约定）、`native`（真实原生
  Responses 路由透传）、`bridge`（function 翻译）、`disabled`（明确的
  客户端搜索请求返回 422，不静默丢弃）。
- `custom-tools`：`inherit`、`native`、`function`（完整包装与恢复）、
  `strip`（有损：删除未来声明但保留可转换历史；强制调用被删除工具返回
  422）、`reject`（遇到 custom 契约直接 422）。
- `custom-grammar`：`reject`（默认）、`describe`（仅 function 模式：把
  grammar 语法折叠进说明；上游不保证 grammar 采样约束）。
- `schema.complete-search-required`：三态。缺省按该路由的约定解析，
  `true` 补齐 required 并把可选字段放宽为 nullable，`false` 保持声明
  原样。它只适配 client-search 参数 schema，不全局 strictify 所有工具。
- `schema.local-refs`：`preserve`（默认）或 `inline`（展开本地引用；
  递归、外部、缺失或不支持的引用直接失败，不删除约束）或 `flatten`
  （所有引用保持原样，只有成环的引用变为无约束 schema；外部或缺失
  引用仍然直接失败，不删除约束）。

匹配规则：

- `provider`、`auth-kind`、`upstream-model`、`upstream-format` 均为必填精确
  条件。不支持通配符、正则，也不从模型名推断能力。
- API-key 路由必须填 `base-url`，避免同一 provider/model 名在不同 endpoint
  共用一条规则。OAuth 路由留空。
- 重叠且策略冲突的规则在加载时拒绝；选路时动态冲突返回明确配置错误，
  不做静默 first-match。
- 限额必须为正且有合理上界；负数不表示无限。

## 回滚

设置 `requests.responses-tools.enabled: false`。其余配置不受影响，约定策略
在下一次请求起停止生效。修改路由规则时，等待活跃请求排空后再切换，避免
单个请求中途更换协议语义。

## item 身份与历史修补

Responses 的 item id 带有生成它的 item 类型的命名空间。桥接会改变 item 类型，
id 必须随之改变：客户端原样保存代理发出的 output item，并在下一轮原样重放，
严格的上游会拒绝命名空间不符的 id。

| item 类型 | id 前缀 |
|---|---|
| `function_call` | `fc_` |
| `function_call_output` | `fco_` |
| `custom_tool_call` | `ctc_` |
| `custom_tool_call_output` | `ctco_` |
| `tool_search_call` | `tsc_` |
| `tool_search_output` | `tso_` |

只有这六类参与。`message`、`reasoning` 及其他 item 保留上游给出的身份。显式
类型转换只允许以下方向，且每一对都可逆：

- `function_call` ↔ `custom_tool_call`
- `function_call` ↔ `tool_search_call`
- `function_call_output` ↔ `custom_tool_call_output`
- `function_call_output` ↔ `tool_search_output`

请求适配之前，若重放的 `input[]` item 的类型与 id 前缀构成上述配对，就地迁移
该 id。修补只改 id 的值：不重新编码整个请求，其他字节不变。修补在路由策略判断
之前执行，因此完整的污染历史在原生路由上无需声明工具、也无需建立 attempt 即可
恢复。

修补明确不做的事：

- 不猜测。服务端执行的 search、不属于任何已知命名空间的 id、缺失或非字符串
  id，以及所有非工具 item 一律不动。
- 不补造请求 id。input item 的可选 id 可以缺省。
- 不解析 opaque 历史。需要修补且请求带 `previous_response_id`、
  `previous_item_id` 或 `item_reference` 时返回 422 `opaque_history`；绝不为
  让修补通过而删除父引用。无需修补的 opaque 历史不受影响。
- 不复用 id。当迁移会让两个 item 拥有同一客户端可见 id 时返回 422
  `ambiguous_identity`，不随机改名，也不按出现顺序决定身份。

响应侧执行方向相反的同一套转换。桥接 item 缺少可用 id 属于上游协议错误，
返回 502 `upstream_contract`：代理凭空生成的 id 会被客户端保存并在下一轮重放。
恢复工具名称不改变 item 类型——名称解析到 custom 工具并不构成上游产生了
custom 调用的证据。

流式中，客户端可见的类型与 id 在 item 首次被跟踪时计算一次，之后所有引用复用
同一值：`added` item、参数 delta 与 done、`output_item.done` 以及终态
`response.completed` 的 output 都指向同一个 id。上游 id 仍是跟踪键，因此既有的
`call_id` 与 `output_index` 一致性校验仍然比较上游真实发送的内容。

原生 WebSocket duplex 连接会修补每一个后续的 `response.create`、
`response.append` 和 `response.steer` 帧，而不只是首个请求：这些帧不经过
Manager。无法修补的帧以携带相同状态码与 reason 的本地 error frame 应答，不
向上游发送，也不会进入 pending 或 steering 状态。连接建立后，该状态码存在于
error frame 内部，不改写 HTTP 状态。

## 预算

| 配置 | 含义 |
|---|---|
| `limits.max-active-tool-bytes` | 单次尝试保留的工具声明字节 |
| `limits.max-active-attempts` | Manager 范围内并发保留的尝试数 |
| `limits.max-state-bytes` | Manager 共享状态预算 |
| `limits.max-attempt-bytes` | 单次尝试状态上限 |
| `limits.max-schema-expansion-*` | schema 改写上限 |

这些上限约束的是桥接自身保留的状态：工具声明、被跟踪的调用、暂存的
arguments 以及 SSE 缓冲。转发的请求正文属于调用方内容，代理在桥接之前
已完整持有，因此不计入尝试预算；对话长度不会触发任何上限。

诊断只携带路由/策略 generation、工具种类计数、声明字节、已用预算和
固定原因码；不记录 arguments、patch、grammar 正文、prompt、结果、令牌或
凭据。

## 边界

- `interactions`、直接插件 executor 路由、自定义未知 executor、
  images/video、`responses/compact` 不启用桥接；保持各自路径，也不新增
  搜索能力声明。
- 已配置规则命中不支持目标时返回明确 422，不忽略规则后透传。
- `CountTokens` 共用请求 Prepare 与声明预算，但不建立响应流状态；
  无法计数的 executor 各自返回自身错误。
- 原生有状态透传/steering：只允许无需转换的原生透传；需要桥接的普通
  WebSocket 走 replay；需要桥接的 live steering 组合明确拒绝。live duplex
  不做逐帧桥接。
- 服务端历史：只转换本地可展开历史；依赖远端 opaque
  `previous_response_id` 的请求在需要转换的路由上返回 422。原生路径不受
  该限制。
