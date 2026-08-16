// Package interactions provides request translation functionality for Gemini Interactions to Claude Code API compatibility.
// It handles parsing and transforming Interactions API requests into Claude Code API format,
// extracting model information, system instructions, message contents, and tool declarations.
// The package performs JSON data transformation to ensure compatibility
// between Interactions API format and Claude Code API's expected format.
package interactions

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

// ConvertInteractionsRequestToClaude parses and transforms a Gemini Interactions API request into Claude Code API format.
// It extracts the model name, system instruction, message contents, and tool declarations
// from the raw JSON request and returns them in the format expected by the Claude Code API.
// The function performs comprehensive transformation including:
// 1. Model name mapping and parameter extraction (max_tokens, top_p, etc.)
// 2. Message content conversion from Interactions to Claude Code format
// 3. Tool call and tool result handling with proper ID mapping
// 4. Input items (user input, function calls, function results) conversion
// 5. System instruction and thinking configuration handling
//
// Parameters:
//   - modelName: The name of the model to use for the request
//   - inputRawJSON: The raw JSON request data from the Interactions API
//   - stream: A boolean indicating if the request is for a streaming response
//
// Returns:
//   - []byte: The transformed request data in Claude Code API format
func ConvertInteractionsRequestToClaude(modelName string, inputRawJSON []byte, stream bool) []byte {
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

	out := []byte(fmt.Sprintf(`{"model":"","max_tokens":32000,"messages":[],"metadata":{"user_id":"%s"}}`, userID))
	root := gjson.ParseBytes(rawJSON)

	out, _ = sjson.SetBytes(out, "model", modelName)
	if maxTokens := firstClaudeInteractionsExisting(root, "generation_config.max_output_tokens", "generationConfig.maxOutputTokens", "max_tokens"); maxTokens.Exists() {
		out, _ = sjson.SetBytes(out, "max_tokens", maxTokens.Int())
	}
	if temperature := firstClaudeInteractionsExisting(root, "generation_config.temperature", "generationConfig.temperature", "temperature"); temperature.Exists() {
		out, _ = sjson.SetBytes(out, "temperature", temperature.Float())
	}
	if topP := firstClaudeInteractionsExisting(root, "generation_config.top_p", "generationConfig.topP", "top_p"); topP.Exists() {
		out, _ = sjson.SetBytes(out, "top_p", topP.Float())
	}
	if topK := firstClaudeInteractionsExisting(root, "generation_config.top_k", "generationConfig.topK", "top_k"); topK.Exists() {
		out, _ = sjson.SetBytes(out, "top_k", topK.Int())
	}

	if thinkingBudget := firstClaudeInteractionsExisting(root, "generation_config.thinking_config.thinking_budget", "generationConfig.thinkingConfig.thinkingBudget", "thinking.budget_tokens"); thinkingBudget.Exists() {
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

	if systemInstruction := firstClaudeInteractionsExisting(root, "system_instruction", "systemInstruction", "system"); systemInstruction.Exists() {
		if systemInstruction.Type == gjson.String {
			out, _ = sjson.SetBytes(out, "system", systemInstruction.String())
		} else if parts := systemInstruction.Get("parts"); parts.IsArray() {
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

	toolIDMap := make(map[string]string)
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

	input := root.Get("input")
	if input.Exists() {
		if input.Type == gjson.String {
			msg := []byte(`{"role":"user","content":[{"type":"text","text":""}]}`)
			msg, _ = sjson.SetBytes(msg, "content.0.text", input.String())
			out, _ = sjson.SetRawBytes(out, "messages", []byte(`[`+string(msg)+`]`))
		} else if input.IsArray() {
			messageAccumulator := translatorcommon.NewClaudeMessageAccumulator(int(input.Get("#").Int()))
			input.ForEach(func(_, item gjson.Result) bool {
				itemType := item.Get("type").String()
				switch itemType {
				case "user_input", "user":
					content := item.Get("content")
					var contentItems [][]byte
					if content.Type == gjson.String {
						part := []byte(`{"type":"text","text":""}`)
						part, _ = sjson.SetBytes(part, "text", content.String())
						contentItems = append(contentItems, part)
					} else if content.IsArray() {
						content.ForEach(func(_, part gjson.Result) bool {
							switch part.Get("type").String() {
							case "text":
								textPart := []byte(`{"type":"text","text":""}`)
								textPart, _ = sjson.SetBytes(textPart, "text", part.Get("text").String())
								contentItems = append(contentItems, textPart)
							case "image":
								source := part.Get("source")
								imagePart := []byte(`{"type":"image","source":{}}`)
								imagePart, _ = sjson.SetRawBytes(imagePart, "source", []byte(source.Raw))
								contentItems = append(contentItems, imagePart)
							}
							return true
						})
					}
					if len(contentItems) > 0 {
						msg := []byte(`{"role":"user","content":[]}`)
						msg, _ = sjson.SetRawBytes(msg, "content", translatorcommon.JoinRawArray(contentItems))
						messageAccumulator.Append(msg)
					}

				case "function_call":
					name := item.Get("name").String()
					callID := item.Get("call_id").String()
					if callID == "" {
						callID = genToolCallID(name)
					}
					part := []byte(`{"type":"tool_use","id":"","name":"","input":{}}`)
					part, _ = sjson.SetBytes(part, "id", callID)
					part, _ = sjson.SetBytes(part, "name", name)
					if args := item.Get("arguments"); args.Exists() {
						part, _ = sjson.SetRawBytes(part, "input", []byte(args.Raw))
					}
					msg := []byte(`{"role":"assistant","content":[]}`)
					msg, _ = sjson.SetRawBytes(msg, "content", []byte(`[`+string(part)+`]`))
					messageAccumulator.Append(msg)

				case "function_result":
					name := item.Get("name").String()
					callID := item.Get("call_id").String()
					if callID == "" {
						callID = genToolCallID(name)
					}
					part := []byte(`{"type":"tool_result","tool_use_id":"","content":""}`)
					part, _ = sjson.SetBytes(part, "tool_use_id", callID)
					if result := item.Get("result"); result.Exists() {
						part, _ = sjson.SetBytes(part, "content", result.Raw)
					}
					msg := []byte(`{"role":"user","content":[]}`)
					msg, _ = sjson.SetRawBytes(msg, "content", []byte(`[`+string(part)+`]`))
					messageAccumulator.Append(msg)
				}
				return true
			})
			out = translatorcommon.SetRawArrayItems(out, "messages", messageAccumulator.Messages())
		}
	}

	tools := root.Get("tools")
	if tools.IsArray() {
		var toolItems [][]byte
		tools.ForEach(func(_, tool gjson.Result) bool {
			if tool.Get("function_declarations").IsArray() {
				tool.Get("function_declarations").ForEach(func(_, decl gjson.Result) bool {
					if converted := interactionsClaudeTool(decl); len(converted) > 0 {
						toolItems = append(toolItems, converted)
					}
					return true
				})
				return true
			}
			if tool.Get("functionDeclarations").IsArray() {
				tool.Get("functionDeclarations").ForEach(func(_, decl gjson.Result) bool {
					if converted := interactionsClaudeTool(decl); len(converted) > 0 {
						toolItems = append(toolItems, converted)
					}
					return true
				})
				return true
			}
			if converted := interactionsClaudeTool(tool); len(converted) > 0 {
				toolItems = append(toolItems, converted)
			}
			return true
		})
		if len(toolItems) > 0 {
			out, _ = sjson.SetRawBytes(out, "tools", translatorcommon.JoinRawArray(toolItems))
		}
	}

	if toolChoice := firstClaudeInteractionsExisting(root, "generation_config.tool_choice", "generationConfig.toolChoice", "tool_choice"); toolChoice.Exists() {
		switch toolChoice.Type {
		case gjson.String:
			switch strings.ToLower(toolChoice.String()) {
			case "none":
				out, _ = sjson.SetBytes(out, "tool_choice.type", "none")
			case "auto":
				out, _ = sjson.SetBytes(out, "tool_choice.type", "auto")
			case "required", "any":
				out, _ = sjson.SetBytes(out, "tool_choice.type", "any")
			}
		case gjson.JSON:
			if toolChoice.Get("type").String() == "function" {
				fnName := toolChoice.Get("function.name").String()
				if fnName != "" {
					out, _ = sjson.SetBytes(out, "tool_choice.type", "tool")
					out, _ = sjson.SetBytes(out, "tool_choice.name", fnName)
				}
			}
		}
	}

	out, _ = sjson.SetBytes(out, "stream", stream)
	return out
}

func interactionsClaudeTool(tool gjson.Result) []byte {
	name := tool.Get("name").String()
	if name == "" {
		name = tool.Get("function.name").String()
	}
	if name == "" {
		return nil
	}
	sanitizedName := util.SanitizeClaudeFunctionName(name)
	converted := []byte(`{"name":"","input_schema":{"type":"object","properties":{}}}`)
	converted, _ = sjson.SetBytes(converted, "name", sanitizedName)
	if desc := tool.Get("description"); desc.Exists() {
		converted, _ = sjson.SetBytes(converted, "description", desc.String())
	} else if desc := tool.Get("function.description"); desc.Exists() {
		converted, _ = sjson.SetBytes(converted, "description", desc.String())
	}

	params := firstClaudeInteractionsExisting(tool, "parameters", "parametersJsonSchema", "parameters_json_schema", "input_schema")
	if params.Exists() {
		converted, _ = sjson.SetRawBytes(converted, "input_schema", []byte(params.Raw))
	}
	return converted
}

func firstClaudeInteractionsExisting(root gjson.Result, paths ...string) gjson.Result {
	for _, p := range paths {
		if r := root.Get(p); r.Exists() {
			return r
		}
	}
	return gjson.Result{}
}
