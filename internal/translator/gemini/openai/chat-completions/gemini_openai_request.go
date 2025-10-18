// Package openai provides request translation functionality for OpenAI to Gemini API compatibility.
// It converts OpenAI Chat Completions requests into Gemini compatible JSON using gjson/sjson only.
package chat_completions

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

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
	// Base envelope
	out := []byte(`{"contents":[],"generationConfig":{"thinkingConfig":{"include_thoughts":true}}}`)

	// Model
	out, _ = sjson.SetBytes(out, "model", modelName)

	// Reasoning effort -> thinkingBudget/include_thoughts
	re := gjson.GetBytes(rawJSON, "reasoning_effort")
	if re.Exists() {
		switch re.String() {
		case "none":
			out, _ = sjson.DeleteBytes(out, "generationConfig.thinkingConfig.include_thoughts")
			out, _ = sjson.SetBytes(out, "generationConfig.thinkingConfig.thinkingBudget", 0)
		case "auto":
			out, _ = sjson.SetBytes(out, "generationConfig.thinkingConfig.thinkingBudget", -1)
		case "low":
			out, _ = sjson.SetBytes(out, "generationConfig.thinkingConfig.thinkingBudget", 1024)
		case "medium":
			out, _ = sjson.SetBytes(out, "generationConfig.thinkingConfig.thinkingBudget", 8192)
		case "high":
			out, _ = sjson.SetBytes(out, "generationConfig.thinkingConfig.thinkingBudget", 24576)
		default:
			out, _ = sjson.SetBytes(out, "generationConfig.thinkingConfig.thinkingBudget", -1)
		}
	} else {
		out, _ = sjson.SetBytes(out, "generationConfig.thinkingConfig.thinkingBudget", -1)
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
	// e.g. "modalities": ["image", "text"] -> ["Image", "Text"]
	if mods := gjson.GetBytes(rawJSON, "modalities"); mods.Exists() && mods.IsArray() {
		var responseMods []string
		for _, m := range mods.Array() {
			switch strings.ToLower(m.String()) {
			case "text":
				responseMods = append(responseMods, "Text")
			case "image":
				responseMods = append(responseMods, "Image")
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
					if c.Type == gjson.String {
						toolResponses[toolCallID] = c.String()
					} else if c.IsObject() && c.Get("type").String() == "text" {
						toolResponses[toolCallID] = c.Get("text").String()
					}
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
				if content.Type == gjson.String {
					// Assistant text -> single model content
					node := []byte(`{"role":"model","parts":[{"text":""}]}`)
					node, _ = sjson.SetBytes(node, "parts.0.text", content.String())
					out, _ = sjson.SetRawBytes(out, "contents.-1", node)
				} else if content.IsArray() {
					// Assistant multimodal content (e.g. text + image) -> single model content with parts
					node := []byte(`{"role":"model","parts":[]}`)
					p := 0
					for _, item := range content.Array() {
						switch item.Get("type").String() {
						case "text":
							node, _ = sjson.SetBytes(node, "parts."+itoa(p)+".text", item.Get("text").String())
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
					out, _ = sjson.SetRawBytes(out, "contents.-1", node)
				} else if !content.Exists() || content.Type == gjson.Null {
					// Tool calls -> single model content with functionCall parts
					tcs := m.Get("tool_calls")
					if tcs.IsArray() {
						node := []byte(`{"role":"model","parts":[]}`)
						p := 0
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
							p++
							if fid != "" {
								fIDs = append(fIDs, fid)
							}
						}
						out, _ = sjson.SetRawBytes(out, "contents.-1", node)

						// Append a single tool content combining name + response per function
						toolNode := []byte(`{"role":"tool","parts":[]}`)
						pp := 0
						for _, fid := range fIDs {
							if name, ok := tcID2Name[fid]; ok {
								toolNode, _ = sjson.SetBytes(toolNode, "parts."+itoa(pp)+".functionResponse.name", name)
								resp := toolResponses[fid]
								if resp == "" {
									resp = "{}"
								}
								toolNode, _ = sjson.SetRawBytes(toolNode, "parts."+itoa(pp)+".functionResponse.response", []byte(`{"result":`+quoteIfNeeded(resp)+`}`))
								pp++
							}
						}
						if pp > 0 {
							out, _ = sjson.SetRawBytes(out, "contents.-1", toolNode)
						}
					}
				}
			}
		}
	}

	// tools -> tools[0].functionDeclarations
	tools := gjson.GetBytes(rawJSON, "tools")
	if tools.IsArray() && len(tools.Array()) > 0 {
		out, _ = sjson.SetRawBytes(out, "tools", []byte(`[{"functionDeclarations":[]}]`))
		fdPath := "tools.0.functionDeclarations"
		for _, t := range tools.Array() {
			if t.Get("type").String() == "function" {
				fn := t.Get("function")
				if fn.Exists() && fn.IsObject() {
					parametersJsonSchema, _ := util.RenameKey(fn.Raw, "parameters", "parametersJsonSchema")
					out, _ = sjson.SetRawBytes(out, fdPath+".-1", []byte(parametersJsonSchema))
				}
			}
		}
	}

	return out
}

// itoa converts int to string without strconv import for few usages.
func itoa(i int) string { return fmt.Sprintf("%d", i) }

// quoteIfNeeded ensures a string is valid JSON value (quotes plain text), pass-through for JSON objects/arrays.
func quoteIfNeeded(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "\"\""
	}
	if len(s) > 0 && (s[0] == '{' || s[0] == '[') {
		return s
	}
	// escape quotes minimally
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	return "\"" + s + "\""
}
