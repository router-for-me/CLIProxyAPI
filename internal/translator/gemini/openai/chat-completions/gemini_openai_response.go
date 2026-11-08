// Package openai provides response translation functionality for Gemini to OpenAI API compatibility.
// This package handles the conversion of Gemini API responses into OpenAI Chat Completions-compatible
// JSON format, transforming streaming events and non-streaming responses into the format
// expected by OpenAI API clients. It supports both streaming and non-streaming modes,
// handling text content, tool calls, reasoning content, and usage metadata appropriately.
package chat_completions

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/MoeForge/moego/ojson"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	log "github.com/sirupsen/logrus"
)

// convertGeminiResponseToOpenAIChatParams holds parameters for response conversion.
type convertGeminiResponseToOpenAIChatParams struct {
	UnixTimestamp int64
	// FunctionIndex tracks tool call indices per candidate index to support multiple candidates.
	FunctionIndex            map[int]int
	SawToolCall              map[int]bool
	SawFinishReason          map[int]bool
	UpstreamFinishReason     map[int]string
	ModelVersion             string
	ResponseID               string
	PendingUsageMetadata     []byte
	SanitizedNameMap         map[string]string
	SanitizedNameMapResolved bool
	ActiveToolCall           map[int]*translatorcommon.VertexStreamingToolCall
	HasIncompleteToolCall    map[int]bool
}

// functionCallIDCounter provides a process-wide unique counter for function call identifiers.
var functionCallIDCounter uint64

// ConvertGeminiResponseToOpenAI translates a single chunk of a streaming response from the
// Gemini API format to the OpenAI Chat Completions streaming format.
// It processes various Gemini event types and transforms them into OpenAI-compatible JSON responses.
// The function handles text content, tool calls, reasoning content, and usage metadata, outputting
// responses that match the OpenAI API format. It supports incremental updates for streaming responses.
//
// Parameters:
//   - ctx: The context for the request, used for cancellation and timeout handling
//   - modelName: The name of the model being used for the response (unused in current implementation)
//   - rawJSON: The raw JSON response from the Gemini API
//   - param: A pointer to a parameter object for maintaining state between calls
//
// Returns:
//   - [][]byte: A slice of OpenAI-compatible JSON responses
func ConvertGeminiResponseToOpenAI(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	// Initialize parameters if nil.
	if *param == nil {
		*param = &convertGeminiResponseToOpenAIChatParams{
			UnixTimestamp:            0,
			FunctionIndex:            make(map[int]int),
			SawToolCall:              make(map[int]bool),
			SawFinishReason:          make(map[int]bool),
			UpstreamFinishReason:     make(map[int]string),
			SanitizedNameMap:         util.SanitizedToolNameMap(originalRequestRawJSON),
			SanitizedNameMapResolved: true,
		}
	}

	// Ensure the Map is initialized (handling cases where param might be reused from older context).
	p := (*param).(*convertGeminiResponseToOpenAIChatParams)
	if p.FunctionIndex == nil {
		p.FunctionIndex = make(map[int]int)
	}
	if p.SawToolCall == nil {
		p.SawToolCall = make(map[int]bool)
	}
	if p.SawFinishReason == nil {
		p.SawFinishReason = make(map[int]bool)
	}
	if p.UpstreamFinishReason == nil {
		p.UpstreamFinishReason = make(map[int]string)
	}
	if p.ActiveToolCall == nil {
		p.ActiveToolCall = make(map[int]*translatorcommon.VertexStreamingToolCall)
	}
	if p.HasIncompleteToolCall == nil {
		p.HasIncompleteToolCall = make(map[int]bool)
	}
	if !p.SanitizedNameMapResolved {
		if p.SanitizedNameMap == nil {
			p.SanitizedNameMap = util.SanitizedToolNameMap(originalRequestRawJSON)
		}
		p.SanitizedNameMapResolved = true
	}

	if bytes.HasPrefix(rawJSON, []byte("data:")) {
		rawJSON = bytes.TrimSpace(rawJSON[5:])
	}

	if bytes.Equal(rawJSON, []byte("[DONE]")) {
		var indices []int
		for idx, reason := range p.UpstreamFinishReason {
			if reason != "" && !p.SawFinishReason[idx] && p.ActiveToolCall[idx] == nil && !p.HasIncompleteToolCall[idx] {
				indices = append(indices, idx)
			}
		}
		if len(indices) == 0 {
			return [][]byte{}
		}
		sort.Ints(indices)
		var doneChunks [][]byte
		for _, idx := range indices {
			p.SawFinishReason[idx] = true
			finishReason, nativeFinishReason := resolveGeminiOpenAIFinishReason(p.UpstreamFinishReason[idx], p.SawToolCall[idx])
			templateDoc := ojson.NewDocString(`{"id":"","object":"chat.completion.chunk","created":0,"model":"model","choices":[{"index":0,"delta":{"role":null,"content":null,"reasoning_content":null,"tool_calls":null},"finish_reason":null,"native_finish_reason":null}]}`)
			_ = templateDoc.Set("created", p.UnixTimestamp)
			if p.ModelVersion != "" {
				_ = templateDoc.Set("model", p.ModelVersion)
			}
			if p.ResponseID != "" {
				_ = templateDoc.Set("id", p.ResponseID)
			}
			if len(p.PendingUsageMetadata) > 0 {
				applyGeminiOpenAIUsageMetadataDoc(templateDoc, util.ParseGJSONBytesNoCopy(p.PendingUsageMetadata))
			}
			_ = templateDoc.Set("choices.0.index", idx)
			_ = templateDoc.Set("choices.0.finish_reason", finishReason)
			_ = templateDoc.Set("choices.0.native_finish_reason", nativeFinishReason)
			doneChunks = append(doneChunks, templateDoc.Bytes())
		}
		return doneChunks
	}

	// Initialize the OpenAI SSE base template.
	// We use a base template and clone it for each candidate to support multiple candidates.
	baseDoc := ojson.NewDocString(`{"id":"","object":"chat.completion.chunk","created":12345,"model":"model","choices":[{"index":0,"delta":{"role":null,"content":null,"reasoning_content":null,"tool_calls":null},"finish_reason":null,"native_finish_reason":null}]}`)

	// Extract and set the model version.
	if modelVersionResult := ojson.GetBytesNoCopy(rawJSON, "modelVersion"); modelVersionResult.Exists() {
		p.ModelVersion = strings.Clone(modelVersionResult.String())
		_ = baseDoc.Set("model", p.ModelVersion)
	} else if p.ModelVersion != "" {
		_ = baseDoc.Set("model", p.ModelVersion)
	}

	// Extract and set the creation timestamp.
	if createTimeResult := ojson.GetBytesNoCopy(rawJSON, "createTime"); createTimeResult.Exists() {
		t, err := time.Parse(time.RFC3339Nano, createTimeResult.String())
		if err == nil {
			p.UnixTimestamp = t.Unix()
		}
		_ = baseDoc.Set("created", p.UnixTimestamp)
	} else {
		_ = baseDoc.Set("created", p.UnixTimestamp)
	}

	// Extract and set the response ID.
	if responseIDResult := ojson.GetBytesNoCopy(rawJSON, "responseId"); responseIDResult.Exists() {
		p.ResponseID = strings.Clone(responseIDResult.String())
		_ = baseDoc.Set("id", p.ResponseID)
	} else if p.ResponseID != "" {
		_ = baseDoc.Set("id", p.ResponseID)
	}

	// Extract and set usage metadata (token counts).
	// Usage is applied to the base template so it appears in the chunks.
	usageResult := ojson.GetBytesNoCopy(rawJSON, "usageMetadata")
	usageExists := usageResult.Exists()
	if usageExists {
		applyGeminiOpenAIUsageMetadataDoc(baseDoc, usageResult)
		p.PendingUsageMetadata = nil
	} else if pendingUsage := ojson.GetBytesNoCopy(rawJSON, "cpaUsageMetadata"); pendingUsage.Exists() {
		p.PendingUsageMetadata = append(p.PendingUsageMetadata[:0], pendingUsage.Raw...)
	}

	baseTemplate := baseDoc.Bytes()

	var responseStrings [][]byte
	candidates := ojson.GetBytesNoCopy(rawJSON, "candidates")

	// Iterate over all candidates to support candidate_count > 1.
	if candidates.IsArray() {
		candidates.ForEach(func(_, candidate ojson.Result) bool {
			// Create a Doc from baseTemplate for the current candidate.
			templateDoc := ojson.NewDoc(baseTemplate)

			// Set the specific index for this candidate.
			candidateIndex := int(candidate.Get("index").Int())
			_ = templateDoc.Set("choices.0.index", candidateIndex)

			if finishReasonResult := candidate.Get("finishReason"); finishReasonResult.Exists() {
				p.UpstreamFinishReason[candidateIndex] = strings.Clone(strings.ToUpper(finishReasonResult.String()))
			}

			partsResult := candidate.Get("content.parts")
			assistantRoleSet := false
			sawNoopFunctionCall := false
			toolCallsInitialized := false
			imagesCount := 0
			setAssistantRole := func() {
				if assistantRoleSet {
					return
				}
				_ = templateDoc.Set("choices.0.delta.role", "assistant")
				assistantRoleSet = true
			}
			ensureToolCallsArray := func() {
				if !toolCallsInitialized {
					_ = templateDoc.SetRaw("choices.0.delta.tool_calls", `[]`)
					toolCallsInitialized = true
				}
			}

			if partsResult.IsArray() {
				partResults := partsResult.Array()
				var textContent strings.Builder
				var reasoningContent strings.Builder
				hasTextContent := false
				hasReasoningContent := false
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

					// Speech-to-text models (gemini-3.5-transcribe) deliver the
					// transcript in an audioTranscription part instead of text.
					audioTranscriptionResult := partResult.Get("audioTranscription")
					if audioTranscriptionResult.Exists() && !partTextResult.Exists() {
						partTextResult = audioTranscriptionResult.Get("text")
					}

					hasThoughtSignature := thoughtSignatureResult.Exists() && thoughtSignatureResult.String() != ""
					hasTextPayload := partTextResult.Exists() && partTextResult.String() != ""
					hasContentPayload := hasTextPayload || functionCallResult.Exists() || inlineDataResult.Exists()

					// Skip pure thoughtSignature parts but keep any actual payload in the same part.
					if hasThoughtSignature && !hasContentPayload {
						if p.ActiveToolCall[candidateIndex] != nil {
							sawNoopFunctionCall = true
						}
						continue
					}

					if partTextResult.Exists() {
						text := partTextResult.String()
						if p.ActiveToolCall[candidateIndex] != nil {
							if text == "" {
								sawNoopFunctionCall = true
								continue
							}
							p.HasIncompleteToolCall[candidateIndex] = true
							p.ActiveToolCall[candidateIndex] = nil
						}
						setAssistantRole()
						// Handle text content, distinguishing between regular content and reasoning/thoughts.
						if partResult.Get("thought").Bool() {
							hasReasoningContent = true
							reasoningContent.WriteString(text)
						} else {
							hasTextContent = true
							textContent.WriteString(text)
						}
					} else if functionCallResult.Exists() {
						// Handle function call content.
						if active := p.ActiveToolCall[candidateIndex]; active != nil && active.CanContinueWith(functionCallResult) {
							p.SawToolCall[candidateIndex] = true
							delta, done := active.Assembler.PushFunctionCall(functionCallResult)
							if active.Assembler.Err() != nil {
								p.HasIncompleteToolCall[candidateIndex] = true
								p.ActiveToolCall[candidateIndex] = nil
								sawNoopFunctionCall = true
								continue
							}
							if done {
								p.ActiveToolCall[candidateIndex] = nil
							}
							if delta != "" {
								ensureToolCallsArray()
								fcDoc := ojson.NewDocString(`{"index":0,"function":{"arguments":""}}`)
								_ = fcDoc.Set("index", active.Index)
								_ = translatorcommon.SetStringWithoutHTMLEscapeDoc(fcDoc, "function.arguments", delta)
								setAssistantRole()
								_ = templateDoc.SetRawBytes("choices.0.delta.tool_calls.-1", fcDoc.Bytes())
							} else {
								sawNoopFunctionCall = true
							}
						} else if translatorcommon.IsVertexStreamingFunctionCall(functionCallResult) {
							if p.ActiveToolCall[candidateIndex] != nil {
								p.HasIncompleteToolCall[candidateIndex] = true
								p.ActiveToolCall[candidateIndex] = nil
							}
							rawName := strings.Clone(strings.TrimSpace(functionCallResult.Get("name").String()))
							if rawName == "" {
								p.HasIncompleteToolCall[candidateIndex] = true
								sawNoopFunctionCall = true
								continue
							}
							fcName := strings.Clone(util.RestoreSanitizedToolName(p.SanitizedNameMap, rawName))
							upstreamID := strings.Clone(strings.TrimSpace(functionCallResult.Get("id").String()))
							fcID := upstreamID
							if fcID == "" {
								fcID = fmt.Sprintf("%s-%d-%d", fcName, time.Now().UnixNano(), atomic.AddUint64(&functionCallIDCounter, 1))
							}
							functionCallIndex := p.FunctionIndex[candidateIndex]
							active := translatorcommon.NewVertexStreamingToolCall(rawName, fcName, upstreamID, fcID, functionCallIndex)
							delta, done := active.Assembler.PushFunctionCall(functionCallResult)
							if active.Assembler.Err() != nil {
								p.HasIncompleteToolCall[candidateIndex] = true
								sawNoopFunctionCall = true
								continue
							}
							p.SawToolCall[candidateIndex] = true
							p.FunctionIndex[candidateIndex]++
							if !done {
								p.ActiveToolCall[candidateIndex] = active
							}

							ensureToolCallsArray()

							fcDoc := ojson.NewDocString(`{"id":"","index":0,"type":"function","function":{"name":"","arguments":""}}`)
							_ = fcDoc.Set("id", fcID)
							_ = fcDoc.Set("index", functionCallIndex)
							_ = fcDoc.Set("function.name", fcName)
							_ = translatorcommon.SetStringWithoutHTMLEscapeDoc(fcDoc, "function.arguments", delta)
							setAssistantRole()
							_ = templateDoc.SetRawBytes("choices.0.delta.tool_calls.-1", fcDoc.Bytes())
						} else {
							if p.ActiveToolCall[candidateIndex] != nil {
								p.HasIncompleteToolCall[candidateIndex] = true
								p.ActiveToolCall[candidateIndex] = nil
							}
							rawName := strings.TrimSpace(functionCallResult.Get("name").String())
							if rawName == "" && !functionCallResult.Get("args").Exists() {
								sawNoopFunctionCall = true
								continue
							}
							p.SawToolCall[candidateIndex] = true

							// Retrieve the function index for this specific candidate.
							functionCallIndex := p.FunctionIndex[candidateIndex]
							p.FunctionIndex[candidateIndex]++

							ensureToolCallsArray()

							fcDoc := ojson.NewDocString(`{"id":"","index":0,"type":"function","function":{"name":"","arguments":""}}`)
							fcName := util.RestoreSanitizedToolName(p.SanitizedNameMap, rawName)
							fcID := strings.TrimSpace(functionCallResult.Get("id").String())
							if fcID == "" {
								fcID = fmt.Sprintf("%s-%d-%d", fcName, time.Now().UnixNano(), atomic.AddUint64(&functionCallIDCounter, 1))
							}
							_ = fcDoc.Set("id", fcID)
							_ = fcDoc.Set("index", functionCallIndex)
							_ = fcDoc.Set("function.name", fcName)
							if fcArgsResult := functionCallResult.Get("args"); fcArgsResult.Exists() {
								_ = fcDoc.Set("function.arguments", fcArgsResult.Raw)
							}
							setAssistantRole()
							_ = templateDoc.SetRawBytes("choices.0.delta.tool_calls.-1", fcDoc.Bytes())
						}
					} else if inlineDataResult.Exists() {
						if p.ActiveToolCall[candidateIndex] != nil {
							p.HasIncompleteToolCall[candidateIndex] = true
							p.ActiveToolCall[candidateIndex] = nil
						}
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
						if imagesCount == 0 {
							_ = templateDoc.SetRaw("choices.0.delta.images", `[]`)
						}
						imageIndex := imagesCount
						imagesCount++
						imageDoc := ojson.NewDocString(`{"type":"image_url","image_url":{"url":""}}`)
						_ = imageDoc.Set("index", imageIndex)
						_ = imageDoc.Set("image_url.url", imageURL)
						setAssistantRole()
						_ = templateDoc.SetRawBytes("choices.0.delta.images.-1", imageDoc.Bytes())
					}
				}
				if hasTextContent {
					_ = templateDoc.Set("choices.0.delta.content", textContent.String())
				}
				if hasReasoningContent {
					_ = templateDoc.Set("choices.0.delta.reasoning_content", reasoningContent.String())
				}
			}

			upstreamFinishReason := p.UpstreamFinishReason[candidateIndex]
			sawToolCall := p.SawToolCall[candidateIndex]
			isFinalChunk := upstreamFinishReason != "" && usageExists && p.ActiveToolCall[candidateIndex] == nil && !p.HasIncompleteToolCall[candidateIndex]

			if isFinalChunk {
				p.SawFinishReason[candidateIndex] = true
				finishReason, nativeFinishReason := resolveGeminiOpenAIFinishReason(upstreamFinishReason, sawToolCall)
				_ = templateDoc.Set("choices.0.finish_reason", finishReason)
				_ = templateDoc.Set("choices.0.native_finish_reason", nativeFinishReason)
			}

			if sawNoopFunctionCall && !assistantRoleSet && !isFinalChunk && !usageExists {
				return true
			}

			responseStrings = append(responseStrings, templateDoc.Bytes())
			return true // continue loop
		})
	} else {
		// If there are no candidates (e.g., a pure usageMetadata chunk), return the usage chunk if present.
		if usageExists && len(responseStrings) == 0 {
			if upstreamFinishReason := p.UpstreamFinishReason[0]; upstreamFinishReason != "" && !p.SawFinishReason[0] && p.ActiveToolCall[0] == nil && !p.HasIncompleteToolCall[0] {
				p.SawFinishReason[0] = true
				finishReason, nativeFinishReason := resolveGeminiOpenAIFinishReason(upstreamFinishReason, p.SawToolCall[0])
				chunkDoc := ojson.NewDoc(baseTemplate)
				_ = chunkDoc.Set("choices.0.finish_reason", finishReason)
				_ = chunkDoc.Set("choices.0.native_finish_reason", nativeFinishReason)
				responseStrings = append(responseStrings, chunkDoc.Bytes())
			} else {
				chunk := append([]byte(nil), baseTemplate...)
				responseStrings = append(responseStrings, chunk)
			}
		}
	}

	return responseStrings
}

// ConvertGeminiResponseToOpenAINonStream converts a non-streaming Gemini response to a non-streaming OpenAI response.
// This function processes the complete Gemini response and transforms it into a single OpenAI-compatible
// JSON response. It handles message content, tool calls, reasoning content, and usage metadata, combining all
// the information into a single response that matches the OpenAI API format.
//
// Parameters:
//   - ctx: The context for the request, used for cancellation and timeout handling
//   - modelName: The name of the model being used for the response (unused in current implementation)
//   - rawJSON: The raw JSON response from the Gemini API
//   - param: A pointer to a parameter object for the conversion (unused in current implementation)
//
// Returns:
//   - []byte: An OpenAI-compatible JSON response containing all message content and metadata
func ConvertGeminiResponseToOpenAINonStream(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) []byte {
	sanitizedNameMap := util.SanitizedToolNameMap(originalRequestRawJSON)
	var unixTimestamp int64
	// Initialize template with an empty choices array to support multiple candidates.
	templateDoc := ojson.NewDocString(`{"id":"","object":"chat.completion","created":123456,"model":"model","choices":[]}`)

	if modelVersionResult := ojson.GetBytesNoCopy(rawJSON, "modelVersion"); modelVersionResult.Exists() {
		_ = templateDoc.Set("model", modelVersionResult.String())
	}

	if createTimeResult := ojson.GetBytesNoCopy(rawJSON, "createTime"); createTimeResult.Exists() {
		t, err := time.Parse(time.RFC3339Nano, createTimeResult.String())
		if err == nil {
			unixTimestamp = t.Unix()
		}
		_ = templateDoc.Set("created", unixTimestamp)
	} else {
		_ = templateDoc.Set("created", unixTimestamp)
	}

	if responseIDResult := ojson.GetBytesNoCopy(rawJSON, "responseId"); responseIDResult.Exists() {
		_ = templateDoc.Set("id", responseIDResult.String())
	}

	if usageResult := ojson.GetBytesNoCopy(rawJSON, "usageMetadata"); usageResult.Exists() {
		_ = templateDoc.Set("usage.completion_tokens", usageResult.Get("candidatesTokenCount").Int()+usageResult.Get("thoughtsTokenCount").Int())
		if totalTokenCountResult := usageResult.Get("totalTokenCount"); totalTokenCountResult.Exists() {
			_ = templateDoc.Set("usage.total_tokens", totalTokenCountResult.Int())
		}
		promptTokenCount := usageResult.Get("promptTokenCount").Int()
		thoughtsTokenCount := usageResult.Get("thoughtsTokenCount").Int()
		cachedTokenCount := usageResult.Get("cachedContentTokenCount").Int()
		_ = templateDoc.Set("usage.prompt_tokens", promptTokenCount)
		if thoughtsTokenCount > 0 {
			_ = templateDoc.Set("usage.completion_tokens_details.reasoning_tokens", thoughtsTokenCount)
		}
		// Include cached token count if present (indicates prompt caching is working)
		if cachedTokenCount > 0 {
			if err := templateDoc.Set("usage.prompt_tokens_details.cached_tokens", cachedTokenCount); err != nil {
				log.Warnf("gemini openai response: failed to set cached_tokens in non-streaming: %v", err)
			}
		}
	}

	// Process the main content part of the response for all candidates.
	candidates := ojson.GetBytesNoCopy(rawJSON, "candidates")
	if candidates.IsArray() {
		var choicesList [][]byte
		candidates.ForEach(func(_, candidate ojson.Result) bool {
			// Construct a single Choice object.
			choiceDoc := ojson.NewDocString(`{"index":0,"message":{"role":"assistant","content":null,"reasoning_content":null,"tool_calls":null},"finish_reason":null,"native_finish_reason":null}`)

			// Set the index for this choice.
			_ = choiceDoc.Set("index", candidate.Get("index").Int())

			// Set finish reason.
			if finishReasonResult := candidate.Get("finishReason"); finishReasonResult.Exists() {
				finishReason := strings.ToLower(finishReasonResult.String())
				if isGeminiContentFilterFinishReason(finishReasonResult.String()) {
					finishReason = "content_filter"
				}
				_ = choiceDoc.Set("finish_reason", finishReason)
				_ = choiceDoc.Set("native_finish_reason", strings.ToLower(finishReasonResult.String()))
			}

			partsResult := candidate.Get("content.parts")
			hasFunctionCall := false
			if partsResult.IsArray() {
				partsResults := partsResult.Array()
				var toolCalls [][]byte
				var images [][]byte
				var textContent strings.Builder
				var reasoningContent strings.Builder
				hasTextContent := false
				hasReasoningContent := false

				for i := 0; i < len(partsResults); i++ {
					partResult := partsResults[i]
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

					// Speech-to-text models (gemini-3.5-transcribe) deliver the
					// transcript in an audioTranscription part instead of text.
					audioTranscriptionResult := partResult.Get("audioTranscription")
					if audioTranscriptionResult.Exists() && !partTextResult.Exists() {
						partTextResult = audioTranscriptionResult.Get("text")
					}

					hasThoughtSignature := thoughtSignatureResult.Exists() && thoughtSignatureResult.String() != ""
					hasTextPayload := partTextResult.Exists() && partTextResult.String() != ""
					hasContentPayload := hasTextPayload || functionCallResult.Exists() || inlineDataResult.Exists()

					// Skip pure thoughtSignature parts but keep any actual payload in the same part.
					if hasThoughtSignature && !hasContentPayload {
						continue
					}

					if partTextResult.Exists() {
						// Append text content, distinguishing between regular content and reasoning.
						if partResult.Get("thought").Bool() {
							hasReasoningContent = true
							reasoningContent.WriteString(partTextResult.String())
						} else {
							hasTextContent = true
							textContent.WriteString(partTextResult.String())
						}
					} else if functionCallResult.Exists() {
						// Append function call content to the tool_calls array.
						hasFunctionCall = true
						fcItemDoc := ojson.NewDocString(`{"id":"","type":"function","function":{"name":"","arguments":""}}`)
						fcName := util.RestoreSanitizedToolName(sanitizedNameMap, functionCallResult.Get("name").String())
						fcID := strings.TrimSpace(functionCallResult.Get("id").String())
						if fcID == "" {
							fcID = fmt.Sprintf("%s-%d-%d", fcName, time.Now().UnixNano(), atomic.AddUint64(&functionCallIDCounter, 1))
						}
						_ = fcItemDoc.Set("id", fcID)
						_ = fcItemDoc.Set("function.name", fcName)
						if fcArgsResult := functionCallResult.Get("args"); fcArgsResult.Exists() {
							_ = fcItemDoc.Set("function.arguments", fcArgsResult.Raw)
						}
						toolCalls = append(toolCalls, fcItemDoc.Bytes())
					} else if inlineDataResult.Exists() {
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
							imageDoc := ojson.NewDocString(`{"type":"image_url","image_url":{"url":""}}`)
							_ = imageDoc.Set("index", len(images))
							_ = imageDoc.Set("image_url.url", imageURL)
							images = append(images, imageDoc.Bytes())
						}
					}
				}

				if hasTextContent {
					_ = choiceDoc.Set("message.content", textContent.String())
				}
				if hasReasoningContent {
					_ = choiceDoc.Set("message.reasoning_content", reasoningContent.String())
				}
				if len(toolCalls) > 0 {
					_ = choiceDoc.SetRawBytes("message.tool_calls", translatorcommon.JoinRawArray(toolCalls))
				}
				if len(images) > 0 {
					_ = choiceDoc.SetRawBytes("message.images", translatorcommon.JoinRawArray(images))
				}
			}

			if hasFunctionCall {
				_ = choiceDoc.Set("finish_reason", "tool_calls")
				_ = choiceDoc.Set("native_finish_reason", "tool_calls")
			}

			// Append the constructed choice to the main choices array.
			choicesList = append(choicesList, choiceDoc.Bytes())
			return true
		})
		if len(choicesList) > 0 {
			translatorcommon.SetRawArrayItemsDoc(templateDoc, "choices", choicesList)
		}
	}

	return templateDoc.Bytes()
}

func resolveGeminiOpenAIFinishReason(upstreamFinishReason string, sawToolCall bool) (finishReason, nativeFinishReason string) {
	switch {
	case sawToolCall:
		finishReason = "tool_calls"
	case upstreamFinishReason == "MAX_TOKENS":
		finishReason = "max_tokens"
	case isGeminiContentFilterFinishReason(upstreamFinishReason):
		finishReason = "content_filter"
	default:
		finishReason = "stop"
	}
	nativeFinishReason = "stop"
	if upstreamFinishReason != "" {
		nativeFinishReason = strings.ToLower(upstreamFinishReason)
	}
	return finishReason, nativeFinishReason
}

func applyGeminiOpenAIUsageMetadataDoc(templateDoc *ojson.Doc, usageResult ojson.Result) {
	cachedTokenCount := usageResult.Get("cachedContentTokenCount").Int()
	_ = templateDoc.Set("usage.completion_tokens", usageResult.Get("candidatesTokenCount").Int()+usageResult.Get("thoughtsTokenCount").Int())
	if totalTokenCountResult := usageResult.Get("totalTokenCount"); totalTokenCountResult.Exists() {
		_ = templateDoc.Set("usage.total_tokens", totalTokenCountResult.Int())
	}
	promptTokenCount := usageResult.Get("promptTokenCount").Int()
	thoughtsTokenCount := usageResult.Get("thoughtsTokenCount").Int()
	_ = templateDoc.Set("usage.prompt_tokens", promptTokenCount)
	if thoughtsTokenCount > 0 {
		_ = templateDoc.Set("usage.completion_tokens_details.reasoning_tokens", thoughtsTokenCount)
	}
	if cachedTokenCount > 0 {
		if err := templateDoc.Set("usage.prompt_tokens_details.cached_tokens", cachedTokenCount); err != nil {
			log.Warnf("gemini openai response: failed to set cached_tokens in streaming: %v", err)
		}
	}
}

func isGeminiContentFilterFinishReason(reason string) bool {
	switch strings.ToUpper(strings.TrimSpace(reason)) {
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "IMAGE_SAFETY", "MALFORMED_RESPONSE", "MALFORMED_FUNCTION_CALL":
		return true
	default:
		return false
	}
}
