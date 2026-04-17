// Package openai provides utilities to translate OpenAI Chat Completions
// request JSON into OpenAI Responses API request JSON.
// It supports tools, multimodal text/image inputs, and Structured Outputs.
// The package handles the conversion of OpenAI API requests into the format
// expected by the OpenAI Responses API, including proper mapping of messages,
// tools, and generation parameters.
package chat_completions

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	"github.com/tidwall/gjson"
)

// ConvertOpenAIRequestToCodex converts an OpenAI Chat Completions request JSON
// into an OpenAI Responses API request JSON. The transformation follows the
// examples defined in docs/2.md exactly, including tools, multi-turn dialog,
// multimodal text/image handling, and Structured Outputs mapping.
//
// Parameters:
//   - modelName: The name of the model to use for the request
//   - rawJSON: The raw JSON request data from the OpenAI Chat Completions API
//   - stream: A boolean indicating if the request is for a streaming response
//
// Returns:
//   - []byte: The transformed request data in OpenAI Responses API format
func ConvertOpenAIRequestToCodex(modelName string, inputRawJSON []byte, stream bool) []byte {
	rawJSON := inputRawJSON
	out := codexRequest{
		Instructions:      "",
		Stream:            stream,
		Reasoning:         codexReasoning{Effort: "medium", Summary: "auto"},
		ParallelToolCalls: true,
		Include:           []string{"reasoning.encrypted_content"},
		Model:             modelName,
		Input:             make([]any, 0),
		Store:             false,
	}

	if v := gjson.GetBytes(rawJSON, "reasoning_effort"); v.Exists() {
		out.Reasoning.Effort = v.Value()
	}

	tools := gjson.GetBytes(rawJSON, "tools")
	originalToolNameMap := buildOriginalToolNameMap(tools)

	messages := gjson.GetBytes(rawJSON, "messages")
	if messages.IsArray() {
		messageItems := messages.Array()
		for i := 0; i < len(messageItems); i++ {
			m := messageItems[i]
			role := m.Get("role").String()

			if role == "tool" {
				out.Input = append(out.Input, codexFunctionCallOutput{
					Type:   "function_call_output",
					CallID: m.Get("tool_call_id").String(),
					Output: m.Get("content").String(),
				})
				continue
			}

			msg := codexMessage{
				Type:    "message",
				Role:    codexMessageRole(role),
				Content: make([]any, 0),
			}
			appendCodexMessageContent(&msg.Content, role, m.Get("content"))

			// Keep non-assistant messages even when content is empty.
			if role != "assistant" || len(msg.Content) > 0 {
				out.Input = append(out.Input, msg)
			}

			if role == "assistant" {
				appendCodexAssistantToolCalls(&out.Input, m.Get("tool_calls"), originalToolNameMap)
			}
		}
	}

	rf := gjson.GetBytes(rawJSON, "response_format")
	text := gjson.GetBytes(rawJSON, "text")
	if rf.Exists() {
		out.Text = &codexText{}

		switch rf.Get("type").String() {
		case "text":
			out.Text.Format = &codexTextFormat{Type: "text"}
		case "json_schema":
			if js := rf.Get("json_schema"); js.Exists() {
				out.Text.Format = &codexTextFormat{
					Type:   "json_schema",
					Name:   jsonFieldValue(js.Get("name")),
					Strict: jsonFieldValue(js.Get("strict")),
					Schema: util.RawJSON(js.Get("schema").Raw),
				}
			}
		}

		if v := text.Get("verbosity"); v.Exists() {
			out.Text.Verbosity = v.Value()
		}
	} else if v := text.Get("verbosity"); v.Exists() {
		out.Text = &codexText{Verbosity: v.Value()}
	}

	if tools.IsArray() {
		toolItems := tools.Array()
		out.Tools = make([]any, 0, len(toolItems))
		for i := 0; i < len(toolItems); i++ {
			t := toolItems[i]
			toolType := t.Get("type").String()
			if toolType != "" && toolType != "function" && t.IsObject() {
				out.Tools = append(out.Tools, util.RawJSON(t.Raw))
				continue
			}
			if toolType != "function" {
				continue
			}

			fn := t.Get("function")
			if !fn.Exists() {
				continue
			}
			out.Tools = append(out.Tools, codexTool{
				Type:        "function",
				Name:        resolveToolName(fn.Get("name").String(), originalToolNameMap),
				Description: jsonFieldValue(fn.Get("description")),
				Parameters:  util.RawJSON(fn.Get("parameters").Raw),
				Strict:      jsonFieldValue(fn.Get("strict")),
			})
		}
		if len(out.Tools) == 0 {
			out.Tools = nil
		}
	}

	if tc := gjson.GetBytes(rawJSON, "tool_choice"); tc.Exists() {
		switch {
		case tc.Type == gjson.String:
			out.ToolChoice = tc.String()
		case tc.IsObject():
			tcType := tc.Get("type").String()
			if tcType == "function" {
				out.ToolChoice = codexFunctionToolChoice{
					Type: "function",
					Name: resolveToolName(tc.Get("function.name").String(), originalToolNameMap),
				}
			} else if tcType != "" {
				out.ToolChoice = util.RawJSON(tc.Raw)
			}
		}
	}

	marshaled, _ := json.Marshal(out)
	return marshaled
}

type codexRequest struct {
	Instructions      string         `json:"instructions"`
	Stream            bool           `json:"stream"`
	Reasoning         codexReasoning `json:"reasoning"`
	ParallelToolCalls bool           `json:"parallel_tool_calls"`
	Include           []string       `json:"include"`
	Model             string         `json:"model"`
	Input             []any          `json:"input"`
	Text              *codexText     `json:"text,omitempty"`
	Tools             []any          `json:"tools,omitempty"`
	ToolChoice        any            `json:"tool_choice,omitempty"`
	Store             bool           `json:"store"`
}

type codexReasoning struct {
	Effort  any    `json:"effort"`
	Summary string `json:"summary"`
}

type codexText struct {
	Format    *codexTextFormat `json:"format,omitempty"`
	Verbosity any              `json:"verbosity,omitempty"`
}

type codexTextFormat struct {
	Type   string          `json:"type,omitempty"`
	Name   any             `json:"name,omitempty"`
	Strict any             `json:"strict,omitempty"`
	Schema json.RawMessage `json:"schema,omitempty"`
}

type codexMessage struct {
	Type    string `json:"type"`
	Role    string `json:"role"`
	Content []any  `json:"content"`
}

type codexTextContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type codexImageContent struct {
	Type     string `json:"type"`
	ImageURL string `json:"image_url,omitempty"`
}

type codexFileContent struct {
	Type     string `json:"type"`
	FileData string `json:"file_data"`
	Filename string `json:"filename,omitempty"`
}

type codexFunctionCall struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments"`
}

type codexFunctionCallOutput struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

type codexTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name,omitempty"`
	Description any             `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      any             `json:"strict,omitempty"`
}

type codexFunctionToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

func buildOriginalToolNameMap(tools gjson.Result) map[string]string {
	if !tools.IsArray() {
		return map[string]string{}
	}

	names := make([]string, 0, len(tools.Array()))
	toolItems := tools.Array()
	for i := 0; i < len(toolItems); i++ {
		t := toolItems[i]
		if t.Get("type").String() != "function" {
			continue
		}
		if name := t.Get("function.name"); name.Exists() {
			names = append(names, name.String())
		}
	}
	if len(names) == 0 {
		return map[string]string{}
	}
	return buildShortNameMap(names)
}

func codexMessageRole(role string) string {
	if role == "system" {
		return "developer"
	}
	return role
}

func codexTextPartType(role string) string {
	if role == "assistant" {
		return "output_text"
	}
	return "input_text"
}

func appendCodexMessageContent(parts *[]any, role string, content gjson.Result) {
	if !content.Exists() {
		return
	}

	if content.Type == gjson.String {
		if text := content.String(); text != "" {
			*parts = append(*parts, codexTextContent{
				Type: codexTextPartType(role),
				Text: text,
			})
		}
		return
	}

	if !content.IsArray() {
		return
	}

	contentItems := content.Array()
	for i := 0; i < len(contentItems); i++ {
		it := contentItems[i]
		switch it.Get("type").String() {
		case "text":
			*parts = append(*parts, codexTextContent{
				Type: codexTextPartType(role),
				Text: it.Get("text").String(),
			})
		case "image_url":
			if role == "user" {
				*parts = append(*parts, codexImageContent{
					Type:     "input_image",
					ImageURL: it.Get("image_url.url").String(),
				})
			}
		case "file":
			if role != "user" {
				continue
			}
			fileData := it.Get("file.file_data").String()
			if fileData == "" {
				continue
			}
			*parts = append(*parts, codexFileContent{
				Type:     "input_file",
				FileData: fileData,
				Filename: it.Get("file.filename").String(),
			})
		}
	}
}

func appendCodexAssistantToolCalls(input *[]any, toolCalls gjson.Result, originalToolNameMap map[string]string) {
	if !toolCalls.Exists() || !toolCalls.IsArray() {
		return
	}

	callItems := toolCalls.Array()
	for i := 0; i < len(callItems); i++ {
		tc := callItems[i]
		if tc.Get("type").String() != "function" {
			continue
		}
		*input = append(*input, codexFunctionCall{
			Type:      "function_call",
			CallID:    tc.Get("id").String(),
			Name:      resolveToolName(tc.Get("function.name").String(), originalToolNameMap),
			Arguments: tc.Get("function.arguments").String(),
		})
	}
}

func resolveToolName(name string, originalToolNameMap map[string]string) string {
	if short, ok := originalToolNameMap[name]; ok {
		return short
	}
	return shortenNameIfNeeded(name)
}

func jsonFieldValue(result gjson.Result) any {
	if !result.Exists() {
		return nil
	}
	return result.Value()
}

// shortenNameIfNeeded applies the simple shortening rule for a single name.
// If the name length exceeds 64, it will try to preserve the "mcp__" prefix and last segment.
// Otherwise it truncates to 64 characters.
func shortenNameIfNeeded(name string) string {
	const limit = 64
	if len(name) <= limit {
		return name
	}
	if strings.HasPrefix(name, "mcp__") {
		// Keep prefix and last segment after '__'
		idx := strings.LastIndex(name, "__")
		if idx > 0 {
			candidate := "mcp__" + name[idx+2:]
			if len(candidate) > limit {
				return candidate[:limit]
			}
			return candidate
		}
	}
	return name[:limit]
}

// buildShortNameMap generates unique short names (<=64) for the given list of names.
// It preserves the "mcp__" prefix with the last segment when possible and ensures uniqueness
// by appending suffixes like "~1", "~2" if needed.
func buildShortNameMap(names []string) map[string]string {
	const limit = 64
	used := map[string]struct{}{}
	m := map[string]string{}

	baseCandidate := func(n string) string {
		if len(n) <= limit {
			return n
		}
		if strings.HasPrefix(n, "mcp__") {
			idx := strings.LastIndex(n, "__")
			if idx > 0 {
				cand := "mcp__" + n[idx+2:]
				if len(cand) > limit {
					cand = cand[:limit]
				}
				return cand
			}
		}
		return n[:limit]
	}

	makeUnique := func(cand string) string {
		if _, ok := used[cand]; !ok {
			return cand
		}
		base := cand
		for i := 1; ; i++ {
			suffix := "_" + strconv.Itoa(i)
			allowed := limit - len(suffix)
			if allowed < 0 {
				allowed = 0
			}
			tmp := base
			if len(tmp) > allowed {
				tmp = tmp[:allowed]
			}
			tmp = tmp + suffix
			if _, ok := used[tmp]; !ok {
				return tmp
			}
		}
	}

	for _, n := range names {
		cand := baseCandidate(n)
		uniq := makeUnique(cand)
		used[uniq] = struct{}{}
		m[n] = uniq
	}
	return m
}
