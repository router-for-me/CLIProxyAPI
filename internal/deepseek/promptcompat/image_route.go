package promptcompat

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/deepseek/modelinfo"
)

const visionModel = "deepseek-v4-vision"
const visionModelNoThinking = visionModel + configNoThinkingSuffix

const configNoThinkingSuffix = "-nothinking"

// RequestContainsImageContent reports whether the current user turn carries any
// image content that should trigger auto-routing to a vision model. It checks
// the latest user message in OpenAI-style chat messages, the Responses input,
// and top-level attachments, but intentionally ignores images in earlier
// conversation history so that follow-up text-only requests switch back to the
// originally selected model.
func RequestContainsImageContent(req map[string]any) bool {
	if len(req) == 0 {
		return false
	}
	if raw, ok := req["input"]; ok && !isPlainString(raw) && containsImageInAny(raw) {
		return true
	}
	if raw, ok := req["attachments"]; ok && containsImageInAny(raw) {
		return true
	}
	if raw, ok := req["messages"].([]any); ok {
		if latest := latestUserMessage(raw); latest != nil {
			if content := latest["content"]; content != nil && containsImageInAny(content) {
				return true
			}
		}
	}
	return false
}

func isPlainString(v any) bool {
	_, ok := v.(string)
	return ok
}

func latestUserMessage(messages []any) map[string]any {
	for i := len(messages) - 1; i >= 0; i-- {
		msg, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(asString(msg["role"])))
		if role == "user" {
			return msg
		}
	}
	return nil
}

func containsImageInAny(raw any) bool {
	switch x := raw.(type) {
	case []any:
		for _, item := range x {
			if containsImageInAny(item) {
				return true
			}
		}
	case []map[string]any:
		for _, item := range x {
			if isImageContentBlock(item) {
				return true
			}
		}
	case map[string]any:
		if isImageContentBlock(x) {
			return true
		}
		for _, key := range []string{"content", "parts", "image_url", "file", "source"} {
			if containsImageInAny(x[key]) {
				return true
			}
		}
	}
	return false
}

func isImageContentBlock(block map[string]any) bool {
	if block == nil {
		return false
	}
	blockType := strings.ToLower(strings.TrimSpace(asString(block["type"])))
	if blockType == "image_url" || blockType == "input_image" || blockType == "image" {
		return true
	}
	if blockType == "input_file" || blockType == "file" {
		return false
	}
	if _, ok := block["image_url"]; ok {
		return true
	}
	if _, ok := block["image_url"].(map[string]any); ok {
		return true
	}
	if blockType == "" {
		if _, ok := block["url"]; ok {
			return true
		}
	}
	return false
}

// StripImageBlocksFromRequest removes image content blocks from the request's
// messages, input, and attachments containers. It is safe to call after inline
// file preprocessing because document-style file references are preserved.
func StripImageBlocksFromRequest(req map[string]any) {
	if len(req) == 0 {
		return
	}
	for _, key := range []string{"messages", "input", "attachments"} {
		switch x := req[key].(type) {
		case []any:
			req[key] = StripImageBlocksFromMessages(x)
		}
	}
}

// StripImageBlocksFromMessages removes image content blocks from an OpenAI-style
// message array. Elements that look like bare content parts (have a "type" but no
// "role"/"content") are filtered directly; elements that look like messages have
// their content array filtered. Document-style file references such as input_file
// are preserved.
func StripImageBlocksFromMessages(messages []any) []any {
	out := make([]any, 0, len(messages))
	for _, raw := range messages {
		msg, ok := raw.(map[string]any)
		if !ok {
			out = append(out, raw)
			continue
		}
		if isBareContentPart(msg) {
			if isImageContentBlock(msg) {
				continue
			}
			out = append(out, shallowCopyMap(msg))
			continue
		}
		content := msg["content"]
		stripped := stripImageContent(content)
		if stripped == nil {
			continue
		}
		if text, ok := stripped.(string); ok && strings.TrimSpace(text) == "" {
			continue
		}
		copied := shallowCopyMap(msg)
		copied["content"] = stripped
		out = append(out, copied)
	}
	return out
}

func isBareContentPart(m map[string]any) bool {
	if m == nil {
		return false
	}
	_, hasRole := m["role"]
	_, hasContent := m["content"]
	if hasRole || hasContent {
		return false
	}
	typeStr := strings.ToLower(strings.TrimSpace(asString(m["type"])))
	return typeStr != ""
}

func stripImageContent(v any) any {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		out := make([]any, 0, len(x))
		for _, item := range x {
			block, ok := item.(map[string]any)
			if !ok {
				out = append(out, item)
				continue
			}
			if isImageContentBlock(block) {
				continue
			}
			out = append(out, block)
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case []map[string]any:
		out := make([]map[string]any, 0, len(x))
		for _, block := range x {
			if isImageContentBlock(block) {
				continue
			}
			out = append(out, block)
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	return v
}

func shallowCopyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// VisionModelEquivalent returns the DeepSeek vision model equivalent for a
// resolved non-vision DeepSeek model. If the model is already a vision model or
// not a recognized DeepSeek model, it returns an empty string.
func VisionModelEquivalent(model string) string {
	if strings.TrimSpace(model) == "" {
		return ""
	}
	if modelType, ok := modelinfo.GetModelType(model); !ok || modelType == "vision" {
		return ""
	}
	if modelinfo.IsNoThinkingModel(model) {
		return visionModelNoThinking
	}
	return visionModel
}

// MaybeAutoRouteVision rewrites req["model"] to the vision equivalent when the
// auto-route setting is enabled, the requested model is a non-vision DeepSeek
// model, and the request contains image content. It returns the original model
// name and a flag indicating whether a rewrite happened. Callers that change the
// visible response model should restore it from the returned original model.
func MaybeAutoRouteVision(req map[string]any, store ConfigReader) (originalModel string, rerouted bool) {
	if req == nil || store == nil || !store.AutoRouteVisionEnabled() {
		return "", false
	}
	model, _ := req["model"].(string)
	model = strings.TrimSpace(model)
	if model == "" {
		return "", false
	}
	resolved, ok := modelinfo.ResolveModel(store, model)
	if !ok {
		return model, false
	}
	modelType, ok := modelinfo.GetModelType(resolved)
	if !ok || modelType == "vision" {
		return model, false
	}
	if !RequestContainsImageContent(req) {
		return model, false
	}
	visionModel := VisionModelEquivalent(resolved)
	if visionModel == "" {
		return model, false
	}
	req["model"] = visionModel
	return model, true
}
