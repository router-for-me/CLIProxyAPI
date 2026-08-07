package handlers

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"golang.org/x/net/context"
)

// applyVisionBridgeIfNeeded runs the per-router vision bridge when the resolved
// tier target model lacks vision support and the request contains image(s). It
// returns the (possibly rewritten) request body with image blocks replaced by
// the bridge's textual analysis. When the router has no bridge model, the
// target supports vision, the request has no images, or the bridge call fails,
// it returns the original body unchanged (a bridge outage never breaks the tier
// workflow).
func (h *BaseAPIHandler) applyVisionBridgeIfNeeded(ctx context.Context, resolved autoRouterResolved, rawJSON []byte) []byte {
	if !resolved.matched || strings.TrimSpace(resolved.visionBridgeModel) == "" {
		return rawJSON
	}
	if modelSupportsVision(resolved.targetModel) {
		return rawJSON
	}
	if !requestContainsImage(rawJSON) {
		return rawJSON
	}
	analysis, err := h.runVisionBridge(ctx, resolved.visionBridgeModel, rawJSON)
	if err != nil {
		log.WithError(err).
			WithField("vision_bridge_model", resolved.visionBridgeModel).
			WithField("target_model", resolved.targetModel).
			Warn("vision bridge failed; continuing with the original request")
		return rawJSON
	}
	return replaceImagesWithVisionText(rawJSON, "", analysis)
}

// inflateThinkingModelMaxTokens raises the request's max_tokens to the target
// model's MaxCompletionTokens when the target is a thinking model (Thinking
// config present) and the client requested a smaller cap. Thinking models share
// the max_tokens budget between reasoning and visible output, so a small client
// cap makes long answers truncate with finish_reason "max_tokens" (the Complex
// tier antigravity claude-sonnet-4-6 symptom). Inflating to the model cap lets
// the full answer complete. A no-op when the model isn't a thinking model, has
// no registered cap, or the request already caps at/above it. The original
// []byte is not mutated.
func inflateThinkingModelMaxTokens(rawJSON []byte, targetModel string) []byte {
	if len(rawJSON) == 0 || strings.TrimSpace(targetModel) == "" {
		return rawJSON
	}
	// The resolved model may carry a thinking suffix (e.g. "(8192)"); look up
	// the base model id.
	base := thinking.ParseSuffix(targetModel).ModelName
	if base == "" {
		base = strings.TrimSpace(targetModel)
	}
	info := registry.LookupModelInfo(base)
	if info == nil || info.Thinking == nil || info.MaxCompletionTokens <= 0 {
		return rawJSON
	}

	// Handle both "max_tokens" (OpenAI/Claude chat) and "max_completion_tokens"
	// (Responses) request fields.
	for _, field := range []string{"max_tokens", "max_completion_tokens"} {
		v := gjson.GetBytes(rawJSON, field)
		if !v.Exists() || v.Type != gjson.Number {
			continue
		}
		if int(v.Int()) >= info.MaxCompletionTokens {
			continue
		}
		if out, err := sjson.SetBytes(rawJSON, field, int64(info.MaxCompletionTokens)); err == nil {
			rawJSON = out
		}
	}
	return rawJSON
}

// autoRouterRequestAdjustments applies per-request adjustments for an auto-router
// match: the vision bridge (when the tier target lacks vision and the request
// has images) and max_tokens inflation for thinking models. Both are best-effort
// and never fail the request. Returns the (possibly rewritten) request body.
func (h *BaseAPIHandler) autoRouterRequestAdjustments(ctx context.Context, resolved autoRouterResolved, rawJSON []byte) []byte {
	if !resolved.matched {
		return rawJSON
	}
	rawJSON = h.applyVisionBridgeIfNeeded(ctx, resolved, rawJSON)
	rawJSON = inflateThinkingModelMaxTokens(rawJSON, resolved.targetModel)
	return rawJSON
}

// modelSupportsVision reports whether a model id is declared to accept image
// input (per its registered SupportedInputModalities). When the model is not in
// the registry (unknown), it is treated as vision-capable (returns true) so a
// vision model whose metadata was never registered is not spuriously forced
// through the vision bridge.
func modelSupportsVision(modelID string) bool {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return true
	}
	info := registry.GetGlobalRegistry().GetModelInfo(modelID, "")
	if info == nil {
		return true
	}
	for _, m := range info.SupportedInputModalities {
		if strings.EqualFold(strings.TrimSpace(m), "image") {
			return true
		}
	}
	return false
}

// visionImageBlockKind describes which image representation a block carries.
type visionImageBlockKind int

const (
	visionBlockNone visionImageBlockKind = iota
	visionBlockOpenAIImageURL
	visionBlockClaudeImage
	visionBlockGeminiInline
	visionBlockGeminiFileData
)

// classifyVisionBlock reports the image kind for a content part at messages.N
// / contents.N. parts.M, or none. This powers both image detection and
// replacement.
func classifyVisionBlock(part gjson.Result) visionImageBlockKind {
	typ := strings.ToLower(part.Get("type").String())
	switch {
	case typ == "image_url" || part.Get("image_url").Exists():
		return visionBlockOpenAIImageURL
	case typ == "input_image" || part.Get("input_image").Exists():
		return visionBlockOpenAIImageURL
	case typ == "image":
		if src := part.Get("source"); src.Exists() {
			return visionBlockClaudeImage
		}
		return visionBlockNone
	case part.Get("inline_data").Exists() || part.Get("inlineData").Exists():
		return visionBlockGeminiInline
	case part.Get("file_data").Exists() || part.Get("fileData").Exists():
		return visionBlockGeminiFileData
	default:
		return visionBlockNone
	}
}

// requestContainsImage reports whether the request body carries any image block
// across the supported formats (OpenAI/Claude messages; Gemini contents).
func requestContainsImage(rawJSON []byte) bool {
	if len(rawJSON) == 0 {
		return false
	}
	root := gjson.ParseBytes(rawJSON)
	// Gemini-style: contents[].parts[].
	for _, content := range root.Get("contents").Array() {
		for _, part := range content.Get("parts").Array() {
			if classifyVisionBlock(part) != visionBlockNone {
				return true
			}
		}
	}
	// OpenAI/Claude messages: messages[].content[].
	for _, msg := range root.Get("messages").Array() {
		content := msg.Get("content")
		if !content.IsArray() {
			continue
		}
		for _, block := range content.Array() {
			if classifyVisionBlock(block) != visionBlockNone {
				return true
			}
		}
	}
	// Responses-format "input": array of {role, content}.
	for _, msg := range root.Get("input").Array() {
		content := msg.Get("content")
		if !content.IsArray() {
			continue
		}
		for _, block := range content.Array() {
			if classifyVisionBlock(block) != visionBlockNone {
				return true
			}
		}
	}
	return false
}

// replaceImagesWithVisionText rewrites every image block in the request body
// into a text block carrying the vision bridge's analysis. Returns a new byte
// slice; the input is not mutated. Formats handled: OpenAI/Claude messages and
// Gemini contents.
func replaceImagesWithVisionText(payload []byte, format string, analysis string) []byte {
	if len(payload) == 0 || !requestContainsImage(payload) {
		return payload
	}
	text := strings.TrimSpace(analysis)
	if text == "" {
		text = "[image analyzed by vision bridge]"
	}
	root := gjson.ParseBytes(payload)
	out := payload

	// Gemini contents[].parts[].
	contents := root.Get("contents").Array()
	for ci := range contents {
		parts := contents[ci].Get("parts").Array()
		for pi := range parts {
			if classifyVisionBlock(parts[pi]) == visionBlockNone {
				continue
			}
			path := fmt.Sprintf("contents.%d.parts.%d", ci, pi)
			next, err := sjson.SetBytes(out, path+".type", "text")
			if err != nil {
				continue
			}
			next, err = sjson.SetBytes(next, path+".text", text)
			if err != nil {
				continue
			}
			out = next
			out, _ = sjson.DeleteBytes(out, path+".inline_data")
			out, _ = sjson.DeleteBytes(out, path+".inlineData")
			out, _ = sjson.DeleteBytes(out, path+".file_data")
			out, _ = sjson.DeleteBytes(out, path+".fileData")
		}
	}

	// messages[].content[].
	messages := root.Get("messages").Array()
	for mi := range messages {
		content := messages[mi].Get("content")
		if !content.IsArray() {
			continue
		}
		blocks := content.Array()
		for bi := range blocks {
			if classifyVisionBlock(blocks[bi]) == visionBlockNone {
				continue
			}
			path := fmt.Sprintf("messages.%d.content.%d", mi, bi)
			out = rewriteVisionBlock(out, path, text)
		}
	}

	// Responses-format input[].content[].
	inputMsgs := root.Get("input").Array()
	for mi := range inputMsgs {
		content := inputMsgs[mi].Get("content")
		if !content.IsArray() {
			continue
		}
		blocks := content.Array()
		for bi := range blocks {
			if classifyVisionBlock(blocks[bi]) == visionBlockNone {
				continue
			}
			path := fmt.Sprintf("input.%d.content.%d", mi, bi)
			out = rewriteVisionBlock(out, path, text)
		}
	}
	return out
}

// rewriteVisionBlock converts one image block at sjson path into a text block
// carrying the vision analysis, removing format-specific image keys.
func rewriteVisionBlock(out []byte, path, text string) []byte {
	if next, err := sjson.SetBytes(out, path+".type", "text"); err == nil {
		out = next
	}
	if next, err := sjson.SetBytes(out, path+".text", text); err == nil {
		out = next
	}
	out, _ = sjson.DeleteBytes(out, path+".image_url")
	out, _ = sjson.DeleteBytes(out, path+".input_image")
	out, _ = sjson.DeleteBytes(out, path+".source")
	return out
}

// collectImageDataURLs extracts every image from the request body as a data:
// URL string (`data:<mime>;base64,<data>`), best-effort across formats. Used to
// build the vision bridge prompt. http(s) image URLs are passed through as-is.
func collectImageDataURLs(rawJSON []byte) []string {
	if len(rawJSON) == 0 {
		return nil
	}
	root := gjson.ParseBytes(rawJSON)
	var out []string

	addSource := func(src gjson.Result) {
		stype := strings.ToLower(src.Get("type").String())
		data := src.Get("data").String()
		switch stype {
		case "base64":
			mime := src.Get("media_type").String()
			if mime == "" {
				mime = src.Get("mediaType").String()
			}
			if data != "" {
				out = append(out, fmt.Sprintf("data:%s;base64,%s", mime, data))
			}
		case "url":
			if data != "" {
				out = append(out, data)
			}
		}
	}

	for _, content := range root.Get("contents").Array() {
		for _, part := range content.Get("parts").Array() {
			switch classifyVisionBlock(part) {
			case visionBlockGeminiInline:
				inline := part.Get("inline_data")
				if !inline.Exists() {
					inline = part.Get("inlineData")
				}
				mime := inline.Get("mime_type").String()
				if mime == "" {
					mime = inline.Get("mimeType").String()
				}
				if data := inline.Get("data").String(); data != "" {
					out = append(out, fmt.Sprintf("data:%s;base64,%s", mime, data))
				}
			case visionBlockGeminiFileData:
				if uri := part.Get("file_data.file_uri").String(); uri != "" {
					out = append(out, uri)
				} else if uri := part.Get("fileData.fileUri").String(); uri != "" {
					out = append(out, uri)
				}
			}
		}
	}

	for _, msg := range root.Get("messages").Array() {
		content := msg.Get("content")
		if !content.IsArray() {
			if url := content.Get("image_url.url").String(); url != "" {
				out = append(out, url)
			}
			continue
		}
		for _, block := range content.Array() {
			switch classifyVisionBlock(block) {
			case visionBlockOpenAIImageURL:
				if url := block.Get("image_url.url").String(); url != "" {
					out = append(out, url)
				} else if url := block.Get("input_image").String(); url != "" {
					out = append(out, url)
				}
			case visionBlockClaudeImage:
				addSource(block.Get("source"))
			}
		}
	}
	for _, msg := range root.Get("input").Array() {
		content := msg.Get("content")
		if !content.IsArray() {
			continue
		}
		for _, block := range content.Array() {
			switch classifyVisionBlock(block) {
			case visionBlockOpenAIImageURL:
				if url := block.Get("image_url.url").String(); url != "" {
					out = append(out, url)
				} else if url := block.Get("input_image").String(); url != "" {
					out = append(out, url)
				}
			case visionBlockClaudeImage:
				addSource(block.Get("source"))
			}
		}
	}
	return out
}

// extractOpenAICompletionText pulls the assistant text out of an OpenAI-compatible
// chat-completion response body (concatenates each choice's message.content).
func extractOpenAICompletionText(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	choices := gjson.GetBytes(payload, "choices")
	if !choices.IsArray() {
		return ""
	}
	var b strings.Builder
	choices.ForEach(func(_, choice gjson.Result) bool {
		if v := choice.Get("message.content"); v.Exists() && v.Type == gjson.String {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(v.String())
		}
		return true
	})
	return b.String()
}

// runVisionBridge calls the configured vision bridge model with the images from
// the request and returns a targeted textual analysis. It mirrors the health
// probe's one-off Execute pattern (OpenAI chat payload, Stream=false). On any
// failure it returns an error so the caller can decide to proceed unchanged.
func (h *BaseAPIHandler) runVisionBridge(ctx context.Context, bridgeModel string, rawJSON []byte) (string, error) {
	if h == nil || h.AuthManager == nil {
		return "", fmt.Errorf("vision bridge: auth manager unavailable")
	}
	bridgeModel = strings.TrimSpace(bridgeModel)
	if bridgeModel == "" {
		return "", fmt.Errorf("vision bridge: empty bridge model")
	}
	images := collectImageDataURLs(rawJSON)
	if len(images) == 0 {
		return "", fmt.Errorf("vision bridge: no images found in request")
	}

	providers := registry.GetGlobalRegistry().GetModelProviders(bridgeModel)
	if len(providers) == 0 {
		return "", fmt.Errorf("vision bridge: no live provider for model %s", bridgeModel)
	}

	const instruction = "Analyze the image(s) in this conversation and provide a detailed, factual textual description of their contents. Do not answer whatever question may be attached; only describe what is visible in the image(s)."
	contentParts := []map[string]any{
		{"type": "text", "text": instruction},
	}
	for _, img := range images {
		contentParts = append(contentParts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": img}})
	}
	chatBody := map[string]any{
		"model": bridgeModel,
		"messages": []map[string]any{
			{"role": "user", "content": contentParts},
		},
		"stream": false,
	}
	body, err := json.Marshal(chatBody)
	if err != nil {
		return "", fmt.Errorf("vision bridge: marshal payload: %w", err)
	}

	req := coreexecutor.Request{
		Model:   bridgeModel,
		Payload: body,
	}
	opts := coreexecutor.Options{
		Stream:       false,
		SourceFormat: sdktranslator.FromString("openai"),
		Metadata: map[string]any{
			coreexecutor.RequestedModelMetadataKey: bridgeModel,
		},
	}
	opts.Metadata[coreexecutor.SelectedAuthCallbackMetadataKey] = func(string) {}

	var lastErr error
	for _, provider := range providers {
		resp, errExec := h.AuthManager.Execute(ctx, []string{provider}, req, opts)
		if errExec != nil {
			lastErr = errExec
			continue
		}
		text := extractOpenAICompletionText(resp.Payload)
		text = strings.TrimSpace(strings.Join(strings.Fields(text), " "))
		if text == "" {
			lastErr = fmt.Errorf("vision bridge: empty analysis from provider %s", provider)
			continue
		}
		return text, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("vision bridge: unknown failure")
	}
	return "", fmt.Errorf("vision bridge for %s: %w", bridgeModel, lastErr)
}
