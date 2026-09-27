# Responses 客户端工具协议（核心）

本文描述核心对 Responses 客户端工具协议的处理：客户端执行的 `tool_search`
发现流程与 `custom` 工具。处理完全位于核心，无需任何插件或动态库。

## 目标与非目标

目标：

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
    enabled: true          # 紧急总闸；缺省或设为 false 即全部关闭
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
          provider: "codex"
          auth-kind: "oauth"
          upstream-model: "gpt-5.6"
          upstream-format: "codex"
        client-search: "bridge"
        custom-tools: "function"
        custom-grammar: "reject"
        schema:
          complete-search-required: false
          local-refs: "preserve"
```

## 默认行为

常规场景无需任何配置。有效策略仅由上游格式推导：

- 上游原生支持 Responses 协议（`openai-response`、`codex`）时，
  `client-search: native`。不做任何改写，保留原生透传路径，包括实时
  duplex steering。
- 其余上游一律 `client-search: bridge`。bridge 在客户端真正声明
  `tool_search` 之前完全不生效，不带该声明的请求原样通过。
- 所有路由 `custom-tools: inherit`。核心从不自行推断 custom 处理方式。
- `custom-grammar: reject`、`schema.local-refs: preserve`。有损策略一律不推断。

有损策略保持手动开启，因为它们要么丢失能力，要么改写用户 schema：

- `custom-tools: strip` 与 `custom-grammar: describe`
- `schema.complete-search-required: true` 与 `schema.local-refs: inline`
- `client-search: disabled`，用于单条路由关闭搜索

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
- `schema.complete-search-required`：只适配 client-search 参数 schema，
  不全局 strictify 所有工具。
- `schema.local-refs`：`preserve`（默认）或 `inline`（展开本地引用；
  递归、外部、缺失或不支持的引用直接失败，不删除约束）。

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

## 预算

| 配置 | 含义 |
|---|---|
| `limits.max-active-tool-bytes` | 单次尝试保留的工具声明字节 |
| `limits.max-active-attempts` | Manager 范围内并发保留的尝试数 |
| `limits.max-state-bytes` | Manager 共享状态预算 |
| `limits.max-attempt-bytes` | 单次尝试状态上限 |
| `limits.max-schema-expansion-*` | schema 改写上限 |

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
