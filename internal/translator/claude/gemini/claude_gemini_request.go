// Package gemini provides request translation functionality for Gemini to Claude Code API compatibility.
// It handles parsing and transforming Gemini API requests into Claude Code API format,
// extracting model information, system instructions, message contents, and tool declarations.
// The package performs JSON data transformation to ensure compatibility
// between Gemini API format and Claude Code API's expected format.
package gemini

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
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
	rawJSON := inputRawJSON

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
	out := []byte(fmt.Sprintf(`{"model":"","max_tokens":32000,"messages":[],"metadata":{"user_id":"%s"}}`, userID))

	root := gjson.ParseBytes(rawJSON)
	messageItems := translatorcommon.NewRawArrayItems(root.Get("contents.#").Int())

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

	getGeminiToolID := func(value gjson.Result) string {
		if toolID := strings.TrimSpace(value.Get("id").String()); toolID != "" {
			return toolID
		}
		return strings.TrimSpace(value.Get("call_id").String())
	}

	removePendingToolID := func(ids []string, toolID string) []string {
		if toolID == "" {
			return ids
		}
		for idx, pendingID := range ids {
			if pendingID == toolID {
				return append(ids[:idx], ids[idx+1:]...)
			}
		}
		return ids
	}

	// FIFO queue to store tool call IDs for matching with tool results
	// Gemini uses sequential pairing across possibly multiple in-flight
	// functionCalls, so we keep a FIFO queue of generated tool IDs and
	// consume them in order when functionResponses arrive.
	var pendingToolIDs []string

	// Model mapping to specify which Claude Code model to use
	out, _ = sjson.SetBytes(out, "model", modelName)
	if serviceTier := root.Get("service_tier"); serviceTier.Exists() && serviceTier.Type == gjson.String {
		out, _ = sjson.SetBytes(out, "service_tier", serviceTier.String())
	}

	// Generation config extraction from Gemini format
	if genConfig := root.Get("generationConfig"); genConfig.Exists() {
		// Max output tokens configuration
		if maxTokens := genConfig.Get("maxOutputTokens"); maxTokens.Exists() {
			out, _ = sjson.SetBytes(out, "max_tokens", maxTokens.Int())
		}
		// Top P setting for nucleus sampling.
		if topP := genConfig.Get("topP"); topP.Exists() {
			out, _ = sjson.SetBytes(out, "top_p", topP.Float())
		}
		// Stop sequences configuration for custom termination conditions
		if stopSeqs := genConfig.Get("stopSequences"); stopSeqs.Exists() && stopSeqs.IsArray() {
			var stopSequences []string
			stopSeqs.ForEach(func(_, value gjson.Result) bool {
				stopSequences = append(stopSequences, value.String())
				return true
			})
			if len(stopSequences) > 0 {
				out, _ = sjson.SetBytes(out, "stop_sequences", stopSequences)
			}
		}
		// Include thoughts configuration for reasoning process visibility
		// Translator only does format conversion, ApplyThinking handles model capability validation.
		if thinkingConfig := genConfig.Get("thinkingConfig"); thinkingConfig.Exists() && thinkingConfig.IsObject() {
			mi := registry.LookupModelInfo(modelName, "claude")
			supportsAdaptive := mi != nil && mi.Thinking != nil && len(mi.Thinking.Levels) > 0
			supportsMax := supportsAdaptive && thinking.HasLevel(mi.Thinking.Levels, string(thinking.LevelMax))

			// MapToClaudeEffort normalizes levels (e.g. minimal→low, xhigh→high) to avoid
			// validation errors since validate treats same-provider unsupported levels as errors.
			thinkingLevel := thinkingConfig.Get("thinkingLevel")
			if !thinkingLevel.Exists() {
				thinkingLevel = thinkingConfig.Get("thinking_level")
			}
			if thinkingLevel.Exists() {
				level := strings.ToLower(strings.TrimSpace(thinkingLevel.String()))
				if supportsAdaptive {
					switch level {
					case "":
					case "none":
						out, _ = sjson.SetBytes(out, "thinking.type", "disabled")
						out, _ = sjson.DeleteBytes(out, "thinking.budget_tokens")
						out, _ = sjson.DeleteBytes(out, "output_config.effort")
					default:
						if mapped, ok := thinking.MapToClaudeEffort(level, supportsMax); ok {
							level = mapped
						}
						out, _ = sjson.SetBytes(out, "thinking.type", "adaptive")
						out, _ = sjson.DeleteBytes(out, "thinking.budget_tokens")
						out, _ = sjson.SetBytes(out, "output_config.effort", level)
					}
				} else {
					switch level {
					case "":
					case "none":
						out, _ = sjson.SetBytes(out, "thinking.type", "disabled")
						out, _ = sjson.DeleteBytes(out, "thinking.budget_tokens")
					case "auto":
						out, _ = sjson.SetBytes(out, "thinking.type", "enabled")
						out, _ = sjson.DeleteBytes(out, "thinking.budget_tokens")
					default:
						if budget, ok := thinking.ConvertLevelToBudget(level); ok {
							out, _ = sjson.SetBytes(out, "thinking.type", "enabled")
							out, _ = sjson.SetBytes(out, "thinking.budget_tokens", budget)
						}
					}
				}
			} else {
				thinkingBudget := thinkingConfig.Get("thinkingBudget")
				if !thinkingBudget.Exists() {
					thinkingBudget = thinkingConfig.Get("thinking_budget")
				}
				if thinkingBudget.Exists() {
					budget := int(thinkingBudget.Int())
					if supportsAdaptive {
						switch budget {
						case 0:
							out, _ = sjson.SetBytes(out, "thinking.type", "disabled")
							out, _ = sjson.DeleteBytes(out, "thinking.budget_tokens")
							out, _ = sjson.DeleteBytes(out, "output_config.effort")
						default:
							level, ok := thinking.ConvertBudgetToLevel(budget)
							if ok {
								if mapped, okM := thinking.MapToClaudeEffort(level, supportsMax); okM {
									level = mapped
								}
								out, _ = sjson.SetBytes(out, "thinking.type", "adaptive")
								out, _ = sjson.DeleteBytes(out, "thinking.budget_tokens")
								out, _ = sjson.SetBytes(out, "output_config.effort", level)
							}
						}
					} else {
						switch budget {
						case 0:
							out, _ = sjson.SetBytes(out, "thinking.type", "disabled")
							out, _ = sjson.DeleteBytes(out, "thinking.budget_tokens")
						case -1:
							out, _ = sjson.SetBytes(out, "thinking.type", "enabled")
							out, _ = sjson.DeleteBytes(out, "thinking.budget_tokens")
						default:
							out, _ = sjson.SetBytes(out, "thinking.type", "enabled")
							out, _ = sjson.SetBytes(out, "thinking.budget_tokens", budget)
						}
					}
				} else if includeThoughts := thinkingConfig.Get("includeThoughts"); includeThoughts.Exists() && includeThoughts.Type == gjson.True {
					out, _ = sjson.SetBytes(out, "thinking.type", "enabled")
				} else if includeThoughts := thinkingConfig.Get("include_thoughts"); includeThoughts.Exists() && includeThoughts.Type == gjson.True {
					out, _ = sjson.SetBytes(out, "thinking.type", "enabled")
				}
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
				// Create system message in Claude Code format.
				systemMessage := []byte(`{"role":"user","content":[{"type":"text","text":""}]}`)
				systemMessage, _ = sjson.SetBytes(systemMessage, "content.0.text", systemText.String())
				messageItems = append(messageItems, systemMessage)
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

			contentItems := make([][]byte, 0, 4)
			if parts := content.Get("parts"); parts.Exists() && parts.IsArray() {
				parts.ForEach(func(_, part gjson.Result) bool {
					// Text content conversion
					if text := part.Get("text"); text.Exists() {
						textContent := []byte(`{"type":"text","text":""}`)
						textContent, _ = sjson.SetBytes(textContent, "text", text.String())
						contentItems = append(contentItems, textContent)
						return true
					}

					// Function call (from model/assistant) conversion to tool use
					if fc := part.Get("functionCall"); fc.Exists() && role == "assistant" {
						toolUse := []byte(`{"type":"tool_use","id":"","name":"","input":{}}`)

						// Reuse gateway-provided IDs when present, otherwise generate one for pairing.
						toolID := getGeminiToolID(fc)
						if toolID == "" {
							toolID = genToolCallID()
						}
						pendingToolIDs = append(pendingToolIDs, toolID)
						toolUse, _ = sjson.SetBytes(toolUse, "id", toolID)

						if name := fc.Get("name"); name.Exists() {
							toolUse, _ = sjson.SetBytes(toolUse, "name", name.String())
						}
						if args := fc.Get("args"); args.Exists() && args.IsObject() {
							toolUse, _ = sjson.SetRawBytes(toolUse, "input", []byte(args.Raw))
						}
						contentItems = append(contentItems, toolUse)
						return true
					}

					// Function response (from user) conversion to tool result
					if fr := part.Get("functionResponse"); fr.Exists() {
						toolResult := []byte(`{"type":"tool_result","tool_use_id":"","content":""}`)

						// Attach the oldest queued tool_id to pair the response
						// with its call. If the queue is empty, generate a new id.
						var toolID string
						if customID := getGeminiToolID(fr); customID != "" {
							toolID = customID
							pendingToolIDs = removePendingToolID(pendingToolIDs, toolID)
						} else if len(pendingToolIDs) > 0 {
							toolID = pendingToolIDs[0]
							// Pop the first element from the queue
							pendingToolIDs = pendingToolIDs[1:]
						} else {
							// Fallback: generate new ID if no pending tool_use found
							toolID = genToolCallID()
						}
						toolResult, _ = sjson.SetBytes(toolResult, "tool_use_id", toolID)

						// Extract result content from the function response
						if result := fr.Get("response.result"); result.Exists() {
							toolResult, _ = sjson.SetBytes(toolResult, "content", result.String())
						} else if response := fr.Get("response"); response.Exists() {
							toolResult, _ = sjson.SetBytes(toolResult, "content", response.Raw)
						}
						contentItems = append(contentItems, toolResult)
						return true
					}

					// Inline data conversion to Claude Code content format
					if inlineData := geminiClaudeInlineData(part); inlineData.Exists() {
						if contentPart, ok := claudeContentPartFromGeminiInlineData(inlineData); ok {
							contentItems = append(contentItems, contentPart)
						}
						return true
					}

					// File data conversion to Claude Code content format
					if fileData := geminiClaudeFileData(part); fileData.Exists() {
						if contentPart, ok := claudeContentPartFromGeminiFileData(fileData); ok {
							contentItems = append(contentItems, contentPart)
						}
						return true
					}

					return true
				})
			}

			// Only add message if it has content.
			if len(contentItems) > 0 {
				msg := []byte(`{"role":"","content":[]}`)
				msg, _ = sjson.SetBytes(msg, "role", role)
				msg, _ = sjson.SetRawBytes(msg, "content", translatorcommon.JoinRawArray(contentItems))
				messageItems = append(messageItems, msg)
			}

			return true
		})
	}
	out = translatorcommon.SetRawArrayItems(out, "messages", messageItems)

	// Tools mapping: Gemini functionDeclarations -> Claude Code tools
	if tools := root.Get("tools"); tools.Exists() && tools.IsArray() {
		var anthropicTools []interface{}

		tools.ForEach(func(_, tool gjson.Result) bool {
			if funcDecls := tool.Get("functionDeclarations"); funcDecls.Exists() && funcDecls.IsArray() {
				funcDecls.ForEach(func(_, funcDecl gjson.Result) bool {
					anthropicTool := []byte(`{"name":"","description":"","input_schema":{}}`)

					if name := funcDecl.Get("name"); name.Exists() {
						anthropicTool, _ = sjson.SetBytes(anthropicTool, "name", name.String())
					}
					if desc := funcDecl.Get("description"); desc.Exists() {
						anthropicTool, _ = sjson.SetBytes(anthropicTool, "description", desc.String())
					}
					if params := funcDecl.Get("parameters"); params.Exists() {
						cleaned := normalizeClaudeToolSchema(params)
						anthropicTool, _ = sjson.SetRawBytes(anthropicTool, "input_schema", cleaned)
					} else if params = funcDecl.Get("parametersJsonSchema"); params.Exists() {
						cleaned := normalizeClaudeToolSchema(params)
						anthropicTool, _ = sjson.SetRawBytes(anthropicTool, "input_schema", cleaned)
					}

					anthropicTool = lowercaseClaudeToolSchemaTypes(anthropicTool)
					anthropicTools = append(anthropicTools, gjson.ParseBytes(anthropicTool).Value())
					return true
				})
			}
			return true
		})

		if len(anthropicTools) > 0 {
			out, _ = sjson.SetBytes(out, "tools", anthropicTools)
		}
	}

	// Tool config mapping from Gemini format to Claude Code format
	if toolConfig := root.Get("tool_config"); toolConfig.Exists() {
		out = setClaudeToolChoiceFromGeminiToolConfig(out, toolConfig.Get("function_calling_config"))
	} else if toolConfig := root.Get("toolConfig"); toolConfig.Exists() {
		out = setClaudeToolChoiceFromGeminiToolConfig(out, toolConfig.Get("functionCallingConfig"))
	}

	// Stream setting configuration
	out, _ = sjson.SetBytes(out, "stream", stream)

	return out
}

func normalizeClaudeToolSchema(parameters gjson.Result) []byte {
	cleaned := []byte(parameters.Raw)
	if parameters.Get("additionalProperties").Type != gjson.False {
		cleaned, _ = sjson.SetBytes(cleaned, "additionalProperties", false)
	}
	const schema = "http://json-schema.org/draft-07/schema#"
	currentSchema := parameters.Get("$schema")
	if currentSchema.Type != gjson.String || currentSchema.String() != schema {
		cleaned, _ = sjson.SetBytes(cleaned, "$schema", schema)
	}
	return cleaned
}

func lowercaseClaudeToolSchemaTypes(tool []byte) []byte {
	var pathsToLower []string
	util.Walk(gjson.ParseBytes(tool), "", "type", &pathsToLower)
	for _, path := range pathsToLower {
		typeValue := gjson.GetBytes(tool, path)
		normalizedType := strings.ToLower(typeValue.String())
		if typeValue.Type == gjson.String && normalizedType == typeValue.String() {
			continue
		}
		tool, _ = sjson.SetBytes(tool, path, normalizedType)
	}
	return tool
}

func setClaudeToolChoiceFromGeminiToolConfig(out []byte, funcCalling gjson.Result) []byte {
	if !funcCalling.Exists() {
		return out
	}
	mode := funcCalling.Get("mode")
	if !mode.Exists() {
		return out
	}
	switch mode.String() {
	case "AUTO":
		out, _ = sjson.SetRawBytes(out, "tool_choice", []byte(`{"type":"auto"}`))
	case "NONE":
		out, _ = sjson.SetRawBytes(out, "tool_choice", []byte(`{"type":"none"}`))
	case "ANY":
		allowedNames := funcCalling.Get("allowedFunctionNames")
		if !allowedNames.Exists() {
			allowedNames = funcCalling.Get("allowed_function_names")
		}
		allowedNameItems := allowedNames.Array()
		if allowedNames.IsArray() && len(allowedNameItems) == 1 {
			choice := []byte(`{"type":"tool","name":""}`)
			choice, _ = sjson.SetBytes(choice, "name", allowedNameItems[0].String())
			out, _ = sjson.SetRawBytes(out, "tool_choice", choice)
		} else {
			out, _ = sjson.SetRawBytes(out, "tool_choice", []byte(`{"type":"any"}`))
		}
	}
	return out
}

func geminiClaudeInlineData(part gjson.Result) gjson.Result {
	inlineData := part.Get("inlineData")
	if inlineData.Exists() {
		return inlineData
	}
	return part.Get("inline_data")
}

func geminiClaudeFileData(part gjson.Result) gjson.Result {
	fileData := part.Get("fileData")
	if fileData.Exists() {
		return fileData
	}
	return part.Get("file_data")
}

func claudeContentPartFromGeminiInlineData(inlineData gjson.Result) ([]byte, bool) {
	mimeType := inlineData.Get("mimeType").String()
	if mimeType == "" {
		mimeType = inlineData.Get("mime_type").String()
	}
	data := inlineData.Get("data").String()
	if mimeType == "" || data == "" {
		return nil, false
	}
	lowerMimeType := strings.ToLower(mimeType)
	switch {
	case strings.HasPrefix(lowerMimeType, "image/"):
		imageContent := []byte(`{"type":"image","source":{"type":"base64","media_type":"","data":""}}`)
		imageContent, _ = sjson.SetBytes(imageContent, "source.media_type", mimeType)
		imageContent, _ = sjson.SetBytes(imageContent, "source.data", data)
		return imageContent, true
	case strings.HasPrefix(lowerMimeType, "application/"), strings.HasPrefix(lowerMimeType, "text/"):
		documentContent := []byte(`{"type":"document","source":{"type":"base64","media_type":"","data":""}}`)
		documentContent, _ = sjson.SetBytes(documentContent, "source.media_type", mimeType)
		documentContent, _ = sjson.SetBytes(documentContent, "source.data", data)
		return documentContent, true
	default:
		return claudeTextContentPart(fmt.Sprintf("Media content: inline data (Type: %s)", mimeType)), true
	}
}

func claudeContentPartFromGeminiFileData(fileData gjson.Result) ([]byte, bool) {
	fileURI := fileData.Get("fileUri").String()
	if fileURI == "" {
		fileURI = fileData.Get("file_uri").String()
	}
	if fileURI == "" {
		return nil, false
	}
	mimeType := fileData.Get("mimeType").String()
	if mimeType == "" {
		mimeType = fileData.Get("mime_type").String()
	}
	lowerMimeType := strings.ToLower(mimeType)
	switch {
	case strings.HasPrefix(lowerMimeType, "image/"):
		imageContent := []byte(`{"type":"image","source":{"type":"url","url":""}}`)
		imageContent, _ = sjson.SetBytes(imageContent, "source.url", fileURI)
		return imageContent, true
	case strings.HasPrefix(lowerMimeType, "application/"), strings.HasPrefix(lowerMimeType, "text/"):
		documentContent := []byte(`{"type":"document","source":{"type":"url","url":""}}`)
		documentContent, _ = sjson.SetBytes(documentContent, "source.url", fileURI)
		if mimeType != "" {
			documentContent, _ = sjson.SetBytes(documentContent, "source.media_type", mimeType)
		}
		return documentContent, true
	default:
		fileInfo := "File: " + fileURI
		if mimeType != "" {
			fileInfo += " (Type: " + mimeType + ")"
		}
		return claudeTextContentPart(fileInfo), true
	}
}

func claudeTextContentPart(text string) []byte {
	textContent := []byte(`{"type":"text","text":""}`)
	textContent, _ = sjson.SetBytes(textContent, "text", text)
	return textContent
}
