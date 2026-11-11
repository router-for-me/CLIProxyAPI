package common

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

// ClaudeMessagesJSONToSSE adapts a native Messages response for the existing SSE
// aggregators and returns its model. SSE and other payloads pass through unchanged.
func ClaudeMessagesJSONToSSE(rawJSON []byte) ([]byte, string) {
	if !ojson.ValidBytes(rawJSON) {
		return rawJSON, ""
	}
	root := util.ParseGJSONBytesNoCopy(rawJSON)
	if root.Get("type").String() != "message" || !root.Get("content").IsArray() {
		return rawJSON, ""
	}

	var out bytes.Buffer
	emit := func(event map[string]any) {
		// All values are JSON primitives or validated raw JSON from the response.
		payload, _ := json.Marshal(event)
		out.WriteString("data: ")
		out.Write(payload)
		out.WriteString("\n\n")
	}
	messageDoc := ojson.NewDoc(rawJSON)
	_ = messageDoc.SetRawBytes("content", []byte(`[]`))
	_ = messageDoc.Set("stop_reason", nil)
	_ = messageDoc.Set("stop_sequence", nil)
	emit(map[string]any{"type": "message_start", "message": json.RawMessage(messageDoc.Bytes())})

	for index, block := range root.Get("content").Array() {
		start := []byte(block.Raw)
		var delta map[string]any
		switch block.Get("type").String() {
		case "text":
			start, _ = ojson.SetBytes(start, "text", "")
			delta = map[string]any{"type": "text_delta", "text": block.Get("text").String()}
		case "tool_use":
			start, _ = ojson.SetRawBytes(start, "input", []byte(`{}`))
			input := block.Get("input").Raw
			if input == "" {
				input = "{}"
			}
			delta = map[string]any{"type": "input_json_delta", "partial_json": input}
		case "thinking":
			startDoc := ojson.NewDoc(start)
			_ = startDoc.Set("thinking", "")
			_ = startDoc.Set("signature", "")
			start = startDoc.Bytes()
			delta = map[string]any{"type": "thinking_delta", "thinking": block.Get("thinking").String()}
		}
		emit(map[string]any{"type": "content_block_start", "index": index, "content_block": json.RawMessage(start)})
		if delta != nil {
			emit(map[string]any{"type": "content_block_delta", "index": index, "delta": delta})
		}
		if block.Get("type").String() == "text" {
			for _, citation := range block.Get("citations").Array() {
				emit(map[string]any{"type": "content_block_delta", "index": index, "delta": map[string]any{
					"type": "citations_delta", "citation": json.RawMessage(citation.Raw),
				}})
			}
		}
		if block.Get("type").String() == "thinking" && block.Get("signature").Exists() {
			emit(map[string]any{"type": "content_block_delta", "index": index, "delta": map[string]any{
				"type": "signature_delta", "signature": block.Get("signature").String(),
			}})
		}
		emit(map[string]any{"type": "content_block_stop", "index": index})
	}
	usage := json.RawMessage(root.Get("usage").Raw)
	if len(usage) == 0 {
		usage = json.RawMessage(`{}`)
	}
	emit(map[string]any{"type": "message_delta", "delta": map[string]any{
		"stop_reason": root.Get("stop_reason").Value(), "stop_sequence": root.Get("stop_sequence").Value(),
	}, "usage": usage})
	emit(map[string]any{"type": "message_stop"})
	return out.Bytes(), strings.Clone(root.Get("model").String())
}

// ClaudeModelSameOrSnapshot reports whether primaryModel and upstreamModel
// refer to the same Anthropic model or an 8-digit YYYYMMDD snapshot thereof.
func ClaudeModelSameOrSnapshot(primaryModel, upstreamModel string) bool {
	p := strings.ToLower(strings.TrimSpace(primaryModel))
	u := strings.ToLower(strings.TrimSpace(upstreamModel))
	if p == "" || u == "" {
		return false
	}
	if p == u {
		return true
	}
	return isClaudeDatedSnapshotOf(p, u) || isClaudeDatedSnapshotOf(u, p)
}

func isClaudeDatedSnapshotOf(base, candidate string) bool {
	if !strings.HasPrefix(candidate, base+"-") {
		return false
	}
	suffix := candidate[len(base)+1:]
	if len(suffix) != 8 {
		return false
	}
	for i := 0; i < len(suffix); i++ {
		if suffix[i] < '0' || suffix[i] > '9' {
			return false
		}
	}
	return true
}

func claudeRequestHasFallbacks(raw []byte) bool {
	if len(raw) == 0 || !ojson.ValidBytes(raw) {
		return false
	}
	fb := ojson.GetBytesNoCopy(raw, "fallbacks")
	if !fb.Exists() {
		return false
	}
	switch fb.Type {
	case ojson.String:
		return strings.TrimSpace(fb.String()) != ""
	case ojson.JSON:
		if fb.IsArray() {
			return len(fb.Array()) > 0
		}
		if fb.IsObject() {
			return len(fb.Map()) > 0
		}
	}
	return false
}

func claudePayloadStringModel(raw []byte) string {
	if len(raw) == 0 || !ojson.ValidBytes(raw) {
		return ""
	}
	m := ojson.GetBytesNoCopy(raw, "model")
	if m.Type != ojson.String {
		return ""
	}
	return strings.TrimSpace(m.String())
}

func isCanonicalClaudeModelID(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "claude-")
}

// ClaudePreTokenFallbackModels detects when an Anthropic message_start model
// already switched to a fallback model (including array fallbacks,
// fallbacks: "default", and sticky-routing turns).
func ClaudePreTokenFallbackModels(originalRequestRawJSON, requestRawJSON []byte, fallbackRequestedModel, upstreamModel string) (fromModel, toModel string, ok bool) {
	upstreamModel = strings.TrimSpace(upstreamModel)
	if upstreamModel == "" {
		return "", "", false
	}
	primary := claudePayloadStringModel(requestRawJSON)
	if primary == "" {
		primary = claudePayloadStringModel(originalRequestRawJSON)
	}
	if primary == "" {
		primary = strings.TrimSpace(fallbackRequestedModel)
	}
	if primary != "" && ClaudeModelSameOrSnapshot(primary, upstreamModel) {
		return "", "", false
	}
	hasFallbacks := claudeRequestHasFallbacks(requestRawJSON) || claudeRequestHasFallbacks(originalRequestRawJSON)
	if !hasFallbacks && !(isCanonicalClaudeModelID(primary) && isCanonicalClaudeModelID(upstreamModel)) {
		return "", "", false
	}
	return strings.Clone(primary), strings.Clone(upstreamModel), true
}

// BuildClaudeFallbackMetadata serializes a structured fallback metadata object
// preserving upstream from/to/trigger objects and an optional boundary index.
func BuildClaudeFallbackMetadata(contentBlock ojson.Result, defaultFromModel, defaultToModel, boundaryKey string, boundaryIndex *int) []byte {
	doc := ojson.NewDocString("{}")
	hasField := false
	if contentBlock.Exists() && contentBlock.Get("from").IsObject() {
		_ = doc.SetRawBytes("from", []byte(contentBlock.Get("from").Raw))
		hasField = true
	} else if m := strings.TrimSpace(defaultFromModel); m != "" {
		_ = doc.Set("from.model", m)
		hasField = true
	}
	if contentBlock.Exists() && contentBlock.Get("to").IsObject() {
		_ = doc.SetRawBytes("to", []byte(contentBlock.Get("to").Raw))
		hasField = true
	} else if m := strings.TrimSpace(defaultToModel); m != "" {
		_ = doc.Set("to.model", m)
		hasField = true
	}
	if contentBlock.Exists() && contentBlock.Get("trigger").IsObject() {
		_ = doc.SetRawBytes("trigger", []byte(contentBlock.Get("trigger").Raw))
		hasField = true
	}
	if !hasField {
		return nil
	}
	if boundaryKey != "" && boundaryIndex != nil {
		_ = doc.Set(boundaryKey, *boundaryIndex)
	}
	return doc.Bytes()
}

// ClaudeFallbackChain accumulates ordered Anthropic fallback hops across
// pre-token message_start switches, mid-stream fallback content blocks, and
// usage.iterations fallback_message entries without losing earlier boundaries.
type ClaudeFallbackChain struct {
	Entries             [][]byte
	inferredPreTokenSrc string
	hasExplicitBlock    bool
}

// Reset clears all accumulated fallback state for a new message.
func (c *ClaudeFallbackChain) Reset() {
	c.Entries = nil
	c.inferredPreTokenSrc = ""
	c.hasExplicitBlock = false
}

// RecordPreToken records a pre-token or sticky fallback detected at message_start.
func (c *ClaudeFallbackChain) RecordPreToken(fromModel, toModel, boundaryKey string, boundaryIndex *int) []byte {
	fb := BuildClaudeFallbackMetadata(ojson.Result{}, fromModel, toModel, boundaryKey, boundaryIndex)
	if len(fb) == 0 {
		return nil
	}
	c.Entries = [][]byte{fb}
	c.inferredPreTokenSrc = strings.Clone(strings.TrimSpace(fromModel))
	c.hasExplicitBlock = false
	return fb
}

// RecordBlock records an explicit Anthropic content_block of type "fallback".
// If the first explicit block describes the same transition already inferred at
// message_start, it upgrades that entry with trigger/boundary details; otherwise
// it appends a new hop in order.
func (c *ClaudeFallbackChain) RecordBlock(contentBlock ojson.Result, defaultFromModel, defaultToModel, boundaryKey string, boundaryIndex *int) []byte {
	fb := BuildClaudeFallbackMetadata(contentBlock, defaultFromModel, defaultToModel, boundaryKey, boundaryIndex)
	if len(fb) == 0 {
		return nil
	}
	blockFrom := strings.TrimSpace(contentBlock.Get("from.model").String())
	if blockFrom == "" {
		blockFrom = strings.TrimSpace(defaultFromModel)
	}
	if !c.hasExplicitBlock && len(c.Entries) == 1 && c.inferredPreTokenSrc != "" && ClaudeModelSameOrSnapshot(c.inferredPreTokenSrc, blockFrom) {
		c.Entries[0] = fb
	} else {
		c.Entries = append(c.Entries, fb)
	}
	c.inferredPreTokenSrc = ""
	c.hasExplicitBlock = true
	return fb
}

// RecordIterations reconciles usage.iterations fallback_message entries,
// returning the final fallback serving model and any newly discovered hop
// metadata when explicit fallback content blocks were absent.
func (c *ClaudeFallbackChain) RecordIterations(iterations ojson.Result, defaultFromModel, boundaryKey string, boundaryIndex *int) (finalModel string, newEntries [][]byte) {
	if !iterations.Exists() || !iterations.IsArray() {
		return "", nil
	}
	curFrom := strings.TrimSpace(defaultFromModel)
	var hops [][]byte
	for _, it := range iterations.Array() {
		m := strings.TrimSpace(it.Get("model").String())
		if m == "" {
			continue
		}
		switch it.Get("type").String() {
		case "message":
			curFrom = m
		case "fallback_message":
			finalModel = strings.Clone(m)
			if !c.hasExplicitBlock {
				if fb := BuildClaudeFallbackMetadata(ojson.Result{}, curFrom, m, boundaryKey, boundaryIndex); len(fb) > 0 {
					hops = append(hops, fb)
				}
			}
			curFrom = m
		}
	}
	if finalModel == "" || c.hasExplicitBlock || len(hops) == 0 {
		return finalModel, nil
	}
	prevLen := len(c.Entries)
	if len(hops) > prevLen {
		c.Entries = hops
		newEntries = hops[prevLen:]
	}
	c.inferredPreTokenSrc = ""
	return finalModel, newEntries
}

// Latest returns the most recent fallback hop metadata, or nil when none occurred.
func (c *ClaudeFallbackChain) Latest() []byte {
	if len(c.Entries) == 0 {
		return nil
	}
	return c.Entries[len(c.Entries)-1]
}

// ArrayBytes serializes all ordered fallback hops as a JSON array, or nil when empty.
func (c *ClaudeFallbackChain) ArrayBytes() []byte {
	if len(c.Entries) == 0 {
		return nil
	}
	return JoinRawArray(c.Entries)
}
