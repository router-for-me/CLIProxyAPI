# Gemini 原生嵌入与 Decisions API

CLIProxyAPI 在 `/v1beta` 下承载 Google AI Studio 的原生（非 chat）端点。将 Google SDK 的 `base_url` 指向本代理后，
可像直连 `generativelanguage.googleapis.com` 一样调用 `embedContent`、`batchEmbedContents` 与 `decisions`，
请求与响应均为透传，不做 chat 协议转换。

## 支持的端点

| 端点 | 方法 | 用途 |
|---|---|---|
| `/v1beta/models/{model}:embedContent` | POST | 单条内容嵌入 |
| `/v1beta/models/{model}:batchEmbedContents` | POST | 一次调用批量嵌入 |
| `/v1beta/decisions` | POST | 结构化决策（校准概率，输出 token 为 0） |

`/v1beta/models/{model}:{action}` 上的其他任何 `{action}` 均返回 **404** 与明确的错误体。未实现的 action 不会挂起，
也不会返回空的 `200`。

## 支持的模型

| 模型 | 端点 | 渠道 |
|---|---|---|
| `gemini-embedding-001` | `embedContent` / `batchEmbedContents` | `gemini`（API 密钥）、`aistudio` |
| `gemini-embedding-2` | `embedContent` / `batchEmbedContents` | `gemini`（API 密钥）、`aistudio` |
| `gemini-decision-1` | `decisions` | `gemini`（API 密钥）、`aistudio` |

嵌入与决策条目为内置模型目录项，远程模型目录刷新后仍会保留。

### 渠道支持

| 渠道 | 嵌入 | Decisions |
|---|---|---|
| `gemini`（Generative Language API 密钥） | ✅ | ✅ |
| `aistudio` | ✅ | ✅ |
| `vertex` | 501 | 501 |

Decisions API 仅在 Gemini Developer API 上提供，且 Vertex 的嵌入协议在各模型间不一致，因此 Vertex 在发起上游调用前
即以 `501 Not Implemented` 显式拒绝。

## 嵌入

### 单条内容

```bash
curl -sS http://localhost:8317/v1beta/models/gemini-embedding-001:embedContent \
  -H "Authorization: Bearer $CPA_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "models/gemini-embedding-001",
    "content": {"parts": [{"text": "The quick brown fox"}]}
  }'
```

### 批量

```bash
curl -sS http://localhost:8317/v1beta/models/gemini-embedding-2:batchEmbedContents \
  -H "Authorization: Bearer $CPA_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "requests": [
      {"model": "models/gemini-embedding-2", "content": {"parts": [{"text": "first"}]}},
      {"model": "models/gemini-embedding-2", "content": {"parts": [{"text": "second"}]}}
    ]
  }'
```

批量响应按请求顺序返回；代理不会重排、合并或拆分条目。

### SDK 示例

```python
from google import genai

client = genai.Client(
    api_key="unused",                      # 上游由 CPA 鉴权；此处请改为你的 CPA 密钥
    http_options={"base_url": "http://localhost:8317"},
)

result = client.models.embed_content(
    model="gemini-embedding-001",
    contents=["The quick brown fox", "jumps over the lazy dog"],
)
print([e.values for e in result.embeddings])
```

## Decisions

模型名在**请求体**中以裸 ID 形式传入（不带 `models/` 前缀）。该端点非流式。

```bash
curl -sS http://localhost:8317/v1beta/decisions \
  -H "Authorization: Bearer $CPA_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gemini-decision-1",
    "input": "The blue mug I ordered on Monday arrived cracked on Wednesday.",
    "questions": {
      "is_damaged": {
        "type": "noul",
        "instructions": "Did the item arrive damaged?"
      }
    }
  }'
```

响应包含校准后的答案与 snake_case 形式的 usage 块：

```json
{
  "answers": {"is_damaged": {"type": "noul", "noul": 0.99, "confidence": 0.99}},
  "usage": {
    "total_input_tokens": 100,
    "input_tokens_by_modality": [{"modality": "text", "tokens": 100}],
    "total_output_tokens": 0,
    "total_tokens": 100
  }
}
```

`input` 也可为多模态数组（文本、图片、音频、视频、文档）。问题键名、类型与内部校验规则原样转发给上游。

## 错误语义

| 场景 | 响应 |
|---|---|
| 未知的 `models/{model}:{action}` | `404`，错误体指明不支持的 action |
| `/v1beta/decisions` 携带 `stream: true` | `400 invalid_request_error`（该端点非流式） |
| 缺少 `model`、缺少或空的 `questions`、请求体非对象 | `400 invalid_request_error` |
| 嵌入或决策请求被路由到 Vertex 凭据 | `501 Not Implemented` |
| CPA API 密钥缺失或无效 | 鉴权中间件返回 `401` |
| 上游 4xx/5xx | 透传上游状态码与响应体 |

## 用量统计

两类能力均走常规 usage 管道，因此会与 chat 请求一同出现在用量记录、配额与轮询负载均衡统计中。

- 嵌入仅上报输入 token，无输出 token。
- Decisions 从 snake_case 的 `usage` 块读取 `total_input_tokens`，输出 token **恒为 0**。该端点不触发 zero-output
  冷却与重试保护，因此连续调用不会冷却凭据。
- 若上游响应未携带 usage，请求仍以 0 token 记录，不会被丢弃。

## Payload 配置

请求 payload 规则（`config.yaml` 中的 `payload.override` / `payload.filter`）依然生效，且只在最终出站 payload 上
求值一次。这些端点不会注入 chat 专属字段（`contents`、`safetySettings`、`tools`、`generationConfig`、thinking 字段）。

## 不支持的能力

- OpenAI 形状的 `POST /v1/embeddings`（需独立翻译层）。
- `asyncBatchEmbedContents`（长时运行任务）。
- 嵌入与 Decisions 的流式变体。
- 将 Decisions 降级模拟为 `/v1/chat/completions`（输出为概率而非文本）。
- 旧版 `text-embedding-*` 模型别名（列为待定问题，尚未实现）。
