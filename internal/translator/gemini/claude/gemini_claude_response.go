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
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

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
//   - []string: A slice of strings, each containing a Claude-compatible JSON response.
func ConvertGeminiResponseToClaude(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []string {
	if *param == nil {
		*param = &Params{
			IsGlAPIKey:       false,
			HasFirstResponse: false,
			ResponseType:     0,
			ResponseIndex:    0,
		}
	}

	if bytes.Equal(rawJSON, []byte("[DONE]")) {
		// Only send message_stop if we have actually output content
		if (*param).(*Params).HasContent {
			return []string{
				"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n\n",
			}
		}
		return []string{}
	}

	// Track whether tools are being used in this response chunk
	usedTool := false
	output := ""

	// Initialize the streaming session with a message_start event
	// This is only sent for the very first response chunk
	if !(*param).(*Params).HasFirstResponse {
		output = "event: message_start\n"

		// Create the initial message structure with default values
		// This follows the Claude API specification for streaming message initialization
		messageStartTemplate := `{"type": "message_start", "message": {"id": "msg_1nZdL29xx5MUA1yADyHTEsnR8uuvGzszyY", "type": "message", "role": "assistant", "content": [], "model": "claude-3-5-sonnet-20241022", "stop_reason": null, "stop_sequence": null, "usage": {"input_tokens": 0, "output_tokens": 0}}}`

		// Override default values with actual response metadata if available
		if modelVersionResult := gjson.GetBytes(rawJSON, "modelVersion"); modelVersionResult.Exists() {
			messageStartTemplate, _ = sjson.Set(messageStartTemplate, "message.model", modelVersionResult.String())
		}
		if responseIDResult := gjson.GetBytes(rawJSON, "responseId"); responseIDResult.Exists() {
			messageStartTemplate, _ = sjson.Set(messageStartTemplate, "message.id", responseIDResult.String())
		}
		output = output + fmt.Sprintf("data: %s\n\n\n", messageStartTemplate)

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

			// Handle text content (both regular content and thinking)
			if partTextResult.Exists() {
				// Process thinking content (internal reasoning)
				if partResult.Get("thought").Bool() {
					// Continue existing thinking block
					if (*param).(*Params).ResponseType == 2 {
						output = output + "event: content_block_delta\n"
						data, _ := sjson.Set(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"thinking_delta","thinking":""}}`, (*param).(*Params).ResponseIndex), "delta.thinking", partTextResult.String())
						output = output + fmt.Sprintf("data: %s\n\n\n", data)
						(*param).(*Params).HasContent = true
					} else {
						// Transition from another state to thinking
						// First, close any existing content block
						if (*param).(*Params).ResponseType != 0 {
							if (*param).(*Params).ResponseType == 2 {
								// output = output + "event: content_block_delta\n"
								// output = output + fmt.Sprintf(`data: {"type":"content_block_delta","index":%d,"delta":{"type":"signature_delta","signature":null}}`, (*param).(*Params).ResponseIndex)
								// output = output + "\n\n\n"
							}
							output = output + "event: content_block_stop\n"
							output = output + fmt.Sprintf(`data: {"type":"content_block_stop","index":%d}`, (*param).(*Params).ResponseIndex)
							output = output + "\n\n\n"
							(*param).(*Params).ResponseIndex++
						}

						// Start a new thinking content block
						output = output + "event: content_block_start\n"
						output = output + fmt.Sprintf(`data: {"type":"content_block_start","index":%d,"content_block":{"type":"thinking","thinking":""}}`, (*param).(*Params).ResponseIndex)
						output = output + "\n\n\n"
						output = output + "event: content_block_delta\n"
						data, _ := sjson.Set(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"thinking_delta","thinking":""}}`, (*param).(*Params).ResponseIndex), "delta.thinking", partTextResult.String())
						output = output + fmt.Sprintf("data: %s\n\n\n", data)
						(*param).(*Params).ResponseType = 2 // Set state to thinking
						(*param).(*Params).HasContent = true
					}
				} else {
					// Process regular text content (user-visible output)
					// Continue existing text block
					if (*param).(*Params).ResponseType == 1 {
						output = output + "event: content_block_delta\n"
						data, _ := sjson.Set(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"text_delta","text":""}}`, (*param).(*Params).ResponseIndex), "delta.text", partTextResult.String())
						output = output + fmt.Sprintf("data: %s\n\n\n", data)
						(*param).(*Params).HasContent = true
					} else {
						// Transition from another state to text content
						// First, close any existing content block
						if (*param).(*Params).ResponseType != 0 {
							if (*param).(*Params).ResponseType == 2 {
								// output = output + "event: content_block_delta\n"
								// output = output + fmt.Sprintf(`data: {"type":"content_block_delta","index":%d,"delta":{"type":"signature_delta","signature":null}}`, (*param).(*Params).ResponseIndex)
								// output = output + "\n\n\n"
							}
							output = output + "event: content_block_stop\n"
							output = output + fmt.Sprintf(`data: {"type":"content_block_stop","index":%d}`, (*param).(*Params).ResponseIndex)
							output = output + "\n\n\n"
							(*param).(*Params).ResponseIndex++
						}

						// Start a new text content block
						output = output + "event: content_block_start\n"
						output = output + fmt.Sprintf(`data: {"type":"content_block_start","index":%d,"content_block":{"type":"text","text":""}}`, (*param).(*Params).ResponseIndex)
						output = output + "\n\n\n"
						output = output + "event: content_block_delta\n"
						data, _ := sjson.Set(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"text_delta","text":""}}`, (*param).(*Params).ResponseIndex), "delta.text", partTextResult.String())
						output = output + fmt.Sprintf("data: %s\n\n\n", data)
						(*param).(*Params).ResponseType = 1 // Set state to content
						(*param).(*Params).HasContent = true
					}
				}
			} else if functionCallResult.Exists() {
				// Handle function/tool calls from the AI model
				// This processes tool usage requests and formats them for Claude API compatibility
				usedTool = true
				fcName := functionCallResult.Get("name").String()

				// Handle state transitions when switching to function calls
				// Close any existing function call block first
				if (*param).(*Params).ResponseType == 3 {
					output = output + "event: content_block_stop\n"
					output = output + fmt.Sprintf(`data: {"type":"content_block_stop","index":%d}`, (*param).(*Params).ResponseIndex)
					output = output + "\n\n\n"
					(*param).(*Params).ResponseIndex++
					(*param).(*Params).ResponseType = 0
				}

				// Special handling for thinking state transition
				if (*param).(*Params).ResponseType == 2 {
					// output = output + "event: content_block_delta\n"
					// output = output + fmt.Sprintf(`data: {"type":"content_block_delta","index":%d,"delta":{"type":"signature_delta","signature":null}}`, (*param).(*Params).ResponseIndex)
					// output = output + "\n\n\n"
				}

				// Close any other existing content block
				if (*param).(*Params).ResponseType != 0 {
					output = output + "event: content_block_stop\n"
					output = output + fmt.Sprintf(`data: {"type":"content_block_stop","index":%d}`, (*param).(*Params).ResponseIndex)
					output = output + "\n\n\n"
					(*param).(*Params).ResponseIndex++
				}

				// Start a new tool use content block
				// This creates the structure for a function call in Claude format
				output = output + "event: content_block_start\n"

				// Create the tool use block with unique ID and function details
				data := fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"tool_use","id":"","name":"","input":{}}}`, (*param).(*Params).ResponseIndex)
				data, _ = sjson.Set(data, "content_block.id", fmt.Sprintf("%s-%d-%d", fcName, time.Now().UnixNano(), atomic.AddUint64(&toolUseIDCounter, 1)))
				data, _ = sjson.Set(data, "content_block.name", fcName)
				output = output + fmt.Sprintf("data: %s\n\n\n", data)

				if fcArgsResult := functionCallResult.Get("args"); fcArgsResult.Exists() {
					output = output + "event: content_block_delta\n"
					data, _ = sjson.Set(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":""}}`, (*param).(*Params).ResponseIndex), "delta.partial_json", fcArgsResult.Raw)
					output = output + fmt.Sprintf("data: %s\n\n\n", data)
				}
				(*param).(*Params).ResponseType = 3
				(*param).(*Params).HasContent = true
			}
		}
	}

	usageResult := gjson.GetBytes(rawJSON, "usageMetadata")
	if usageResult.Exists() && bytes.Contains(rawJSON, []byte(`"finishReason"`)) {
		if candidatesTokenCountResult := usageResult.Get("candidatesTokenCount"); candidatesTokenCountResult.Exists() {
			// Only send final events if we have actually output content
			if (*param).(*Params).HasContent {
				output = output + "event: content_block_stop\n"
				output = output + fmt.Sprintf(`data: {"type":"content_block_stop","index":%d}`, (*param).(*Params).ResponseIndex)
				output = output + "\n\n\n"

				output = output + "event: message_delta\n"
				output = output + `data: `

				template := `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":0,"output_tokens":0}}`
				if usedTool {
					template = `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"input_tokens":0,"output_tokens":0}}`
				}

				thoughtsTokenCount := usageResult.Get("thoughtsTokenCount").Int()
				template, _ = sjson.Set(template, "usage.output_tokens", candidatesTokenCountResult.Int()+thoughtsTokenCount)
				template, _ = sjson.Set(template, "usage.input_tokens", usageResult.Get("promptTokenCount").Int())

				output = output + template + "\n\n\n"
			}
		}
	}

	return []string{output}
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
//   - string: A Claude-compatible JSON response.
func ConvertGeminiResponseToClaudeNonStream(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) string {
	_ = originalRequestRawJSON
	_ = requestRawJSON

	root := gjson.ParseBytes(rawJSON)

	response := map[string]interface{}{
		"id":            root.Get("responseId").String(),
		"type":          "message",
		"role":          "assistant",
		"model":         root.Get("modelVersion").String(),
		"content":       []interface{}{},
		"stop_reason":   nil,
		"stop_sequence": nil,
		"usage": map[string]interface{}{
			"input_tokens":  root.Get("usageMetadata.promptTokenCount").Int(),
			"output_tokens": root.Get("usageMetadata.candidatesTokenCount").Int() + root.Get("usageMetadata.thoughtsTokenCount").Int(),
		},
	}

	parts := root.Get("candidates.0.content.parts")
	var contentBlocks []interface{}
	textBuilder := strings.Builder{}
	thinkingBuilder := strings.Builder{}
	toolIDCounter := 0
	hasToolCall := false

	flushText := func() {
		if textBuilder.Len() == 0 {
			return
		}
		contentBlocks = append(contentBlocks, map[string]interface{}{
			"type": "text",
			"text": textBuilder.String(),
		})
		textBuilder.Reset()
	}

	flushThinking := func() {
		if thinkingBuilder.Len() == 0 {
			return
		}
		contentBlocks = append(contentBlocks, map[string]interface{}{
			"type":     "thinking",
			"thinking": thinkingBuilder.String(),
		})
		thinkingBuilder.Reset()
	}

	if parts.IsArray() {
		for _, part := range parts.Array() {
			if text := part.Get("text"); text.Exists() && text.String() != "" {
				if part.Get("thought").Bool() {
					flushText()
					thinkingBuilder.WriteString(text.String())
					continue
				}
				flushThinking()
				textBuilder.WriteString(text.String())
				continue
			}

			if functionCall := part.Get("functionCall"); functionCall.Exists() {
				flushThinking()
				flushText()
				hasToolCall = true

				name := functionCall.Get("name").String()
				toolIDCounter++
				toolBlock := map[string]interface{}{
					"type":  "tool_use",
					"id":    fmt.Sprintf("tool_%d", toolIDCounter),
					"name":  name,
					"input": map[string]interface{}{},
				}

				if args := functionCall.Get("args"); args.Exists() {
					var parsed interface{}
					if err := json.Unmarshal([]byte(args.Raw), &parsed); err == nil {
						toolBlock["input"] = parsed
					}
				}

				contentBlocks = append(contentBlocks, toolBlock)
				continue
			}
		}
	}

	flushThinking()
	flushText()

	response["content"] = contentBlocks

	stopReason := "end_turn"
	if hasToolCall {
		stopReason = "tool_use"
	} else {
		if finish := root.Get("candidates.0.finishReason"); finish.Exists() {
			switch finish.String() {
			case "MAX_TOKENS":
				stopReason = "max_tokens"
			case "STOP", "FINISH_REASON_UNSPECIFIED", "UNKNOWN":
				stopReason = "end_turn"
			default:
				stopReason = "end_turn"
			}
		}
	}
	response["stop_reason"] = stopReason

	if usage := response["usage"].(map[string]interface{}); usage["input_tokens"] == int64(0) && usage["output_tokens"] == int64(0) {
		if usageMeta := root.Get("usageMetadata"); !usageMeta.Exists() {
			delete(response, "usage")
		}
	}

	encoded, err := json.Marshal(response)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func ClaudeTokenCount(ctx context.Context, count int64) string {
	return fmt.Sprintf(`{"input_tokens":%d}`, count)
}
