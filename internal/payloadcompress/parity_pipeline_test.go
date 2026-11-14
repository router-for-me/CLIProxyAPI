package payloadcompress

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestRegistryCompilesNewEngines checks the two parity engines resolve to real
// pipeline steps instead of being dropped by the registry.
func TestRegistryCompilesNewEngines(t *testing.T) {
	steps, _ := buildEngines([]Step{
		{Engine: EngineMCPAccessibility},
		{Engine: EngineRelevance},
	})
	if len(steps) != 2 {
		t.Fatalf("got %d steps, want 2", len(steps))
	}
	if got := steps[0].eng.name(); got != EngineMCPAccessibility {
		t.Fatalf("step 0 = %q, want %q", got, EngineMCPAccessibility)
	}
	if got := steps[1].eng.name(); got != EngineRelevance {
		t.Fatalf("step 1 = %q, want %q", got, EngineRelevance)
	}
}

// TestPipelineDefaultsAndSanitize pins the off-by-default switches and the
// clamping rules for the new knobs.
func TestPipelineDefaultsAndSanitize(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.QuantumLock {
		t.Fatal("quantum-lock must default to false")
	}
	if knownEngine(EngineMCPAccessibility) == false || knownEngine(EngineRelevance) == false {
		t.Fatal("new engines must be known")
	}

	opts := sanitizeStepOptions(StepOptions{
		McpMinLength:              -1,
		McpMaxChars:               -1,
		McpCollapseThreshold:      -1,
		McpCollapseKeepHead:       -1,
		McpCollapseKeepTail:       -1,
		RelevanceBudgetPercent:    -0.5,
		RelevanceOverlapThreshold: -0.5,
	})
	if opts.McpMinLength != 0 || opts.McpMaxChars != 0 || opts.McpCollapseThreshold != 0 ||
		opts.McpCollapseKeepHead != 0 || opts.McpCollapseKeepTail != 0 {
		t.Fatalf("negative MCP values must clamp to 0: %+v", opts)
	}
	if opts.RelevanceBudgetPercent != 0 || opts.RelevanceOverlapThreshold != 0 {
		t.Fatalf("negative relevance values must keep the default sentinel: %+v", opts)
	}

	opts = sanitizeStepOptions(StepOptions{RelevanceBudgetPercent: 0.01, RelevanceOverlapThreshold: 3})
	if opts.RelevanceBudgetPercent != 0.1 {
		t.Fatalf("budget = %v, want 0.1", opts.RelevanceBudgetPercent)
	}
	if opts.RelevanceOverlapThreshold != 1 {
		t.Fatalf("overlap = %v, want 1", opts.RelevanceOverlapThreshold)
	}
}

// TestLiteBodyRunsOnlyWithLite checks the lite-only body passes fire for a lite
// combo and leave the payload alone otherwise.
func TestLiteBodyRunsOnlyWithLite(t *testing.T) {
	body := []byte(`{"messages":[` +
		`{"role":"system","content":"same"},` +
		`{"role":"system","content":"same"},` +
		`{"role":"user","content":"hi"}]}`)

	lite := testConfig(EngineLite)
	lite.PreserveSystemPrompt = PreserveSystemPromptNever
	out, _ := Apply(lite, "gpt-test", body)
	if n := strings.Count(string(out), `"same"`); n != 1 {
		t.Fatalf("lite combo kept %d duplicate system messages, want 1: %s", n, out)
	}

	plain := testConfig(EngineRTK)
	plain.PreserveSystemPrompt = PreserveSystemPromptNever
	out, _ = Apply(plain, "gpt-test", body)
	if n := strings.Count(string(out), `"same"`); n != 2 {
		t.Fatalf("non-lite combo changed the body: %s", out)
	}
}

// TestQuantumLockThroughApply checks a locked payload round-trips through the
// whole pipeline and stays valid JSON.
func TestQuantumLockThroughApply(t *testing.T) {
	body := []byte(`{"messages":[` +
		`{"role":"system","content":"same"},` +
		`{"role":"system","content":"same"},` +
		`{"role":"user","content":"see https://example.com/a/b?x=1 and run ` +
		"```go\\nfmt.Println(1)\\n```" + `"}]}`)

	cfg := testConfig(EngineLite)
	cfg.PreserveSystemPrompt = PreserveSystemPromptNever
	cfg.QuantumLock = true
	out, stats := Apply(cfg, "gpt-test", body)
	if !json.Valid(out) {
		t.Fatalf("invalid JSON after quantum-locked apply: %s", out)
	}
	if !strings.Contains(string(out), "https://example.com/a/b?x=1") {
		t.Fatalf("quantum lock lost a protected URL: %s", out)
	}
	if !strings.Contains(string(out), "fmt.Println(1)") {
		t.Fatalf("quantum lock lost a code fence: %s", out)
	}
	if stats.Applied != true {
		t.Fatal("lite dedup should have applied")
	}
}

// TestEngineTelemetry counts decoded bytes for every step that ran.
func TestEngineTelemetry(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hello world"}]}`)
	cfg := testConfig(EngineLite, EngineRTK)
	cfg.PreserveSystemPrompt = PreserveSystemPromptNever

	_, stats := Apply(cfg, "gpt-test", body)
	for _, name := range []string{EngineLite, EngineRTK} {
		in, ok := stats.EngineBytesIn[name]
		if !ok || in <= 0 {
			t.Fatalf("%s missing input telemetry: %+v", name, stats.EngineBytesIn)
		}
		out, ok := stats.EngineBytesOut[name]
		if !ok {
			t.Fatalf("%s missing output telemetry: %+v", name, stats.EngineBytesOut)
		}
		if got, want := stats.EngineSaved(name), in-out; got != want {
			t.Fatalf("%s saved = %d, want %d", name, got, want)
		}
	}
}
