package precompact

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// chainSummarizer records every call and returns a summary that names the
// call index and echoes the previous summary, so chaining is observable.
type chainSummarizer struct {
	calls       []string
	previous    []string
	transcripts []string
}

func (c *chainSummarizer) Summarize(ctx context.Context, model, previous, transcript string) (string, error) {
	n := len(c.calls) + 1
	c.previous = append(c.previous, previous)
	c.transcripts = append(c.transcripts, transcript)
	out := fmt.Sprintf("SUMMARY-%d(prev=%q)", n, previous)
	c.calls = append(c.calls, out)
	return out, nil
}

// toolTurns builds n Claude turns where every turn is user -> assistant
// tool_use -> user tool_result -> assistant text, with a filler of fillerBytes.
func toolTurns(n int, fillerBytes int) []map[string]any {
	filler := strings.Repeat("lorem ipsum ", fillerBytes/12)
	msgs := make([]map[string]any, 0, n*4)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("toolu_%d", i)
		msgs = append(msgs,
			map[string]any{"role": "user", "content": fmt.Sprintf("turn %d %s", i, filler)},
			map[string]any{"role": "assistant", "content": []map[string]any{{"type": "tool_use", "id": id, "name": "bash", "input": map[string]any{"cmd": "ls"}}}},
			map[string]any{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": id, "content": filler}}},
			map[string]any{"role": "assistant", "content": fmt.Sprintf("done %d", i)},
		)
	}
	return msgs
}

func claudeBody(msgs []map[string]any) []byte {
	body, _ := json.Marshal(map[string]any{"model": "claude-test", "system": "s", "messages": msgs})
	return body
}

// assertNoOrphans checks that within msgs every tool_use id has its
// tool_result and vice versa.
func assertNoOrphans(t *testing.T, msgs []string) {
	t.Helper()
	uses := map[string]bool{}
	results := map[string]bool{}
	for _, raw := range msgs {
		for _, b := range gjson.Get(raw, "content").Array() {
			switch b.Get("type").String() {
			case "tool_use":
				uses[b.Get("id").String()] = true
			case "tool_result":
				results[b.Get("tool_use_id").String()] = true
			}
		}
	}
	for id := range uses {
		if !results[id] {
			t.Fatalf("tool_use %s has no tool_result in the same block", id)
		}
	}
	for id := range results {
		if !uses[id] {
			t.Fatalf("tool_result %s has no tool_use in the same block", id)
		}
	}
}

// (a) A middle block of about 3x the aux budget yields 3 chained aux calls.
func TestRollingSummaryChunksMiddleForAuxWindow(t *testing.T) {
	tokens := func(raw string) int { return len(raw) / 4 }
	msgs := make([]string, 0)
	for _, m := range toolTurns(9, 4000) {
		raw, _ := json.Marshal(m)
		msgs = append(msgs, string(raw))
	}
	total := 0
	for _, m := range msgs {
		total += tokens(m)
	}
	auxBudget := total/3 + 1 // each chunk holds roughly three turns
	chunks := ChunkTurns(sdktranslator.FormatClaude, msgs, auxBudget, tokens)
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(chunks))
	}
	n := 0
	for _, c := range chunks {
		assertNoOrphans(t, c)
		n += len(c)
	}
	if n != len(msgs) {
		t.Fatalf("chunks lost messages: %d != %d", n, len(msgs))
	}

	// Drive the same chunks through the interceptor path with a fake executor.
	sum := &chainSummarizer{}
	summary := ""
	for _, c := range chunks {
		out, err := sum.Summarize(context.Background(), "aux", summary, Transcript(c))
		if err != nil {
			t.Fatal(err)
		}
		summary = out
	}
	if len(sum.calls) != 3 {
		t.Fatalf("expected 3 aux calls, got %d", len(sum.calls))
	}
	if !strings.HasPrefix(summary, "SUMMARY-3") {
		t.Fatalf("final summary must come from the last call, got %q", summary)
	}
	if sum.previous[1] != sum.calls[0] || sum.previous[2] != sum.calls[1] {
		t.Fatal("each chunk must receive the previous chunk's summary as context")
	}
	if !strings.Contains(sum.transcripts[0], "turn 0") || !strings.Contains(sum.transcripts[2], "turn 8") {
		t.Fatal("chunks must be in conversation order")
	}
}

// (a, end to end) Apply performs the rolling summary itself.
func TestApplyRollsSummaryAcrossChunks(t *testing.T) {
	sum := &chainSummarizer{}
	cfg := testCfg()
	cfg.KeepRecentTurns = 1
	cfg.KeepRecentTokens = 1 << 30
	it := New(cfg, registry.GetGlobalRegistry(), sum)
	// 200k window * 0.7 = 140k aux budget; 24 middle turns of ~40k tokens need >3 chunks.
	body := claudeBody(toolTurns(25, 80000))
	out, res := it.Apply(context.Background(), cfg, cliproxyexecutor.RequestAfterAuthInterceptRequest{
		SourceFormat: sdktranslator.FormatClaude,
		Model:        "claude-test",
		Body:         body,
		Headers:      map[string][]string{"X-Claude-Code-Session-Id": {"roll"}},
	})
	if out == nil || !res.Compacted {
		t.Fatal("expected compaction")
	}
	if len(sum.calls) < 3 {
		t.Fatalf("expected at least 3 aux calls for a middle block ~3x the aux budget, got %d", len(sum.calls))
	}
	if res.AuxCalls != len(sum.calls) {
		t.Fatalf("AuxCalls=%d but summarizer saw %d", res.AuxCalls, len(sum.calls))
	}
	last := sum.calls[len(sum.calls)-1]
	if !bytes.Contains(out, []byte(fmt.Sprintf("SUMMARY-%d", len(sum.calls)))) {
		t.Fatalf("compacted body must carry the last call's output %q", last)
	}
	for i := 1; i < len(sum.previous); i++ {
		if sum.previous[i] != sum.calls[i-1] {
			t.Fatalf("call %d did not receive summary of call %d", i+1, i)
		}
	}
	kept := gjson.GetBytes(out, "messages").Array()
	raws := make([]string, 0, len(kept))
	for _, k := range kept {
		raws = append(raws, k.Raw)
	}
	assertNoOrphans(t, raws)
	if !strings.Contains(kept[len(kept)-1].Raw, "done 24") {
		t.Fatal("last turn must be kept verbatim")
	}
}

// (b) 4 turns of ~50k tokens with keep-turns 6 and keep-tokens 40k: keep 1.
func TestSplitBudgetKeepsOneTurnWhenTokensExceeded(t *testing.T) {
	tokens := func(raw string) int { return len(raw) / 4 }
	body := claudeBody(toolTurns(4, 160000)) // ~80k tokens per turn (two 160k-byte fillers), well over 170k budget in total
	sp, ok := SplitMessagesBudget(sdktranslator.FormatClaude, body, 6, 40000, tokens)
	if !ok {
		t.Fatal("expected a split")
	}
	if len(sp.Recent) != 4 {
		t.Fatalf("expected exactly one full turn (4 messages) kept, got %d", len(sp.Recent))
	}
	if len(sp.Middle) != 12 {
		t.Fatalf("expected 3 turns (12 messages) in the middle, got %d", len(sp.Middle))
	}
	assertNoOrphans(t, sp.Recent)
	assertNoOrphans(t, sp.Middle)
	if !strings.Contains(sp.Recent[0], "turn 3") {
		t.Fatal("kept turn must be the last one")
	}

	// Same body through Apply compacts (previously: "fewer than 6 turns to keep").
	sum := &chainSummarizer{}
	cfg := testCfg()
	cfg.KeepRecentTurns = 6
	cfg.KeepRecentTokens = 40000
	it := New(cfg, registry.GetGlobalRegistry(), sum)
	out, res := it.Apply(context.Background(), cfg, cliproxyexecutor.RequestAfterAuthInterceptRequest{
		SourceFormat: sdktranslator.FormatClaude,
		Model:        "claude-test",
		Body:         body,
	})
	if out == nil || !res.Compacted {
		t.Fatal("expected compaction with one turn kept and three summarized")
	}
	if got := len(gjson.GetBytes(out, "messages").Array()); got != 6 {
		t.Fatalf("expected summary pair + 4 kept messages = 6, got %d", got)
	}
}

// With a single turn there is nothing to summarize.
func TestSplitBudgetNoSplitWithOneTurn(t *testing.T) {
	tokens := func(raw string) int { return len(raw) / 4 }
	body := claudeBody(toolTurns(1, 100000))
	if _, ok := SplitMessagesBudget(sdktranslator.FormatClaude, body, 6, 40000, tokens); ok {
		t.Fatal("one turn must not be split")
	}
}

// (c) A body under budget is forwarded byte-identical (nil replacement).
func TestApplyUnderBudgetIsByteIdentical(t *testing.T) {
	sum := &chainSummarizer{}
	cfg := testCfg()
	it := New(cfg, registry.GetGlobalRegistry(), sum)
	body := claudeBody(toolTurns(3, 2000))
	orig := append([]byte(nil), body...)
	out, res := it.Apply(context.Background(), cfg, cliproxyexecutor.RequestAfterAuthInterceptRequest{
		SourceFormat: sdktranslator.FormatClaude,
		Model:        "claude-test",
		Body:         body,
	})
	if out != nil || res.Compacted || len(sum.calls) != 0 {
		t.Fatal("under budget must not compact")
	}
	if !bytes.Equal(body, orig) {
		t.Fatal("request body must be untouched")
	}
}

func TestConfigKeepRecentTokensDefault(t *testing.T) {
	c := config.PreCompactConfig{Enabled: true}.WithDefaults()
	if c.KeepRecentTokens != config.DefaultPreCompactKeepRecentTokens {
		t.Fatalf("default keep-recent-tokens = %d", c.KeepRecentTokens)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}
