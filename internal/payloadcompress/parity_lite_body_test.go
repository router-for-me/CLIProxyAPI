package payloadcompress

import (
	"encoding/json"
	"strings"
	"testing"
)

func liteTestConfig() Config {
	return Config{PreserveSystemPrompt: PreserveSystemPromptNever}
}

// liteTestOpts opts into the lossy lite passes exercised by these tests.
func liteTestOpts() StepOptions {
	on := true
	return StepOptions{LiteRemoveRedundant: &on, LiteReplaceImages: &on}
}

func liteDecode(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("invalid JSON %s: %v", body, err)
	}
	return out
}

func liteMessages(t *testing.T, body []byte, path string) []any {
	t.Helper()
	root := liteDecode(t, body)
	msgs, ok := root[path].([]any)
	if !ok {
		t.Fatalf("missing %q array in %s", path, body)
	}
	return msgs
}

func TestLiteBodyBeforeDropsDuplicateSystem(t *testing.T) {
	body := []byte(`{"messages":[` +
		`{"role":"system","content":"same"},` +
		`{"role":"system","content":"same"},` +
		`{"role":"user","content":"hi"}]}`)
	out := applyLiteBodyBefore(liteTestConfig(), "gpt-4o", "openai", body)
	msgs := liteMessages(t, out, "messages")
	if len(msgs) != 2 {
		t.Fatalf("got %d messages: %s", len(msgs), out)
	}
}

func TestLiteBodyBeforeKeepsSystemWhenPreserved(t *testing.T) {
	body := []byte(`{"messages":[` +
		`{"role":"system","content":"same"},` +
		`{"role":"system","content":"same"}]}`)
	out := applyLiteBodyBefore(Config{}, "gpt-4o", "openai", body)
	if string(out) != string(body) {
		t.Fatalf("expected untouched body, got %s", out)
	}
}

func TestLiteBodyAfterDropsAdjacentDuplicateSameRole(t *testing.T) {
	body := []byte(`{"messages":[` +
		`{"role":"user","content":"same"},` +
		`{"role":"user","content":"same"},` +
		`{"role":"assistant","content":"ok"}]}`)
	out := applyLiteBodyAfter(liteTestConfig(), liteTestOpts(), "gpt-4o", "openai", body)
	msgs := liteMessages(t, out, "messages")
	if len(msgs) != 2 {
		t.Fatalf("got %d messages: %s", len(msgs), out)
	}
}

func TestLiteBodyAfterKeepsDifferentRoles(t *testing.T) {
	body := []byte(`{"messages":[` +
		`{"role":"user","content":"same"},` +
		`{"role":"assistant","content":"same"}]}`)
	out := applyLiteBodyAfter(liteTestConfig(), liteTestOpts(), "gpt-4o", "openai", body)
	if msgs := liteMessages(t, out, "messages"); len(msgs) != 2 {
		t.Fatalf("got %d messages: %s", len(msgs), out)
	}
}

func TestLiteBodyAfterKeepsToolResults(t *testing.T) {
	body := []byte(`{"messages":[` +
		`{"role":"tool","content":"same"},` +
		`{"role":"tool","content":"same"}]}`)
	out := applyLiteBodyAfter(liteTestConfig(), liteTestOpts(), "gpt-4o", "openai", body)
	if msgs := liteMessages(t, out, "messages"); len(msgs) != 2 {
		t.Fatalf("tool results dropped: %s", out)
	}

	responses := []byte(`{"input":[` +
		`{"type":"function_call_output","output":"same"},` +
		`{"type":"function_call_output","output":"same"}]}`)
	out = applyLiteBodyAfter(liteTestConfig(), liteTestOpts(), "gpt-4o", "openai", responses)
	if items := liteMessages(t, out, "input"); len(items) != 2 {
		t.Fatalf("responses tool outputs dropped: %s", out)
	}
}

func TestLiteBodyAfterReplacesImagesForNonVision(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[` +
		`{"type":"text","text":"look"},` +
		`{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`)
	out := applyLiteBodyAfter(liteTestConfig(), liteTestOpts(), "text-only-model", "openai", body)
	if strings.Contains(string(out), "image_url") || strings.Contains(string(out), "base64") {
		t.Fatalf("image not replaced: %s", out)
	}
	if !strings.Contains(string(out), "[image: png]") {
		t.Fatalf("missing placeholder: %s", out)
	}
	// Remote URLs must stay untouched.
	remote := []byte(`{"messages":[{"role":"user","content":[` +
		`{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]}]}`)
	if got := applyLiteBodyAfter(liteTestConfig(), liteTestOpts(), "text-only-model", "openai", remote); string(got) != string(remote) {
		t.Fatalf("remote image rewritten: %s", got)
	}
}

func TestLiteBodyAfterKeepsImagesForVision(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[` +
		`{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`)
	for _, model := range []string{"gpt-4o", "claude-3-5-sonnet", "claude-sonnet-4", "glm-4.5v", "qwen2.5-vl-7b"} {
		if got := applyLiteBodyAfter(liteTestConfig(), liteTestOpts(), model, "openai", body); string(got) != string(body) {
			t.Fatalf("%s: vision image rewritten: %s", model, got)
		}
	}
}

func TestLiteBodyAfterReplacesResponsesAndGeminiImages(t *testing.T) {
	responses := []byte(`{"input":[{"role":"user","content":[` +
		`{"type":"input_image","image_url":"data:image/jpeg;base64,AAAA"}]}]}`)
	out := applyLiteBodyAfter(liteTestConfig(), liteTestOpts(), "text-only-model", "openai", responses)
	if !strings.Contains(string(out), "[image: jpeg]") {
		t.Fatalf("responses image not replaced: %s", out)
	}

	gemini := []byte(`{"contents":[{"role":"user","parts":[` +
		`{"inlineData":{"mimeType":"image/webp","data":"AAAA"}},` +
		`{"text":"look"}]}]}`)
	out = applyLiteBodyAfter(liteTestConfig(), liteTestOpts(), "text-only-model", "gemini", gemini)
	if !strings.Contains(string(out), "[image: webp]") {
		t.Fatalf("gemini image not replaced: %s", out)
	}
}

func TestLiteBodyAfterUnchangedBodyIsByteIdentical(t *testing.T) {
	body := []byte(`{ "messages" : [ {"role":"user","content":"hello"} ] }`)
	if got := applyLiteBodyAfter(liteTestConfig(), liteTestOpts(), "gpt-4o", "openai", body); string(got) != string(body) {
		t.Fatalf("unchanged body rewritten: %s", got)
	}
	if got := applyLiteBodyBefore(liteTestConfig(), "gpt-4o", "openai", body); string(got) != string(body) {
		t.Fatalf("before rewrote unchanged body: %s", got)
	}
}

func TestLiteBodyUnsupportedShapeFailsOpen(t *testing.T) {
	body := []byte(`{"prompt":"hello"}`)
	if got := applyLiteBodyAfter(liteTestConfig(), liteTestOpts(), "text-only-model", "openai", body); string(got) != string(body) {
		t.Fatalf("unsupported shape rewritten: %s", got)
	}
	if got := applyLiteBodyBefore(liteTestConfig(), "gpt-4o", "openai", body); string(got) != string(body) {
		t.Fatalf("unsupported shape rewritten: %s", got)
	}
}
