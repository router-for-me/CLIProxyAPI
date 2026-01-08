package chat_completions

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// convertGeminiResponseToOpenAIChatParams holds parameters for response conversion.
type convertGeminiResponseToOpenAIChatParams struct {
	UnixTimestamp int64
	// 修改：改为 Map 以支持多 Candidate 的函数索引追踪
	FunctionIndex map[int]int
}

// functionCallIDCounter provides a process-wide unique counter for function call identifiers.
var functionCallIDCounter uint64

// ConvertGeminiResponseToOpenAI translates a single chunk of a streaming response from the
// Gemini API format to the OpenAI Chat Completions streaming format.
func ConvertGeminiResponseToOpenAI(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []string {
	// 初始化参数
	if *param == nil {
		*param = &convertGeminiResponseToOpenAIChatParams{
			UnixTimestamp: 0,
			FunctionIndex: make(map[int]int),
		}
	}
	// 确保 Map 已初始化 (针对旧的 param 可能的情况)
	p := (*param).(*convertGeminiResponseToOpenAIChatParams)
	if p.FunctionIndex == nil {
		p.FunctionIndex = make(map[int]int)
	}

	if bytes.HasPrefix(rawJSON, []byte("data:")) {
		rawJSON = bytes.TrimSpace(rawJSON[5:])
	}

	if bytes.Equal(rawJSON, []byte("[DONE]")) {
		return []string{}
	}

	// 基础模板，注意这里 finish_reason 等稍后设置
	baseTemplate := `{"id":"","object":"chat.completion.chunk","created":12345,"model":"model","choices":[{"index":0,"delta":{"role":null,"content":null,"reasoning_content":null,"tool_calls":null},"finish_reason":null,"native_finish_reason":null}]}`

	// Extract and set the model version.
	if modelVersionResult := gjson.GetBytes(rawJSON, "modelVersion"); modelVersionResult.Exists() {
		baseTemplate, _ = sjson.Set(baseTemplate, "model", modelVersionResult.String())
	}

	// Extract and set the creation timestamp.
	if createTimeResult := gjson.GetBytes(rawJSON, "createTime"); createTimeResult.Exists() {
		t, err := time.Parse(time.RFC3339Nano, createTimeResult.String())
		if err == nil {
			p.UnixTimestamp = t.Unix()
		}
		baseTemplate, _ = sjson.Set(baseTemplate, "created", p.UnixTimestamp)
	} else {
		baseTemplate, _ = sjson.Set(baseTemplate, "created", p.UnixTimestamp)
	}

	// Extract and set the response ID.
	if responseIDResult := gjson.GetBytes(rawJSON, "responseId"); responseIDResult.Exists() {
		baseTemplate, _ = sjson.Set(baseTemplate, "id", responseIDResult.String())
	}

	// 处理 Usage Metadata (通常只在最后一个 chunk 出现，且是对所有 candidate 的汇总)
	// 如果包含 usage，我们将其作为一个单独的 chunk 发送，或者附着在第一个 candidate 的 chunk 上
	// 这里为了保持原有逻辑，我们先处理 usage，如果有 usage，无论是否有 candidate 都需要更新 baseTemplate
	// 但通常 usage 是最后一条，Gemini 可能同时发回 content 和 usage。
	// 原逻辑是在单个 template 上直接 set usage。
	// 为了支持多 candidate，我们只需确保 Usage 字段被设置在返回的列表中其中一个 template 上，或者每个都带（OpenAI允许）。
	// 简单起见，我们在 baseTemplate 上设置 Usage，这样基于它生成的每个 chunk 都会带 Usage (虽然冗余但符合规范)，
	// 或者我们只生成一个专门的 Usage chunk。原代码逻辑是将 usage 并在消息 chunk 里。

	if usageResult := gjson.GetBytes(rawJSON, "usageMetadata"); usageResult.Exists() {
		cachedTokenCount := usageResult.Get("cachedContentTokenCount").Int()
		if candidatesTokenCountResult := usageResult.Get("candidatesTokenCount"); candidatesTokenCountResult.Exists() {
			baseTemplate, _ = sjson.Set(baseTemplate, "usage.completion_tokens", candidatesTokenCountResult.Int())
		}
		if totalTokenCountResult := usageResult.Get("totalTokenCount"); totalTokenCountResult.Exists() {
			baseTemplate, _ = sjson.Set(baseTemplate, "usage.total_tokens", totalTokenCountResult.Int())
		}
		promptTokenCount := usageResult.Get("promptTokenCount").Int() - cachedTokenCount
		thoughtsTokenCount := usageResult.Get("thoughtsTokenCount").Int()
		baseTemplate, _ = sjson.Set(baseTemplate, "usage.prompt_tokens", promptTokenCount+thoughtsTokenCount)
		if thoughtsTokenCount > 0 {
			baseTemplate, _ = sjson.Set(baseTemplate, "usage.completion_tokens_details.reasoning_tokens", thoughtsTokenCount)
		}
		// Include cached token count if present (indicates prompt caching is working)
		if cachedTokenCount > 0 {
			var err error
			baseTemplate, err = sjson.Set(baseTemplate, "usage.prompt_tokens_details.cached_tokens", cachedTokenCount)
			if err != nil {
				log.Warnf("gemini openai response: failed to set cached_tokens in streaming: %v", err)
			}
		}
	}

	var responseStrings []string
	candidates := gjson.GetBytes(rawJSON, "candidates")

	// 遍历所有 Candidate
	if candidates.IsArray() {
		candidates.ForEach(func(_, candidate gjson.Result) bool {
			// 为当前 candidate 复制一份模板
			template := baseTemplate

			// 获取当前 Candidate 的 Index
			candidateIndex := int(candidate.Get("index").Int())
			template, _ = sjson.Set(template, "choices.0.index", candidateIndex)

			// 设置 Finish Reason
			if finishReasonResult := candidate.Get("finishReason"); finishReasonResult.Exists() {
				template, _ = sjson.Set(template, "choices.0.finish_reason", strings.ToLower(finishReasonResult.String()))
				template, _ = sjson.Set(template, "choices.0.native_finish_reason", strings.ToLower(finishReasonResult.String()))
			}

			partsResult := candidate.Get("content.parts")
			hasFunctionCall := false

			if partsResult.IsArray() {
				partResults := partsResult.Array()
				for i := 0; i < len(partResults); i++ {
					partResult := partResults[i]
					partTextResult := partResult.Get("text")
					functionCallResult := partResult.Get("functionCall")
					inlineDataResult := partResult.Get("inlineData")
					if !inlineDataResult.Exists() {
						inlineDataResult = partResult.Get("inline_data")
					}
					thoughtSignatureResult := partResult.Get("thoughtSignature")
					if !thoughtSignatureResult.Exists() {
						thoughtSignatureResult = partResult.Get("thought_signature")
					}

					hasThoughtSignature := thoughtSignatureResult.Exists() && thoughtSignatureResult.String() != ""
					hasContentPayload := partTextResult.Exists() || functionCallResult.Exists() || inlineDataResult.Exists()

			// Skip pure thoughtSignature parts but keep any actual payload in the same part.
					if hasThoughtSignature && !hasContentPayload {
						continue
					}

					if partTextResult.Exists() {
						text := partTextResult.String()
				// Handle text content, distinguishing between regular content and reasoning/thoughts.
						if partResult.Get("thought").Bool() {
							template, _ = sjson.Set(template, "choices.0.delta.reasoning_content", text)
						} else {
							template, _ = sjson.Set(template, "choices.0.delta.content", text)
						}
						template, _ = sjson.Set(template, "choices.0.delta.role", "assistant")
					} else if functionCallResult.Exists() {
				// Handle function call content.
						hasFunctionCall = true
						toolCallsResult := gjson.Get(template, "choices.0.delta.tool_calls")

						// 使用 Map 获取当前 Index 的 FunctionIndex
						functionCallIndex := p.FunctionIndex[candidateIndex]
						p.FunctionIndex[candidateIndex]++

						if toolCallsResult.Exists() && toolCallsResult.IsArray() {
							functionCallIndex = len(toolCallsResult.Array())
						} else {
							template, _ = sjson.SetRaw(template, "choices.0.delta.tool_calls", `[]`)
						}

						functionCallTemplate := `{"id": "","index": 0,"type": "function","function": {"name": "","arguments": ""}}`
						fcName := functionCallResult.Get("name").String()
						functionCallTemplate, _ = sjson.Set(functionCallTemplate, "id", fmt.Sprintf("%s-%d-%d", fcName, time.Now().UnixNano(), atomic.AddUint64(&functionCallIDCounter, 1)))
						functionCallTemplate, _ = sjson.Set(functionCallTemplate, "index", functionCallIndex)
						functionCallTemplate, _ = sjson.Set(functionCallTemplate, "function.name", fcName)
						if fcArgsResult := functionCallResult.Get("args"); fcArgsResult.Exists() {
							functionCallTemplate, _ = sjson.Set(functionCallTemplate, "function.arguments", fcArgsResult.Raw)
						}
						template, _ = sjson.Set(template, "choices.0.delta.role", "assistant")
						template, _ = sjson.SetRaw(template, "choices.0.delta.tool_calls.-1", functionCallTemplate)
					} else if inlineDataResult.Exists() {
						data := inlineDataResult.Get("data").String()
						if data == "" {
							continue
						}
						mimeType := inlineDataResult.Get("mimeType").String()
						if mimeType == "" {
							mimeType = inlineDataResult.Get("mime_type").String()
						}
						if mimeType == "" {
							mimeType = "image/png"
						}
						imageURL := fmt.Sprintf("data:%s;base64,%s", mimeType, data)
						imagesResult := gjson.Get(template, "choices.0.delta.images")
						if !imagesResult.Exists() || !imagesResult.IsArray() {
							template, _ = sjson.SetRaw(template, "choices.0.delta.images", `[]`)
						}
						imageIndex := len(gjson.Get(template, "choices.0.delta.images").Array())
						imagePayload := `{"type":"image_url","image_url":{"url":""}}`
						imagePayload, _ = sjson.Set(imagePayload, "index", imageIndex)
						imagePayload, _ = sjson.Set(imagePayload, "image_url.url", imageURL)
						template, _ = sjson.Set(template, "choices.0.delta.role", "assistant")
						template, _ = sjson.SetRaw(template, "choices.0.delta.images.-1", imagePayload)
					}
				}
			}

			if hasFunctionCall {
				template, _ = sjson.Set(template, "choices.0.finish_reason", "tool_calls")
				template, _ = sjson.Set(template, "choices.0.native_finish_reason", "tool_calls")
			}

			responseStrings = append(responseStrings, template)
			return true // continue loop
		})
	} else {
		// 如果没有 candidates (可能是纯 usage 块)，则直接返回 baseTemplate
		// 但通常 gemini 至少有一个 candidate 结构即使是空的，或者是 usageMetadata 块。
		// 如果 rawJSON 只有 usageMetadata 而没有 candidates，则上面的 Loop 不会执行。
		// 在这种情况下，我们需要返回包含 usage 的 template。
		if gjson.GetBytes(rawJSON, "usageMetadata").Exists() && len(responseStrings) == 0 {
			// 对于纯 Usage chunk，OpenAI 期望 choices 数组存在且通常为空，或者维持原样
			responseStrings = append(responseStrings, baseTemplate)
		}
	}

	return responseStrings
}

// ConvertGeminiResponseToOpenAINonStream converts a non-streaming Gemini response to a non-streaming OpenAI response.
func ConvertGeminiResponseToOpenAINonStream(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) string {
	var unixTimestamp int64
	// 修改：初始 choices 设为空数组
	template := `{"id":"","object":"chat.completion","created":123456,"model":"model","choices":[]}`

	if modelVersionResult := gjson.GetBytes(rawJSON, "modelVersion"); modelVersionResult.Exists() {
		template, _ = sjson.Set(template, "model", modelVersionResult.String())
	}

	if createTimeResult := gjson.GetBytes(rawJSON, "createTime"); createTimeResult.Exists() {
		t, err := time.Parse(time.RFC3339Nano, createTimeResult.String())
		if err == nil {
			unixTimestamp = t.Unix()
		}
		template, _ = sjson.Set(template, "created", unixTimestamp)
	} else {
		template, _ = sjson.Set(template, "created", unixTimestamp)
	}

	if responseIDResult := gjson.GetBytes(rawJSON, "responseId"); responseIDResult.Exists() {
		template, _ = sjson.Set(template, "id", responseIDResult.String())
	}

	// Usage Metadata 设置 (保持原逻辑)
	if usageResult := gjson.GetBytes(rawJSON, "usageMetadata"); usageResult.Exists() {
		if candidatesTokenCountResult := usageResult.Get("candidatesTokenCount"); candidatesTokenCountResult.Exists() {
			template, _ = sjson.Set(template, "usage.completion_tokens", candidatesTokenCountResult.Int())
		}
		if totalTokenCountResult := usageResult.Get("totalTokenCount"); totalTokenCountResult.Exists() {
			template, _ = sjson.Set(template, "usage.total_tokens", totalTokenCountResult.Int())
		}
		promptTokenCount := usageResult.Get("promptTokenCount").Int()
		thoughtsTokenCount := usageResult.Get("thoughtsTokenCount").Int()
		cachedTokenCount := usageResult.Get("cachedContentTokenCount").Int()
		template, _ = sjson.Set(template, "usage.prompt_tokens", promptTokenCount+thoughtsTokenCount)
		if thoughtsTokenCount > 0 {
			template, _ = sjson.Set(template, "usage.completion_tokens_details.reasoning_tokens", thoughtsTokenCount)
		}
		if cachedTokenCount > 0 {
			var err error
			template, err = sjson.Set(template, "usage.prompt_tokens_details.cached_tokens", cachedTokenCount)
			if err != nil {
				log.Warnf("gemini openai response: failed to set cached_tokens in non-streaming: %v", err)
			}
		}
	}

	// 遍历 candidates
	candidates := gjson.GetBytes(rawJSON, "candidates")
	if candidates.IsArray() {
		candidates.ForEach(func(_, candidate gjson.Result) bool {
			// 构建单个 Choice
			// 注意：这里我们构建一个独立的 choice 对象，然后 append 到 template 的 choices 数组中
			choiceTemplate := `{"index":0,"message":{"role":"assistant","content":null,"reasoning_content":null,"tool_calls":null},"finish_reason":null,"native_finish_reason":null}`

			// 设置 Index
			choiceTemplate, _ = sjson.Set(choiceTemplate, "index", candidate.Get("index").Int())

			// 设置 Finish Reason
			if finishReasonResult := candidate.Get("finishReason"); finishReasonResult.Exists() {
				choiceTemplate, _ = sjson.Set(choiceTemplate, "finish_reason", strings.ToLower(finishReasonResult.String()))
				choiceTemplate, _ = sjson.Set(choiceTemplate, "native_finish_reason", strings.ToLower(finishReasonResult.String()))
			}

			// 处理 Content Parts
			partsResult := candidate.Get("content.parts")
			hasFunctionCall := false
			if partsResult.IsArray() {
				partsResults := partsResult.Array()
				for i := 0; i < len(partsResults); i++ {
					partResult := partsResults[i]
					partTextResult := partResult.Get("text")
					functionCallResult := partResult.Get("functionCall")
					inlineDataResult := partResult.Get("inlineData")
					if !inlineDataResult.Exists() {
						inlineDataResult = partResult.Get("inline_data")
					}

					if partTextResult.Exists() {
						if partResult.Get("thought").Bool() {
							// 累加 reasoning (Gemini 可能分多个 part，虽然非流式通常是一个，但保持健壮性)
							oldVal := gjson.Get(choiceTemplate, "message.reasoning_content").String()
							choiceTemplate, _ = sjson.Set(choiceTemplate, "message.reasoning_content", oldVal+partTextResult.String())
						} else {
							oldVal := gjson.Get(choiceTemplate, "message.content").String()
							choiceTemplate, _ = sjson.Set(choiceTemplate, "message.content", oldVal+partTextResult.String())
						}
					} else if functionCallResult.Exists() {
						hasFunctionCall = true
						toolCallsResult := gjson.Get(choiceTemplate, "message.tool_calls")
						if !toolCallsResult.Exists() || !toolCallsResult.IsArray() {
							choiceTemplate, _ = sjson.SetRaw(choiceTemplate, "message.tool_calls", `[]`)
						}
						functionCallItemTemplate := `{"id": "","type": "function","function": {"name": "","arguments": ""}}`
						fcName := functionCallResult.Get("name").String()
						functionCallItemTemplate, _ = sjson.Set(functionCallItemTemplate, "id", fmt.Sprintf("%s-%d-%d", fcName, time.Now().UnixNano(), atomic.AddUint64(&functionCallIDCounter, 1)))
						functionCallItemTemplate, _ = sjson.Set(functionCallItemTemplate, "function.name", fcName)
						if fcArgsResult := functionCallResult.Get("args"); fcArgsResult.Exists() {
							functionCallItemTemplate, _ = sjson.Set(functionCallItemTemplate, "function.arguments", fcArgsResult.Raw)
						}
						choiceTemplate, _ = sjson.SetRaw(choiceTemplate, "message.tool_calls.-1", functionCallItemTemplate)
					} else if inlineDataResult.Exists() {
						// Image Handling
						data := inlineDataResult.Get("data").String()
						if data != "" {
							mimeType := inlineDataResult.Get("mimeType").String()
							if mimeType == "" {
								mimeType = inlineDataResult.Get("mime_type").String()
							}
							if mimeType == "" {
								mimeType = "image/png"
							}
							imageURL := fmt.Sprintf("data:%s;base64,%s", mimeType, data)
							imagesResult := gjson.Get(choiceTemplate, "message.images")
							if !imagesResult.Exists() || !imagesResult.IsArray() {
								choiceTemplate, _ = sjson.SetRaw(choiceTemplate, "message.images", `[]`)
							}
							imageIndex := len(gjson.Get(choiceTemplate, "message.images").Array())
							imagePayload := `{"type":"image_url","image_url":{"url":""}}`
							imagePayload, _ = sjson.Set(imagePayload, "index", imageIndex)
							imagePayload, _ = sjson.Set(imagePayload, "image_url.url", imageURL)
							choiceTemplate, _ = sjson.SetRaw(choiceTemplate, "message.images.-1", imagePayload)
						}
					}
				}
			}

			if hasFunctionCall {
				choiceTemplate, _ = sjson.Set(choiceTemplate, "finish_reason", "tool_calls")
				choiceTemplate, _ = sjson.Set(choiceTemplate, "native_finish_reason", "tool_calls")
			}

			// 将构建好的 Choice 添加到 template 的 choices 数组中
			template, _ = sjson.SetRaw(template, "choices.-1", choiceTemplate)
			return true
		})
	}

	return template
}
