package handlers

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/tidwall/gjson"
)

func TestRequestContainsImage(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "openai image_url",
			body: `{"messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAA"}}]}]}`,
			want: true,
		},
		{
			name: "claude base64 image",
			body: `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAA"}}]}]}`,
			want: true,
		},
		{
			name: "gemini inline_data",
			body: `{"contents":[{"role":"user","parts":[{"inline_data":{"mime_type":"image/png","data":"AAA"}}]}]}`,
			want: true,
		},
		{
			name: "input_image responses",
			body: `{"input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AAA"}]}]}`,
			want: true,
		},
		{
			name: "no image",
			body: `{"messages":[{"role":"user","content":"hello"}]}`,
			want: false,
		},
		{
			name: "empty",
			body: ``,
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := requestContainsImage([]byte(c.body)); got != c.want {
				t.Fatalf("requestContainsImage = %v, want %v", got, c.want)
			}
		})
	}
}

func TestReplaceImagesWithVisionText(t *testing.T) {
	body := `{"messages":[{"role":"user","content":[{"type":"text","text":"whats this"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAA"}}]}]}`
	out := replaceImagesWithVisionText([]byte(body), "", "a red circle")
	if !strings.Contains(string(out), "a red circle") {
		t.Fatalf("analysis text not injected: %s", out)
	}
	if strings.Contains(string(out), "image_url") {
		t.Fatalf("image_url not removed: %s", out)
	}
	if !strings.Contains(string(out), `"type":"text"`) {
		t.Fatalf("image block not converted to text: %s", out)
	}
}

func TestReplaceImagesGemini(t *testing.T) {
	body := `{"contents":[{"role":"user","parts":[{"text":"hi"},{"inline_data":{"mime_type":"image/png","data":"AAA"}}]}]}`
	out := replaceImagesWithVisionText([]byte(body), "", "a blue square")
	if !strings.Contains(string(out), "a blue square") {
		t.Fatalf("gemini analysis not injected: %s", out)
	}
	if strings.Contains(string(out), "inline_data") {
		t.Fatalf("gemini inline_data not removed: %s", out)
	}
}

func TestCollectImageDataURLs(t *testing.T) {
	body := `{"messages":[{"role":"user","content":[
		{"type":"image_url","image_url":{"url":"https://example.com/a.png"}},
		{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"QUJD"}}
	]}]}`
	urls := collectImageDataURLs([]byte(body))
	if len(urls) != 2 {
		t.Fatalf("expected 2 urls, got %d: %v", len(urls), urls)
	}
	foundData := false
	for _, u := range urls {
		if strings.HasPrefix(u, "data:image/jpeg;base64,QUJD") {
			foundData = true
		}
	}
	if !foundData {
		t.Fatalf("base64 data url not extracted: %v", urls)
	}
}

func TestExtractOpenAICompletionText(t *testing.T) {
	body := `{"choices":[{"message":{"content":"first"}},{"message":{"content":"second"}}]}`
	if got := extractOpenAICompletionText([]byte(body)); got != "first\nsecond" {
		t.Fatalf("extract = %q, want first\\nsecond", got)
	}
	if got := extractOpenAICompletionText([]byte(`{}`)); got != "" {
		t.Fatalf("extract empty = %q, want \"\"", got)
	}
}

func TestApplyVisionBridgeSkipped(t *testing.T) {
	// Resolved with no bridge model => body returned unchanged.
	res := autoRouterResolved{targetModel: "gpt-4o", matched: true}
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AAA"}}]}]}`)
	out := (&BaseAPIHandler{}).applyVisionBridgeIfNeeded(nil, res, body)
	if string(out) != string(body) {
		t.Fatalf("expected body unchanged when no bridge model, got %s", out)
	}
}

func TestInflateThinkingModelMaxTokens(t *testing.T) {
	// claude-sonnet-4-6 is a registered thinking model with MaxCompletionTokens.
	const target = "claude-sonnet-4-6"

	// Small client cap => inflated to the model cap.
	in := []byte(`{"model":"x","messages":[{"role":"user","content":"design a system"}],"max_tokens":16000}`)
	out := inflateThinkingModelMaxTokens(in, target)
	got := gjson.GetBytes(out, "max_tokens").Int()
	if got != 64000 {
		t.Fatalf("expected max_tokens 64000, got %d", got)
	}

	// Already at/over the cap => unchanged.
	in2 := []byte(`{"messages":[{"role":"user","content":"hi"}],"max_tokens":64000}`)
	out2 := inflateThinkingModelMaxTokens(in2, target)
	if string(out2) != string(in2) {
		t.Fatalf("expected unchanged when max_tokens already == cap, got %s", out2)
	}

	// Unknown (non-registered) model => unchanged.
	in3 := []byte(`{"messages":[{"role":"user","content":"hi"}],"max_tokens":1000}`)
	out3 := inflateThinkingModelMaxTokens(in3, "not-a-real-model-xyz")
	if string(out3) != string(in3) {
		t.Fatalf("expected unchanged for unknown model, got %s", out3)
	}
}

func TestInflateThinkingModelMaxTokensSuffix(t *testing.T) {
	// A thinking-suffix target resolves to the base model and still inflates.
	in := []byte(`{"messages":[{"role":"user","content":"hi"}],"max_completion_tokens":2000}`)
	out := inflateThinkingModelMaxTokens(in, "claude-sonnet-4-6(high)")
	got := gjson.GetBytes(out, "max_completion_tokens").Int()
	if got != 64000 {
		t.Fatalf("expected max_completion_tokens 64000, got %d", got)
	}
}

func TestVisionBridgeTimeoutConfig(t *testing.T) {
	// Unset/zero -> default 30s.
	if got := VisionBridgeTimeout(nil); got != 30*time.Second {
		t.Fatalf("VisionBridgeTimeout(nil) = %v, want 30s", got)
	}
	if got := VisionBridgeTimeout(&config.SDKConfig{}); got != 30*time.Second {
		t.Fatalf("VisionBridgeTimeout(empty) = %v, want 30s", got)
	}
	// Explicit value wins.
	cfg := &config.SDKConfig{VisionBridgeTimeoutSeconds: 5}
	if got := VisionBridgeTimeout(cfg); got != 5*time.Second {
		t.Fatalf("VisionBridgeTimeout(5) = %v, want 5s", got)
	}
	// Negative disables the bridge (zero duration sentinel).
	if got := VisionBridgeTimeout(&config.SDKConfig{VisionBridgeTimeoutSeconds: -1}); got != 0 {
		t.Fatalf("VisionBridgeTimeout(-1) = %v, want 0 (disabled)", got)
	}
}

func TestApplyVisionBridgeDisabledByNegativeConfig(t *testing.T) {
	// A negative timeout must skip the bridge call entirely (body unchanged)
	// even when a bridge model is configured and images are present.
	res := autoRouterResolved{targetModel: "gpt-4o", visionBridgeModel: "bridge-model", matched: true}
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AAA"}}]}]}`)
	h := &BaseAPIHandler{Cfg: &config.SDKConfig{VisionBridgeTimeoutSeconds: -1}}
	out := h.applyVisionBridgeIfNeeded(context.Background(), res, body)
	if string(out) != string(body) {
		t.Fatalf("bridge must be skipped when disabled; body changed: %s", out)
	}
}
