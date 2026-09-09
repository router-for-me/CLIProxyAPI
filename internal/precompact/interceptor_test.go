package precompact

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type fakeSummarizer struct {
	calls int
	out   string
	err   error
}

func (f *fakeSummarizer) Summarize(ctx context.Context, model, previous, transcript string) (string, error) {
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	return f.out, nil
}

func bigClaudeBody(turns int) []byte {
	msgs := make([]map[string]any, 0, turns*2)
	filler := make([]byte, 20000)
	for i := range filler {
		filler[i] = 'x'
	}
	for i := 0; i < turns; i++ {
		msgs = append(msgs, map[string]any{"role": "user", "content": string(filler)})
		msgs = append(msgs, map[string]any{"role": "assistant", "content": string(filler)})
	}
	body, _ := json.Marshal(map[string]any{
		"model":    "claude-test",
		"system":   "you are helpful",
		"messages": msgs,
	})
	return body
}

func testCfg() config.PreCompactConfig {
	return config.PreCompactConfig{
		Enabled:          true,
		AuxModel:         "aux-model",
		Threshold:        0.85,
		KeepRecentTurns:  2,
		CacheTTL:         "2h",
		CacheMaxSessions: 100,
	}
}

func TestApplyNoopUnderBudget(t *testing.T) {
	sum := &fakeSummarizer{out: "summary"}
	it := New(testCfg(), registry.GetGlobalRegistry(), sum)
	body := bigClaudeBody(1)
	body, _ = json.Marshal(map[string]any{"model": "claude-test", "system": "s", "messages": []map[string]any{{"role": "user", "content": "hi"}}})
	out, res := it.Apply(context.Background(), testCfg(), cliproxyexecutor.RequestAfterAuthInterceptRequest{
		SourceFormat: sdktranslator.FormatClaude,
		Model:        "claude-test",
		Body:         body,
	})
	if out != nil {
		t.Fatalf("expected no replacement body under budget, got %d bytes", len(out))
	}
	if res.Compacted {
		t.Fatal("did not expect Compacted=true under budget")
	}
	if sum.calls != 0 {
		t.Fatal("summarizer must not be called when under budget")
	}
}

func TestApplyCompactsOverBudgetAndCachesSummary(t *testing.T) {
	sum := &fakeSummarizer{out: "a dense summary"}
	it := New(testCfg(), registry.GetGlobalRegistry(), sum)
	body := bigClaudeBody(60) // large enough to exceed a 200k*0.85 budget with 4000-byte fillers
	req := cliproxyexecutor.RequestAfterAuthInterceptRequest{
		SourceFormat: sdktranslator.FormatClaude,
		Model:        "claude-test",
		Headers:      map[string][]string{"X-Claude-Code-Session-Id": {"sess-abc"}},
		Body:         body,
	}
	out, res := it.Apply(context.Background(), testCfg(), req)
	if out == nil {
		t.Fatal("expected a compacted body over budget")
	}
	if !res.Compacted {
		t.Fatal("expected Compacted=true")
	}
	// The middle block (~300k tokens) exceeds the aux model's 140k budget, so
	// the summary is rolled over several chunks.
	if sum.calls < 1 || sum.calls != res.AuxCalls {
		t.Fatalf("expected aux calls to match AuxCalls=%d, got %d", res.AuxCalls, sum.calls)
	}
	if res.CacheHit {
		t.Fatal("first call must not be a cache hit")
	}

	// Second call with an identical body and the same session should reuse the cached summary
	// and not add new middle content, so the summarizer should not be invoked again.
	out2, res2 := it.Apply(context.Background(), testCfg(), req)
	if out2 == nil {
		t.Fatal("expected a compacted body on second call too")
	}
	if !res2.CacheHit {
		t.Fatal("expected cache hit on identical follow-up request")
	}
	if sum.calls != res.AuxCalls {
		t.Fatalf("expected summarizer not to be called again on cache hit, calls=%d first=%d", sum.calls, res.AuxCalls)
	}
}

func TestApplyForwardsOriginalOnSummarizeError(t *testing.T) {
	sum := &fakeSummarizer{err: errTest{}}
	it := New(testCfg(), registry.GetGlobalRegistry(), sum)
	body := bigClaudeBody(60)
	out, res := it.Apply(context.Background(), testCfg(), cliproxyexecutor.RequestAfterAuthInterceptRequest{
		SourceFormat: sdktranslator.FormatClaude,
		Model:        "claude-test",
		Body:         body,
	})
	if out != nil {
		t.Fatal("expected nil (forward original) when the aux call fails")
	}
	if res.Compacted {
		t.Fatal("must not report Compacted=true on failure")
	}
}

func TestApplySkipsInternalSourceRequests(t *testing.T) {
	sum := &fakeSummarizer{out: "summary"}
	it := New(testCfg(), registry.GetGlobalRegistry(), sum)
	body := bigClaudeBody(60)
	out, _ := it.Apply(context.Background(), testCfg(), cliproxyexecutor.RequestAfterAuthInterceptRequest{
		SourceFormat: sdktranslator.FormatClaude,
		Model:        "claude-test",
		Body:         body,
		Metadata:     map[string]any{"source": "plugin_host_model_callback"},
	})
	if out != nil {
		t.Fatal("aux/internal requests must never be compacted")
	}
	if sum.calls != 0 {
		t.Fatal("summarizer must not run for internal-source requests")
	}
}

func TestApplyDisabledIsNoop(t *testing.T) {
	sum := &fakeSummarizer{out: "summary"}
	it := New(testCfg(), registry.GetGlobalRegistry(), sum)
	cfg := testCfg()
	cfg.Enabled = false
	body := bigClaudeBody(60)
	out, _ := it.Apply(context.Background(), cfg, cliproxyexecutor.RequestAfterAuthInterceptRequest{
		SourceFormat: sdktranslator.FormatClaude,
		Model:        "claude-test",
		Body:         body,
	})
	if out != nil {
		t.Fatal("disabled config must be a no-op")
	}
	if sum.calls != 0 {
		t.Fatal("summarizer must not run when disabled")
	}
}

func TestApplyGeminiOutOfScope(t *testing.T) {
	sum := &fakeSummarizer{out: "summary"}
	it := New(testCfg(), registry.GetGlobalRegistry(), sum)
	body := bigClaudeBody(60)
	out, _ := it.Apply(context.Background(), testCfg(), cliproxyexecutor.RequestAfterAuthInterceptRequest{
		SourceFormat: sdktranslator.FormatGemini,
		Model:        "gemini-pro",
		Body:         body,
	})
	if out != nil {
		t.Fatal("gemini format is out of scope and must never be compacted")
	}
	if sum.calls != 0 {
		t.Fatal("summarizer must not run for gemini requests")
	}
}

type errTest struct{}

func (errTest) Error() string { return "aux call failed" }
