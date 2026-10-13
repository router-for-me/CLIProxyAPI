// Package claude provides response translation functionality for Claude API.
// This package handles the conversion of backend client responses into Claude-compatible
// Server-Sent Events (SSE) format, implementing a sophisticated state machine that manages
// different response types including text content, thinking processes, and function calls.
// The translation ensures proper sequencing of SSE events and maintains state across
// multiple response chunks to provide a seamless streaming experience.
package claude

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync/atomic"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Params holds parameters for response conversion.
type Params struct {
	IsGlAPIKey       bool
	HasFirstResponse bool
	ResponseType     int
	ResponseIndex    int
	HasContent       bool // Tracks whether any content (text, thinking, or tool use) has been output
	ToolNameMap      map[string]string
	SanitizedNameMap map[string]string
	SawToolCall      bool
	HasFinalEvents   bool
	FinishReason     string
	InputTokens      int64
	OutputTokens     int64
	CachedTokens     int64
}

func resolveGeminiClaudeStopReason(finishReason string, sawToolCall bool) string {
	if sawToolCall {
		return "tool_use"
	}
	switch finishReason {
	case "MAX_TOKENS":
		return "max_tokens"
	case "SAFETY", "RECITATION", "PROHIBITED_CONTENT", "SPII", "BLOCKLIST", "MALFORMED_FUNCTION_CALL", "IMAGE_SAFETY":
		return "refusal"
	case "STOP", "FINISH_REASON_UNSPECIFIED", "UNKNOWN", "":
		return "end_turn"
	default:
		return "end_turn"
	}
}

// toolUseIDCounter provides a process-wide unique counter for tool use identifiers.
var toolUseIDCounter uint64

// ConvertGeminiResponseToClaude performs sophisticated streaming response format conversion.
// This function implements a complex state machine that translates backend client responses
// into Claude-compatible Server-Sent Events (SSE) format. It manages different response types
// and handles state transitions between content blocks, thinking processes, and function calls.
//
// Response type states: 0=none, 1=content, 2=thinking, 3=function
// The function maintains state across multiple calls to ensure proper SSE event sequencing.
//
// Parameters:
//   - ctx: The context for the request.
//   - modelName: The name of the model.
//   - rawJSON: The raw JSON response from the Gemini API.
//   - param: A pointer to a parameter object for the conversion.
//
// Returns:
//   - [][]byte: A slice of bytes, each containing a Claude-compatible SSE payload.
func ConvertGeminiResponseToClaude(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	if *param == nil {
		*param = &Params{
			IsGlAPIKey:       false,
			HasFirstResponse: false,
			ResponseType:     0,
			ResponseIndex:    0,
			ToolNameMap:      util.ToolNameMapFromClaudeRequest(originalRequestRawJSON),
			SanitizedNameMap: util.SanitizedToolNameMap(originalRequestRawJSON),
			SawToolCall:      false,
		}
	}

	output := make([]byte, 0, 1024)
	appendEvent := func(event, payload string) {
		output = translatorcommon.AppendSSEEventString(output, event, payload, 3)
	}
	p := (*param).(*Params)

	if bytes.Equal(rawJSON, []byte("[DONE]")) {
		if p.HasFirstResponse && !p.HasContent {
			appendEvent("content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"text","text":""}}`, p.ResponseIndex))
			p.ResponseType = 1
			p.HasContent = true
		}
		if p.HasContent {
			if p.ResponseType != 0 {
				appendEvent("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, p.ResponseIndex))
				p.ResponseType = 0
			}
			if !p.HasFinalEvents {
				stopReason := resolveGeminiClaudeStopReason(p.FinishReason, p.SawToolCall)
				template := []byte(fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":"%s","stop_sequence":null},"usage":{"input_tokens":0,"output_tokens":0}}`, stopReason))
				template, _ = sjson.SetBytes(template, "usage.output_tokens", p.OutputTokens)
				template, _ = sjson.SetBytes(template, "usage.input_tokens", p.InputTokens)
				if p.CachedTokens > 0 {
					template, _ = sjson.SetBytes(template, "usage.cache_read_input_tokens", p.CachedTokens)
				}
				appendEvent("message_delta", string(template))
				p.HasFinalEvents = true
			}
			appendEvent("message_stop", `{"type":"message_stop"}`)
			return [][]byte{output}
		}
		return [][]byte{}
	}

	appendSignatureDelta := func(signature string) {
		if signature == "" || (*param).(*Params).ResponseType != 2 {
			return
		}
		data, _ := sjson.SetBytes([]byte(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"signature_delta","signature":""}}`, (*param).(*Params).ResponseIndex)), "delta.signature", signature)
		appendEvent("content_block_delta", string(data))
		(*param).(*Params).HasContent = true
	}

	appendCarrierThinkingBlock := func(signature string) {
		if signature == "" {
			return
		}
		p := (*param).(*Params)
		if p.ResponseType != 0 {
			appendEvent("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, p.ResponseIndex))
			p.ResponseIndex++
			p.ResponseType = 0
		}
		appendEvent("content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"thinking","thinking":""}}`, p.ResponseIndex))
		data, _ := sjson.SetBytes([]byte(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"signature_delta","signature":""}}`, p.ResponseIndex)), "delta.signature", signature)
		appendEvent("content_block_delta", string(data))
		appendEvent("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, p.ResponseIndex))
		p.ResponseIndex++
		p.ResponseType = 0
		p.HasContent = true
	}

	// Initialize the streaming session with a message_start event
	// This is only sent for the very first response chunk
	if !(*param).(*Params).HasFirstResponse {
		// Create the initial message structure with default values
		// This follows the Claude API specification for streaming message initialization
		messageStartTemplate := []byte(`{"type":"message_start","message":{"id":"msg_1nZdL29xx5MUA1yADyHTEsnR8uuvGzszyY","type":"message","role":"assistant","content":[],"model":"claude-3-5-sonnet-20241022","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}}`)

		// Override default values with actual response metadata if available
		if modelVersionResult := gjson.GetBytes(rawJSON, "modelVersion"); modelVersionResult.Exists() {
			messageStartTemplate, _ = sjson.SetBytes(messageStartTemplate, "message.model", modelVersionResult.String())
		}
		if responseIDResult := gjson.GetBytes(rawJSON, "responseId"); responseIDResult.Exists() {
			messageStartTemplate, _ = sjson.SetBytes(messageStartTemplate, "message.id", responseIDResult.String())
		}
		appendEvent("message_start", string(messageStartTemplate))

		(*param).(*Params).HasFirstResponse = true
	}

	// Process the response parts array from the backend client
	// Each part can contain text content, thinking content, or function calls
	partsResult := gjson.GetBytes(rawJSON, "candidates.0.content.parts")
	if partsResult.IsArray() {
		partResults := partsResult.Array()
		for i := 0; i < len(partResults); i++ {
			partResult := partResults[i]

			// Extract the different types of content from each part
			partTextResult := partResult.Get("text")
			functionCallResult := partResult.Get("functionCall")
			thoughtSignatureResult := partResult.Get("thoughtSignature")
			if !thoughtSignatureResult.Exists() {
				thoughtSignatureResult = partResult.Get("thought_signature")
			}
			partSig := ""
			if thoughtSignatureResult.Exists() && thoughtSignatureResult.String() != "" {
				partSig = thoughtSignatureResult.String()
			}
			hasThoughtSignature := partSig != ""
			isThought := partResult.Get("thought").Bool()

			if hasThoughtSignature && (!partTextResult.Exists() || partTextResult.String() == "") && !functionCallResult.Exists() {
				if (*param).(*Params).ResponseType == 2 {
					appendSignatureDelta(partSig)
					continue
				}
				appendCarrierThinkingBlock(partSig)
				continue
			}

			if isThought {
				if hasThoughtSignature && (!partTextResult.Exists() || partTextResult.String() == "") {
					if (*param).(*Params).ResponseType == 2 {
						appendSignatureDelta(partSig)
					} else {
						appendCarrierThinkingBlock(partSig)
					}
					continue
				}

				partText := partTextResult.String()
				if (*param).(*Params).ResponseType == 2 {
					data, _ := sjson.SetBytes([]byte(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"thinking_delta","thinking":""}}`, (*param).(*Params).ResponseIndex)), "delta.thinking", partText)
					appendEvent("content_block_delta", string(data))
					(*param).(*Params).HasContent = true
				} else {
					if (*param).(*Params).ResponseType != 0 {
						appendEvent("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, (*param).(*Params).ResponseIndex))
						(*param).(*Params).ResponseIndex++
					}

					appendEvent("content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"thinking","thinking":""}}`, (*param).(*Params).ResponseIndex))
					data, _ := sjson.SetBytes([]byte(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"thinking_delta","thinking":""}}`, (*param).(*Params).ResponseIndex)), "delta.thinking", partText)
					appendEvent("content_block_delta", string(data))
					(*param).(*Params).ResponseType = 2
					(*param).(*Params).HasContent = true
				}
				if hasThoughtSignature {
					appendSignatureDelta(partSig)
				}
				continue
			}

			// From here on, !isThought (visible text, functionCall, or standalone signature)
			if functionCallResult.Exists() {
				(*param).(*Params).SawToolCall = true
				upstreamToolName := functionCallResult.Get("name").String()
				upstreamToolName = util.RestoreSanitizedToolName((*param).(*Params).SanitizedNameMap, upstreamToolName)
				clientToolName := util.MapToolName((*param).(*Params).ToolNameMap, upstreamToolName)

				if (*param).(*Params).ResponseType == 3 && upstreamToolName == "" {
					if fcArgsResult := functionCallResult.Get("args"); fcArgsResult.Exists() {
						data, _ := sjson.SetBytes([]byte(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":""}}`, (*param).(*Params).ResponseIndex)), "delta.partial_json", fcArgsResult.Raw)
						appendEvent("content_block_delta", string(data))
					}
					if hasThoughtSignature {
						appendCarrierThinkingBlock(partSig)
					}
					continue
				}

				if hasThoughtSignature {
					appendCarrierThinkingBlock(partSig)
				}

				if (*param).(*Params).ResponseType == 3 {
					appendEvent("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, (*param).(*Params).ResponseIndex))
					(*param).(*Params).ResponseIndex++
					(*param).(*Params).ResponseType = 0
				}
				if (*param).(*Params).ResponseType != 0 {
					appendEvent("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, (*param).(*Params).ResponseIndex))
					(*param).(*Params).ResponseIndex++
				}

				data := []byte(fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"tool_use","id":"","name":"","input":{}}}`, (*param).(*Params).ResponseIndex))
				data, _ = sjson.SetBytes(data, "content_block.id", util.SanitizeClaudeToolID(fmt.Sprintf("%s-%d", upstreamToolName, atomic.AddUint64(&toolUseIDCounter, 1))))
				data, _ = sjson.SetBytes(data, "content_block.name", clientToolName)
				appendEvent("content_block_start", string(data))

				if fcArgsResult := functionCallResult.Get("args"); fcArgsResult.Exists() {
					data, _ = sjson.SetBytes([]byte(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":""}}`, (*param).(*Params).ResponseIndex)), "delta.partial_json", fcArgsResult.Raw)
					appendEvent("content_block_delta", string(data))
				}
				(*param).(*Params).ResponseType = 3
				(*param).(*Params).HasContent = true
				continue
			}

			if partTextResult.Exists() {
				partText := partTextResult.String()
				if hasThoughtSignature && partText == "" {
					if (*param).(*Params).ResponseType == 2 {
						appendSignatureDelta(partSig)
						continue
					}
					appendCarrierThinkingBlock(partSig)
					continue
				}
				if hasThoughtSignature {
					appendCarrierThinkingBlock(partSig)
				}
				if (*param).(*Params).ResponseType == 1 {
					data, _ := sjson.SetBytes([]byte(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"text_delta","text":""}}`, (*param).(*Params).ResponseIndex)), "delta.text", partText)
					appendEvent("content_block_delta", string(data))
					(*param).(*Params).HasContent = true
				} else {
					if (*param).(*Params).ResponseType != 0 {
						appendEvent("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, (*param).(*Params).ResponseIndex))
						(*param).(*Params).ResponseIndex++
					}
					appendEvent("content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"text","text":""}}`, (*param).(*Params).ResponseIndex))
					data, _ := sjson.SetBytes([]byte(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"text_delta","text":""}}`, (*param).(*Params).ResponseIndex)), "delta.text", partText)
					appendEvent("content_block_delta", string(data))
					(*param).(*Params).ResponseType = 1
					(*param).(*Params).HasContent = true
				}
				continue
			}

			if hasThoughtSignature {
				appendCarrierThinkingBlock(partSig)
				continue
			}
		}
	}

	if finish := gjson.GetBytes(rawJSON, "candidates.0.finishReason"); finish.Exists() && finish.String() != "" {
		(*param).(*Params).FinishReason = finish.String()
	}

	usageResult := gjson.GetBytes(rawJSON, "usageMetadata")
	if usageResult.Exists() {
		cachedTokens := usageResult.Get("cachedContentTokenCount").Int()
		promptTokens := usageResult.Get("promptTokenCount").Int() - cachedTokens
		if promptTokens < 0 {
			promptTokens = 0
		}
		outputTokens := usageResult.Get("candidatesTokenCount").Int() + usageResult.Get("thoughtsTokenCount").Int()
		if outputTokens == 0 && usageResult.Get("totalTokenCount").Int() > 0 {
			outputTokens = usageResult.Get("totalTokenCount").Int() - usageResult.Get("promptTokenCount").Int()
			if outputTokens < 0 {
				outputTokens = 0
			}
		}
		(*param).(*Params).InputTokens = promptTokens
		(*param).(*Params).OutputTokens = outputTokens
		(*param).(*Params).CachedTokens = cachedTokens
	}

	if usageResult.Exists() && bytes.Contains(rawJSON, []byte(`"finishReason"`)) && !(*param).(*Params).HasFinalEvents {
		if !(*param).(*Params).HasContent && (*param).(*Params).HasFirstResponse {
			appendEvent("content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"text","text":""}}`, (*param).(*Params).ResponseIndex))
			(*param).(*Params).ResponseType = 1
			(*param).(*Params).HasContent = true
		}

		if (*param).(*Params).HasContent {
			if (*param).(*Params).ResponseType != 0 {
				appendEvent("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, (*param).(*Params).ResponseIndex))
				(*param).(*Params).ResponseType = 0
			}

			stopReason := resolveGeminiClaudeStopReason((*param).(*Params).FinishReason, (*param).(*Params).SawToolCall)
			template := []byte(fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":"%s","stop_sequence":null},"usage":{"input_tokens":0,"output_tokens":0}}`, stopReason))
			template, _ = sjson.SetBytes(template, "usage.output_tokens", (*param).(*Params).OutputTokens)
			template, _ = sjson.SetBytes(template, "usage.input_tokens", (*param).(*Params).InputTokens)
			if (*param).(*Params).CachedTokens > 0 {
				template, _ = sjson.SetBytes(template, "usage.cache_read_input_tokens", (*param).(*Params).CachedTokens)
			}

			appendEvent("message_delta", string(template))
			(*param).(*Params).HasFinalEvents = true
		}
	}

	return [][]byte{output}
}

// ConvertGeminiResponseToClaudeNonStream converts a non-streaming Gemini response to a non-streaming Claude response.
//
// Parameters:
//   - ctx: The context for the request.
//   - modelName: The name of the model.
//   - rawJSON: The raw JSON response from the Gemini API.
//   - param: A pointer to a parameter object for the conversion.
//
// Returns:
//   - []byte: A Claude-compatible JSON response.
func ConvertGeminiResponseToClaudeNonStream(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) []byte {
	_ = requestRawJSON

	root := gjson.ParseBytes(rawJSON)
	toolNameMap := util.ToolNameMapFromClaudeRequest(originalRequestRawJSON)
	sanitizedNameMap := util.SanitizedToolNameMap(originalRequestRawJSON)

	out := []byte(`{"id":"","type":"message","role":"assistant","model":"","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}`)
	out, _ = sjson.SetBytes(out, "id", root.Get("responseId").String())
	out, _ = sjson.SetBytes(out, "model", root.Get("modelVersion").String())

	cachedTokens := root.Get("usageMetadata.cachedContentTokenCount").Int()
	inputTokens := root.Get("usageMetadata.promptTokenCount").Int() - cachedTokens
	if inputTokens < 0 {
		inputTokens = 0
	}
	outputTokens := root.Get("usageMetadata.candidatesTokenCount").Int() + root.Get("usageMetadata.thoughtsTokenCount").Int()
	out, _ = sjson.SetBytes(out, "usage.input_tokens", inputTokens)
	out, _ = sjson.SetBytes(out, "usage.output_tokens", outputTokens)
	if cachedTokens > 0 {
		out, _ = sjson.SetBytes(out, "usage.cache_read_input_tokens", cachedTokens)
	}

	parts := root.Get("candidates.0.content.parts")
	textBuilder := strings.Builder{}
	thinkingBuilder := strings.Builder{}
	var thinkingSignature string
	toolIDCounter := 0
	hasToolCall := false
	var blocks [][]byte

	flushText := func() {
		if textBuilder.Len() == 0 {
			return
		}
		block := []byte(`{"type":"text","text":""}`)
		block, _ = sjson.SetBytes(block, "text", textBuilder.String())
		blocks = append(blocks, block)
		textBuilder.Reset()
	}

	flushThinking := func() {
		if thinkingBuilder.Len() == 0 && thinkingSignature == "" {
			return
		}
		block := []byte(`{"type":"thinking","thinking":""}`)
		block, _ = sjson.SetBytes(block, "thinking", thinkingBuilder.String())
		if thinkingSignature != "" {
			block, _ = sjson.SetBytes(block, "signature", thinkingSignature)
		}
		blocks = append(blocks, block)
		thinkingBuilder.Reset()
		thinkingSignature = ""
	}

	appendCarrierThinkingBlock := func(signature string) {
		if signature == "" {
			return
		}
		carrier := []byte(`{"type":"thinking","thinking":"","signature":""}`)
		carrier, _ = sjson.SetBytes(carrier, "signature", signature)
		blocks = append(blocks, carrier)
	}

	if parts.IsArray() {
		for _, part := range parts.Array() {
			thoughtSignatureResult := part.Get("thoughtSignature")
			if !thoughtSignatureResult.Exists() {
				thoughtSignatureResult = part.Get("thought_signature")
			}
			partSig := ""
			if thoughtSignatureResult.Exists() && thoughtSignatureResult.String() != "" {
				partSig = thoughtSignatureResult.String()
			}

			text := part.Get("text")
			functionCall := part.Get("functionCall")
			isThought := part.Get("thought").Bool()

			if isThought {
				flushText()
				if partSig != "" {
					thinkingSignature = partSig
				}
				if text.Exists() && text.String() != "" {
					thinkingBuilder.WriteString(text.String())
				}
				continue
			}

			// If this is a part without visible text or function call, and thinking is in progress:
			// this signature belongs to the thinking block!
			if (!text.Exists() || text.String() == "") && !functionCall.Exists() {
				if thinkingBuilder.Len() > 0 && partSig != "" {
					thinkingSignature = partSig
					continue
				}
				if partSig != "" {
					flushThinking()
					flushText()
					appendCarrierThinkingBlock(partSig)
					continue
				}
				continue
			}

			// From here on, !isThought (visible text or functionCall)
			flushThinking()

			if functionCall.Exists() {
				flushText()
				if partSig != "" {
					appendCarrierThinkingBlock(partSig)
				}
				hasToolCall = true

				upstreamToolName := functionCall.Get("name").String()
				upstreamToolName = util.RestoreSanitizedToolName(sanitizedNameMap, upstreamToolName)
				clientToolName := util.MapToolName(toolNameMap, upstreamToolName)
				toolIDCounter++
				toolBlock := []byte(`{"type":"tool_use","id":"","name":"","input":{}}`)
				toolBlock, _ = sjson.SetBytes(toolBlock, "id", util.SanitizeClaudeToolID(fmt.Sprintf("%s-%d", upstreamToolName, toolIDCounter)))
				toolBlock, _ = sjson.SetBytes(toolBlock, "name", clientToolName)
				inputRaw := "{}"
				if args := functionCall.Get("args"); args.Exists() && gjson.Valid(args.Raw) && args.IsObject() {
					inputRaw = args.Raw
				}
				toolBlock, _ = sjson.SetRawBytes(toolBlock, "input", []byte(inputRaw))
				blocks = append(blocks, toolBlock)
				continue
			}

			if text.Exists() && text.String() != "" {
				if partSig != "" {
					flushText()
					appendCarrierThinkingBlock(partSig)
				}
				textBuilder.WriteString(text.String())
				continue
			}

			if partSig != "" {
				flushText()
				appendCarrierThinkingBlock(partSig)
				continue
			}
		}
	}

	flushThinking()
	flushText()

	if len(blocks) > 0 {
		out, _ = sjson.SetRawBytes(out, "content", translatorcommon.JoinRawArray(blocks))
	}

	var finishReason string
	if finish := root.Get("candidates.0.finishReason"); finish.Exists() {
		finishReason = finish.String()
	}
	out, _ = sjson.SetBytes(out, "stop_reason", resolveGeminiClaudeStopReason(finishReason, hasToolCall))

	if inputTokens == int64(0) && outputTokens == int64(0) && !root.Get("usageMetadata").Exists() {
		out, _ = sjson.DeleteBytes(out, "usage")
	}

	return out
}

func ClaudeTokenCount(ctx context.Context, count int64) []byte {
	return translatorcommon.ClaudeInputTokensJSON(count)
}
