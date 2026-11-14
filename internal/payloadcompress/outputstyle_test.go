package payloadcompress

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func styleConfig(styles ...OutputStyle) Config {
	cfg := testConfig(EngineLite)
	cfg.OutputStyles = styles
	return cfg
}

func TestOutputStyleInjectsIntoChatSystem(t *testing.T) {
	cfg := styleConfig(OutputStyle{ID: "terse-prose", Level: "full"})
	body := []byte(`{"model":"m","messages":[{"role":"system","content":"be helpful"},{"role":"user","content":"hi"}]}`)

	out, stats := ApplyAll(cfg, "m", body)
	if stats.Style != "chat" {
		t.Fatalf("style target = %q", stats.Style)
	}
	system := gjson.GetBytes(out, "messages.0.content").String()
	if !strings.HasPrefix(system, "be helpful") {
		t.Fatalf("original system prompt lost:\n%s", system)
	}
	if !strings.Contains(system, outputStyleMarker) || !strings.Contains(system, "terse like smart caveman") {
		t.Fatalf("style missing:\n%s", system)
	}
	if !strings.Contains(system, sharedBoundaries) {
		t.Fatal("shared boundaries missing")
	}
	if !json.Valid(out) {
		t.Fatalf("invalid JSON: %s", out)
	}
}

func TestOutputStyleIdempotent(t *testing.T) {
	cfg := styleConfig(OutputStyle{ID: "ponytail", Level: "full"})
	body := []byte(`{"model":"m","messages":[{"role":"system","content":"sys"},{"role":"user","content":"hi"}]}`)

	once, _ := ApplyAll(cfg, "m", body)
	twice, stats := ApplyAll(cfg, "m", once)
	if string(once) != string(twice) {
		t.Fatalf("style injection is not idempotent:\n1: %s\n2: %s", once, twice)
	}
	if stats.Style != "already" {
		t.Fatalf("second pass reported %q, want already", stats.Style)
	}
	if got := strings.Count(string(once), outputStyleMarker); got != 1 {
		t.Fatalf("marker count = %d", got)
	}
}

func TestOutputStyleInjectsIntoChatSystemParts(t *testing.T) {
	cfg := styleConfig(OutputStyle{ID: "terse-prose", Level: "full"})
	body := []byte(`{"model":"m","messages":[` +
		`{"role":"system","content":[{"type":"text","text":"base"}]},` +
		`{"role":"user","content":"hi"}]}`)

	out, stats := ApplyAll(cfg, "m", body)
	if stats.Style != "chat" {
		t.Fatalf("style target = %q", stats.Style)
	}
	parts := gjson.GetBytes(out, "messages.0.content")
	if parts.Get("#").Int() != 2 {
		t.Fatalf("expected two system parts, got %s", parts.Raw)
	}
	if parts.Get("0.text").String() != "base" {
		t.Fatalf("cached part was rewritten: %s", parts.Raw)
	}
	if !strings.Contains(parts.Get("1.text").String(), outputStyleMarker) {
		t.Fatalf("style part missing: %s", parts.Raw)
	}

	_, again := ApplyAll(cfg, "m", out)
	if again.Style != "already" {
		t.Fatalf("second pass reported %q, want already", again.Style)
	}
}

func TestOutputStylePrefersTopLevelSystemOverMidSystemTurn(t *testing.T) {
	cfg := styleConfig(OutputStyle{ID: "terse-prose", Level: "full"})
	body := []byte(`{"model":"m","system":[{"type":"text","text":"base"}],"messages":[` +
		`{"role":"system","content":[{"type":"text","text":"mid"}]},` +
		`{"role":"user","content":"hi"}]}`)

	out, stats := ApplyAll(cfg, "m", body)
	if stats.Style != "claude" {
		t.Fatalf("style target = %q", stats.Style)
	}
	if !strings.Contains(gjson.GetBytes(out, "system.1.text").String(), outputStyleMarker) {
		t.Fatalf("top-level system not styled: %s", gjson.GetBytes(out, "system").Raw)
	}
	if strings.Contains(gjson.GetBytes(out, "messages.0.content").Raw, outputStyleMarker) {
		t.Fatal("style leaked into the mid-system turn")
	}
}

func TestOutputStyleInjectsIntoGeminiSystemInstruction(t *testing.T) {
	cfg := styleConfig(OutputStyle{ID: "terse-prose", Level: "full"})
	body := []byte(`{"model":"m","systemInstruction":{"parts":[{"text":"base"}]},` +
		`"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)

	out, stats := ApplyAll(cfg, "m", body)
	if stats.Style != "gemini" {
		t.Fatalf("style target = %q", stats.Style)
	}
	parts := gjson.GetBytes(out, "systemInstruction.parts")
	if parts.Get("#").Int() != 2 {
		t.Fatalf("expected two system parts, got %s", parts.Raw)
	}
	if parts.Get("0.text").String() != "base" {
		t.Fatalf("cached part was rewritten: %s", parts.Raw)
	}
	if !strings.Contains(parts.Get("1.text").String(), outputStyleMarker) {
		t.Fatalf("style part missing: %s", parts.Raw)
	}

	_, again := ApplyAll(cfg, "m", out)
	if again.Style != "already" {
		t.Fatalf("second pass reported %q, want already", again.Style)
	}
}

func TestOutputStyleInjectsIntoLegacyGeminiSystemInstruction(t *testing.T) {
	cfg := styleConfig(OutputStyle{ID: "terse-prose", Level: "full"})
	body := []byte(`{"model":"m","system_instruction":"base",` +
		`"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)

	out, stats := ApplyAll(cfg, "m", body)
	if stats.Style != "gemini" {
		t.Fatalf("style target = %q", stats.Style)
	}
	system := gjson.GetBytes(out, "system_instruction").String()
	if !strings.HasPrefix(system, "base") || !strings.Contains(system, outputStyleMarker) {
		t.Fatalf("legacy system instruction not extended:\n%s", system)
	}
}

func TestOutputStyleMarkerEchoedBackDoesNotDisableStyling(t *testing.T) {
	cfg := styleConfig(OutputStyle{ID: "terse-prose", Level: "full"})
	body := []byte(`{"model":"m","messages":[` +
		`{"role":"system","content":"be helpful"},` +
		`{"role":"assistant","content":"quoted the marker: [CLIProxyAPI Output Styles]"},` +
		`{"role":"user","content":"hi"}]}`)

	out, stats := ApplyAll(cfg, "m", body)
	if stats.Style != "chat" {
		t.Fatalf("style = %q, want chat: echoed marker must not disable styling", stats.Style)
	}
	system := gjson.GetBytes(out, "messages.0.content").String()
	if !strings.Contains(system, outputStyleMarker) {
		t.Fatalf("style missing after echoed marker:\n%s", system)
	}
}

func TestOutputStyleInjectsIntoClaudeSystem(t *testing.T) {
	cfg := styleConfig(OutputStyle{ID: "i-have-adhd", Level: "lite"})
	body := []byte(`{"model":"m","system":"base prompt","messages":[{"role":"user","content":"hi"}]}`)

	out, stats := ApplyAll(cfg, "m", body)
	if stats.Style != "claude" {
		t.Fatalf("style target = %q", stats.Style)
	}
	system := gjson.GetBytes(out, "system").String()
	if !strings.HasPrefix(system, "base prompt") || !strings.Contains(system, "I have ADHD") {
		t.Fatalf("claude system not extended:\n%s", system)
	}
}

func TestOutputStyleInjectsIntoClaudeSystemBlocks(t *testing.T) {
	cfg := styleConfig(OutputStyle{ID: "terse-prose", Level: "ultra"})
	body := []byte(`{"model":"m","system":[{"type":"text","text":"base","cache_control":{"type":"ephemeral"}}],` +
		`"messages":[{"role":"user","content":"hi"}]}`)

	out, stats := ApplyAll(cfg, "m", body)
	if stats.Style != "claude" {
		t.Fatalf("style target = %q", stats.Style)
	}
	blocks := gjson.GetBytes(out, "system")
	if blocks.Get("#").Int() != 2 {
		t.Fatalf("expected two system blocks, got %s", blocks.Raw)
	}
	if !strings.Contains(blocks.Get("1.text").String(), outputStyleMarker) {
		t.Fatalf("style block missing: %s", blocks.Raw)
	}
	if blocks.Get("0.text").String() != "base" {
		t.Fatalf("cached block was rewritten: %s", blocks.Raw)
	}
}

func TestOutputStyleInjectsIntoResponsesInstructions(t *testing.T) {
	cfg := styleConfig(OutputStyle{ID: "less-code", Level: "full"})
	body := []byte(`{"model":"m","instructions":"base","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)

	out, stats := ApplyAll(cfg, "m", body)
	if stats.Style != "responses" {
		t.Fatalf("style target = %q", stats.Style)
	}
	instructions := gjson.GetBytes(out, "instructions").String()
	if !strings.HasPrefix(instructions, "base") || !strings.Contains(instructions, safetyBoundaries) {
		t.Fatalf("instructions not extended:\n%s", instructions)
	}
}

func TestOutputStyleSkipsWhenNoSystemSlot(t *testing.T) {
	cfg := styleConfig(OutputStyle{ID: "terse-prose", Level: "full"})
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)

	out, stats := ApplyAll(cfg, "m", body)
	if stats.Style != "skipped:no-system-slot" {
		t.Fatalf("style = %q", stats.Style)
	}
	if string(out) != string(body) {
		t.Fatal("payload must be untouched without a system slot")
	}
}

func TestOutputStyleBypassesTerseSensitiveRequests(t *testing.T) {
	cfg := styleConfig(OutputStyle{ID: "terse-prose", Level: "ultra"})

	for name, prompt := range map[string]string{
		"security":     "review this for security issues",
		"irreversible": "delete the production database now",
		"detail":       "explain in detail how the cache works",
	} {
		body := []byte(`{"model":"m","messages":[{"role":"system","content":"sys"},{"role":"user","content":` + quote(prompt) + `}]}`)
		out, stats := ApplyAll(cfg, "m", body)
		if !strings.HasPrefix(stats.Style, "skipped:keyword") {
			t.Fatalf("%s: style = %q", name, stats.Style)
		}
		if string(out) != string(body) {
			t.Fatalf("%s: payload changed", name)
		}
	}
}

// Tool output mentioning "delete" must not flip styling. The injected text lives
// in the system prompt, which is the first bytes the provider caches, so a
// decision that flips mid-conversation costs the whole prefix.
func TestOutputStyleIgnoresBypassKeywordsInToolOutput(t *testing.T) {
	cfg := styleConfig(OutputStyle{ID: "terse-prose", Level: "full"})
	body := []byte(`{"model":"m","messages":[{"role":"system","content":"sys"},` +
		`{"role":"user","content":"ship it"},` +
		`{"role":"tool","content":"deleted 3 files, security scan clean"}]}`)
	_, stats := ApplyAll(cfg, "m", body)
	if stats.Style != "chat" {
		t.Fatalf("style = %q, want chat: tool output must not trigger bypass", stats.Style)
	}
}

func TestOutputStyleBypassesMediaRequests(t *testing.T) {
	cfg := styleConfig(OutputStyle{ID: "terse-prose", Level: "full"})
	body := []byte(`{"model":"m","messages":[{"role":"system","content":"sys"},{"role":"user","content":[` +
		`{"type":"text","text":"what is this"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA"}}]}]}`)

	_, stats := ApplyAll(cfg, "m", body)
	if stats.Style != "skipped:media" {
		t.Fatalf("style = %q", stats.Style)
	}
}

func TestOutputStyleIgnoresImageFieldsInToolOutput(t *testing.T) {
	cfg := styleConfig(OutputStyle{ID: "terse-prose", Level: "full"})
	body := []byte(`{"model":"m","messages":[{"role":"system","content":"sys"},{"role":"user","content":"deploy it"},` +
		`{"role":"tool","content":"{\"image\": \"nginx:latest\", \"document\": \"runbook.md\"}"}]}`)
	_, stats := ApplyAll(cfg, "m", body)
	if stats.Style != "chat" {
		t.Fatalf("tool output mentioning image/document skipped styling: %q", stats.Style)
	}
}

func TestOutputStyleUnknownIDIsDropped(t *testing.T) {
	if got := styleInstruction([]OutputStyle{{ID: "nope", Level: "full"}}); got != "" {
		t.Fatalf("unknown style produced %q", got)
	}
	cfg := Config{OutputStyles: []OutputStyle{{ID: "nope"}, {ID: "TERSE-PROSE", Level: "ULTRA"}, {ID: "terse-prose"}}}
	cfg.Sanitize()
	if len(cfg.OutputStyles) != 1 {
		t.Fatalf("styles = %+v", cfg.OutputStyles)
	}
	if cfg.OutputStyles[0].ID != "terse-prose" || cfg.OutputStyles[0].Level != "ultra" {
		t.Fatalf("style not normalized: %+v", cfg.OutputStyles[0])
	}
}

func TestOutputStyleWorksWithoutCompression(t *testing.T) {
	cfg := styleConfig(OutputStyle{ID: "terse-prose", Level: "full"})
	cfg.Enabled = false
	body := []byte(`{"model":"m","messages":[{"role":"system","content":"sys"},{"role":"user","content":"hi"}]}`)

	out, stats := ApplyAll(cfg, "m", body)
	if stats.Combo != "" || stats.Saved != nil {
		t.Fatalf("compression must stay off: %+v", stats)
	}
	if stats.Style != "chat" {
		t.Fatalf("style = %q", stats.Style)
	}
	if !strings.Contains(string(out), outputStyleMarker) {
		t.Fatal("style was not injected")
	}
}

func TestOutputStyleShadowDoesNotInject(t *testing.T) {
	cfg := styleConfig(OutputStyle{ID: "terse-prose", Level: "full"})
	cfg.Shadow = true
	body := []byte(`{"model":"m","messages":[{"role":"system","content":"sys"},{"role":"user","content":"hi"}]}`)

	out, stats := ApplyAll(cfg, "m", body)
	if string(out) != string(body) {
		t.Fatal("shadow mode must not inject")
	}
	if stats.Style != "chat" || stats.StyleBytes == 0 {
		t.Fatalf("shadow mode must still report: %+v", stats)
	}
}

func TestOutputStyleComboOverridesGlobal(t *testing.T) {
	cfg := styleConfig(OutputStyle{ID: "terse-prose", Level: "full"})
	cfg.Combos = []Combo{
		{Name: "code", Models: []string{"coder-*"}, Pipeline: []Step{{Engine: EngineLite}}, OutputStyles: []OutputStyle{{ID: "less-code", Level: "full"}}},
		{Name: "standard", Models: []string{"*"}, Pipeline: []Step{{Engine: EngineLite}}},
	}
	cfg.DefaultCombo = "standard"
	cfg.Sanitize()
	body := []byte(`{"model":"m","messages":[{"role":"system","content":"be helpful"},{"role":"user","content":"hi"}]}`)

	out, _ := ApplyAll(cfg, "coder-1", body)
	system := gjson.GetBytes(out, "messages.0.content").String()
	if strings.Contains(system, "terse like smart caveman") || !strings.Contains(system, outputStyleMarker) {
		t.Fatalf("combo style should replace global style:\n%s", system)
	}
	out, _ = ApplyAll(cfg, "other", body)
	if system = gjson.GetBytes(out, "messages.0.content").String(); !strings.Contains(system, "terse like smart caveman") {
		t.Fatalf("global style missing for unmatched combo:\n%s", system)
	}
}

func TestSanitizeKeepsStyleOnlyCombo(t *testing.T) {
	cfg := Config{Combos: []Combo{{Name: "s", Models: []string{"*"}, OutputStyles: []OutputStyle{{ID: "ponytail"}, {ID: "bogus"}}}}}
	cfg.Sanitize()
	if len(cfg.Combos) != 1 || len(cfg.Combos[0].OutputStyles) != 1 {
		t.Fatalf("style-only combo not kept: %+v", cfg.Combos)
	}
}
