package payloadcompress

import (
	"strings"
	"unicode/utf8"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// Reactive context-fit trim, a port of upstream OmniRoute's compressContext
// safety pass (open-sse/services/contextManager.ts) driven by the two-pass
// structure of chatCore.ts:1925-2085. The proactive pass fires when the
// messages-only estimate exceeds the proactive threshold and trims to it; the
// last-resort pass fires only when the whole input (messages, tools, system,
// instructions) still reaches the ceiling and trims to ceiling minus the tools
// reserve and one token. Layers run in upstream order (trim tool results, prune
// old inline images, strip thinking from non-last assistant turns), with an
// aggressive purify of the history as the last layer. The pass is independent
// of the optional prompt-compression engines and gated solely on
// compression.context-trim.enabled (default on, like upstream).

// Constants extracted from upstream contextManager.ts.
const (
	// Upstream trims every tool_result/tool message at 2000 chars.
	contextTrimDefaultToolChars = 2000
	// Bounded per-image token budget (IMAGE_TOKEN_ESTIMATE): inline base64
	// payloads are not counted as text.
	contextTrimImageTokens = 1200
	// Rough chars-per-token ratio for the trigger estimate (CHARS_PER_TOKEN).
	// This is a trigger heuristic, not a tokenizer.
	contextTrimCharsPerToken = 4
	contextTrimPlaceholder   = "[Earlier image removed to fit context window]"
	contextTrimSuffix        = "\n... [truncated]"
	// Byte-stable: no per-request counts so the system prompt prefix stays
	// cacheable across a growing conversation (upstream #14600).
	contextTrimDropNotice = "[Context trimmed: older messages were removed to fit the context window]"
	// Upstream writes this into an assistant turn left with no content after the
	// thinking strip (contextManager.ts:745).
	contextTrimThinkingPlaceholder = "[thinking compressed]"
	// Upstream keeps this many newest inline images.
	contextTrimDefaultKeepImages = 2
	// Upstream getTokenLimit fallbacks (DEFAULT_LIMITS, contextManager.ts:13) for
	// a model the registry does not know.
	contextTrimFallbackClaude  = 200000
	contextTrimFallbackGemini  = 1000000
	contextTrimFallbackCodex   = 400000
	contextTrimFallbackDefault = 128000
	// Which pass fired, recorded on ContextFitStats.Pass.
	contextTrimPassProactive  = "proactive"
	contextTrimPassLastResort = "last_resort"
)

// ContextFitStats reports what the reactive trim did for one request.
type ContextFitStats struct {
	Applied bool
	Limit   int    // resolved context window
	Pass    string // proactive, last_resort, or "proactive,last_resort"
	Before  int    // estimated message tokens before the passes
	After   int    // estimated message tokens after the passes
	Layers  []string
}

// ContextTrimLimit resolves the context window for a model from the registry:
// the explicit per-model MaxContextLength override wins over ContextLength.
// A model the registry does not know falls back to upstream's provider defaults
// (contextManager.ts resolveTokenLimit), so the pass still runs.
func ContextTrimLimit(model string) int {
	if model != "" {
		if info := registry.LookupModelInfo(model); info != nil {
			if info.MaxContextLength > 0 {
				return info.MaxContextLength
			}
			if info.ContextLength > 0 {
				return info.ContextLength
			}
		}
	}
	return contextTrimFallbackLimit(model)
}

// contextTrimFallbackLimit is the tail of upstream resolveTokenLimit: a window
// hinted by the model name, then the generic 128000 catch-all.
func contextTrimFallbackLimit(model string) int {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "claude"):
		return contextTrimFallbackClaude
	case strings.Contains(m, "gemini"):
		return contextTrimFallbackGemini
	case strings.Contains(m, "gpt"), strings.Contains(m, "o1"),
		strings.Contains(m, "o3"), strings.Contains(m, "o4"),
		strings.Contains(m, "codex"):
		return contextTrimFallbackCodex
	}
	return contextTrimFallbackDefault
}

// ApplyContextFit runs the reactive context-trim passes over the doc in place.
// Supported shapes (detected by top-level array key, provider-final at this
// stage): Anthropic/OpenAI chat "messages", Codex Responses "input", Gemini
// "contents". Any other shape is a clean no-op.
func ApplyContextFit(doc *ojson.Doc, model string, trim ContextTrimConfig) ContextFitStats {
	var st ContextFitStats
	if doc == nil {
		return st
	}
	// Upstream reads the model from the body when present.
	if m := doc.Get("model").String(); m != "" {
		model = m
	}
	limit := ContextTrimLimit(model)
	path, messages := contextTrimArray(doc)
	if messages == nil {
		return st
	}
	st.Limit = limit
	// Pass 1 target: floor((ceiling - tools reserve) * ratio), floored at 1.
	threshold := (limit - contextTrimToolsReserve(doc)) * trim.EffectiveProactiveThresholdPercent() / 100
	if threshold < 1 {
		threshold = 1
	}
	st.Before = contextTrimEstimate(messages)
	if st.Before > threshold {
		st.notePass(contextTrimPassProactive)
		messages = contextTrimLayers(doc, path, messages, threshold, trim, &st)
	}
	// Pass 2: last resort against the concrete input budget, not the threshold.
	total := contextTrimEstimate(messages) + contextTrimToolsReserve(doc) +
		contextTrimMeasure(doc.Get("system")) + contextTrimMeasure(doc.Get("instructions"))
	if total >= limit {
		target := limit - contextTrimToolsReserve(doc) - 1
		if target < 1 {
			target = 1
		}
		st.notePass(contextTrimPassLastResort)
		messages = contextTrimLayers(doc, path, messages, target, trim, &st)
	}
	st.finish(contextTrimEstimate(messages))
	return st
}

// contextTrimToolsReserve is upstream's `estimateTokens(body.tools)`: the whole
// tools array measured as text. Pass 1 subtracts it from the ceiling before
// applying the proactive ratio (chatCore.ts:1945).
func contextTrimToolsReserve(doc *ojson.Doc) int {
	tools := doc.Get("tools")
	if !tools.IsArray() {
		return 0
	}
	return (len(tools.Raw) + contextTrimCharsPerToken - 1) / contextTrimCharsPerToken
}

// contextTrimLayers applies the upstream layer chain in order and stops as soon
// as the message estimate fits target. Applied layers are recorded on st.
func contextTrimLayers(doc *ojson.Doc, path string, messages []ojson.Result, target int, trim ContextTrimConfig, st *ContextFitStats) []ojson.Result {
	tokens := contextTrimEstimate(messages)
	if tokens <= target {
		return messages
	}
	apply := func(name string, layer func([]ojson.Result) ([]ojson.Result, bool)) {
		next, changed := layer(messages)
		if !changed {
			return
		}
		messages = next
		contextTrimSetArray(doc, path, messages)
		st.Layers = append(st.Layers, name)
		tokens = contextTrimEstimate(messages)
	}
	// Upstream layer order: trim tools, prune images, compress thinking,
	// then last-resort purify.
	apply("trim_tools", func(items []ojson.Result) ([]ojson.Result, bool) {
		return contextTrimToolResults(items, trim.EffectiveToolResultChars())
	})
	if tokens <= target {
		return messages
	}
	apply("prune_images", func(items []ojson.Result) ([]ojson.Result, bool) {
		return contextTrimPruneImages(items, trim.EffectiveKeepLatestImages())
	})
	if tokens <= target {
		return messages
	}
	// Anthropic thinking block_binding validation still references preserved
	// thinking blocks (see applyClaudePreservedThinkingBindingDoc); removing
	// them would break the request, so skip the layer for bound thinking.
	if !doc.Get("thinking.block_binding").Exists() {
		apply("strip_thinking", contextTrimThinking)
	}
	if tokens <= target {
		return messages
	}
	apply("purify", func(items []ojson.Result) ([]ojson.Result, bool) {
		return contextTrimPurify(items, target)
	})
	return messages
}

// notePass records which pass fired; a request can fire both.
func (s *ContextFitStats) notePass(pass string) {
	if s.Pass == "" {
		s.Pass = pass
		return
	}
	s.Pass += "," + pass
}

func (s *ContextFitStats) finish(tokens int) {
	if len(s.Layers) > 0 {
		s.Applied = true
	}
	s.After = tokens
}

// contextTrimArray finds the conversation array for the supported shapes.
func contextTrimArray(doc *ojson.Doc) (string, []ojson.Result) {
	for _, path := range []string{"messages", "input", "contents"} {
		arr := doc.Get(path)
		if arr.IsArray() {
			return path, arr.Array()
		}
	}
	return "", nil
}

// contextTrimSetArray rewrites the whole conversation array from item raws.
func contextTrimSetArray(doc *ojson.Doc, path string, items []ojson.Result) {
	parts := make([]string, len(items))
	for i, it := range items {
		parts[i] = it.Raw
	}
	_ = doc.SetRaw(path, "["+strings.Join(parts, ",")+"]")
}

// contextTrimEditItem applies fn to one message and returns its new raw JSON.
func contextTrimEditItem(raw string, fn func(d *ojson.Doc)) string {
	d := ojson.NewDocString(raw)
	fn(d)
	return d.String()
}

// ─── Layer 1: trim tool messages ──────────────────────────────────────────

// contextTrimToolResults hard-cuts oversized tool results at maxChars:
// OpenAI "tool" string contents and Claude "user" tool_result string blocks.
func contextTrimToolResults(items []ojson.Result, maxChars int) ([]ojson.Result, bool) {
	changed := false
	out := make([]ojson.Result, len(items))
	for i, item := range items {
		role := item.Get("role").String()
		content := item.Get("content")
		switch {
		case role == "tool" && content.Type == ojson.String && len(content.String()) > maxChars:
			raw := content.String()
			out[i] = resultFromRaw(contextTrimEditItem(item.Raw, func(d *ojson.Doc) {
				_ = d.Set("content", contextTrimCut(raw, maxChars)+contextTrimSuffix)
			}))
			changed = true
			continue
		case role == "user" && content.IsArray():
			blocks := content.Array()
			next := make([]string, 0, len(blocks))
			itemChanged := false
			for _, block := range blocks {
				if block.Get("type").String() == "tool_result" {
					if bc := block.Get("content"); bc.Type == ojson.String && len(bc.String()) > maxChars {
						raw := bc.String()
						next = append(next, contextTrimEditItem(block.Raw, func(d *ojson.Doc) {
							_ = d.Set("content", contextTrimCut(raw, maxChars)+contextTrimSuffix)
						}))
						itemChanged = true
						continue
					}
				}
				next = append(next, block.Raw)
			}
			if itemChanged {
				out[i] = resultFromRaw(contextTrimEditItem(item.Raw, func(d *ojson.Doc) {
					_ = d.SetRaw("content", "["+strings.Join(next, ",")+"]")
				}))
				changed = true
				continue
			}
		}
		out[i] = item
	}
	return out, changed
}

// contextTrimCut cuts raw at maxChars on a rune boundary so multi-byte UTF-8
// is never split mid-character (upstream cuts on UTF-16 code units).
func contextTrimCut(raw string, maxChars int) string {
	if len(raw) <= maxChars {
		return raw
	}
	cut := maxChars
	for cut > 0 && !utf8.RuneStart(raw[cut]) {
		cut--
	}
	return raw[:cut]
}

// ─── Estimator ────────────────────────────────────────────────────────────

// contextTrimEstimate mirrors upstream estimateTokens: ceil(chars/4) per text
// plus a flat budget per inline base64 image. Trigger heuristic only.
func contextTrimEstimate(items []ojson.Result) int {
	tokens := 0
	for _, m := range items {
		tokens += 4 // per-message overhead
		tokens += contextTrimMeasure(m.Get("content"))
		// Gemini items carry parts[] instead of content.
		if parts := m.Get("parts"); parts.IsArray() {
			tokens += contextTrimMeasure(parts)
		}
		if tc := m.Get("tool_calls"); tc.IsArray() {
			tokens += (len(tc.Raw) + contextTrimCharsPerToken - 1) / contextTrimCharsPerToken
		}
	}
	return tokens
}

func contextTrimMeasure(v ojson.Result) int {
	switch {
	case !v.Exists():
		return 0
	case v.IsArray():
		total := 2
		for _, b := range v.Array() {
			total += contextTrimBlockTokens(b)
		}
		return total
	case v.IsObject():
		// Gemini: content is {parts: [...]}.
		if parts := v.Get("parts"); parts.IsArray() {
			total := 2
			for _, b := range parts.Array() {
				total += contextTrimBlockTokens(b)
			}
			return total
		}
		return (len(v.Raw) + contextTrimCharsPerToken - 1) / contextTrimCharsPerToken
	case v.Type == ojson.String:
		return (len(v.String()) + contextTrimCharsPerToken - 1) / contextTrimCharsPerToken
	default:
		return (len(v.Raw) + contextTrimCharsPerToken - 1) / contextTrimCharsPerToken
	}
}

func contextTrimBlockTokens(b ojson.Result) int {
	if contextTrimIsImageBlock(b) {
		return contextTrimImageTokens
	}
	if b.IsArray() {
		return contextTrimMeasure(b)
	}
	if b.IsObject() {
		// Nested tool_result content or Codex part arrays.
		if nested := b.Get("content"); nested.Exists() && nested.Type != ojson.String {
			return contextTrimMeasure(nested) + 4
		}
		if nested := b.Get("parts"); nested.IsArray() {
			return contextTrimMeasure(nested) + 4
		}
	}
	return (len(b.Raw) + contextTrimCharsPerToken - 1) / contextTrimCharsPerToken
}

// contextTrimIsImageBlock covers the inline base64 shapes: Claude
// {type:"image",source:{type:"base64",data:...}}, OpenAI
// {type:"image_url",image_url:"data:..."|{url:"data:..."}},
// Codex {type:"input_image",image_url:"data:..."}, and Gemini
// {inline_data:{mime_type:"image/...",data:...}} (either snake or camel case).
func contextTrimIsImageBlock(b ojson.Result) bool {
	if !b.IsObject() {
		return false
	}
	switch b.Get("type").String() {
	case "image":
		src := b.Get("source")
		return src.Get("type").String() == "base64" && src.Get("data").Type == ojson.String
	case "image_url", "input_image":
		return contextTrimDataURL(b.Get("image_url"))
	}
	mime := b.Get("inline_data.mime_type").String()
	if mime == "" {
		mime = b.Get("inlineData.mimeType").String()
	}
	if !strings.HasPrefix(mime, "image/") {
		return false
	}
	return b.Get("inline_data.data").Type == ojson.String || b.Get("inlineData.data").Type == ojson.String
}

func contextTrimDataURL(v ojson.Result) bool {
	s := ""
	if v.Type == ojson.String {
		s = v.String()
	} else if v.IsObject() {
		s = v.Get("url").String()
	}
	return strings.HasPrefix(s, "data:image/")
}

func resultFromRaw(raw string) ojson.Result {
	// ojson's Doc.Get cannot address the document root, so wrap the item in
	// a one-key object and return the nested result.
	return ojson.NewDocString(`{"v":` + raw + `}`).Get("v")
}

// ─── Layer 2: prune older inline images ───────────────────────────────────

// contextTrimPruneImages replaces all but the newest keepLatest inline base64
// images (outside the last message) with a placeholder text block.
func contextTrimPruneImages(items []ojson.Result, keepLatest int) ([]ojson.Result, bool) {
	if len(items) == 0 {
		return items, false
	}
	type loc struct{ msg, block int }
	var locs []loc
	for mi := 0; mi < len(items)-1; mi++ { // never touch the last message
		blocks := contextTrimBlocks(items[mi])
		for bi, block := range blocks {
			if contextTrimIsImageBlock(block) {
				locs = append(locs, loc{mi, bi})
			}
		}
	}
	if len(locs) <= keepLatest {
		return items, false
	}
	prunable := locs[:len(locs)-keepLatest]
	byMsg := map[int][]int{}
	for _, l := range prunable {
		byMsg[l.msg] = append(byMsg[l.msg], l.block)
	}
	changed := false
	out := make([]ojson.Result, len(items))
	for i, item := range items {
		idxs, ok := byMsg[i]
		if !ok {
			out[i] = item
			continue
		}
		drop := map[int]bool{}
		for _, bi := range idxs {
			drop[bi] = true
		}
		raw := contextTrimEditItem(item.Raw, func(d *ojson.Doc) {
			if d.Get("content").IsObject() {
				// Gemini {content:{parts:[...]}}
				parts := d.Get("content.parts").Array()
				next := make([]string, 0, len(parts))
				for bi, b := range parts {
					if drop[bi] {
						next = append(next, `{"text":`+ojsonQuote(contextTrimPlaceholder)+`}`)
					} else {
						next = append(next, b.Raw)
					}
				}
				_ = d.SetRaw("content.parts", "["+strings.Join(next, ",")+"]")
				return
			}
			blocks := d.Get("content").Array()
			next := make([]string, 0, len(blocks))
			for bi, b := range blocks {
				if drop[bi] {
					next = append(next, `{"type":"text","text":`+ojsonQuote(contextTrimPlaceholder)+`}`)
				} else {
					next = append(next, b.Raw)
				}
			}
			_ = d.SetRaw("content", "["+strings.Join(next, ",")+"]")
		})
		out[i] = resultFromRaw(raw)
		changed = true
	}
	return out, changed
}

// contextTrimBlocks returns the content blocks of a conversation item,
// handling array content (messages/input) and Gemini {content:{parts:[]}}.
func contextTrimBlocks(item ojson.Result) []ojson.Result {
	c := item.Get("content")
	if c.IsArray() {
		return c.Array()
	}
	if c.IsObject() {
		if parts := c.Get("parts"); parts.IsArray() {
			return parts.Array()
		}
	}
	return nil
}

// ─── Layer 3: strip thinking from non-last assistant turns ────────────────

// contextTrimThinking strips thinking blocks from every assistant turn except
// the last one, matching upstream compressThinking (contextManager.ts:728 keeps
// only block.type === "thinking" as a filter target). Role-less "reasoning"
// items are stripped only when they belong to an older turn: ones positioned
// after the last user message carry the live tool loop and must be replayed.
func contextTrimThinking(items []ojson.Result) ([]ojson.Result, bool) {
	lastAssistant := -1
	lastUser := -1
	for i := len(items) - 1; i >= 0; i-- {
		switch items[i].Get("role").String() {
		case "assistant":
			if lastAssistant < 0 {
				lastAssistant = i
			}
		case "user":
			if lastUser < 0 {
				lastUser = i
			}
		}
		if lastAssistant >= 0 && lastUser >= 0 {
			break
		}
	}
	changed := false
	out := make([]ojson.Result, 0, len(items))
	for i, item := range items {
		// Codex/Responses reasoning items carry no role: keep the live turn's.
		if item.Get("type").String() == "reasoning" && lastUser >= 0 && i < lastUser {
			changed = true
			continue
		}
		if item.Get("role").String() != "assistant" || i == lastAssistant {
			out = append(out, item)
			continue
		}
		content := item.Get("content")
		if content.IsArray() {
			blocks := content.Array()
			next := make([]string, 0, len(blocks))
			itemChanged := false
			for _, b := range blocks {
				if contextTrimIsThinkingBlock(b) {
					itemChanged = true
					continue
				}
				next = append(next, b.Raw)
			}
			if !itemChanged {
				out = append(out, item)
				continue
			}
			raw := contextTrimEditItem(item.Raw, func(d *ojson.Doc) {
				if len(next) == 0 {
					// Upstream marks a thinking-only turn instead of emptying it
					// (contextManager.ts:745); empty assistant content is rejected.
					_ = d.Set("content", contextTrimThinkingPlaceholder)
				} else {
					_ = d.SetRaw("content", "["+strings.Join(next, ",")+"]")
				}
			})
			out = append(out, resultFromRaw(raw))
			changed = true
			continue
		}
		// OpenAI assistant reasoning fields on the message itself.
		if content.IsObject() {
			parts := content.Get("parts")
			if parts.IsArray() {
				blocks := parts.Array()
				next := make([]string, 0, len(blocks))
				itemChanged := false
				for _, b := range blocks {
					if contextTrimIsThinkingBlock(b) {
						itemChanged = true
						continue
					}
					next = append(next, b.Raw)
				}
				if !itemChanged {
					out = append(out, item)
					continue
				}
				raw := contextTrimEditItem(item.Raw, func(d *ojson.Doc) {
					if len(next) == 0 {
						_ = d.SetRaw("content.parts", `[{"text":"`+contextTrimThinkingPlaceholder+`"}]`)
					} else {
						_ = d.SetRaw("content.parts", "["+strings.Join(next, ",")+"]")
					}
				})
				out = append(out, resultFromRaw(raw))
				changed = true
				continue
			}
		}
		// OpenAI chat assistant "reasoning" string. "reasoning_content" is left
		// alone: upstream does not strip it, and providers that require the
		// reasoning replay answer a missing field with HTTP 400.
		raw := item.Raw
		itemChanged := false
		if item.Get("reasoning").Exists() && item.Get("reasoning").Type == ojson.String {
			raw = contextTrimEditItem(raw, func(d *ojson.Doc) { _ = d.Delete("reasoning") })
			itemChanged = true
		}
		if itemChanged {
			out = append(out, resultFromRaw(raw))
			changed = true
			continue
		}
		out = append(out, item)
	}
	return out, changed
}

func contextTrimIsThinkingBlock(b ojson.Result) bool {
	if !b.IsObject() {
		return false
	}
	// Upstream filters "thinking" only (contextManager.ts:744); a redacted
	// thinking block is an opaque replay token, not reasoning text, so it stays.
	switch b.Get("type").String() {
	case "thinking", "reasoning":
		return true
	}
	return false
}

// ─── Layer 4: last-resort purify ──────────────────────────────────────────

// contextTrimPurify drops the oldest non-system turns: keep = system +
// last N, shrinking the kept tail 30% per iteration (upstream purifyHistory)
// until the estimate fits the target, then repairs tool_use/tool_result pair
// consistency across the kept tail.
func contextTrimPurify(items []ojson.Result, target int) ([]ojson.Result, bool) {
	system := make([]ojson.Result, 0, len(items))
	nonSystem := make([]ojson.Result, 0, len(items))
	for _, item := range items {
		role := item.Get("role").String()
		if role == "system" || role == "developer" {
			system = append(system, item)
		} else {
			nonSystem = append(nonSystem, item)
		}
	}
	if len(nonSystem) <= 2 {
		return items, false
	}
	build := func(keep int) []ojson.Result {
		result := make([]ojson.Result, 0, len(system)+keep)
		result = append(result, system...)
		result = append(result, nonSystem[len(nonSystem)-keep:]...)
		return result
	}
	keep := len(nonSystem)
	for keep > 2 {
		if contextTrimEstimate(build(keep)) <= target {
			break
		}
		keep = keep * 7 / 10 // drop 30% each iteration
		if keep < 2 {
			keep = 2
		}
	}
	if keep >= len(nonSystem) {
		return items, false
	}
	kept := nonSystem[len(nonSystem)-keep:]

	// Merge a byte-stable drop notice into the leading system/developer
	// message; prepend a synthetic one when there is no system message.
	if len(system) > 0 {
		first := system[0]
		raw := contextTrimEditItem(first.Raw, func(d *ojson.Doc) {
			c := d.Get("content")
			if c.Type == ojson.String {
				_ = d.Set("content", contextTrimDropNotice+"\n\n"+c.String())
			} else if c.IsObject() && c.Get("parts").IsArray() {
				parts := c.Get("parts").Array()
				next := []string{`{"text":` + ojsonQuote(contextTrimDropNotice) + `}`}
				for _, b := range parts {
					next = append(next, b.Raw)
				}
				_ = d.SetRaw("content.parts", "["+strings.Join(next, ",")+"]")
			} else if c.IsArray() {
				blocks := c.Array()
				next := []string{`{"type":"text","text":` + ojsonQuote(contextTrimDropNotice) + `}`}
				for _, b := range blocks {
					next = append(next, b.Raw)
				}
				_ = d.SetRaw("content", "["+strings.Join(next, ",")+"]")
			}
		})
		system = append([]ojson.Result{resultFromRaw(raw)}, system[1:]...)
	} else {
		system = []ojson.Result{resultFromRaw(`{"role":"system","content":` + ojsonQuote(contextTrimDropNotice) + `}`)}
	}

	result := make([]ojson.Result, 0, len(system)+len(kept))
	result = append(result, system...)
	result = append(result, kept...)
	result = contextTrimReanchorUser(result, len(system))
	result = contextTrimFixToolPairs(result)
	return result, true
}

// contextTrimReanchorUser moves the newest real user query (not a
// tool_result carrier) right after the system messages, mirroring upstream:
// some chat templates (vLLM/Qwen) reject a tail with no user turn.
func contextTrimReanchorUser(items []ojson.Result, systemCount int) []ojson.Result {
	for i := systemCount; i < len(items); i++ {
		item := items[i]
		if item.Get("role").String() != "user" {
			continue
		}
		if contextTrimIsToolResultCarrier(item) {
			continue
		}
		if i == systemCount {
			return items
		}
		out := make([]ojson.Result, 0, len(items))
		out = append(out, items[:systemCount]...)
		out = append(out, item)
		out = append(out, items[systemCount:i]...)
		out = append(out, items[i+1:]...)
		return out
	}
	return items
}

func contextTrimIsToolResultCarrier(item ojson.Result) bool {
	blocks := contextTrimBlocks(item)
	if len(blocks) == 0 {
		return false
	}
	for _, b := range blocks {
		if b.Get("type").String() != "tool_result" {
			return false
		}
	}
	return true
}

// contextTrimResponsesKeepCalls returns the call_ids of Codex Responses
// function_call items that can be kept: a run of consecutive (parallel) calls
// pairs as one unit with the run of function_call_output items that follows it,
// and a call without an output there is rejected by the Responses API.
func contextTrimResponsesKeepCalls(items []ojson.Result) map[string]bool {
	keep := map[string]bool{}
	for i := 0; i < len(items); {
		if items[i].Get("type").String() != "function_call" {
			i++
			continue
		}
		end := i
		for end < len(items) && items[end].Get("type").String() == "function_call" {
			end++
		}
		outputs := map[string]bool{}
		for k := end; k < len(items) && items[k].Get("type").String() == "function_call_output"; k++ {
			if id := items[k].Get("call_id").String(); id != "" {
				outputs[id] = true
			}
		}
		for _, call := range items[i:end] {
			if id := call.Get("call_id").String(); id != "" && outputs[id] {
				keep[id] = true
			}
		}
		i = end
	}
	return keep
}

// contextTrimFixToolPairs keeps tool_use/tool_result pairs consistent after
// the tail slice: tool_result/tool messages whose call vanished are dropped,
// orphan tool_use blocks are removed (except on the last message), empty
// assistant turns are dropped, and Claude-style adjacency is enforced.
func contextTrimFixToolPairs(items []ojson.Result) []ojson.Result {
	toolCallIDs := map[string]bool{}
	toolResultIDs := map[string]bool{}
	survivingCalls := contextTrimResponsesKeepCalls(items)
	for _, item := range items {
		role := item.Get("role").String()
		if role == "assistant" {
			if tc := item.Get("tool_calls"); tc.IsArray() {
				for _, c := range tc.Array() {
					if id := c.Get("id").String(); id != "" {
						toolCallIDs[id] = true
					}
				}
			}
			for _, b := range contextTrimBlocks(item) {
				if b.Get("type").String() == "tool_use" {
					if id := b.Get("id").String(); id != "" {
						toolCallIDs[id] = true
					}
				}
			}
		} else if role == "tool" {
			if id := item.Get("tool_call_id").String(); id != "" {
				toolResultIDs[id] = true
			}
		} else {
			switch item.Get("type").String() {
			case "function_call":
				// Codex Responses: calls pair with outputs via call_id. Only the
				// calls that survive contextTrimResponsesKeepCalls count as live,
				// so a dropped call orphans its own output in the pass below.
				for id := range survivingCalls {
					toolCallIDs[id] = true
				}
			case "function_call_output":
				if id := item.Get("call_id").String(); id != "" {
					toolResultIDs[id] = true
				}
			default:
				for _, b := range contextTrimBlocks(item) {
					if b.Get("type").String() == "tool_result" {
						if id := b.Get("tool_use_id").String(); id != "" {
							toolResultIDs[id] = true
						}
					}
				}
			}
		}
	}

	last := len(items) - 1
	out := make([]ojson.Result, 0, len(items))
	for i, item := range items {
		role := item.Get("role").String()
		// OpenAI tool messages: drop results without a surviving call.
		if role == "tool" {
			id := item.Get("tool_call_id").String()
			if id != "" && !toolCallIDs[id] {
				continue
			}
		} else if item.Get("type").String() == "function_call_output" {
			if id := item.Get("call_id").String(); id != "" && !toolCallIDs[id] {
				continue
			}
		}
		if item.Get("type").String() == "function_call" {
			// Parallel function_calls form one unit with the outputs after
			// them; contextTrimResponsesKeepCalls already decided survival.
			if survivingCalls[item.Get("call_id").String()] {
				out = append(out, item)
			}
			continue
		}
		if role == "assistant" && i != last {
			// Claude/OpenAI-array tool_use blocks: keep only those with a
			// surviving result AND adjacent result in the immediately next
			// message (upstream fixToolPairs + fixToolAdjacency).
			next := items[i+1]
			var nextResultIDs map[string]bool
			if nr := next.Get("role").String(); nr == "tool" {
				nextResultIDs = map[string]bool{}
				if id := next.Get("tool_call_id").String(); id != "" {
					nextResultIDs[id] = true
				}
			} else if nr == "user" {
				nextResultIDs = map[string]bool{}
				for _, b := range contextTrimBlocks(next) {
					if b.Get("type").String() == "tool_result" {
						if id := b.Get("tool_use_id").String(); id != "" {
							nextResultIDs[id] = true
						}
					}
				}
			}
			raw := item.Raw
			itemChanged := false
			if tc := item.Get("tool_calls"); tc.IsArray() {
				calls := tc.Array()
				keepCalls := make([]string, 0, len(calls))
				for _, c := range calls {
					id := c.Get("id").String()
					if id != "" && (!toolResultIDs[id] || nextResultIDs == nil || !nextResultIDs[id]) {
						itemChanged = true
						continue
					}
					keepCalls = append(keepCalls, c.Raw)
				}
				if itemChanged {
					updated := raw
					updated = contextTrimEditItem(updated, func(d *ojson.Doc) {
						if len(keepCalls) == 0 {
							_ = d.Delete("tool_calls")
						} else {
							_ = d.SetRaw("tool_calls", "["+strings.Join(keepCalls, ",")+"]")
						}
					})
					raw, itemChanged = updated, true
				}
			}
			// Array-content tool_use blocks.
			if content := item.Get("content"); content.IsArray() {
				blocks := content.Array()
				keepBlocks := make([]string, 0, len(blocks))
				blockChanged := false
				for _, b := range blocks {
					id := b.Get("id").String()
					if b.Get("type").String() == "tool_use" &&
						id != "" && (!toolResultIDs[id] || nextResultIDs == nil || !nextResultIDs[id]) {
						blockChanged = true
						continue
					}
					keepBlocks = append(keepBlocks, b.Raw)
				}
				if blockChanged {
					updated := raw
					updated = contextTrimEditItem(updated, func(d *ojson.Doc) {
						if len(keepBlocks) == 0 {
							_ = d.Set("content", "")
						} else {
							_ = d.SetRaw("content", "["+strings.Join(keepBlocks, ",")+"]")
						}
					})
					raw, itemChanged = updated, true
				}
			}
			if itemChanged {
				// Drop the turn if it became empty.
				r := resultFromRaw(raw)
				if contextTrimIsEmptyAssistant(r) {
					continue
				}
				out = append(out, r)
				continue
			}
		}
		// user/tool_result blocks without a surviving call.
		if role == "user" {
			blocks := contextTrimBlocks(item)
			if len(blocks) > 0 {
				keepBlocks := make([]string, 0, len(blocks))
				changed := false
				for _, b := range blocks {
					if b.Get("type").String() == "tool_result" {
						id := b.Get("tool_use_id").String()
						if id != "" && !toolCallIDs[id] {
							changed = true
							continue
						}
					}
					keepBlocks = append(keepBlocks, b.Raw)
				}
				if changed {
					raw := contextTrimEditItem(item.Raw, func(d *ojson.Doc) {
						if item.Get("content").IsObject() && item.Get("content.parts").IsArray() {
							_ = d.SetRaw("content.parts", "["+strings.Join(keepBlocks, ",")+"]")
						} else {
							_ = d.SetRaw("content", "["+strings.Join(keepBlocks, ",")+"]")
						}
					})
					out = append(out, resultFromRaw(raw))
					continue
				}
			}
		}
		out = append(out, item)
	}
	return out
}

func contextTrimIsEmptyAssistant(item ojson.Result) bool {
	if tc := item.Get("tool_calls"); tc.IsArray() && len(tc.Array()) > 0 {
		return false
	}
	c := item.Get("content")
	if c.Type == ojson.String {
		return strings.TrimSpace(c.String()) == ""
	}
	if c.IsArray() {
		return len(c.Array()) == 0
	}
	if c.IsObject() {
		if parts := c.Get("parts"); parts.IsArray() {
			return len(parts.Array()) == 0
		}
	}
	return !c.Exists()
}

// ojsonQuote encodes s as a JSON string literal.
func ojsonQuote(s string) string {
	return string(ojson.AppendJSONString(make([]byte, 0, len(s)+2), s))
}
