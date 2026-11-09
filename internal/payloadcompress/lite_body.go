package payloadcompress

import (
	"bytes"
	"regexp"
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// liteBodyHasArray is a cheap pre-check that keeps bodies without a message
// array on the zero-allocation fast path.
func liteBodyHasArray(body []byte) bool {
	return bytes.Contains(body, []byte(`"messages"`)) ||
		bytes.Contains(body, []byte(`"input"`)) ||
		bytes.Contains(body, []byte(`"contents"`))
}

// applyLiteBodyBefore runs upstream lite pass 1 (dedupSystemPrompt): drop
// duplicate system messages, first wins. It is a no-op when the system prompt
// is part of a cached prefix.
func applyLiteBodyBefore(cfg Config, model, provider string, body []byte) []byte {
	return applyLiteBodyBeforePreserve(cfg.shouldPreserveSystemPrompt(model, body), body)
}

func applyLiteBodyBeforePreserve(preserveSystem bool, body []byte) []byte {
	if preserveSystem {
		return body
	}
	doc, path, items, ok := liteBodyArray(body)
	if !ok {
		return body
	}
	seen := make(map[string]struct{}, len(items))
	kept := make([]string, 0, len(items))
	changed := false
	for _, item := range items {
		if item.Get("role").String() == "system" && item.Get("content").Type == ojson.String {
			key := strings.TrimSpace(item.Get("content").String())
			if len(key) > 200 {
				key = key[:200]
			}
			if _, dup := seen[key]; dup {
				changed = true
				continue
			}
			seen[key] = struct{}{}
		}
		kept = append(kept, item.Raw)
	}
	return liteSetArray(doc, path, kept, changed, body)
}

// applyLiteBodyAfter runs upstream lite passes 4 and 5 when enabled: opt-in
// removeRedundantContent then replaceImageUrls (non-vision models only).
func applyLiteBodyAfter(cfg Config, opts StepOptions, model, provider string, body []byte) []byte {
	return applyLiteBodyAfterPreserve(cfg.shouldPreserveSystemPrompt(model, body), opts, model, provider, body)
}

func applyLiteBodyAfterPreserve(preserveSystem bool, opts StepOptions, model, provider string, body []byte) []byte {
	if opts.LiteRemoveRedundant != nil && *opts.LiteRemoveRedundant {
		body = liteRemoveRedundant(preserveSystem, body)
	}
	if opts.LiteReplaceImages != nil && *opts.LiteReplaceImages {
		body = liteReplaceImageUrls(model, provider, body)
	}
	return body
}

func liteRemoveRedundant(preserveSystem bool, body []byte) []byte {
	doc, path, items, ok := liteBodyArray(body)
	if !ok {
		return body
	}
	kept := make([]string, 0, len(items))
	changed := false
	for i, item := range items {
		role := item.Get("role").String()
		if preserveSystem && role == "system" {
			kept = append(kept, item.Raw)
			continue
		}
		content := item.Get("content")
		contentStr := content.Raw
		if content.Type == ojson.String {
			contentStr = content.String()
		}
		if i > 0 && role != "tool" && !liteToolResult(item, path) &&
			items[i-1].Get("role").String() == role {
			prev := items[i-1].Get("content")
			if prev.Type == ojson.String && prev.String() == contentStr {
				changed = true
				continue
			}
		}
		kept = append(kept, item.Raw)
	}
	return liteSetArray(doc, path, kept, changed, body)
}

func liteReplaceImageUrls(model, provider string, body []byte) []byte {
	if liteModelSupportsVision(model) && !liteTextOnlyProvider(provider) {
		return body
	}
	doc, path, items, ok := liteBodyArray(body)
	if !ok {
		return body
	}
	contentPath := "content"
	if path == "contents" {
		contentPath = "parts"
	}
	kept := make([]string, 0, len(items))
	changed := false
	for _, item := range items {
		rewritten, itemChanged := liteRewriteImageItem(item, contentPath, path == "contents", path == "input")
		if itemChanged {
			kept = append(kept, rewritten)
			changed = true
		} else {
			kept = append(kept, item.Raw)
		}
	}
	return liteSetArray(doc, path, kept, changed, body)
}

// liteRewriteImageItem replaces data:image parts inside one message, preserving
// every other byte of that message.
func liteRewriteImageItem(item ojson.Result, contentPath string, gemini, responses bool) (string, bool) {
	parts := item.Get(contentPath)
	if !parts.IsArray() {
		return "", false
	}
	sub := ojson.NewDocString(item.Raw)
	kept := make([]string, 0, len(parts.Array()))
	changed := false
	for _, part := range parts.Array() {
		if placeholder, ok := liteImagePlaceholder(part, gemini, responses); ok {
			kept = append(kept, placeholder)
			changed = true
		} else {
			kept = append(kept, part.Raw)
		}
	}
	if !changed {
		return "", false
	}
	if err := sub.SetRaw(contentPath, "["+strings.Join(kept, ",")+"]"); err != nil {
		return "", false
	}
	return sub.String(), true
}

func liteImagePlaceholder(part ojson.Result, gemini, responses bool) (string, bool) {
	textType := "text"
	if responses {
		// The Responses API only accepts input_text in input content arrays.
		textType = "input_text"
	}
	if gemini {
		mime := part.Get("inlineData.mimeType").String()
		if mime == "" {
			mime = part.Get("inline_data.mime_type").String()
		}
		if !strings.HasPrefix(mime, "image/") {
			return "", false
		}
		format := strings.TrimPrefix(mime, "image/")
		if format == "" {
			format = "unknown"
		}
		return `{"text":"[image: ` + format + `]"}`, true
	}
	switch part.Get("type").String() {
	case "image_url":
		url := part.Get("image_url.url").String()
		format, ok := liteDataImageFormat(url)
		if !ok {
			return "", false
		}
		return `{"type":"` + textType + `","text":"[image: ` + format + `]"}`, true
	case "input_image":
		url := part.Get("image_url").String()
		if url == "" {
			url = part.Get("image_url.url").String()
		}
		format, ok := liteDataImageFormat(url)
		if !ok {
			return "", false
		}
		return `{"type":"` + textType + `","text":"[image: ` + format + `]"}`, true
	}
	return "", false
}

func liteDataImageFormat(url string) (string, bool) {
	const prefix = "data:image/"
	if !strings.HasPrefix(url, prefix) {
		return "", false
	}
	format := strings.TrimPrefix(url, prefix)
	if i := strings.IndexAny(format, ";,"); i >= 0 {
		format = format[:i]
	}
	if format == "" {
		format = "unknown"
	}
	return format, true
}

// liteBodyArray locates the message array for chat (messages), Responses
// (input) and Gemini (contents). Shapes that cannot be edited safely fail open.
func liteBodyArray(body []byte) (*ojson.Doc, string, []ojson.Result, bool) {
	doc := ojson.NewDocString(string(body))
	for _, path := range []string{"messages", "input", "contents"} {
		arr := doc.Get(path)
		if arr.IsArray() {
			return doc, path, arr.Array(), true
		}
	}
	return nil, "", nil, false
}

func liteSetArray(doc *ojson.Doc, path string, raws []string, changed bool, body []byte) []byte {
	if !changed {
		return body
	}
	if err := doc.SetRaw(path, "["+strings.Join(raws, ",")+"]"); err != nil {
		return body
	}
	return []byte(doc.String())
}

// liteVisionFragments mirrors upstream src/shared/constants/visionModels.ts
// (cheap/release/v3.8.53). Conservative: false positives keep images for models
// that cannot see them.
var liteVisionFragments = []string{
	"pixtral",
	"llava",
	"bakllava",
	"qwen-vl",
	"qwen2-vl",
	"qwen2.5-vl",
	"qwen3-vl",
	"qvq",
	"internvl",
	"minicpm-v",
	"moondream",
	"mimo-vl",
	"mimo-v2.5",
	"mimo-v2.6-pro",
	"mimo-v2.6-flash",
	"step-3.7-flash",
	"kimi-vl",
	"glm-4v",
	"glm-4.5v",
	"glm-4.6v",
	"glm-5.3-flash",
	"gpt-4o",
	"gpt-4.1",
	"gpt-4-turbo",
	"gpt-4-vision",
	"gpt-5",
	"gemini-1.5",
	"gemini-2",
	"gemini-3",
	"gemini-4",
	"gemini-exp",
	"claude-3",
	"claude-fable",
	"claude-opus-4",
	"claude-sonnet-4",
	"claude-haiku-4",
	"mistral-medium-3",
	"minimax-m3",
	"kimi-k2.",
	"hcx-005",
	"-vision",
	"multimodal",
}

// liteMiMoTextOnlyRe matches MiMo V2/V2.5 Pro ids, including provider-qualified
// forms such as openrouter/xiaomi/mimo-v2.5-pro. Upstream keeps these text-only
// siblings beside the shared vision list.
var liteMiMoTextOnlyRe = regexp.MustCompile(`(?:^|/)mimo-v2\.5-pro(?:$|[:/])|(?:^|/)mimo-v2-pro(?:$|[:/])`)

func liteModelSupportsVision(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if normalized == "" {
		return false
	}
	// Text-only siblings of multimodal families stay text-only.
	if liteMiMoTextOnlyRe.MatchString(normalized) {
		return false
	}
	for _, fragment := range liteVisionFragments {
		if strings.Contains(normalized, fragment) {
			return true
		}
	}
	return false
}

func liteTextOnlyProvider(provider string) bool {
	normalized := strings.ToLower(strings.TrimSpace(provider))
	if normalized == "" {
		return false
	}
	for _, sep := range []string{"/", ":"} {
		if i := strings.Index(normalized, sep); i >= 0 {
			normalized = normalized[:i]
		}
	}
	return normalized == "cfp" || normalized == "cloudflare-playground" || normalized == "cloudflare"
}

// liteToolResult reports whether an item carries tool output, which must never
// be dropped by removeRedundantContent.
func liteToolResult(item ojson.Result, arrayPath string) bool {
	if item.Get("role").String() == "tool" {
		return true
	}
	if strings.HasSuffix(item.Get("type").String(), "_call_output") {
		return true
	}
	if arrayPath == "contents" {
		parts := item.Get("parts")
		if parts.IsArray() {
			for _, part := range parts.Array() {
				if part.Get("functionResponse").Exists() || part.Get("function_response").Exists() {
					return true
				}
			}
		}
	}
	return false
}
