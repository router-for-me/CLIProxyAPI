package payloadcompress

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/MoeForge/moego/ojson"
)

// contextTrimBody builds an Anthropic-style messages body with one big tool
// result, an old inline image, a thinking turn, and a final user turn.
func contextTrimBody(filler string) string {
	return `{"model":"devin/swe-1-6-slow","messages":[` +
		`{"role":"user","content":[{"type":"text","text":"task"}]},` +
		`{"role":"assistant","content":[{"type":"thinking","thinking":"step by step ` + filler + `"},{"type":"text","text":"using tool"}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"` + filler + `"}]},` +
		`{"role":"assistant","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBOR"}}]},` +
		`{"role":"user","content":[{"type":"text","text":"final question"}]}]}`
}

func contextFitOnDoc(t *testing.T, body, model string, trim ContextTrimConfig) (*ojson.Doc, ContextFitStats) {
	t.Helper()
	doc := ojson.NewDocString(body)
	stats := ApplyContextFit(doc, model, trim)
	return doc, stats
}

func contextFitMessages(t *testing.T, body, model string, trim ContextTrimConfig) []ojson.Result {
	t.Helper()
	doc, stats := contextFitOnDoc(t, body, model, trim)
	msgs := doc.Get("messages")
	if !msgs.Exists() || !msgs.IsArray() {
		t.Fatalf("messages not an array after fit")
	}
	_ = stats
	return msgs.Array()
}

func contextFitFiller(n int) string {
	return strings.Repeat("x", n)
}

func (s ContextFitStats) applied(name string) bool {
	for _, l := range s.Layers {
		if l == name {
			return true
		}
	}
	return false
}

func TestContextTrimDefaults(t *testing.T) {
	cfg := Config{}
	cfg.Sanitize()
	if !cfg.ContextTrim.EnabledOn() {
		t.Fatalf("context trim must default to enabled")
	}
	if got := cfg.ContextTrim.EffectiveReservePercent(); got != 15 {
		t.Fatalf("reserve percent default = %d, want 15", got)
	}
	if got := cfg.ContextTrim.EffectiveToolResultChars(); got != 2000 {
		t.Fatalf("tool result chars default = %d, want 2000", got)
	}
	if got := cfg.ContextTrim.EffectiveKeepLatestImages(); got != 2 {
		t.Fatalf("keep latest images default = %d, want 2", got)
	}
	off := false
	if (&ContextTrimConfig{Enabled: &off}).EnabledOn() {
		t.Fatalf("explicitly disabled trim must stay off")
	}
}

func TestContextTrimNoopUnderLimit(t *testing.T) {
	body := contextTrimBody(contextFitFiller(200))
	messages := contextFitMessages(t, body, "devin/swe-1-6-slow", ContextTrimConfig{})
	if len(messages) != 5 {
		t.Fatalf("small body must stay untouched, got %d messages", len(messages))
	}
}

func TestContextTrimUnknownModelSkips(t *testing.T) {
	body := strings.Replace(contextTrimBody(contextFitFiller(3000000)), `"model":"devin/swe-1-6-slow",`, ``, 1)
	doc, stats := contextFitOnDoc(t, body, "no-such-model-anywhere", ContextTrimConfig{})
	if len(stats.Layers) != 0 || stats.Before != 0 {
		t.Fatalf("unknown model must skip the pass, stats %+v", stats)
	}
	if doc.Get("messages").Array()[1].Get("content.0.thinking").String() == "" {
		t.Fatalf("unknown model must leave the body unchanged")
	}
}

func TestContextTrimToolResultLayer(t *testing.T) {
	// A huge tool result triggers the pass; trim_tools must cut it to 2000 chars.
	body := contextTrimBody(contextFitFiller(3000000))
	doc, stats := contextFitOnDoc(t, body, "devin/swe-1-6-slow", ContextTrimConfig{})
	if !stats.applied("trim_tools") {
		t.Fatalf("trim_tools layer missing from %v", stats.Layers)
	}
	msgs := doc.Get("messages").Array()
	result := msgs[2].Get("content.0.content")
	if got := len([]rune(result.String())); got > 2000+len("\n... [truncated]") {
		t.Fatalf("tool result not trimmed, %d chars", got)
	}
	// Thinking strip only fires while still over target. The single image
	// survives prune: upstream keeps the newest keepLatest (2) images.
	if !stats.applied("strip_thinking") || stats.applied("prune_images") {
		t.Fatalf("unexpected layers in %v", stats.Layers)
	}
}

func TestContextTrimPurifyKeepsToolPairs(t *testing.T) {
	// Keep only two non-system turns: the tool_use and its tool_result must
	// survive together or not at all.
	body := `{"model":"devin/swe-1-6-slow","messages":[` +
		`{"role":"user","content":[{"type":"text","text":"task"}]},` +
		`{"role":"assistant","content":[{"type":"text","text":"` + contextFitFiller(3000000) + `"}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"` + contextFitFiller(3000000) + `"}]},` +
		`{"role":"user","content":[{"type":"text","text":"final question"}]}]}`
	doc, stats := contextFitOnDoc(t, body, "devin/swe-1-6-slow", ContextTrimConfig{})
	if !stats.applied("purify") {
		t.Fatalf("purify layer missing from %v", stats.Layers)
	}
	msgs := doc.Get("messages").Array()
	if len(msgs) >= 5 {
		t.Fatalf("purify did not drop old turns: %d messages", len(msgs))
	}
	toolUses, toolResults := 0, 0
	for _, m := range msgs {
		if role := m.Get("role").String(); role == "assistant" {
			content := m.Get("content")
			if content.IsArray() {
				for _, b := range content.Array() {
					if b.Get("type").String() == "tool_use" {
						toolUses++
					}
				}
			}
		}
		if role := m.Get("role").String(); role == "user" {
			content := m.Get("content")
			if content.IsArray() {
				for _, b := range content.Array() {
					if b.Get("type").String() == "tool_result" {
						toolResults++
					}
				}
			}
		}
	}
	if toolUses != toolResults {
		t.Fatalf("tool pair broken: %d tool_use vs %d tool_result", toolUses, toolResults)
	}
}

func TestContextTrimShapeNoOps(t *testing.T) {
	doc := ojson.NewDocString(`{"weird":{"messages":[1,2,3]}}`)
	stats := ApplyContextFit(doc, "devin/swe-1-6-slow", ContextTrimConfig{})
	if len(stats.Layers) != 0 {
		t.Fatalf("foreign shape must be a clean no-op, got %v", stats.Layers)
	}
	if doc.String() != `{"weird":{"messages":[1,2,3]}}` {
		t.Fatalf("foreign shape body must be untouched")
	}
}

func TestContextTrimGeminiShape(t *testing.T) {
	// Gemini contents shape: a huge functionResponse body triggers trimming.
	body := `{"contents":[` +
		strings.Repeat(`{"role":"user","parts":[{"functionResponse":{"name":"f","response":{"out":"`+contextFitFiller(400000)+`"}}}]},`, 4) +
		`{"role":"user","parts":[{"text":"question"}]}]}`
	doc, stats := contextFitOnDoc(t, body, "devin/swe-1-6-slow", ContextTrimConfig{})
	if len(stats.Layers) == 0 {
		t.Fatalf("gemini shape must trigger the pass")
	}
	if len(doc.Get("contents").Array()) >= 5 {
		t.Fatalf("gemini contents not purified: %d", len(doc.Get("contents").Array()))
	}
}

func TestContextTrimLayerStats(t *testing.T) {
	body := contextTrimBody(contextFitFiller(3000000))
	doc, stats := contextFitOnDoc(t, body, "devin/swe-1-6-slow", ContextTrimConfig{})
	_ = doc
	if stats.Before <= 0 || stats.After <= 0 {
		t.Fatalf("stats must record before/after estimates, got %+v", stats)
	}
	if stats.After >= stats.Before {
		t.Fatalf("after estimate must shrink: %d -> %d", stats.Before, stats.After)
	}
}

func TestContextTrimToolResultCutKeepsUTF8(t *testing.T) {
	items := ojson.NewDocString(`{"m":[{"role":"tool","content":"` + strings.Repeat("汉", 1000) + `"}]}`).Get("m").Array()
	out, changed := contextTrimToolResults(items, 2000)
	if !changed {
		t.Fatal("tool result must be truncated")
	}
	g := out[0].Get("content").String()
	if !strings.HasSuffix(g, contextTrimSuffix) {
		t.Fatalf("tool result not truncated: %q", g)
	}
	if !utf8.ValidString(g) {
		t.Fatal("truncated tool result is not valid UTF-8")
	}
}
func TestContextTrimFixToolPairsResponsesOrphans(t *testing.T) {
	arr := ojson.NewDocString(`{"input":[{"type":"function_call","call_id":"c1","name":"f","arguments":"{}"},` +
		`{"type":"function_call","call_id":"c4","name":"f","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"c1","output":"ok"},` +
		`{"type":"function_call_output","call_id":"c2","output":"orphan"},` +
		`{"type":"function_call","call_id":"c3","name":"g","arguments":"{}"},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`).Get("input").Array()
	out := contextTrimFixToolPairs(arr)
	var callIDs, outputIDs []string
	for _, it := range out {
		if it.Get("type").String() == "function_call" {
			callIDs = append(callIDs, it.Get("call_id").String())
		}
		if it.Get("type").String() == "function_call_output" {
			outputIDs = append(outputIDs, it.Get("call_id").String())
		}
	}
	if strings.Join(callIDs, ",") != "c1" {
		t.Fatalf("orphan/non-adjacent function_call not cleaned: %v", callIDs)
	}
	if strings.Join(outputIDs, ",") != "c1" {
		t.Fatalf("orphaned function_call_output not cleaned: %v", outputIDs)
	}
}

func TestContextTrimFixToolPairsResponsesParallel(t *testing.T) {
	// Parallel calls arrive as a run of function_calls followed by a run of
	// outputs; upstream groups them into one assistant tool_calls message, so
	// all of them must survive together.
	arr := ojson.NewDocString(`{"input":[` +
		`{"type":"function_call","call_id":"c1","name":"f","arguments":"{}"},` +
		`{"type":"function_call","call_id":"c2","name":"f","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"c1","output":"ok"},` +
		`{"type":"function_call_output","call_id":"c2","output":"ok"},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`).Get("input").Array()
	out := contextTrimFixToolPairs(arr)
	var callIDs, outputIDs []string
	for _, it := range out {
		if it.Get("type").String() == "function_call" {
			callIDs = append(callIDs, it.Get("call_id").String())
		}
		if it.Get("type").String() == "function_call_output" {
			outputIDs = append(outputIDs, it.Get("call_id").String())
		}
	}
	if strings.Join(callIDs, ",") != "c1,c2" {
		t.Fatalf("parallel function_calls must all survive: %v", callIDs)
	}
	if strings.Join(outputIDs, ",") != "c1,c2" {
		t.Fatalf("parallel function_call_outputs must all survive: %v", outputIDs)
	}
}

func TestContextTrimFixToolPairsResponsesDroppedCallOrphans(t *testing.T) {
	// c2 has no output in its run, so it is dropped; its output placed after
	// the user turn must not survive as an orphan. The trailing c3 has no
	// output at all and is rejected by Responses for the same reason.
	arr := ojson.NewDocString(`{"input":[` +
		`{"type":"function_call","call_id":"c1","name":"f","arguments":"{}"},` +
		`{"type":"function_call","call_id":"c2","name":"f","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"c1","output":"ok"},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},` +
		`{"type":"function_call_output","call_id":"c2","output":"late"},` +
		`{"type":"function_call","call_id":"c3","name":"g","arguments":"{}"}]}`).Get("input").Array()
	out := contextTrimFixToolPairs(arr)
	var callIDs, outputIDs []string
	for _, it := range out {
		if it.Get("type").String() == "function_call" {
			callIDs = append(callIDs, it.Get("call_id").String())
		}
		if it.Get("type").String() == "function_call_output" {
			outputIDs = append(outputIDs, it.Get("call_id").String())
		}
	}
	if strings.Join(callIDs, ",") != "c1" {
		t.Fatalf("only the call with an adjacent output survives, got %v", callIDs)
	}
	if strings.Join(outputIDs, ",") != "c1" {
		t.Fatalf("output of a dropped call must be dropped too, got %v", outputIDs)
	}
}
func TestContextTrimFixToolPairsDropsOrphanToolUse(t *testing.T) {
	// Anthropic tool_use blocks carry "id", not "tool_use_id"; a revert to
	// tool_use_id here would silently stop dropping orphan tool_use blocks.
	body := `{"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"q"}]},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t2","name":"g","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","content":"ok"}]},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"f","input":{}},{"type":"text","text":"mid"}]},` +
		`{"role":"user","content":[{"type":"text","text":"last"}]}` +
		`]}`
	items := ojson.NewDocString(body).Get("messages").Array()
	fixed := contextTrimFixToolPairs(items)
	var paired, textKept bool
	for _, it := range fixed {
		if it.Get("role").String() != "assistant" {
			continue
		}
		for _, b := range it.Get("content").Array() {
			id := b.Get("id").String()
			if id == "t1" {
				t.Fatalf("orphan tool_use t1 must be dropped")
			}
			if id == "t2" {
				paired = true
			}
			if b.Get("type").String() == "text" && b.Get("text").String() == "mid" {
				textKept = true
			}
		}
	}
	if !paired {
		t.Fatalf("paired tool_use t2 must survive")
	}
	if !textKept {
		t.Fatalf("non-tool blocks in the orphan message must survive")
	}
}

func TestContextTrimSkipStripThinkingWithBlockBinding(t *testing.T) {
	// strip_thinking must not remove them; without the binding it must. Only two
	// non-system turns here, so purify cannot shrink anything and the surviving
	// non-last assistant thinking block stays observable.
	body := `{"model":"devin/swe-1-6-slow","messages":[` +
		`{"role":"system","content":"sys"},` +
		`{"role":"assistant","content":[{"type":"thinking","thinking":"` + contextFitFiller(3000000) + `"},{"type":"text","text":"using tool"}]},` +
		`{"role":"assistant","content":[{"type":"text","text":"done"}]}]}`
	bound := strings.Replace(body, `,"messages"`, `,"thinking":{"type":"adaptive","block_binding":{"prefix_mismatch_behavior":"omit"}},"messages"`, 1)
	doc, stats := contextFitOnDoc(t, bound, "devin/swe-1-6-slow", ContextTrimConfig{})
	if stats.applied("strip_thinking") {
		t.Fatalf("strip_thinking must be skipped when thinking.block_binding is set, layers: %v", stats.Layers)
	}
	if !contextTrimHasThinkingBlock(doc) {
		t.Fatalf("bound thinking block must survive trim, layers: %v", stats.Layers)
	}

	plain, plainStats := contextFitOnDoc(t, body, "devin/swe-1-6-slow", ContextTrimConfig{})
	if !plainStats.applied("strip_thinking") {
		t.Fatalf("strip_thinking must run without block_binding, layers: %v", plainStats.Layers)
	}
	if contextTrimHasThinkingBlock(plain) {
		t.Fatalf("unbound thinking block must be stripped, layers: %v", plainStats.Layers)
	}
}

func contextTrimHasThinkingBlock(doc *ojson.Doc) bool {
	messages := doc.Get("messages")
	if !messages.IsArray() {
		return false
	}
	for _, m := range messages.Array() {
		for _, b := range contextTrimBlocks(m) {
			if contextTrimIsThinkingBlock(b) {
				return true
			}
		}
	}
	return false
}
