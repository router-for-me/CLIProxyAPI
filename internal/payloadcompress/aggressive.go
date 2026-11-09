package payloadcompress

import (
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// aggressiveEngine ports upstream's aggressive compression pipeline:
// tool-result compression, progressive aging, a rule-based summarizer for very
// long messages, then a caveman/lite downgrade chain when the first passes save
// less than the configured threshold. Chat `messages` bodies only.

const (
	aggressiveMaxTokensPerMessage = 2048
	aggressiveMinSavingsPercent   = 5.0
	aggressivePreserveSystem      = true
)

type aggressiveEngine struct{}

func newAggressiveEngine() *aggressiveEngine { return &aggressiveEngine{} }

func (aggressiveEngine) name() string { return "aggressive" }

// applyBody rewrites the chat messages array and fails open to the original
// body on any unexpected shape. Untouched messages stay byte-identical.
func (e *aggressiveEngine) applyBody(body []byte) (out []byte) {
	out = body
	defer func() {
		if recover() != nil {
			out = body
		}
	}()

	doc, items, ok := bodyMessageItems(body)
	if !ok {
		return body
	}

	originalTokens := 0
	lastUserIdx := -1
	for i, item := range items {
		originalTokens += charTokens(msgText(item.Get("content")))
		if item.Get("role").String() == "user" {
			lastUserIdx = i
		}
	}

	raws := make([]string, len(items))
	for i, item := range items {
		raws[i] = item.Raw
	}

	// Step 1: tool-result compression.
	for i, item := range items {
		role := item.Get("role").String()
		if aggressivePreserveSystem && role == "system" {
			continue
		}
		if role == "tool" || role == "function" {
			text := msgText(item.Get("content"))
			if text == "" || strings.HasPrefix(text, compressedPrefix) {
				continue
			}
			result := compressToolResult(text)
			if result.strategy == toolStrategyNone || result.saved <= 0 {
				continue
			}
			raws[i] = msgReplaceTextItem(item, result.compressed)
			continue
		}
		if next, ok := aggressiveCompressToolResultBlocks(item); ok {
			raws[i] = next
		}
	}

	// Step 2: progressive aging.
	raws, _ = applyAging(raws, agingDefaultThresholds, aggressivePreserveSystem, lastUserIdx)

	// Step 3: rule-based summary for messages above maxTokensPerMessage.
	for i, raw := range raws {
		item := ojson.Parse(raw)
		role := item.Get("role").String()
		if aggressivePreserveSystem && role == "system" {
			continue
		}
		if i == lastUserIdx {
			continue
		}
		text := msgText(item.Get("content"))
		if text == "" || strings.HasPrefix(text, compressedPrefix) {
			continue
		}
		if charLen(text) <= aggressiveMaxTokensPerMessage*4 {
			continue
		}
		summary := summarizeRuleBased([]ojson.Result{item}, aggressiveMaxTokensPerMessage, true)
		if summary == "" || charLen(summary) >= charLen(text) {
			continue
		}
		raws[i] = msgReplaceTextItem(item, summary)
	}

	// Downgrade chain: below the savings threshold, try caveman then lite.
	savingsPercent := aggressiveSavingsPercent(originalTokens, raws)
	if savingsPercent < aggressiveMinSavingsPercent {
		if candidate := aggressiveFallback(raws, lastUserIdx, true); candidate != nil {
			if p := aggressiveSavingsPercent(originalTokens, candidate); p > savingsPercent {
				raws = candidate
				savingsPercent = p
			}
		}
		if candidate := aggressiveFallback(raws, lastUserIdx, false); candidate != nil {
			if p := aggressiveSavingsPercent(originalTokens, candidate); p > savingsPercent {
				raws = candidate
			}
		}
	}

	changed := false
	for i := range raws {
		if raws[i] != items[i].Raw {
			changed = true
			break
		}
	}
	if !changed {
		return body
	}

	nextBody, ok := setBodyMessages(doc, raws)
	if !ok {
		return body
	}
	return nextBody
}

// aggressiveFallback applies caveman or lite per message, sparing the system
// prompt and the current user turn. It returns nil when nothing changed.
func aggressiveFallback(raws []string, lastUserIdx int, useCaveman bool) []string {
	out := make([]string, len(raws))
	caveman := cavemanEngine{}
	lite := liteEngine{}
	changed := false
	for i, raw := range raws {
		item := ojson.Parse(raw)
		role := item.Get("role").String()
		if i == lastUserIdx || (aggressivePreserveSystem && role == "system") {
			out[i] = raw
			continue
		}
		text := msgText(item.Get("content"))
		if text == "" || strings.HasPrefix(text, compressedPrefix) {
			out[i] = raw
			continue
		}
		kind := roleSegmentKind(role)
		var next string
		var ok bool
		if useCaveman {
			next, ok = caveman.compressKind(kind, text)
		} else {
			next, ok = lite.compressKind(kind, text)
		}
		if !ok || charLen(next) >= charLen(text) {
			out[i] = raw
			continue
		}
		out[i] = msgReplaceTextItem(item, next)
		changed = true
	}
	if !changed {
		return nil
	}
	return out
}

func aggressiveSavingsPercent(originalTokens int, raws []string) float64 {
	if originalTokens <= 0 {
		return 0
	}
	compressed := 0
	for _, raw := range raws {
		compressed += charTokens(msgText(ojson.Parse(raw).Get("content")))
	}
	return float64(originalTokens-compressed) / float64(originalTokens) * 100
}

// aggressiveCompressToolResultBlocks compresses Anthropic `tool_result` blocks
// inside a message's array content, preserving tool_use_id and block shape.
func aggressiveCompressToolResultBlocks(item ojson.Result) (string, bool) {
	content := item.Get("content")
	if !content.IsArray() {
		return item.Raw, false
	}
	parts := content.Array()
	out := make([]string, len(parts))
	changed := false
	for i, part := range parts {
		out[i] = part.Raw
		if next, ok := aggressiveCompressToolResultBlock(part); ok {
			out[i] = next
			changed = true
		}
	}
	if !changed {
		return item.Raw, false
	}
	doc := ojson.NewDocString(item.Raw)
	if err := doc.SetRaw("content", "["+strings.Join(out, ",")+"]"); err != nil {
		return item.Raw, false
	}
	return doc.String(), true
}

// aggressiveCompressToolResultBlock compresses the inner text of one Anthropic
// `tool_result` block when the content shape allows it.
func aggressiveCompressToolResultBlock(part ojson.Result) (string, bool) {
	if !msgIsToolResultBlock(part) {
		return part.Raw, false
	}
	content := part.Get("content")
	if content.Type == ojson.String {
		text := content.String()
		if text == "" || strings.HasPrefix(text, compressedPrefix) {
			return part.Raw, false
		}
		result := compressToolResult(text)
		if result.strategy == toolStrategyNone || result.saved <= 0 {
			return part.Raw, false
		}
		doc := ojson.NewDocString(part.Raw)
		if err := doc.Set("content", result.compressed); err != nil {
			return part.Raw, false
		}
		return doc.String(), true
	}
	if !content.IsArray() {
		return part.Raw, false
	}
	blocks := content.Array()
	out := make([]string, len(blocks))
	changed := false
	for i, block := range blocks {
		out[i] = block.Raw
		if block.Get("type").String() != "text" {
			continue
		}
		text := block.Get("text").String()
		if text == "" || strings.HasPrefix(text, compressedPrefix) {
			continue
		}
		result := compressToolResult(text)
		if result.strategy == toolStrategyNone || result.saved <= 0 {
			continue
		}
		doc := ojson.NewDocString(block.Raw)
		if err := doc.Set("text", result.compressed); err != nil {
			continue
		}
		out[i] = doc.String()
		changed = true
	}
	if !changed {
		return part.Raw, false
	}
	doc := ojson.NewDocString(part.Raw)
	if err := doc.SetRaw("content", "["+strings.Join(out, ",")+"]"); err != nil {
		return part.Raw, false
	}
	return doc.String(), true
}
