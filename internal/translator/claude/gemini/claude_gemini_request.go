// Package gemini provides request translation functionality for Gemini to Claude Code API compatibility.
// It handles parsing and transforming Gemini API requests into Claude Code API format,
// extracting model information, system instructions, message contents, and tool declarations.
// The package performs JSON data transformation to ensure compatibility
// between Gemini API format and Claude Code API's expected format.
package gemini

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var (
	user    = ""
	account = ""
	session = ""
)

// ConvertGeminiRequestToClaude parses and transforms a Gemini API request into Claude Code API format.
// It extracts the model name, system instruction, message contents, and tool declarations
// from the raw JSON request and returns them in the format expected by the Claude Code API.
// The function performs comprehensive transformation including:
// 1. Model name mapping and generation configuration extraction
// 2. System instruction conversion to Claude Code format
// 3. Message content conversion with proper role mapping
// 4. Tool call and tool result handling with FIFO queue for ID matching
// 5. Image and file data conversion to Claude Code base64 format
// 6. Tool declaration and tool choice configuration mapping
//
// Parameters:
//   - modelName: The name of the model to use for the request
//   - rawJSON: The raw JSON request data from the Gemini API
//   - stream: A boolean indicating if the request is for a streaming response
//
// Returns:
//   - []byte: The transformed request data in Claude Code API format
func ConvertGeminiRequestToClaude(modelName string, inputRawJSON []byte, stream bool) []byte {
	rawJSON := bytes.Clone(inputRawJSON)

	if account == "" {
		u, _ := uuid.NewRandom()
		account = u.String()
	}
	if session == "" {
		u, _ := uuid.NewRandom()
		session = u.String()
	}
	if user == "" {
		sum := sha256.Sum256([]byte(account + session))
		user = hex.EncodeToString(sum[:])
	}
	userID := fmt.Sprintf("user_%s_account_%s_session_%s", user, account, session)

	// Base Claude message payload
	out := fmt.Sprintf(`{"model":"","max_tokens":32000,"messages":[],"metadata":{"user_id":"%s"}}`, userID)

	root := gjson.ParseBytes(rawJSON)

	// Helper for generating tool call IDs in the form: toolu_<alphanum>
	// This ensures unique identifiers for tool calls in the Claude Code format
	genToolCallID := func() string {
		const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
		var b strings.Builder
		// 24 chars random suffix for uniqueness
		for i := 0; i < 24; i++ {
			n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(letters))))
			b.WriteByte(letters[n.Int64()])
		}
		return "toolu_" + b.String()
	}

	// FIFO queue to store tool call IDs for matching with tool results
	// Gemini uses sequential pairing across possibly multiple in-flight
	// functionCalls, so we keep a FIFO queue of generated tool IDs and
	// consume them in order when functionResponses arrive.
	var pendingToolIDs []string

	// Model mapping to specify which Claude Code model to use
	out, _ = sjson.Set(out, "model", modelName)

	// Generation config extraction from Gemini format
	if genConfig := root.Get("generationConfig"); genConfig.Exists() {
		// Max output tokens configuration
		if maxTokens := genConfig.Get("maxOutputTokens"); maxTokens.Exists() {
			out, _ = sjson.Set(out, "max_tokens", maxTokens.Int())
		}
		// Temperature setting for controlling response randomness
		if temp := genConfig.Get("temperature"); temp.Exists() {
			out, _ = sjson.Set(out, "temperature", temp.Float())
		}
		// Top P setting for nucleus sampling
		if topP := genConfig.Get("topP"); topP.Exists() {
			out, _ = sjson.Set(out, "top_p", topP.Float())
		}
		// Stop sequences configuration for custom termination conditions
		if stopSeqs := genConfig.Get("stopSequences"); stopSeqs.Exists() && stopSeqs.IsArray() {
			var stopSequences []string
			stopSeqs.ForEach(func(_, value gjson.Result) bool {
				stopSequences = append(stopSequences, value.String())
				return true
			})
			if len(stopSequences) > 0 {
				out, _ = sjson.Set(out, "stop_sequences", stopSequences)
			}
		}
		// Include thoughts configuration for reasoning process visibility
		// Only apply for models that support thinking and use numeric budgets, not discrete levels.
		if thinkingConfig := genConfig.Get("thinkingConfig"); thinkingConfig.Exists() && thinkingConfig.IsObject() && util.ModelSupportsThinking(modelName) && !util.ModelUsesThinkingLevels(modelName) {
			// Check for thinkingBudget first - if present, enable thinking with budget
			if thinkingBudget := thinkingConfig.Get("thinkingBudget"); thinkingBudget.Exists() && thinkingBudget.Int() > 0 {
				out, _ = sjson.Set(out, "thinking.type", "enabled")
				normalizedBudget := util.NormalizeThinkingBudget(modelName, int(thinkingBudget.Int()))
				out, _ = sjson.Set(out, "thinking.budget_tokens", normalizedBudget)
			} else if includeThoughts := thinkingConfig.Get("include_thoughts"); includeThoughts.Exists() && includeThoughts.Type == gjson.True {
				// Fallback to include_thoughts if no budget specified
				out, _ = sjson.Set(out, "thinking.type", "enabled")
			}
		}
	}

	// System instruction conversion to Claude Code format
	if sysInstr := root.Get("system_instruction"); sysInstr.Exists() {
		if parts := sysInstr.Get("parts"); parts.Exists() && parts.IsArray() {
			var systemText strings.Builder
			parts.ForEach(func(_, part gjson.Result) bool {
				if text := part.Get("text"); text.Exists() {
					if systemText.Len() > 0 {
						systemText.WriteString("\n")
					}
					systemText.WriteString(text.String())
				}
				return true
			})
			if systemText.Len() > 0 {
				// Create system message in Claude Code format
				systemMessage := `{"role":"user","content":[{"type":"text","text":""}]}`
				systemMessage, _ = sjson.Set(systemMessage, "content.0.text", systemText.String())
				out, _ = sjson.SetRaw(out, "messages.-1", systemMessage)
			}
		}
	}

	// Contents conversion to messages with proper role mapping
	if contents := root.Get("contents"); contents.Exists() && contents.IsArray() {
		contents.ForEach(func(_, content gjson.Result) bool {
			role := content.Get("role").String()
			// Map Gemini roles to Claude Code roles
			if role == "model" {
				role = "assistant"
			}

			if role == "function" {
				role = "user"
			}

			if role == "tool" {
				role = "user"
			}

			// Create message structure in Claude Code format
			msg := `{"role":"","content":[]}`
			msg, _ = sjson.Set(msg, "role", role)

			if parts := content.Get("parts"); parts.Exists() && parts.IsArray() {
				parts.ForEach(func(_, part gjson.Result) bool {
					// Text content conversion
					if text := part.Get("text"); text.Exists() {
						textContent := `{"type":"text","text":""}`
						textContent, _ = sjson.Set(textContent, "text", text.String())
						msg, _ = sjson.SetRaw(msg, "content.-1", textContent)
						return true
					}

					// Function call (from model/assistant) conversion to tool use
					if fc := part.Get("functionCall"); fc.Exists() && role == "assistant" {
						toolUse := `{"type":"tool_use","id":"","name":"","input":{}}`

						// Generate a unique tool ID and enqueue it for later matching
						// with the corresponding functionResponse
						toolID := genToolCallID()
						pendingToolIDs = append(pendingToolIDs, toolID)
						toolUse, _ = sjson.Set(toolUse, "id", toolID)

						if name := fc.Get("name"); name.Exists() {
							toolUse, _ = sjson.Set(toolUse, "name", name.String())
						}
						if args := fc.Get("args"); args.Exists() && args.IsObject() {
							toolUse, _ = sjson.SetRaw(toolUse, "input", args.Raw)
						}
						msg, _ = sjson.SetRaw(msg, "content.-1", toolUse)
						return true
					}

					// Function response (from user) conversion to tool result
					if fr := part.Get("functionResponse"); fr.Exists() {
						toolResult := `{"type":"tool_result","tool_use_id":"","content":""}`

						// Attach the oldest queued tool_id to pair the response
						// with its call. If the queue is empty, generate a new id.
						var toolID string
						if len(pendingToolIDs) > 0 {
							toolID = pendingToolIDs[0]
							// Pop the first element from the queue
							pendingToolIDs = pendingToolIDs[1:]
						} else {
							// Fallback: generate new ID if no pending tool_use found
							toolID = genToolCallID()
						}
						toolResult, _ = sjson.Set(toolResult, "tool_use_id", toolID)

						// Extract result content from the function response
						if result := fr.Get("response.result"); result.Exists() {
							toolResult, _ = sjson.Set(toolResult, "content", result.String())
						} else if response := fr.Get("response"); response.Exists() {
							toolResult, _ = sjson.Set(toolResult, "content", response.Raw)
						}
						msg, _ = sjson.SetRaw(msg, "content.-1", toolResult)
						return true
					}

					// Image content (inline_data) conversion to Claude Code format
					if inlineData := part.Get("inline_data"); inlineData.Exists() {
						imageContent := `{"type":"image","source":{"type":"base64","media_type":"","data":""}}`
						if mimeType := inlineData.Get("mime_type"); mimeType.Exists() {
							imageContent, _ = sjson.Set(imageContent, "source.media_type", mimeType.String())
						}
						if data := inlineData.Get("data"); data.Exists() {
							imageContent, _ = sjson.Set(imageContent, "source.data", data.String())
						}
						msg, _ = sjson.SetRaw(msg, "content.-1", imageContent)
						return true
					}

					// File data conversion to text content with file info
					if fileData := part.Get("file_data"); fileData.Exists() {
						// For file data, we'll convert to text content with file info
						textContent := `{"type":"text","text":""}`
						fileInfo := "File: " + fileData.Get("file_uri").String()
						if mimeType := fileData.Get("mime_type"); mimeType.Exists() {
							fileInfo += " (Type: " + mimeType.String() + ")"
						}
						textContent, _ = sjson.Set(textContent, "text", fileInfo)
						msg, _ = sjson.SetRaw(msg, "content.-1", textContent)
						return true
					}

					return true
				})
			}

			// Only add message if it has content
			if contentArray := gjson.Get(msg, "content"); contentArray.Exists() && len(contentArray.Array()) > 0 {
				out, _ = sjson.SetRaw(out, "messages.-1", msg)
			}

			return true
		})
	}

	// Tools mapping: Gemini functionDeclarations -> Claude Code tools
	if tools := root.Get("tools"); tools.Exists() && tools.IsArray() {
		var anthropicTools []interface{}

		tools.ForEach(func(_, tool gjson.Result) bool {
			if funcDecls := tool.Get("functionDeclarations"); funcDecls.Exists() && funcDecls.IsArray() {
				funcDecls.ForEach(func(_, funcDecl gjson.Result) bool {
					anthropicTool := `{"name":"","description":"","input_schema":{}}`

					if name := funcDecl.Get("name"); name.Exists() {
						anthropicTool, _ = sjson.Set(anthropicTool, "name", name.String())
					}
					if desc := funcDecl.Get("description"); desc.Exists() {
						anthropicTool, _ = sjson.Set(anthropicTool, "description", desc.String())
					}
					if params := funcDecl.Get("parameters"); params.Exists() {
						// Clean up the parameters schema for Claude Code compatibility
						cleaned := params.Raw
						cleaned, _ = sjson.Set(cleaned, "additionalProperties", false)
						cleaned, _ = sjson.Set(cleaned, "$schema", "http://json-schema.org/draft-07/schema#")
						anthropicTool, _ = sjson.SetRaw(anthropicTool, "input_schema", cleaned)
					} else if params = funcDecl.Get("parametersJsonSchema"); params.Exists() {
						// Clean up the parameters schema for Claude Code compatibility
						cleaned := params.Raw
						cleaned, _ = sjson.Set(cleaned, "additionalProperties", false)
						cleaned, _ = sjson.Set(cleaned, "$schema", "http://json-schema.org/draft-07/schema#")
						anthropicTool, _ = sjson.SetRaw(anthropicTool, "input_schema", cleaned)
					}

					anthropicTools = append(anthropicTools, gjson.Parse(anthropicTool).Value())
					return true
				})
			}
			return true
		})

		if len(anthropicTools) > 0 {
			out, _ = sjson.Set(out, "tools", anthropicTools)
		}
	}

	// Tool config mapping from Gemini format to Claude Code format
	if toolConfig := root.Get("tool_config"); toolConfig.Exists() {
		if funcCalling := toolConfig.Get("function_calling_config"); funcCalling.Exists() {
			if mode := funcCalling.Get("mode"); mode.Exists() {
				switch mode.String() {
				case "AUTO":
					out, _ = sjson.SetRaw(out, "tool_choice", `{"type":"auto"}`)
				case "NONE":
					out, _ = sjson.SetRaw(out, "tool_choice", `{"type":"none"}`)
				case "ANY":
					out, _ = sjson.SetRaw(out, "tool_choice", `{"type":"any"}`)
				}
			}
		}
	}

	// Stream setting configuration
	out, _ = sjson.Set(out, "stream", stream)

	// Convert tool parameter types to lowercase for Claude Code compatibility
	var pathsToLower []string
	toolsResult := gjson.Get(out, "tools")
	util.Walk(toolsResult, "", "type", &pathsToLower)
	for _, p := range pathsToLower {
		fullPath := fmt.Sprintf("tools.%s", p)
		out, _ = sjson.Set(out, fullPath, strings.ToLower(gjson.Get(out, fullPath).String()))
	}

	return []byte(out)
}
