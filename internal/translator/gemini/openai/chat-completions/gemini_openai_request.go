// Package openai provides request translation functionality for OpenAI to Gemini API compatibility.
// It converts OpenAI Chat Completions requests into Gemini compatible JSON using gjson/sjson only.
package chat_completions

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator/gemini/common"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const geminiFunctionThoughtSignature = "skip_thought_signature_validator"

// ConvertOpenAIRequestToGemini converts an OpenAI Chat Completions request (raw JSON)
// into a complete Gemini request JSON. All JSON construction uses sjson and lookups use gjson.
//
// Parameters:
//   - modelName: The name of the model to use for the request
//   - rawJSON: The raw JSON request data from the OpenAI API
//   - stream: A boolean indicating if the request is for a streaming response (unused in current implementation)
//
// Returns:
//   - []byte: The transformed request data in Gemini API format
func ConvertOpenAIRequestToGemini(modelName string, inputRawJSON []byte, _ bool) []byte {
	rawJSON := bytes.Clone(inputRawJSON)
	// Base envelope (no default thinkingConfig)
	out := []byte(`{"contents":[]}`)

	// Model
	out, _ = sjson.SetBytes(out, "model", modelName)

	// Reasoning effort -> thinkingBudget/include_thoughts
	// Note: OpenAI official fields take precedence over extra_body.google.thinking_config
	// Only apply numeric budgets for models that use budgets (not discrete levels) to avoid
	// incorrectly applying thinkingBudget for level-based models like gpt-5. Gemini 3 models
	// use thinkingLevel/includeThoughts instead.
	re := gjson.GetBytes(rawJSON, "reasoning_effort")
	hasOfficialThinking := re.Exists()
	if hasOfficialThinking && util.ModelSupportsThinking(modelName) {
		effort := strings.ToLower(strings.TrimSpace(re.String()))
		if util.IsGemini3Model(modelName) {
			switch effort {
			case "none":
				out, _ = sjson.DeleteBytes(out, "generationConfig.thinkingConfig")
			case "auto":
				includeThoughts := true
				out = util.ApplyGeminiThinkingLevel(out, "", &includeThoughts)
			default:
				if level, ok := util.ValidateGemini3ThinkingLevel(modelName, effort); ok {
					out = util.ApplyGeminiThinkingLevel(out, level, nil)
				}
			}
		} else if !util.ModelUsesThinkingLevels(modelName) {
			out = util.ApplyReasoningEffortToGemini(out, effort)
		}
	}

	// Cherry Studio extension extra_body.google.thinking_config (effective only when official fields are absent)
	// Only apply for models that use numeric budgets, not discrete levels.
	if !hasOfficialThinking && util.ModelSupportsThinking(modelName) && !util.ModelUsesThinkingLevels(modelName) {
		if tc := gjson.GetBytes(rawJSON, "extra_body.google.thinking_config"); tc.Exists() && tc.IsObject() {
			var setBudget bool
			var budget int

			if v := tc.Get("thinkingBudget"); v.Exists() {
				budget = int(v.Int())
				out, _ = sjson.SetBytes(out, "generationConfig.thinkingConfig.thinkingBudget", budget)
				setBudget = true
			} else if v := tc.Get("thinking_budget"); v.Exists() {
				budget = int(v.Int())
				out, _ = sjson.SetBytes(out, "generationConfig.thinkingConfig.thinkingBudget", budget)
				setBudget = true
			}

			if v := tc.Get("includeThoughts"); v.Exists() {
				out, _ = sjson.SetBytes(out, "generationConfig.thinkingConfig.include_thoughts", v.Bool())
			} else if v := tc.Get("include_thoughts"); v.Exists() {
				out, _ = sjson.SetBytes(out, "generationConfig.thinkingConfig.include_thoughts", v.Bool())
			} else if setBudget && budget != 0 {
				out, _ = sjson.SetBytes(out, "generationConfig.thinkingConfig.include_thoughts", true)
			}
		}
	}

	// Temperature/top_p/top_k
	if tr := gjson.GetBytes(rawJSON, "temperature"); tr.Exists() && tr.Type == gjson.Number {
		out, _ = sjson.SetBytes(out, "generationConfig.temperature", tr.Num)
	}
	if tpr := gjson.GetBytes(rawJSON, "top_p"); tpr.Exists() && tpr.Type == gjson.Number {
		out, _ = sjson.SetBytes(out, "generationConfig.topP", tpr.Num)
	}
	if tkr := gjson.GetBytes(rawJSON, "top_k"); tkr.Exists() && tkr.Type == gjson.Number {
		out, _ = sjson.SetBytes(out, "generationConfig.topK", tkr.Num)
	}

	// Map OpenAI modalities -> Gemini generationConfig.responseModalities
	// e.g. "modalities": ["image", "text"] -> ["IMAGE", "TEXT"]
	if mods := gjson.GetBytes(rawJSON, "modalities"); mods.Exists() && mods.IsArray() {
		var responseMods []string
		for _, m := range mods.Array() {
			switch strings.ToLower(m.String()) {
			case "text":
				responseMods = append(responseMods, "TEXT")
			case "image":
				responseMods = append(responseMods, "IMAGE")
			}
		}
		if len(responseMods) > 0 {
			out, _ = sjson.SetBytes(out, "generationConfig.responseModalities", responseMods)
		}
	}

	// OpenRouter-style image_config support
	// If the input uses top-level image_config.aspect_ratio, map it into generationConfig.imageConfig.aspectRatio.
	if imgCfg := gjson.GetBytes(rawJSON, "image_config"); imgCfg.Exists() && imgCfg.IsObject() {
		if ar := imgCfg.Get("aspect_ratio"); ar.Exists() && ar.Type == gjson.String {
			out, _ = sjson.SetBytes(out, "generationConfig.imageConfig.aspectRatio", ar.Str)
		}
		if size := imgCfg.Get("image_size"); size.Exists() && size.Type == gjson.String {
			out, _ = sjson.SetBytes(out, "generationConfig.imageConfig.imageSize", size.Str)
		}
	}

	// messages -> systemInstruction + contents
	messages := gjson.GetBytes(rawJSON, "messages")
	if messages.IsArray() {
		arr := messages.Array()
		// First pass: assistant tool_calls id->name map
		tcID2Name := map[string]string{}
		for i := 0; i < len(arr); i++ {
			m := arr[i]
			if m.Get("role").String() == "assistant" {
				tcs := m.Get("tool_calls")
				if tcs.IsArray() {
					for _, tc := range tcs.Array() {
						if tc.Get("type").String() == "function" {
							id := tc.Get("id").String()
							name := tc.Get("function.name").String()
							if id != "" && name != "" {
								tcID2Name[id] = name
							}
						}
					}
				}
			}
		}

		// Second pass build systemInstruction/tool responses cache
		toolResponses := map[string]string{} // tool_call_id -> response text
		for i := 0; i < len(arr); i++ {
			m := arr[i]
			role := m.Get("role").String()
			if role == "tool" {
				toolCallID := m.Get("tool_call_id").String()
				if toolCallID != "" {
					c := m.Get("content")
					toolResponses[toolCallID] = c.Raw
				}
			}
		}

		for i := 0; i < len(arr); i++ {
			m := arr[i]
			role := m.Get("role").String()
			content := m.Get("content")

			if role == "system" && len(arr) > 1 {
				// system -> system_instruction as a user message style
				if content.Type == gjson.String {
					out, _ = sjson.SetBytes(out, "system_instruction.role", "user")
					out, _ = sjson.SetBytes(out, "system_instruction.parts.0.text", content.String())
				} else if content.IsObject() && content.Get("type").String() == "text" {
					out, _ = sjson.SetBytes(out, "system_instruction.role", "user")
					out, _ = sjson.SetBytes(out, "system_instruction.parts.0.text", content.Get("text").String())
				} else if content.IsArray() {
					contents := content.Array()
					if len(contents) > 0 {
						out, _ = sjson.SetBytes(out, "request.systemInstruction.role", "user")
						for j := 0; j < len(contents); j++ {
							out, _ = sjson.SetBytes(out, fmt.Sprintf("request.systemInstruction.parts.%d.text", j), contents[j].Get("text").String())
						}
					}
				}
			} else if role == "user" || (role == "system" && len(arr) == 1) {
				// Build single user content node to avoid splitting into multiple contents
				node := []byte(`{"role":"user","parts":[]}`)
				if content.Type == gjson.String {
					node, _ = sjson.SetBytes(node, "parts.0.text", content.String())
				} else if content.IsArray() {
					items := content.Array()
					p := 0
					for _, item := range items {
						switch item.Get("type").String() {
						case "text":
							node, _ = sjson.SetBytes(node, "parts."+itoa(p)+".text", item.Get("text").String())
							p++
						case "image_url":
							imageURL := item.Get("image_url.url").String()
							if len(imageURL) > 5 {
								pieces := strings.SplitN(imageURL[5:], ";", 2)
								if len(pieces) == 2 && len(pieces[1]) > 7 {
									mime := pieces[0]
									data := pieces[1][7:]
									node, _ = sjson.SetBytes(node, "parts."+itoa(p)+".inlineData.mime_type", mime)
									node, _ = sjson.SetBytes(node, "parts."+itoa(p)+".inlineData.data", data)
									p++
								}
							}
						case "file":
							filename := item.Get("file.filename").String()
							fileData := item.Get("file.file_data").String()
							ext := ""
							if sp := strings.Split(filename, "."); len(sp) > 1 {
								ext = sp[len(sp)-1]
							}
							if mimeType, ok := misc.MimeTypes[ext]; ok {
								node, _ = sjson.SetBytes(node, "parts."+itoa(p)+".inlineData.mime_type", mimeType)
								node, _ = sjson.SetBytes(node, "parts."+itoa(p)+".inlineData.data", fileData)
								p++
							} else {
								log.Warnf("Unknown file name extension '%s' in user message, skip", ext)
							}
						}
					}
				}
				out, _ = sjson.SetRawBytes(out, "contents.-1", node)
			} else if role == "assistant" {
				node := []byte(`{"role":"model","parts":[]}`)
				p := 0
				if content.Type == gjson.String {
					// Assistant text -> single model content
					node, _ = sjson.SetBytes(node, "parts.-1.text", content.String())
					p++
				} else if content.IsArray() {
					// Assistant multimodal content (e.g. text + image) -> single model content with parts
					for _, item := range content.Array() {
						switch item.Get("type").String() {
						case "text":
							p++
						case "image_url":
							// If the assistant returned an inline data URL, preserve it for history fidelity.
							imageURL := item.Get("image_url.url").String()
							if len(imageURL) > 5 { // expect data:...
								pieces := strings.SplitN(imageURL[5:], ";", 2)
								if len(pieces) == 2 && len(pieces[1]) > 7 {
									mime := pieces[0]
									data := pieces[1][7:]
									node, _ = sjson.SetBytes(node, "parts."+itoa(p)+".inlineData.mime_type", mime)
									node, _ = sjson.SetBytes(node, "parts."+itoa(p)+".inlineData.data", data)
									p++
								}
							}
						}
					}
				}

				// Tool calls -> single model content with functionCall parts
				tcs := m.Get("tool_calls")
				if tcs.IsArray() {
					fIDs := make([]string, 0)
					for _, tc := range tcs.Array() {
						if tc.Get("type").String() != "function" {
							continue
						}
						fid := tc.Get("id").String()
						fname := tc.Get("function.name").String()
						fargs := tc.Get("function.arguments").String()
						node, _ = sjson.SetBytes(node, "parts."+itoa(p)+".functionCall.name", fname)
						node, _ = sjson.SetRawBytes(node, "parts."+itoa(p)+".functionCall.args", []byte(fargs))
						node, _ = sjson.SetBytes(node, "parts."+itoa(p)+".thoughtSignature", geminiFunctionThoughtSignature)
						p++
						if fid != "" {
							fIDs = append(fIDs, fid)
						}
					}
					out, _ = sjson.SetRawBytes(out, "contents.-1", node)

					// Append a single tool content combining name + response per function
					toolNode := []byte(`{"role":"user","parts":[]}`)
					pp := 0
					for _, fid := range fIDs {
						if name, ok := tcID2Name[fid]; ok {
							toolNode, _ = sjson.SetBytes(toolNode, "parts."+itoa(pp)+".functionResponse.name", name)
							resp := toolResponses[fid]
							if resp == "" {
								resp = "{}"
							}
							toolNode, _ = sjson.SetBytes(toolNode, "parts."+itoa(pp)+".functionResponse.response.result", []byte(resp))
							pp++
						}
					}
					if pp > 0 {
						out, _ = sjson.SetRawBytes(out, "contents.-1", toolNode)
					}
				} else {
					out, _ = sjson.SetRawBytes(out, "contents.-1", node)
				}
			}
		}
	}

	// tools -> tools[0].functionDeclarations + tools[0].googleSearch passthrough
	tools := gjson.GetBytes(rawJSON, "tools")
	if tools.IsArray() && len(tools.Array()) > 0 {
		toolNode := []byte(`{}`)
		hasTool := false
		hasFunction := false
		for _, t := range tools.Array() {
			if t.Get("type").String() == "function" {
				fn := t.Get("function")
				if fn.Exists() && fn.IsObject() {
					fnRaw := fn.Raw
					if fn.Get("parameters").Exists() {
						renamed, errRename := util.RenameKey(fnRaw, "parameters", "parametersJsonSchema")
						if errRename != nil {
							log.Warnf("Failed to rename parameters for tool '%s': %v", fn.Get("name").String(), errRename)
							var errSet error
							fnRaw, errSet = sjson.Set(fnRaw, "parametersJsonSchema.type", "object")
							if errSet != nil {
								log.Warnf("Failed to set default schema type for tool '%s': %v", fn.Get("name").String(), errSet)
								continue
							}
							fnRaw, errSet = sjson.SetRaw(fnRaw, "parametersJsonSchema.properties", `{}`)
							if errSet != nil {
								log.Warnf("Failed to set default schema properties for tool '%s': %v", fn.Get("name").String(), errSet)
								continue
							}
						} else {
							fnRaw = renamed
						}
					} else {
						var errSet error
						fnRaw, errSet = sjson.Set(fnRaw, "parametersJsonSchema.type", "object")
						if errSet != nil {
							log.Warnf("Failed to set default schema type for tool '%s': %v", fn.Get("name").String(), errSet)
							continue
						}
						fnRaw, errSet = sjson.SetRaw(fnRaw, "parametersJsonSchema.properties", `{}`)
						if errSet != nil {
							log.Warnf("Failed to set default schema properties for tool '%s': %v", fn.Get("name").String(), errSet)
							continue
						}
					}
					fnRaw, _ = sjson.Delete(fnRaw, "strict")
					if !hasFunction {
						toolNode, _ = sjson.SetRawBytes(toolNode, "functionDeclarations", []byte("[]"))
					}
					tmp, errSet := sjson.SetRawBytes(toolNode, "functionDeclarations.-1", []byte(fnRaw))
					if errSet != nil {
						log.Warnf("Failed to append tool declaration for '%s': %v", fn.Get("name").String(), errSet)
						continue
					}
					toolNode = tmp
					hasFunction = true
					hasTool = true
				}
			}
			if gs := t.Get("google_search"); gs.Exists() {
				var errSet error
				toolNode, errSet = sjson.SetRawBytes(toolNode, "googleSearch", []byte(gs.Raw))
				if errSet != nil {
					log.Warnf("Failed to set googleSearch tool: %v", errSet)
					continue
				}
				hasTool = true
			}
		}
		if hasTool {
			out, _ = sjson.SetRawBytes(out, "tools", []byte("[]"))
			out, _ = sjson.SetRawBytes(out, "tools.0", toolNode)
		}
	}

	out = common.AttachDefaultSafetySettings(out, "safetySettings")

	return out
}

// itoa converts int to string without strconv import for few usages.
func itoa(i int) string { return fmt.Sprintf("%d", i) }
