// Package claude provides request translation functionality for Claude Code API compatibility.
// This package handles the conversion of Claude Code API requests into Gemini CLI-compatible
// JSON format, transforming message contents, system instructions, and tool declarations
// into the format expected by Gemini CLI API clients. It performs JSON data transformation
// to ensure compatibility between Claude Code API format and Gemini CLI API's expected format.
package claude

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"regexp"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator/gemini/common"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertClaudeRequestToAntigravity parses and transforms a Claude Code API request into Gemini CLI API format.
// It extracts the model name, system instruction, message contents, and tool declarations
// from the raw JSON request and returns them in the format expected by the Gemini CLI API.
// The function performs the following transformations:
// 1. Extracts the model information from the request
// 2. Restructures the JSON to match Gemini CLI API format
// 3. Converts system instructions to the expected format
// 4. Maps message contents with proper role transformations
// 5. Handles tool declarations and tool choices
// 6. Maps generation configuration parameters
//
// Parameters:
//   - modelName: The name of the model to use for the request
//   - rawJSON: The raw JSON request data from the Claude Code API
//   - stream: A boolean indicating if the request is for a streaming response (unused in current implementation)
//
// Returns:
//   - []byte: The transformed request data in Gemini CLI API format
func ConvertClaudeRequestToAntigravity(modelName string, inputRawJSON []byte, _ bool) []byte {
	enableThoughtTranslate := true
	rawJSON := bytes.Clone(inputRawJSON)

	// system instruction
	systemInstructionJSON := ""
	hasSystemInstruction := false
	systemResult := gjson.GetBytes(rawJSON, "system")
	if systemResult.IsArray() {
		systemResults := systemResult.Array()
		systemInstructionJSON = `{"role":"user","parts":[]}`
		for i := 0; i < len(systemResults); i++ {
			systemPromptResult := systemResults[i]
			systemTypePromptResult := systemPromptResult.Get("type")
			if systemTypePromptResult.Type == gjson.String && systemTypePromptResult.String() == "text" {
				systemPrompt := systemPromptResult.Get("text").String()
				partJSON := `{}`
				if systemPrompt != "" {
					partJSON, _ = sjson.Set(partJSON, "text", systemPrompt)
				}
				systemInstructionJSON, _ = sjson.SetRaw(systemInstructionJSON, "parts.-1", partJSON)
				hasSystemInstruction = true
			}
		}
	} else if systemResult.Type == gjson.String {
		systemInstructionJSON = `{"role":"user","parts":[{"text":""}]}`
		systemInstructionJSON, _ = sjson.Set(systemInstructionJSON, "parts.0.text", systemResult.String())
		hasSystemInstruction = true
	}

	// contents
	contentsJSON := "[]"
	hasContents := false

	messagesResult := gjson.GetBytes(rawJSON, "messages")
	if messagesResult.IsArray() {
		messageResults := messagesResult.Array()
		numMessages := len(messageResults)
		for i := 0; i < numMessages; i++ {
			messageResult := messageResults[i]
			roleResult := messageResult.Get("role")
			if roleResult.Type != gjson.String {
				continue
			}
			originalRole := roleResult.String()
			role := originalRole
			if role == "assistant" {
				role = "model"
			}
			clientContentJSON := `{"role":"","parts":[]}`
			clientContentJSON, _ = sjson.Set(clientContentJSON, "role", role)
			contentsResult := messageResult.Get("content")
			if contentsResult.IsArray() {
				contentResults := contentsResult.Array()
				numContents := len(contentResults)
				var currentMessageThinkingSignature string
				for j := 0; j < numContents; j++ {
					contentResult := contentResults[j]
					contentTypeResult := contentResult.Get("type")
					if contentTypeResult.Type == gjson.String && contentTypeResult.String() == "thinking" {
						// Use GetThinkingText to handle wrapped thinking objects
						thinkingText := thinking.GetThinkingText(contentResult)

						signature := ""
						if cache.SignatureCacheEnabled() {
							// Cache mode: prefer cached signature, validate length
							if thinkingText != "" {
								if cachedSig := cache.GetCachedSignature(modelName, thinkingText); cachedSig != "" {
									signature = cachedSig
								}
							}

							// Fallback to client signature if cache miss
							if signature == "" {
								signatureResult := contentResult.Get("signature")
								if signatureResult.Exists() && signatureResult.String() != "" {
									clientSig := ""
									arrayClientSignatures := strings.SplitN(signatureResult.String(), "#", 2)
									if len(arrayClientSignatures) == 2 {
										if modelName == arrayClientSignatures[0] || cache.GetModelGroup(modelName) == arrayClientSignatures[0] {
											clientSig = arrayClientSignatures[1]
										}
									}
									if cache.HasValidSignature(modelName, clientSig) {
										signature = clientSig
									}
								}
							}

							// Store for subsequent tool_use in the same message
							if cache.HasValidSignature(modelName, signature) {
								currentMessageThinkingSignature = signature
							}

							// Skip unsigned thinking blocks when cache is enabled
							if !cache.HasValidSignature(modelName, signature) {
								log.Warnf("antigravity claude request: dropping thinking block with invalid signature (cache mode, textLen=%d)", len(thinkingText))
								enableThoughtTranslate = false
								continue
							}
						} else {
							// Bypass mode: validate Claude signature format, drop invalid
							signatureResult := contentResult.Get("signature")
							if signatureResult.Exists() && signatureResult.String() != "" {
								rawSig := signatureResult.String()
								if isValidClaudeSignature(rawSig) {
									signature = normalizeSignature(rawSig)
									currentMessageThinkingSignature = signature
								}
							}
							// Invalid/missing signature: drop thinking block (Antigravity validates signatures strictly)
							if signature == "" {
								log.Warnf("antigravity claude request: dropping thinking block with invalid signature (bypass mode, textLen=%d)", len(thinkingText))
								enableThoughtTranslate = false
								continue
							}
						}

						// Send as thought block
						partJSON := `{}`
						partJSON, _ = sjson.Set(partJSON, "thought", true)
						if thinkingText != "" {
							partJSON, _ = sjson.Set(partJSON, "text", thinkingText)
						}
						partJSON, _ = sjson.Set(partJSON, "thoughtSignature", signature)
						clientContentJSON, _ = sjson.SetRaw(clientContentJSON, "parts.-1", partJSON)
					} else if contentTypeResult.Type == gjson.String && contentTypeResult.String() == "text" {
						prompt := contentResult.Get("text").String()
						// Skip empty text parts to avoid Gemini API error:
						// "required oneof field 'data' must have one initialized field"
						if prompt == "" {
							continue
						}
						partJSON := `{}`
						partJSON, _ = sjson.Set(partJSON, "text", prompt)
						clientContentJSON, _ = sjson.SetRaw(clientContentJSON, "parts.-1", partJSON)
					} else if contentTypeResult.Type == gjson.String && contentTypeResult.String() == "tool_use" {
						// NOTE: Do NOT inject dummy thinking blocks here.
						// Antigravity API validates signatures, so dummy values are rejected.

						functionName := contentResult.Get("name").String()
						argsResult := contentResult.Get("input")
						functionID := contentResult.Get("id").String()

						// Handle both object and string input formats
						var argsRaw string
						if argsResult.IsObject() {
							argsRaw = argsResult.Raw
						} else if argsResult.Type == gjson.String {
							// Input is a JSON string, parse and validate it
							parsed := gjson.Parse(argsResult.String())
							if parsed.IsObject() {
								argsRaw = parsed.Raw
							}
						}

						if argsRaw != "" {
							partJSON := `{}`

							// Attach signature for tool calls
							if cache.SignatureCacheEnabled() {
								if cache.HasValidSignature(modelName, currentMessageThinkingSignature) {
									partJSON, _ = sjson.Set(partJSON, "thoughtSignature", currentMessageThinkingSignature)
								} else {
									const skipSentinel = "skip_thought_signature_validator"
									partJSON, _ = sjson.Set(partJSON, "thoughtSignature", skipSentinel)
								}
							} else {
								if currentMessageThinkingSignature != "" {
									partJSON, _ = sjson.Set(partJSON, "thoughtSignature", currentMessageThinkingSignature)
								} else {
									const skipSentinel = "skip_thought_signature_validator"
									partJSON, _ = sjson.Set(partJSON, "thoughtSignature", skipSentinel)
								}
							}

							if functionID != "" {
								partJSON, _ = sjson.Set(partJSON, "functionCall.id", functionID)
							}
							partJSON, _ = sjson.Set(partJSON, "functionCall.name", functionName)
							partJSON, _ = sjson.SetRaw(partJSON, "functionCall.args", argsRaw)
							clientContentJSON, _ = sjson.SetRaw(clientContentJSON, "parts.-1", partJSON)
						}
					} else if contentTypeResult.Type == gjson.String && contentTypeResult.String() == "tool_result" {
						toolCallID := contentResult.Get("tool_use_id").String()
						if toolCallID != "" {
							funcName := toolCallID
							toolCallIDs := strings.Split(toolCallID, "-")
							if len(toolCallIDs) > 1 {
								funcName = strings.Join(toolCallIDs[0:len(toolCallIDs)-2], "-")
							}
							functionResponseResult := contentResult.Get("content")

							functionResponseJSON := `{}`
							functionResponseJSON, _ = sjson.Set(functionResponseJSON, "id", toolCallID)
							functionResponseJSON, _ = sjson.Set(functionResponseJSON, "name", funcName)

							responseData := ""
							if functionResponseResult.Type == gjson.String {
								responseData = functionResponseResult.String()
								functionResponseJSON, _ = sjson.Set(functionResponseJSON, "response.result", responseData)
							} else if functionResponseResult.IsArray() {
								frResults := functionResponseResult.Array()
								if len(frResults) == 1 {
									functionResponseJSON, _ = sjson.SetRaw(functionResponseJSON, "response.result", frResults[0].Raw)
								} else {
									functionResponseJSON, _ = sjson.SetRaw(functionResponseJSON, "response.result", functionResponseResult.Raw)
								}

							} else if functionResponseResult.IsObject() {
								functionResponseJSON, _ = sjson.SetRaw(functionResponseJSON, "response.result", functionResponseResult.Raw)
							} else {
								functionResponseJSON, _ = sjson.SetRaw(functionResponseJSON, "response.result", functionResponseResult.Raw)
							}

							partJSON := `{}`
							partJSON, _ = sjson.SetRaw(partJSON, "functionResponse", functionResponseJSON)
							clientContentJSON, _ = sjson.SetRaw(clientContentJSON, "parts.-1", partJSON)
						}
					} else if contentTypeResult.Type == gjson.String && contentTypeResult.String() == "image" {
						sourceResult := contentResult.Get("source")
						if sourceResult.Get("type").String() == "base64" {
							inlineDataJSON := `{}`
							if mimeType := sourceResult.Get("media_type").String(); mimeType != "" {
								inlineDataJSON, _ = sjson.Set(inlineDataJSON, "mime_type", mimeType)
							}
							if data := sourceResult.Get("data").String(); data != "" {
								inlineDataJSON, _ = sjson.Set(inlineDataJSON, "data", data)
							}

							partJSON := `{}`
							partJSON, _ = sjson.SetRaw(partJSON, "inlineData", inlineDataJSON)
							clientContentJSON, _ = sjson.SetRaw(clientContentJSON, "parts.-1", partJSON)
						}
					}
				}

				// Reorder parts for 'model' role to ensure thinking block is first
				if role == "model" {
					partsResult := gjson.Get(clientContentJSON, "parts")
					if partsResult.IsArray() {
						parts := partsResult.Array()
						var thinkingParts []gjson.Result
						var otherParts []gjson.Result
						for _, part := range parts {
							if part.Get("thought").Bool() {
								thinkingParts = append(thinkingParts, part)
							} else {
								otherParts = append(otherParts, part)
							}
						}
						if len(thinkingParts) > 0 {
							firstPartIsThinking := parts[0].Get("thought").Bool()
							if !firstPartIsThinking || len(thinkingParts) > 1 {
								var newParts []interface{}
								for _, p := range thinkingParts {
									newParts = append(newParts, p.Value())
								}
								for _, p := range otherParts {
									newParts = append(newParts, p.Value())
								}
								clientContentJSON, _ = sjson.Set(clientContentJSON, "parts", newParts)
							}
						}
					}
				}

				// Skip messages with empty parts array to avoid Gemini API error:
				// "required oneof field 'data' must have one initialized field"
				partsCheck := gjson.Get(clientContentJSON, "parts")
				if !partsCheck.IsArray() || len(partsCheck.Array()) == 0 {
					continue
				}

				contentsJSON, _ = sjson.SetRaw(contentsJSON, "-1", clientContentJSON)
				hasContents = true
			} else if contentsResult.Type == gjson.String {
				prompt := contentsResult.String()
				partJSON := `{}`
				if prompt != "" {
					partJSON, _ = sjson.Set(partJSON, "text", prompt)
				}
				clientContentJSON, _ = sjson.SetRaw(clientContentJSON, "parts.-1", partJSON)
				contentsJSON, _ = sjson.SetRaw(contentsJSON, "-1", clientContentJSON)
				hasContents = true
			}
		}
	}

	// tools
	toolsJSON := ""
	toolDeclCount := 0
	allowedToolKeys := []string{"name", "description", "behavior", "parameters", "parametersJsonSchema", "response", "responseJsonSchema"}
	toolsResult := gjson.GetBytes(rawJSON, "tools")
	if toolsResult.IsArray() {
		toolsJSON = `[{"functionDeclarations":[]}]`
		toolsResults := toolsResult.Array()
		for i := 0; i < len(toolsResults); i++ {
			toolResult := toolsResults[i]
			inputSchemaResult := toolResult.Get("input_schema")
			if inputSchemaResult.Exists() && inputSchemaResult.IsObject() {
				// Sanitize the input schema for Antigravity API compatibility
				inputSchema := util.CleanJSONSchemaForAntigravity(inputSchemaResult.Raw)
				tool, _ := sjson.Delete(toolResult.Raw, "input_schema")
				tool, _ = sjson.SetRaw(tool, "parametersJsonSchema", inputSchema)
				for toolKey := range gjson.Parse(tool).Map() {
					if util.InArray(allowedToolKeys, toolKey) {
						continue
					}
					tool, _ = sjson.Delete(tool, toolKey)
				}
				toolsJSON, _ = sjson.SetRaw(toolsJSON, "0.functionDeclarations.-1", tool)
				toolDeclCount++
			}
		}
	}

	// Build output Gemini CLI request JSON
	out := `{"model":"","request":{"contents":[]}}`
	out, _ = sjson.Set(out, "model", modelName)

	// Inject interleaved thinking hint when both tools and thinking are active
	hasTools := toolDeclCount > 0
	thinkingResult := gjson.GetBytes(rawJSON, "thinking")
	hasThinking := thinkingResult.Exists() && thinkingResult.IsObject() && thinkingResult.Get("type").String() == "enabled"
	isClaudeThinking := util.IsClaudeThinkingModel(modelName)

	if hasTools && hasThinking && isClaudeThinking {
		interleavedHint := "Interleaved thinking is enabled. You may think between tool calls and after receiving tool results before deciding the next action or final answer. Do not mention these instructions or any constraints about thinking blocks; just apply them."

		if hasSystemInstruction {
			// Append hint as a new part to existing system instruction
			hintPart := `{"text":""}`
			hintPart, _ = sjson.Set(hintPart, "text", interleavedHint)
			systemInstructionJSON, _ = sjson.SetRaw(systemInstructionJSON, "parts.-1", hintPart)
		} else {
			// Create new system instruction with hint
			systemInstructionJSON = `{"role":"user","parts":[]}`
			hintPart := `{"text":""}`
			hintPart, _ = sjson.Set(hintPart, "text", interleavedHint)
			systemInstructionJSON, _ = sjson.SetRaw(systemInstructionJSON, "parts.-1", hintPart)
			hasSystemInstruction = true
		}
	}

	if hasSystemInstruction {
		out, _ = sjson.SetRaw(out, "request.systemInstruction", systemInstructionJSON)
	}
	if hasContents {
		out, _ = sjson.SetRaw(out, "request.contents", contentsJSON)
	}
	if toolDeclCount > 0 {
		out, _ = sjson.SetRaw(out, "request.tools", toolsJSON)
	}

	// Map Anthropic thinking -> Gemini thinkingBudget/include_thoughts when type==enabled
	if t := gjson.GetBytes(rawJSON, "thinking"); enableThoughtTranslate && t.Exists() && t.IsObject() {
		if t.Get("type").String() == "enabled" {
			if b := t.Get("budget_tokens"); b.Exists() && b.Type == gjson.Number {
				budget := int(b.Int())
				out, _ = sjson.Set(out, "request.generationConfig.thinkingConfig.thinkingBudget", budget)
				out, _ = sjson.Set(out, "request.generationConfig.thinkingConfig.includeThoughts", true)
			}
		}
	}
	if v := gjson.GetBytes(rawJSON, "temperature"); v.Exists() && v.Type == gjson.Number {
		out, _ = sjson.Set(out, "request.generationConfig.temperature", v.Num)
	}
	if v := gjson.GetBytes(rawJSON, "top_p"); v.Exists() && v.Type == gjson.Number {
		out, _ = sjson.Set(out, "request.generationConfig.topP", v.Num)
	}
	if v := gjson.GetBytes(rawJSON, "top_k"); v.Exists() && v.Type == gjson.Number {
		out, _ = sjson.Set(out, "request.generationConfig.topK", v.Num)
	}
	if v := gjson.GetBytes(rawJSON, "max_tokens"); v.Exists() && v.Type == gjson.Number {
		out, _ = sjson.Set(out, "request.generationConfig.maxOutputTokens", v.Num)
	}

	outBytes := []byte(out)
	outBytes = common.AttachDefaultSafetySettings(outBytes, "request.safetySettings")

	return outBytes
}

// normalizeSignature converts signatures to the format required by Antigravity (2-layer Base64).
// Used in bypass mode (signature-cache-enabled: false) to ensure correct signature format.
//
// Input formats:
//   - "xxx#..." (CLIProxyAPI format) -> extract signature after "#"
//   - "E..." (Anthropic, 1-layer Base64) -> Base64 encode to 2-layer
//   - "R..." (Vertex, 2-layer Base64) -> return as-is
//
// Output: raw signature in 2-layer Base64 format (no prefix), ready for Antigravity.
func normalizeSignature(rawSignature string) string {
	// Extract signature if it has "xxx#" prefix
	if idx := strings.Index(rawSignature, "#"); idx != -1 {
		rawSignature = rawSignature[idx+1:]
	}

	// E... (Anthropic, 1-layer) -> Base64 encode to 2-layer
	if strings.HasPrefix(rawSignature, "E") {
		return base64.StdEncoding.EncodeToString([]byte(rawSignature))
	}

	// R... (Vertex, 2-layer) or other formats -> return as-is
	return rawSignature
}

// isValidClaudeSignature validates Claude signature format in bypass mode.
// Claude signatures have the following characteristics:
//   - Start with "E" (1-layer Base64) or "R" (2-layer Base64)
//   - After decoding all Base64 layers, the first byte must be 0x12 (Protobuf Field 2 tag)
//
// Signature block byte patterns (after full decode):
//   - Max subscription: 08 0c 18 02 (Channel=12)
//   - Anthropic API/Azure: 08 0b 18 02 (Channel=11)
//   - AWS Bedrock: 08 0b 10 01 18 02 (Channel=11, Field2=1)
//   - Google Vertex: 08 0b 10 02 18 02 (Channel=11, Field2=2)
func isValidClaudeSignature(rawSignature string) bool {
	if rawSignature == "" {
		return false
	}

	// Extract signature if it has "xxx#" prefix
	sig := rawSignature
	if idx := strings.Index(sig, "#"); idx != -1 {
		sig = sig[idx+1:]
	}

	if sig == "" {
		return false
	}

	// Minimum length check (Claude signatures are substantial)
	if len(sig) < 50 {
		return false
	}

	// Decode to get the raw Protobuf bytes
	var decoded []byte

	if strings.HasPrefix(sig, "R") {
		// R... (2-layer Base64): decode twice
		firstLayer, err := base64.StdEncoding.DecodeString(sig)
		if err != nil {
			return false
		}
		// First layer should give us E... format
		if len(firstLayer) == 0 || firstLayer[0] != 'E' {
			return false
		}
		var decodeErr error
		decoded, decodeErr = base64.StdEncoding.DecodeString(string(firstLayer))
		if decodeErr != nil {
			return false
		}
	} else if strings.HasPrefix(sig, "E") {
		// E... (1-layer Base64): decode once
		var err error
		decoded, err = base64.StdEncoding.DecodeString(sig)
		if err != nil {
			return false
		}
	} else {
		// Unknown format
		return false
	}

	// Check Protobuf structure: first byte must be 0x12 (Field 2, wire type 2)
	if len(decoded) < 4 {
		return false
	}
	if decoded[0] != 0x12 {
		return false
	}

	return true
}
