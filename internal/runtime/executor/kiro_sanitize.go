package executor

import (
	"encoding/json"
	"strings"
)

// kiroUnsupportedTopLevel lists Responses API fields the Kiro runtime rejects with
// `unsupported_parameter` (verified against runtime.us-east-1.kiro.dev).
var kiroUnsupportedTopLevel = []string{
	"tool_choice", "temperature", "top_p", "text", "reasoning", "max_output_tokens",
	"include", "previous_response_id", "prompt_cache_retention", "prompt_cache_key",
	"safety_identifier", "stream_options", "parallel_tool_calls", "service_tier", "user",
	"n", "logit_bias", "logprobs", "top_logprobs", "seed", "store", "metadata",
	"frequency_penalty", "presence_penalty", "client_side_tools", "truncation", "background",
}

// sanitizeKiroPayload adapts a Responses API body to what the Kiro runtime accepts:
//   - drops unsupported top-level fields;
//   - keeps only function tools with name/description/parameters (no `strict`);
//   - folds `instructions` and system/developer messages into the first user message,
//     since Kiro free accounts reject both `instructions` and non-user roles.
func sanitizeKiroPayload(body []byte) []byte {
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		return body
	}
	for _, k := range kiroUnsupportedTopLevel {
		delete(doc, k)
	}

	if rawTools, ok := doc["tools"].([]any); ok {
		tools := make([]any, 0, len(rawTools))
		for _, t := range rawTools {
			tm, okMap := t.(map[string]any)
			if !okMap {
				continue
			}
			if typ, _ := tm["type"].(string); typ != "" && typ != "function" {
				continue
			}
			name, _ := tm["name"].(string)
			if strings.TrimSpace(name) == "" {
				continue
			}
			clean := map[string]any{"type": "function", "name": name}
			if d, okD := tm["description"].(string); okD && d != "" {
				clean["description"] = d
			}
			if p, okP := tm["parameters"]; okP && p != nil {
				clean["parameters"] = p
			} else {
				clean["parameters"] = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			tools = append(tools, clean)
		}
		if len(tools) == 0 {
			delete(doc, "tools")
		} else {
			doc["tools"] = tools
		}
	}

	var systemParts []string
	if inst, ok := doc["instructions"].(string); ok && strings.TrimSpace(inst) != "" {
		systemParts = append(systemParts, strings.TrimSpace(inst))
	}
	delete(doc, "instructions")

	switch input := doc["input"].(type) {
	case string:
		doc["input"] = []any{map[string]any{"role": "user", "content": input}}
	case []any:
		kept := make([]any, 0, len(input))
		for _, item := range input {
			im, okMap := item.(map[string]any)
			if !okMap {
				kept = append(kept, item)
				continue
			}
			role, _ := im["role"].(string)
			if role == "system" || role == "developer" {
				if txt := kiroContentText(im["content"]); txt != "" {
					systemParts = append(systemParts, txt)
				}
				continue
			}
			if typ, _ := im["type"].(string); typ == "message" {
				delete(im, "type")
			}
			kept = append(kept, im)
		}
		doc["input"] = kept
	}

	if len(systemParts) > 0 {
		prefix := "<system_instructions>\n" + strings.Join(systemParts, "\n\n") + "\n</system_instructions>\n\n"
		input, _ := doc["input"].([]any)
		merged := false
		for _, item := range input {
			im, okMap := item.(map[string]any)
			if !okMap || im["role"] != "user" {
				continue
			}
			switch c := im["content"].(type) {
			case string:
				im["content"] = prefix + c
			case []any:
				im["content"] = append([]any{map[string]any{"type": "input_text", "text": prefix}}, c...)
			default:
				im["content"] = prefix
			}
			merged = true
			break
		}
		if !merged {
			doc["input"] = append([]any{map[string]any{"role": "user", "content": strings.TrimSpace(prefix)}}, input...)
		}
	}

	out, err := json.Marshal(doc)
	if err != nil {
		return body
	}
	return out
}

func kiroContentText(content any) string {
	switch c := content.(type) {
	case string:
		return strings.TrimSpace(c)
	case []any:
		parts := make([]string, 0, len(c))
		for _, p := range c {
			if pm, ok := p.(map[string]any); ok {
				if t, okT := pm["text"].(string); okT && strings.TrimSpace(t) != "" {
					parts = append(parts, strings.TrimSpace(t))
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}
