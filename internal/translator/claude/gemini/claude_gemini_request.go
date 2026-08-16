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
// 1. Model name mapping and parameter extraction (max_tokens, top_p, etc.)
// 2. Message content conversion from Gemini to Claude Code format
// 3. Tool call and tool result handling with proper ID mapping
// 4. Inline data (images, documents) conversion to Claude Code base64 format
// 5. System instruction and thinking configuration handling
//
// Parameters:
//   - modelName: The name of the model to use for the request
//   - inputRawJSON: The raw JSON request data from the Gemini API
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

	// Base Claude Code API template with default max_tokens value
	out := []byte(fmt.Sprintf(`{"model":"","max_tokens":32000,"messages":[],"metadata":{"user_id":"%s"}}`, userID))

	root := gjson.ParseBytes(rawJSON)

	// Model mapping to specify which Claude Code model to use
	out, _ = sjson.SetBytes(out, "model", modelName)

	// Max tokens configuration with fallback to default value
	if maxTokens := root.Get("generationConfig.maxOutputTokens"); maxTokens.Exists() {
		out, _ = sjson.SetBytes(out, "max_tokens", maxTokens.Int())
	} else if maxTokens := root.Get("generation_config.max_output_tokens"); maxTokens.Exists() {
		out, _ = sjson.SetBytes(out, "max_tokens", maxTokens.Int())
	}

	// Temperature configuration for controlling randomness
	if temperature := root.Get("generationConfig.temperature"); temperature.Exists() {
		out, _ = sjson.SetBytes(out, "temperature", temperature.Float())
	} else if temperature := root.Get("generation_config.temperature"); temperature.Exists() {
		out, _ = sjson.SetBytes(out, "temperature", temperature.Float())
	}

	// Thinking configuration for controlling the thinking budget
	// When thinkingBudget is set to 0, thinking is disabled
	// When thinkingBudget is -1, dynamic thinking budget is used
	// When thinkingBudget > 0, the specific budget value is used
	if thinkingBudget := root.Get("generationConfig.thinkingConfig.thinkingBudget"); thinkingBudget.Exists() {
		switch thinkingBudget.Int() {
		case 0:
			out, _ = sjson.SetBytes(out, "thinking.type", "disabled")
		case -1:
			out, _ = sjson.SetBytes(out, "thinking.type", "enabled")
		default:
			if thinkingBudget.Int() > 0 {
				out, _ = sjson.SetBytes(out, "thinking.type", "enabled")
				out, _ = sjson.SetBytes(out, "thinking.budget_tokens", thinkingBudget.Int())
			}
		}
	} else if thinkingBudget := root.Get("generation_config.thinking_config.thinking_budget"); thinkingBudget.Exists() {
		switch thinkingBudget.Int() {
		case 0:
			out, _ = sjson.SetBytes(out, "thinking.type", "disabled")
		case -1:
			out, _ = sjson.SetBytes(out, "thinking.type", "enabled")
		default:
			if thinkingBudget.Int() > 0 {
				out, _ = sjson.SetBytes(out, "thinking.type", "enabled")
				out, _ = sjson.SetBytes(out, "thinking.budget_tokens", thinkingBudget.Int())
			}
		}
	}

	// System instruction processing and mapping to Claude Code format
	if systemInstruction := root.Get("systemInstruction"); systemInstruction.Exists() {
		parts := systemInstruction.Get("parts")
		if parts.Exists() && parts.IsArray() {
			var systemText []string
			parts.ForEach(func(_, part gjson.Result) bool {
				if text := part.Get("text"); text.Exists() {
					systemText = append(systemText, text.String())
				}
				return true
			})
			if len(systemText) > 0 {
				out, _ = sjson.SetBytes(out, "system", strings.Join(systemText, "\n\n"))
			}
		}
	} else if systemInstruction := root.Get("system_instruction"); systemInstruction.Exists() {
		parts := systemInstruction.Get("parts")
		if parts.Exists() && parts.IsArray() {
			var systemText []string
			parts.ForEach(func(_, part gjson.Result) bool {
				if text := part.Get("text"); text.Exists() {
					systemText = append(systemText, text.String())
				}
				return true
			})
			if len(systemText) > 0 {
				out, _ = sjson.SetBytes(out, "system", strings.Join(systemText, "\n\n"))
			}
		}
	}

	// Messages processing and transformation
	var toolIDMap = make(map[string]string)
	genToolCallID := func(name string) string {
		if id, exists := toolIDMap[name]; exists {
			return id
		}
		const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
		var b strings.Builder
		for i := 0; i < 24; i++ {
			n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(letters))))
			b.WriteByte(letters[n.Int64()])
		}
		id := "toolu_" + b.String()
		toolIDMap[name] = id
		return id
	}

	// Helper function to extract tool call ID from function call part
	// Supports custom IDs, standard tool IDs, and fallback generation
	getToolCallID := func(part gjson.Result, funcName string) string {
		if id := part.Get("id"); id.Exists() && id.String() != "" {
			return id.String()
		}
		if id := part.Get("functionCall.id"); id.Exists() && id.String() != "" {
			return id.String()
		}
		if id := part.Get("function_call.id"); id.Exists() && id.String() != "" {
			return id.String()
		}
		return genToolCallID(funcName)
	}

	// Helper function to extract tool response ID from function response part
	// Supports custom IDs, standard response IDs, and fallback generation
	getToolResponseID := func(part gjson.Result, funcName string) string {
		if id := part.Get("id"); id.Exists() && id.String() != "" {
			return id.String()
		}
		if id := part.Get("functionResponse.id"); id.Exists() && id.String() != "" {
			return id.String()
		}
		if id := part.Get("function_response.id"); id.Exists() && id.String() != "" {
			return id.String()
		}
		return genToolCallID(funcName)
	}

	messageAccumulator := translatorcommon.NewClaudeMessageAccumulator(int(root.Get("contents.#").Int()))
	if contents := root.Get("contents"); contents.Exists() && contents.IsArray() {
		contents.ForEach(func(_, content gjson.Result) bool {
			role := content.Get("role").String()
			// Map Gemini roles to Claude Code roles
			switch role {
			case "model":
				role = "assistant"
			default:
				role = "user"
			}

			parts := content.Get("parts")
			var contentItems [][]byte

			if parts.Exists() && parts.IsArray() {
				parts.ForEach(func(_, part gjson.Result) bool {
					// Text content mapping
					if text := part.Get("text"); text.Exists() {
						if isHiddenThoughtPart(part) {
							return true
						}
						textContent := text.String()
						contentPart := []byte(`{"type":"text","text":""}`)
						contentPart, _ = sjson.SetBytes(contentPart, "text", textContent)
						contentItems = append(contentItems, contentPart)
						return true
					}

					// Function call mapping to tool_use
					if funcCall := part.Get("functionCall"); funcCall.Exists() {
						funcName := funcCall.Get("name").String()
						callID := getToolCallID(part, funcName)
						contentPart := []byte(`{"type":"tool_use","id":"","name":"","input":{}}`)
						contentPart, _ = sjson.SetBytes(contentPart, "id", callID)
						contentPart, _ = sjson.SetBytes(contentPart, "name", funcName)

						if args := funcCall.Get("args"); args.Exists() {
							contentPart, _ = sjson.SetRawBytes(contentPart, "input", []byte(args.Raw))
						}

						contentItems = append(contentItems, contentPart)
						return true
					} else if funcCall := part.Get("function_call"); funcCall.Exists() {
						funcName := funcCall.Get("name").String()
						callID := getToolCallID(part, funcName)
						contentPart := []byte(`{"type":"tool_use","id":"","name":"","input":{}}`)
						contentPart, _ = sjson.SetBytes(contentPart, "id", callID)
						contentPart, _ = sjson.SetBytes(contentPart, "name", funcName)

						if args := funcCall.Get("args"); args.Exists() {
							contentPart, _ = sjson.SetRawBytes(contentPart, "input", []byte(args.Raw))
						}

						contentItems = append(contentItems, contentPart)
						return true
					}

					// Function response mapping to tool_result
					if funcResp := part.Get("functionResponse"); funcResp.Exists() {
						funcName := funcResp.Get("name").String()
						callID := getToolResponseID(part, funcName)
						contentPart := []byte(`{"type":"tool_result","tool_use_id":"","content":""}`)
						contentPart, _ = sjson.SetBytes(contentPart, "tool_use_id", callID)

						if resp := funcResp.Get("response"); resp.Exists() {
							contentPart, _ = sjson.SetBytes(contentPart, "content", resp.Raw)
						}

						contentItems = append(contentItems, contentPart)
						return true
					} else if funcResp := part.Get("function_response"); funcResp.Exists() {
						funcName := funcResp.Get("name").String()
						callID := getToolResponseID(part, funcName)
						contentPart := []byte(`{"type":"tool_result","tool_use_id":"","content":""}`)
						contentPart, _ = sjson.SetBytes(contentPart, "tool_use_id", callID)

						if resp := funcResp.Get("response"); resp.Exists() {
							contentPart, _ = sjson.SetBytes(contentPart, "content", resp.Raw)
						}

						contentItems = append(contentItems, contentPart)
						return true
					}

					// Inline data conversion to Claude Code image format
					if inlineData := part.Get("inlineData"); inlineData.Exists() {
						mimeType := inlineData.Get("mimeType").String()
						data := inlineData.Get("data").String()
						if contentPart, ok := claudeContentPartFromInlineData(mimeType, data); ok {
							contentItems = append(contentItems, contentPart)
						}
						return true
					} else if inlineData := part.Get("inline_data"); inlineData.Exists() {
						mimeType := inlineData.Get("mime_type").String()
						data := inlineData.Get("data").String()
						if contentPart, ok := claudeContentPartFromInlineData(mimeType, data); ok {
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
				messageAccumulator.Append(msg)
			}

			return true
		})
	}
	out = translatorcommon.SetRawArrayItems(out, "messages", messageAccumulator.Messages())

	// Tools mapping: Gemini functionDeclarations -> Claude Code tools
	if tools := root.Get("tools"); tools.Exists() && tools.IsArray() {
		var anthropicTools []interface{}

		tools.ForEach(func(_, tool gjson.Result) bool {
			funcDecls := tool.Get("functionDeclarations")
			if !funcDecls.Exists() || !funcDecls.IsArray() {
				funcDecls = tool.Get("function_declarations")
			}
			if funcDecls.Exists() && funcDecls.IsArray() {
				funcDecls.ForEach(func(_, funcDecl gjson.Result) bool {
					anthropicTool := []byte(`{"name":"","description":"","input_schema":{"type":"object","properties":{}}}`)

					if name := funcDecl.Get("name"); name.Exists() {
						anthropicTool, _ = sjson.SetBytes(anthropicTool, "name", util.SanitizeClaudeFunctionName(name.String()))
					}
					if desc := funcDecl.Get("description"); desc.Exists() {
						anthropicTool, _ = sjson.SetBytes(anthropicTool, "description", desc.String())
					}
					var schemaRaw []byte
					for _, k := range []string{"parameters", "parametersJsonSchema", "parameters_json_schema", "input_schema", "schema"} {
						if s := funcDecl.Get(k); s.Exists() && s.Raw != "" && s.Raw != "null" {
							schemaRaw = []byte(s.Raw)
							break
						}
					}
					if len(schemaRaw) > 0 {
						cleaned := normalizeClaudeToolSchema(gjson.ParseBytes(schemaRaw))
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
	schema := gjson.GetBytes(tool, "input_schema")
	if !schema.Exists() {
		return tool
	}
	paths := findTypePaths(schema.Raw)
	out := tool
	for _, p := range paths {
		val := gjson.GetBytes(tool, "input_schema."+p)
		if val.Exists() && val.Type == gjson.String {
			out, _ = sjson.SetBytes(out, "input_schema."+p, strings.ToLower(val.String()))
		}
	}
	return out
}

func findTypePaths(rawJSON string) []string {
	var paths []string
	root := gjson.Parse(rawJSON)
	var walk func(path string, val gjson.Result)
	walk = func(path string, val gjson.Result) {
		if val.IsObject() {
			val.ForEach(func(k, v gjson.Result) bool {
				key := k.String()
				subPath := key
				if path != "" {
					subPath = path + "." + key
				}
				if key == "type" && v.Type == gjson.String {
					paths = append(paths, subPath)
				} else {
					walk(subPath, v)
				}
				return true
			})
		} else if val.IsArray() {
			val.ForEach(func(k, v gjson.Result) bool {
				idx := k.String()
				subPath := idx
				if path != "" {
					subPath = path + "." + idx
				}
				walk(subPath, v)
				return true
			})
		}
	}
	walk("", root)
	return paths
}

func setClaudeToolChoiceFromGeminiToolConfig(out []byte, functionCallingConfig gjson.Result) []byte {
	if !functionCallingConfig.Exists() {
		return out
	}
	mode := functionCallingConfig.Get("mode").String()
	switch strings.ToUpper(mode) {
	case "NONE":
		out, _ = sjson.SetBytes(out, "tool_choice.type", "none")
	case "AUTO":
		out, _ = sjson.SetBytes(out, "tool_choice.type", "auto")
	case "ANY":
		allowed := functionCallingConfig.Get("allowed_function_names")
		if !allowed.Exists() {
			allowed = functionCallingConfig.Get("allowedFunctionNames")
		}
		if allowed.Exists() && allowed.IsArray() && len(allowed.Array()) > 0 {
			first := allowed.Array()[0].String()
			out, _ = sjson.SetBytes(out, "tool_choice.type", "tool")
			out, _ = sjson.SetBytes(out, "tool_choice.name", first)
		} else {
			out, _ = sjson.SetBytes(out, "tool_choice.type", "any")
		}
	}
	return out
}

func isHiddenThoughtPart(part gjson.Result) bool {
	thought := part.Get("thought")
	if thought.Exists() && thought.Bool() {
		return true
	}
	ts := part.Get("thoughtSignature")
	if !ts.Exists() {
		ts = part.Get("thought_signature")
	}
	if ts.Exists() && ts.String() != "" && !part.Get("text").Exists() && !part.Get("functionCall").Exists() && !part.Get("function_call").Exists() {
		return true
	}
	return false
}

func claudeContentPartFromInlineData(mimeType, data string) ([]byte, bool) {
	if data == "" {
		return nil, false
	}
	if strings.HasPrefix(mimeType, "image/") {
		part := []byte(`{"type":"image","source":{"type":"base64","media_type":"","data":""}}`)
		part, _ = sjson.SetBytes(part, "source.media_type", mimeType)
		part, _ = sjson.SetBytes(part, "source.data", data)
		return part, true
	}
	part := []byte(`{"type":"document","source":{"type":"base64","media_type":"","data":""}}`)
	part, _ = sjson.SetBytes(part, "source.media_type", mimeType)
	part, _ = sjson.SetBytes(part, "source.data", data)
	return part, true
}

func geminiClaudeFileData(part gjson.Result) gjson.Result {
	if fd := part.Get("fileData"); fd.Exists() {
		return fd
	}
	return part.Get("file_data")
}

func claudeContentPartFromGeminiFileData(fileData gjson.Result) ([]byte, bool) {
	mimeType := fileData.Get("mimeType").String()
	if mimeType == "" {
		mimeType = fileData.Get("mime_type").String()
	}
	data := fileData.Get("data").String()
	if data != "" {
		return claudeContentPartFromInlineData(mimeType, data)
	}
	return nil, false
}
