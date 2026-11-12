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

	"github.com/MoeForge/moego/ojson"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	geminicommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

// Params holds parameters for response conversion.
type Params struct {
	IsGlAPIKey                   bool
	HasFirstResponse             bool
	ResponseType                 int
	ResponseIndex                int
	HasContent                   bool // Tracks whether any content (text, thinking, or tool use) has been output
	ToolNameMap                  map[string]string
	SanitizedNameMap             map[string]string
	SawToolCall                  bool
	HasFinalEvents               bool
	FinishReason                 string
	InputTokens                  int64
	OutputTokens                 int64
	CachedTokens                 int64
	CurrentToolName              string // Client-facing name of the currently open tool_use block
	CurrentToolIsParameterless   bool   // Whether the open tool_use block was declared parameterless
	CurrentToolInputEmitted      bool   // Whether cleaned input JSON was already emitted for the open tool_use block
	CurrentToolPendingEmpty      bool   // Whether an empty "{}" input_json_delta is deferred awaiting tool_use completion
	ActiveToolCall               *translatorcommon.VertexStreamingToolCall
	PendingToolThoughtSignatures []string
	HasIncompleteToolCall        bool
	LastSignatureTarget          string
	CurrentThinkingHasText       bool
}

const geminiMalformedFunctionCallErrorPayload = `{"type":"error","error":{"type":"api_error","message":"Gemini response finished with MALFORMED_FUNCTION_CALL"}}`

func isGeminiMalformedFunctionCall(finishReason string, sawToolCall bool) bool {
	return finishReason == "MALFORMED_FUNCTION_CALL" && !sawToolCall
}

func resolveGeminiClaudeStopReason(finishReason string, sawToolCall bool) string {
	if sawToolCall {
		return "tool_use"
	}
	switch finishReason {
	case "MAX_TOKENS":
		return "max_tokens"
	case "SAFETY", "RECITATION", "PROHIBITED_CONTENT", "SPII", "BLOCKLIST", "IMAGE_SAFETY", "MALFORMED_RESPONSE":
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
			ToolNameMap:      util.UnambiguousToolNameMapFromClaudeRequest(originalRequestRawJSON),
			SanitizedNameMap: util.SanitizedToolNameMap(originalRequestRawJSON),
			SawToolCall:      false,
		}
	}

	output := make([]byte, 0, 1024)
	appendEvent := func(event, payload string) {
		output = translatorcommon.AppendSSEEventString(output, event, payload, 3)
	}
	p := (*param).(*Params)
	flushPendingToolInput := func() {
		if p.ResponseType == 3 && p.CurrentToolPendingEmpty && !p.CurrentToolInputEmitted {
			data, _ := ojson.SetBytes([]byte(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":""}}`, p.ResponseIndex)), "delta.partial_json", "{}")
			appendEvent("content_block_delta", string(data))
			p.CurrentToolInputEmitted = true
			p.CurrentToolPendingEmpty = false
		}
	}
	abortActiveToolCall := func() {
		p.HasIncompleteToolCall = true
		p.LastSignatureTarget = ""
		if p.ActiveToolCall != nil {
			p.ActiveToolCall = nil
			p.PendingToolThoughtSignatures = nil
			p.CurrentToolPendingEmpty = false
			if p.ResponseType == 3 {
				p.ResponseIndex++
				p.ResponseType = 0
			}
		}
	}

	if bytes.Equal(rawJSON, []byte("[DONE]")) {
		if p.ActiveToolCall != nil || p.HasIncompleteToolCall {
			return [][]byte{}
		}
		if isGeminiMalformedFunctionCall(p.FinishReason, p.SawToolCall) {
			if p.ResponseType != 0 {
				flushPendingToolInput()
				appendEvent("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, p.ResponseIndex))
				p.ResponseType = 0
			}
			if !p.HasFinalEvents {
				appendEvent("error", geminiMalformedFunctionCallErrorPayload)
				p.HasFinalEvents = true
			}
			if len(output) > 0 {
				return [][]byte{output}
			}
			return [][]byte{}
		}
		if p.HasFirstResponse && !p.HasContent {
			appendEvent("content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"text","text":""}}`, p.ResponseIndex))
			p.ResponseType = 1
			p.HasContent = true
		}
		if p.HasContent {
			if p.ResponseType != 0 {
				flushPendingToolInput()
				appendEvent("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, p.ResponseIndex))
				p.ResponseType = 0
			}
			if !p.HasFinalEvents {
				stopReason := resolveGeminiClaudeStopReason(p.FinishReason, p.SawToolCall)
				templateDoc := ojson.NewDocString(fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":"%s","stop_sequence":null},"usage":{"input_tokens":0,"output_tokens":0}}`, stopReason))
				_ = templateDoc.Set("usage.output_tokens", p.OutputTokens)
				_ = templateDoc.Set("usage.input_tokens", p.InputTokens)
				if p.CachedTokens > 0 {
					_ = templateDoc.Set("usage.cache_read_input_tokens", p.CachedTokens)
				}
				appendEvent("message_delta", templateDoc.String())
				p.HasFinalEvents = true
			}
			appendEvent("message_stop", `{"type":"message_stop"}`)
			return [][]byte{output}
		}
		return [][]byte{}
	}

	if isGeminiMalformedFunctionCall(p.FinishReason, p.SawToolCall) && p.HasFinalEvents {
		return [][]byte{}
	}

	appendSignatureDelta := func(signature string) {
		if signature == "" || (*param).(*Params).ResponseType != 2 {
			return
		}
		data, _ := ojson.SetBytes([]byte(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"signature_delta","signature":""}}`, (*param).(*Params).ResponseIndex)), "delta.signature", signature)
		appendEvent("content_block_delta", string(data))
		(*param).(*Params).HasContent = true
	}

	appendCarrierThinkingBlock := func(signature string) {
		if signature == "" {
			return
		}
		p := (*param).(*Params)
		if p.ResponseType != 0 {
			flushPendingToolInput()
			appendEvent("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, p.ResponseIndex))
			p.ResponseIndex++
			p.ResponseType = 0
		}
		appendEvent("content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"thinking","thinking":""}}`, p.ResponseIndex))
		data, _ := ojson.SetBytes([]byte(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"signature_delta","signature":""}}`, p.ResponseIndex)), "delta.signature", signature)
		appendEvent("content_block_delta", string(data))
		appendEvent("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, p.ResponseIndex))
		p.ResponseIndex++
		p.ResponseType = 0
		p.HasContent = true
	}

	appendTrailingCarrierThinkingBlock := func(signature string) {
		target := p.LastSignatureTarget
		if target == geminiClaudeUnsupportedSignatureTarget {
			return
		}
		if target != "" {
			signature = translatorcommon.EncodeGeminiClaudeCarrierSignature(signature, translatorcommon.GeminiClaudeCarrierPrevious, target)
		}
		appendCarrierThinkingBlock(signature)
	}

	// Initialize the streaming session with a message_start event
	// This is only sent for the very first response chunk
	if !(*param).(*Params).HasFirstResponse {
		// Create the initial message structure with default values
		// This follows the Claude API specification for streaming message initialization
		messageStartDoc := ojson.NewDocString(`{"type":"message_start","message":{"id":"msg_1nZdL29xx5MUA1yADyHTEsnR8uuvGzszyY","type":"message","role":"assistant","content":[],"model":"claude-3-5-sonnet-20241022","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}}`)

		// Override default values with actual response metadata if available
		if modelVersionResult := ojson.GetBytesNoCopy(rawJSON, "modelVersion"); modelVersionResult.Exists() {
			_ = messageStartDoc.Set("message.model", modelVersionResult.String())
		}
		if responseIDResult := ojson.GetBytesNoCopy(rawJSON, "responseId"); responseIDResult.Exists() {
			_ = messageStartDoc.Set("message.id", responseIDResult.String())
		}
		appendEvent("message_start", messageStartDoc.String())

		(*param).(*Params).HasFirstResponse = true
	}

	// Process the response parts array from the backend client
	// Each part can contain text content, thinking content, or function calls
	partsResult := ojson.GetBytesNoCopy(rawJSON, "candidates.0.content.parts")
	if partsResult.IsArray() {
		partResults := partsResult.Array()
		for i := 0; i < len(partResults); i++ {
			partResult := partResults[i]
			if geminicommon.HasUnsupportedPartPayload(partResult, "text", "functionCall") {
				if p.ActiveToolCall != nil {
					abortActiveToolCall()
				}
				if p.ResponseType != 0 {
					flushPendingToolInput()
					appendEvent("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, p.ResponseIndex))
					p.ResponseIndex++
					p.ResponseType = 0
				}
				p.LastSignatureTarget = geminiClaudeUnsupportedSignatureTarget
				p.CurrentThinkingHasText = false
				continue
			}

			// Extract the different types of content from each part
			partTextResult := partResult.Get("text")
			functionCallResult := partResult.Get("functionCall")
			thoughtSignatureResult := partResult.Get("thoughtSignature")
			if !thoughtSignatureResult.Exists() {
				thoughtSignatureResult = partResult.Get("thought_signature")
			}
			partSig := ""
			if thoughtSignatureResult.Exists() && thoughtSignatureResult.String() != "" {
				partSig = strings.Clone(thoughtSignatureResult.String())
			}
			hasThoughtSignature := partSig != ""
			isThought := partResult.Get("thought").Bool()

			if hasThoughtSignature && (!partTextResult.Exists() || partTextResult.String() == "") && !functionCallResult.Exists() {
				if p.ResponseType == 2 && p.CurrentThinkingHasText {
					appendSignatureDelta(partSig)
					continue
				}
				if p.ActiveToolCall != nil {
					p.PendingToolThoughtSignatures = append(p.PendingToolThoughtSignatures, partSig)
					continue
				}
				appendTrailingCarrierThinkingBlock(partSig)
				continue
			}

			if isThought {
				if hasThoughtSignature && (!partTextResult.Exists() || partTextResult.String() == "") {
					if p.ResponseType == 2 && p.CurrentThinkingHasText {
						appendSignatureDelta(partSig)
					} else if p.ActiveToolCall != nil {
						p.PendingToolThoughtSignatures = append(p.PendingToolThoughtSignatures, partSig)
					} else {
						appendCarrierThinkingBlock(partSig)
					}
					continue
				}
				if p.ActiveToolCall != nil {
					if !partTextResult.Exists() || partTextResult.String() == "" {
						continue
					}
					abortActiveToolCall()
				}

				partText := partTextResult.String()
				if (*param).(*Params).ResponseType == 2 {
					data, _ := ojson.SetBytes([]byte(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"thinking_delta","thinking":""}}`, (*param).(*Params).ResponseIndex)), "delta.thinking", partText)
					appendEvent("content_block_delta", string(data))
					(*param).(*Params).HasContent = true
				} else {
					if (*param).(*Params).ResponseType != 0 {
						flushPendingToolInput()
						appendEvent("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, (*param).(*Params).ResponseIndex))
						(*param).(*Params).ResponseIndex++
					}

					p.CurrentThinkingHasText = false
					appendEvent("content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"thinking","thinking":""}}`, (*param).(*Params).ResponseIndex))
					data, _ := ojson.SetBytes([]byte(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"thinking_delta","thinking":""}}`, (*param).(*Params).ResponseIndex)), "delta.thinking", partText)
					appendEvent("content_block_delta", string(data))
					(*param).(*Params).ResponseType = 2
					(*param).(*Params).HasContent = true
				}
				if partText != "" {
					p.CurrentThinkingHasText = true
					p.LastSignatureTarget = translatorcommon.GeminiClaudeCarrierText
				}
				if hasThoughtSignature {
					appendSignatureDelta(partSig)
				}
				continue
			}

			// From here on, !isThought (visible text, functionCall, or standalone signature)
			if functionCallResult.Exists() {
				rawToolName := strings.Clone(strings.TrimSpace(functionCallResult.Get("name").String()))
				upstreamToolName := strings.Clone(util.RestoreSanitizedToolName((*param).(*Params).SanitizedNameMap, rawToolName))
				clientToolName := strings.Clone(util.MapToolName((*param).(*Params).ToolNameMap, upstreamToolName))

				if active := p.ActiveToolCall; active != nil && active.CanContinueWith(functionCallResult) {
					p.SawToolCall = true
					delta, fcDone := active.Assembler.PushFunctionCall(functionCallResult)
					if active.Assembler.Err() != nil {
						abortActiveToolCall()
						continue
					}
					if p.CurrentToolIsParameterless {
						if fcDone && !p.CurrentToolInputEmitted {
							data, _ := translatorcommon.SetStringWithoutHTMLEscape([]byte(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":""}}`, active.Index)), "delta.partial_json", "{}")
							appendEvent("content_block_delta", string(data))
							p.CurrentToolInputEmitted = true
							p.CurrentToolPendingEmpty = false
						}
					} else if delta != "" {
						data, _ := translatorcommon.SetStringWithoutHTMLEscape([]byte(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":""}}`, active.Index)), "delta.partial_json", delta)
						appendEvent("content_block_delta", string(data))
						p.CurrentToolInputEmitted = true
						p.CurrentToolPendingEmpty = false
					}
					if hasThoughtSignature {
						p.PendingToolThoughtSignatures = append(p.PendingToolThoughtSignatures, partSig)
					}
					if fcDone {
						p.ActiveToolCall = nil
						flushPendingToolInput()
						appendEvent("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, active.Index))
						p.ResponseIndex = active.Index + 1
						p.ResponseType = 0
						if len(p.PendingToolThoughtSignatures) > 0 {
							sigs := p.PendingToolThoughtSignatures
							p.PendingToolThoughtSignatures = nil
							for _, sig := range sigs {
								appendCarrierThinkingBlock(translatorcommon.EncodeGeminiClaudeCarrierSignature(sig, translatorcommon.GeminiClaudeCarrierPrevious, translatorcommon.GeminiClaudeCarrierFunction))
							}
						}
					}
					continue
				}

				if translatorcommon.IsVertexStreamingFunctionCall(functionCallResult) {
					if p.ActiveToolCall != nil {
						abortActiveToolCall()
					}
					if rawToolName == "" {
						abortActiveToolCall()
						continue
					}
					upstreamID := strings.Clone(strings.TrimSpace(functionCallResult.Get("id").String()))
					toolID := util.SanitizeClaudeToolID(fmt.Sprintf("%s-%d", upstreamToolName, atomic.AddUint64(&toolUseIDCounter, 1)))
					active := translatorcommon.NewVertexStreamingToolCall(rawToolName, clientToolName, upstreamID, toolID, 0)
					if len(originalRequestRawJSON) > 0 {
						toolNameForClean := clientToolName
						origReq := originalRequestRawJSON
						transReq := requestRawJSON
						active.Assembler.SetArgumentCleaner(func(rawArgs string) string {
							return util.CleanClaudeToolArgumentsWithTranslated(rawArgs, toolNameForClean, origReq, transReq)
						})
					}
					delta, fcDone := active.Assembler.PushFunctionCall(functionCallResult)
					if active.Assembler.Err() != nil {
						p.HasIncompleteToolCall = true
						continue
					}

					p.SawToolCall = true
					if hasThoughtSignature {
						appendCarrierThinkingBlock(partSig)
					}
					if p.ResponseType != 0 {
						flushPendingToolInput()
						appendEvent("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, p.ResponseIndex))
						p.ResponseIndex++
						p.ResponseType = 0
					}

					active.Index = p.ResponseIndex
					p.CurrentToolName = clientToolName
					p.CurrentToolIsParameterless = util.ClaudeToolIsParameterless(clientToolName, originalRequestRawJSON)
					p.CurrentToolInputEmitted = false
					p.CurrentToolPendingEmpty = false
					dataDoc := ojson.NewDocString(fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"tool_use","id":"","name":"","input":{}}}`, p.ResponseIndex))
					_ = dataDoc.Set("content_block.id", toolID)
					_ = dataDoc.Set("content_block.name", clientToolName)
					appendEvent("content_block_start", dataDoc.String())
					p.ResponseType = 3
					p.HasContent = true
					p.LastSignatureTarget = translatorcommon.GeminiClaudeCarrierFunction

					if p.CurrentToolIsParameterless {
						if fcDone {
							deltaData, _ := translatorcommon.SetStringWithoutHTMLEscape([]byte(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":""}}`, active.Index)), "delta.partial_json", "{}")
							appendEvent("content_block_delta", string(deltaData))
							p.CurrentToolInputEmitted = true
						}
					} else if delta != "" {
						deltaData, _ := translatorcommon.SetStringWithoutHTMLEscape([]byte(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":""}}`, active.Index)), "delta.partial_json", delta)
						appendEvent("content_block_delta", string(deltaData))
						p.CurrentToolInputEmitted = true
					}
					if fcDone {
						flushPendingToolInput()
						appendEvent("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, active.Index))
						p.ResponseIndex = active.Index + 1
						p.ResponseType = 0
					} else {
						p.ActiveToolCall = active
					}
					continue
				}

				if p.ActiveToolCall != nil {
					abortActiveToolCall()
				}
				if (*param).(*Params).ResponseType == 3 && upstreamToolName == "" {
					if fcArgsResult := functionCallResult.Get("args"); fcArgsResult.Exists() {
						(*param).(*Params).SawToolCall = true
						partialJSON := util.CleanClaudeToolArgumentsWithTranslated(fcArgsResult.Raw, (*param).(*Params).CurrentToolName, originalRequestRawJSON, requestRawJSON)
						if (*param).(*Params).CurrentToolIsParameterless {
							if (*param).(*Params).CurrentToolInputEmitted {
								partialJSON = ""
							} else {
								(*param).(*Params).CurrentToolInputEmitted = true
								(*param).(*Params).CurrentToolPendingEmpty = false
							}
						} else if partialJSON == "{}" {
							if !(*param).(*Params).CurrentToolInputEmitted {
								(*param).(*Params).CurrentToolPendingEmpty = true
							}
							partialJSON = ""
						} else if partialJSON != "" {
							(*param).(*Params).CurrentToolInputEmitted = true
							(*param).(*Params).CurrentToolPendingEmpty = false
						}
						if partialJSON != "" || !(*param).(*Params).CurrentToolPendingEmpty {
							data, _ := ojson.SetBytes([]byte(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":""}}`, (*param).(*Params).ResponseIndex)), "delta.partial_json", partialJSON)
							appendEvent("content_block_delta", string(data))
						}
					}
					if hasThoughtSignature {
						appendTrailingCarrierThinkingBlock(partSig)
					}
					continue
				}
				if rawToolName == "" && !functionCallResult.Get("args").Exists() {
					if hasThoughtSignature {
						appendTrailingCarrierThinkingBlock(partSig)
					}
					continue
				}
				(*param).(*Params).SawToolCall = true

				if hasThoughtSignature {
					appendCarrierThinkingBlock(partSig)
				}

				if (*param).(*Params).ResponseType == 3 {
					flushPendingToolInput()
					appendEvent("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, (*param).(*Params).ResponseIndex))
					(*param).(*Params).ResponseIndex++
					(*param).(*Params).ResponseType = 0
				}
				if (*param).(*Params).ResponseType != 0 {
					flushPendingToolInput()
					appendEvent("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, (*param).(*Params).ResponseIndex))
					(*param).(*Params).ResponseIndex++
				}

				(*param).(*Params).CurrentToolName = clientToolName
				(*param).(*Params).CurrentToolIsParameterless = util.ClaudeToolIsParameterless(clientToolName, originalRequestRawJSON)
				(*param).(*Params).CurrentToolInputEmitted = false
				(*param).(*Params).CurrentToolPendingEmpty = false
				dataDoc := ojson.NewDocString(fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"tool_use","id":"","name":"","input":{}}}`, (*param).(*Params).ResponseIndex))
				_ = dataDoc.Set("content_block.id", util.SanitizeClaudeToolID(fmt.Sprintf("%s-%d", upstreamToolName, atomic.AddUint64(&toolUseIDCounter, 1))))
				_ = dataDoc.Set("content_block.name", clientToolName)
				appendEvent("content_block_start", dataDoc.String())

				if fcArgsResult := functionCallResult.Get("args"); fcArgsResult.Exists() {
					partialJSON := util.CleanClaudeToolArgumentsWithTranslated(fcArgsResult.Raw, clientToolName, originalRequestRawJSON, requestRawJSON)
					if (*param).(*Params).CurrentToolIsParameterless {
						if (*param).(*Params).CurrentToolInputEmitted {
							partialJSON = ""
						} else {
							(*param).(*Params).CurrentToolInputEmitted = true
						}
					} else if partialJSON == "{}" {
						(*param).(*Params).CurrentToolPendingEmpty = true
						partialJSON = ""
					} else if partialJSON != "" {
						(*param).(*Params).CurrentToolInputEmitted = true
					}
					if partialJSON != "" || !(*param).(*Params).CurrentToolPendingEmpty {
						data, _ := ojson.SetBytes([]byte(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":""}}`, (*param).(*Params).ResponseIndex)), "delta.partial_json", partialJSON)
						appendEvent("content_block_delta", string(data))
					}
				}
				(*param).(*Params).ResponseType = 3
				(*param).(*Params).HasContent = true
				p.LastSignatureTarget = translatorcommon.GeminiClaudeCarrierFunction
				continue
			}

			if partTextResult.Exists() {
				partText := partTextResult.String()
				if hasThoughtSignature && partText == "" {
					if p.ResponseType == 2 && p.CurrentThinkingHasText {
						appendSignatureDelta(partSig)
						continue
					}
					if p.ActiveToolCall != nil {
						p.PendingToolThoughtSignatures = append(p.PendingToolThoughtSignatures, partSig)
						continue
					}
					appendCarrierThinkingBlock(partSig)
					continue
				}
				if p.ActiveToolCall != nil {
					if partText == "" {
						continue
					}
					abortActiveToolCall()
				}
				if hasThoughtSignature {
					appendCarrierThinkingBlock(partSig)
				}
				if (*param).(*Params).ResponseType == 1 {
					data, _ := ojson.SetBytes([]byte(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"text_delta","text":""}}`, (*param).(*Params).ResponseIndex)), "delta.text", partText)
					appendEvent("content_block_delta", string(data))
					(*param).(*Params).HasContent = true
				} else {
					if (*param).(*Params).ResponseType != 0 {
						flushPendingToolInput()
						appendEvent("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, (*param).(*Params).ResponseIndex))
						(*param).(*Params).ResponseIndex++
					}
					appendEvent("content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"text","text":""}}`, (*param).(*Params).ResponseIndex))
					data, _ := ojson.SetBytes([]byte(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"text_delta","text":""}}`, (*param).(*Params).ResponseIndex)), "delta.text", partText)
					appendEvent("content_block_delta", string(data))
					(*param).(*Params).ResponseType = 1
					(*param).(*Params).HasContent = true
				}
				if partText != "" {
					p.LastSignatureTarget = translatorcommon.GeminiClaudeCarrierText
				}
				if partText != "" {
					// Preserve native part boundaries for signatures that may arrive later.
					appendEvent("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, p.ResponseIndex))
					p.ResponseIndex++
					p.ResponseType = 0
				}
				continue
			}

			if hasThoughtSignature {
				if p.ActiveToolCall != nil {
					p.PendingToolThoughtSignatures = append(p.PendingToolThoughtSignatures, partSig)
					continue
				}
				appendCarrierThinkingBlock(partSig)
				continue
			}
		}
	}

	if finish := ojson.GetBytesNoCopy(rawJSON, "candidates.0.finishReason"); finish.Exists() && finish.String() != "" {
		(*param).(*Params).FinishReason = strings.Clone(finish.String())
		flushPendingToolInput()
	}

	usageResult := ojson.GetBytesNoCopy(rawJSON, "usageMetadata")
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

	if isGeminiMalformedFunctionCall((*param).(*Params).FinishReason, (*param).(*Params).SawToolCall) && !(*param).(*Params).HasFinalEvents {
		if (*param).(*Params).ResponseType != 0 {
			flushPendingToolInput()
			appendEvent("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, (*param).(*Params).ResponseIndex))
			(*param).(*Params).ResponseType = 0
		}
		appendEvent("error", geminiMalformedFunctionCallErrorPayload)
		(*param).(*Params).HasFinalEvents = true
		return [][]byte{output}
	}

	if usageResult.Exists() && bytes.Contains(rawJSON, []byte(`"finishReason"`)) && !(*param).(*Params).HasFinalEvents && p.ActiveToolCall == nil && !p.HasIncompleteToolCall {
		if !(*param).(*Params).HasContent && (*param).(*Params).HasFirstResponse {
			appendEvent("content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"text","text":""}}`, (*param).(*Params).ResponseIndex))
			(*param).(*Params).ResponseType = 1
			(*param).(*Params).HasContent = true
		}

		if (*param).(*Params).HasContent {
			if (*param).(*Params).ResponseType != 0 {
				flushPendingToolInput()
				appendEvent("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, (*param).(*Params).ResponseIndex))
				(*param).(*Params).ResponseType = 0
			}

			stopReason := resolveGeminiClaudeStopReason((*param).(*Params).FinishReason, (*param).(*Params).SawToolCall)
			templateDoc := ojson.NewDocString(fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":"%s","stop_sequence":null},"usage":{"input_tokens":0,"output_tokens":0}}`, stopReason))
			_ = templateDoc.Set("usage.output_tokens", (*param).(*Params).OutputTokens)
			_ = templateDoc.Set("usage.input_tokens", (*param).(*Params).InputTokens)
			if (*param).(*Params).CachedTokens > 0 {
				_ = templateDoc.Set("usage.cache_read_input_tokens", (*param).(*Params).CachedTokens)
			}

			appendEvent("message_delta", templateDoc.String())
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

	root := util.ParseGJSONBytesNoCopy(rawJSON)
	toolNameMap := util.UnambiguousToolNameMapFromClaudeRequest(originalRequestRawJSON)
	sanitizedNameMap := util.SanitizedToolNameMap(originalRequestRawJSON)

	outDoc := ojson.NewDocString(`{"id":"","type":"message","role":"assistant","model":"","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}`)
	_ = outDoc.Set("id", root.Get("responseId").String())
	_ = outDoc.Set("model", root.Get("modelVersion").String())

	cachedTokens := root.Get("usageMetadata.cachedContentTokenCount").Int()
	inputTokens := root.Get("usageMetadata.promptTokenCount").Int() - cachedTokens
	if inputTokens < 0 {
		inputTokens = 0
	}
	outputTokens := root.Get("usageMetadata.candidatesTokenCount").Int() + root.Get("usageMetadata.thoughtsTokenCount").Int()
	_ = outDoc.Set("usage.input_tokens", inputTokens)
	_ = outDoc.Set("usage.output_tokens", outputTokens)
	if cachedTokens > 0 {
		_ = outDoc.Set("usage.cache_read_input_tokens", cachedTokens)
	}

	parts := root.Get("candidates.0.content.parts")
	textBuilder := strings.Builder{}
	thinkingBuilder := strings.Builder{}
	var thinkingSignature string
	hasToolCall := false
	lastSignatureTarget := ""
	var blocks [][]byte

	flushText := func() {
		if textBuilder.Len() == 0 {
			return
		}
		block := []byte(`{"type":"text","text":""}`)
		block, _ = ojson.SetBytes(block, "text", textBuilder.String())
		blocks = append(blocks, block)
		textBuilder.Reset()
	}

	flushThinking := func() {
		if thinkingBuilder.Len() == 0 && thinkingSignature == "" {
			return
		}
		blockDoc := ojson.NewDocString(`{"type":"thinking","thinking":""}`)
		_ = blockDoc.Set("thinking", thinkingBuilder.String())
		if thinkingSignature != "" {
			_ = blockDoc.Set("signature", thinkingSignature)
		}
		blocks = append(blocks, blockDoc.Bytes())
		thinkingBuilder.Reset()
		thinkingSignature = ""
	}

	appendCarrierThinkingBlock := func(signature string) {
		if signature == "" {
			return
		}
		carrier := []byte(`{"type":"thinking","thinking":"","signature":""}`)
		carrier, _ = ojson.SetBytes(carrier, "signature", signature)
		blocks = append(blocks, carrier)
	}

	if parts.IsArray() {
		for _, part := range parts.Array() {
			if geminicommon.HasUnsupportedPartPayload(part, "text", "functionCall") {
				flushThinking()
				flushText()
				lastSignatureTarget = geminiClaudeUnsupportedSignatureTarget
				continue
			}
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

			if isThought && (text.String() != "" || partSig == "") {
				flushText()
				if partSig != "" {
					thinkingSignature = partSig
				}
				if text.Exists() && text.String() != "" {
					thinkingBuilder.WriteString(text.String())
					lastSignatureTarget = translatorcommon.GeminiClaudeCarrierText
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
					if lastSignatureTarget == geminiClaudeUnsupportedSignatureTarget {
						continue
					}
					flushThinking()
					flushText()
					if lastSignatureTarget != "" {
						partSig = translatorcommon.EncodeGeminiClaudeCarrierSignature(partSig, translatorcommon.GeminiClaudeCarrierPrevious, lastSignatureTarget)
					}
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
				lastSignatureTarget = translatorcommon.GeminiClaudeCarrierFunction

				upstreamToolName := functionCall.Get("name").String()
				upstreamToolName = util.RestoreSanitizedToolName(sanitizedNameMap, upstreamToolName)
				clientToolName := util.MapToolName(toolNameMap, upstreamToolName)
				toolBlockDoc := ojson.NewDocString(`{"type":"tool_use","id":"","name":"","input":{}}`)
				_ = toolBlockDoc.Set("id", util.SanitizeClaudeToolID(fmt.Sprintf("%s-%d", upstreamToolName, atomic.AddUint64(&toolUseIDCounter, 1))))
				_ = toolBlockDoc.Set("name", clientToolName)
				inputRaw := "{}"
				if args := functionCall.Get("args"); args.Exists() && ojson.Valid(args.Raw) && args.IsObject() {
					inputRaw = util.CleanClaudeToolArgumentsWithTranslated(args.Raw, clientToolName, originalRequestRawJSON, requestRawJSON)
				}
				_ = toolBlockDoc.SetRaw("input", inputRaw)
				blocks = append(blocks, toolBlockDoc.Bytes())
				continue
			}

			if text.Exists() && text.String() != "" {
				if partSig != "" {
					flushText()
					appendCarrierThinkingBlock(partSig)
				}
				textBuilder.WriteString(text.String())
				lastSignatureTarget = translatorcommon.GeminiClaudeCarrierText
				flushText()
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
		_ = outDoc.SetRawBytes("content", translatorcommon.JoinRawArray(blocks))
	}

	var finishReason string
	if finish := root.Get("candidates.0.finishReason"); finish.Exists() {
		finishReason = finish.String()
	}
	if isGeminiMalformedFunctionCall(finishReason, hasToolCall) {
		return []byte(geminiMalformedFunctionCallErrorPayload)
	}
	_ = outDoc.Set("stop_reason", resolveGeminiClaudeStopReason(finishReason, hasToolCall))

	if inputTokens == int64(0) && outputTokens == int64(0) && !root.Get("usageMetadata").Exists() {
		_ = outDoc.Delete("usage")
	}

	return outDoc.Bytes()
}

func ClaudeTokenCount(ctx context.Context, count int64) []byte {
	return translatorcommon.ClaudeInputTokensJSON(count)
}
