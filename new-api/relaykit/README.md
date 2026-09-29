# RelayKit

RelayKit 是从 [new-api](https://github.com/QuantumNous/new-api) 中拆分出的独立 Go 模块，提供常用大模型文本协议的 DTO、请求转换、响应转换和流式事件转换。

它只负责协议层的数据建模与语义转换，不包含 HTTP 服务、上游请求发送、渠道调度、鉴权、计费或数据库逻辑。因此可以脱离 new-api 主模块，嵌入其他 Go 网关或代理服务。

## 能力

- 在 OpenAI Chat Completions、OpenAI Responses、Anthropic Messages 和 Gemini `generateContent` 之间转换
- 同时支持请求、非流式响应和增量流式响应
- 自动根据 DTO 类型识别源协议，并选择内置的直接或多跳转换路径
- 返回转换器 ID、质量等级、实际转换步骤和统一 usage，方便审计与调试
- 支持常用的文本、多模态内容、工具调用、推理内容和 usage 映射
- 作为独立 Go module 构建，不依赖 new-api 主模块、Gin、数据库或全局设置

## 支持矩阵

以下四种文本协议支持任意两种格式之间的转换：

| 源格式 \ 目标格式 | OpenAI Chat | OpenAI Responses | Claude Messages | Gemini |
|---|---:|---:|---:|---:|
| OpenAI Chat | — | Good | Fair | Fair |
| OpenAI Responses | Good | — | Fair | Fair |
| Claude Messages | Fair | Fair | — | Discouraged |
| Gemini | Fair | Fair | Discouraged | — |

质量等级表示协议之间的语义匹配程度：

- `Good`：两种协议的核心结构较接近
- `Fair`：主要能力可转换，但部分协议特性可能需要适配或无法完整保留
- `Discouraged`：目前需要经过中间协议转换，语义损失风险更高

请求、非流式响应和流式响应均覆盖上述矩阵。实际采用的路径可从转换结果的 `Steps` 和 `Quality` 字段中读取。

## 安装

RelayKit 要求 Go 1.25.1 或更高版本。

```bash
go get github.com/QuantumNous/new-api/relaykit@latest
```

主要包：

| 包 | 用途 |
|---|---|
| `relaykit/dto` | 各协议的请求、响应、流式事件和 usage DTO |
| `relaykit/types` | 协议格式、错误、文件来源及共享类型 |
| `relaykit/relayconvert` | 请求、响应和流式转换入口 |
| `relaykit/relayconvert/convmeta` | 与宿主实现解耦的转换上下文和选项 |
| `relaykit/reasonmap` | 不同协议之间的结束原因映射 |

## 快速开始

下面将 OpenAI Chat Completions 请求转换为 Claude Messages 请求：

```go
package main

import (
	"context"
	"fmt"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	"github.com/QuantumNous/new-api/relaykit/types"
)

func main() {
	maxTokens := uint(1024)
	request := &dto.GeneralOpenAIRequest{
		Model: "claude-sonnet-4-5",
		Messages: []dto.Message{
			{Role: "user", Content: "Hello!"},
		},
		MaxTokens: &maxTokens,
	}

	meta := &convmeta.Values{
		OriginModelName:     "client-model",
		UpstreamModelName:   request.Model,
		ChannelMetaAttached: true,
	}

	result, err := relayconvert.ConvertRequest(
		context.Background(),
		meta,
		types.RelayFormatClaude,
		request,
	)
	if err != nil {
		panic(err)
	}

	claudeRequest, ok := result.Value.(*dto.ClaudeRequest)
	if !ok {
		panic(fmt.Sprintf("unexpected result type %T", result.Value))
	}

	fmt.Printf("model=%s messages=%d\n", claudeRequest.Model, len(claudeRequest.Messages))
}
```

`ConvertRequest` 根据请求的具体 DTO 类型推断源格式。传入原始 JSON、`map[string]any` 或不受支持的 DTO 会返回错误。

### 非流式响应

响应转换使用相同的目标格式模型：

```go
result, err := relayconvert.ConvertResponse(
	ctx,
	meta,
	types.RelayFormatOpenAI,
	claudeResponse,
)
if err != nil {
	return err
}

openAIResponse := result.Value.(*dto.OpenAITextResponse)
usage := result.Usage
```

支持的响应 DTO：

| 格式 | 非流式响应 | 流式事件 |
|---|---|---|
| OpenAI Chat | `dto.OpenAITextResponse` | `dto.ChatCompletionsStreamResponse` |
| OpenAI Responses | `dto.OpenAIResponsesResponse` | `dto.ResponsesStreamResponse` |
| Claude Messages | `dto.ClaudeResponse` | `dto.ClaudeResponse` |
| Gemini | `dto.GeminiChatResponse` | `dto.GeminiChatResponse` |

### 流式响应

流式转换可能需要跨事件保存工具调用、usage 和结束状态。每条上游流应创建独立的 `ResponseStreamState`，并在上游结束后调用 `FinalizeStreamResponse`：

```go
state, err := relayconvert.NewResponseStreamState(
	types.RelayFormatOpenAI,
	types.RelayFormatOpenAIResponses,
	relayconvert.ResponseStreamOptions{
		ID:           "resp_123",
		Model:        "gpt-4.1",
		IncludeUsage: true,
	},
)
if err != nil {
	return err
}

for _, chunk := range upstreamChunks {
	results, err := relayconvert.ConvertStreamResponseChunk(ctx, meta, state, chunk)
	if err != nil {
		return err
	}
	for _, result := range results {
		emit(result.Value)
	}
}

finalResults, err := relayconvert.FinalizeStreamResponse(ctx, meta, state)
if err != nil {
	return err
}
for _, result := range finalResults {
	emit(result.Value)
}

usage := state.Usage()
```

RelayKit 不负责 SSE 的读取和写入。宿主需要将每个 SSE 事件解析为对应 DTO，并将转换结果重新编码后发送给下游。不要省略 `FinalizeStreamResponse`，部分转换器会在该阶段补发终止事件或最终 usage。

## 转换上下文

大多数基础转换可以传入 `nil` 作为 `convmeta.Meta`。需要模型映射、推理适配、安全设置或流式状态时，应使用 `convmeta.Values`，或在宿主中实现 `convmeta.Meta`。

常用选项通过 `convmeta.Options` 按请求传入：

```go
meta := &convmeta.Values{
	Options: &convmeta.Options{
		Claude: convmeta.ClaudeOptions{
			DefaultMaxTokens: func(model string) int {
				return 4096
			},
		},
		Gemini: convmeta.GeminiOptions{
			ThinkingAdapterEnabled: true,
		},
	},
}
```

需要注意：

- OpenAI Chat 或 OpenAI Responses 转 Claude 时，Claude 请求必须具有 `max_tokens`。源请求未提供时，需要配置 `Claude.DefaultMaxTokens`，否则转换会返回错误。
- RelayKit 不负责选择渠道或映射模型名。调用转换前，应将请求中的 `Model` 设置为目标上游使用的模型名。
- 自定义 `convmeta.Meta` 的指针实现必须保证所有方法对 nil receiver 安全，完整约束见 `convmeta.Meta` 的接口注释。
- 工具损耗策略默认是 `allow`：跨协议转换会成功，损耗以诊断形式返回。`safe` / `strict` 只在请求阶段 opt-in 拒绝；响应和流式转换无论策略如何都不会因损耗失败。
- `ThinkingAdapterEnabled` 只控制是否把已解析的推理意图渲染到 Claude / Gemini 请求上。`-thinking` / `-nothinking` / effort 尾缀等命名约定不再由转换器自动解释。
- 若你的入口仍使用这些模型名后缀，请在调用转换前自行调用 `relayconvert/reasoning` 的 `Parse*` 帮助函数，把结果写成 `dto.ReasoningConversionState`，并通过 `convmeta.Meta.ReasoningState()`（`convmeta.Values.ReasoningConversion`）传入。同时把发给上游的模型名裁成无后缀基础名。

## 多模态内容

某些跨协议的图片转换需要下载 URL 内容或解析 data URL。宿主应在启动时配置媒体解析器：

```go
relayconvert.SetMediaResolver(relayconvert.MediaResolver{
	GetBase64Data:        getBase64Data,
	DecodeBase64FileData: decodeBase64FileData,
})
```

两个回调的签名由 `relayconvert.MediaResolver` 定义。需要媒体解析而未配置对应回调时，转换会明确返回错误；RelayKit 本身不会发起网络请求。

## 转换结果

请求转换返回 `relayconvert.RequestResult`，响应转换返回 `relayconvert.ResponseResult`。除 `Value` 外，建议关注：

- `From` / `To`：源格式和目标格式
- `Converter`：所选转换器 ID
- `Quality`：转换质量等级
- `Steps`：直接转换或多跳转换的实际路径
- `Usage`：响应转换后的统一 token usage
- `Stream`：结果是否来自流式转换

如果需要固定转换路径，可使用 `ConvertRequestVia`；如果需要按转换器 ID 执行，可使用 `ConvertRequestByID`、`ConvertResponseByID` 和 `NewResponseStreamStateByID`。

## 开发

RelayKit 必须始终保持独立可构建。修改模块后，在 `relaykit` 目录运行：

```bash
GOWORK=off go test ./...
GOWORK=off go build ./...
```

转换矩阵由 golden tests 覆盖。确认协议输出变化是预期行为后，可更新快照：

```bash
GOWORK=off go test ./relayconvert -run TestGolden -update
```

## 版本与兼容性

RelayKit 当前使用 `v0.x` 版本。公开 API 和 DTO 仍可能在小版本中调整，升级前请检查发布说明和实际序列化结果。协议之间并非完全同构，建议对业务实际使用的工具调用、多模态、推理和流式场景增加端到端测试。

## 许可证

RelayKit 是 new-api 项目的一部分，遵循项目根目录中的 [GNU Affero General Public License v3.0](../LICENSE)。
