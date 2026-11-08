package helps

import (
	"context"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestIsMistralUpstreamURL(t *testing.T) {
	for _, tt := range []struct {
		url  string
		want bool
	}{
		{"https://api.mistral.ai/v1", true},
		{"https://API.MISTRAL.AI/v1/", true},
		{"https://mistral.ai/v1", true},
		{"https://api.mistral.ai./v1", true},
		{"https://eu.api.mistral.ai:443/v1", true},
		{"https://notmistral.ai/v1", false},
		{"https://mistral.ai.evil.com/v1", false},
		{"https://api.deepseek.com/v1", false},
		{"https://openrouter.ai/api/v1", false},
		{"", false},
		{"://bad", false},
	} {
		if got := isMistralUpstreamURL(tt.url); got != tt.want {
			t.Errorf("isMistralUpstreamURL(%q) = %v, want %v", tt.url, got, tt.want)
		}
	}
}

func TestStripAssistantReasoningContentDoc(t *testing.T) {
	for _, tt := range []struct {
		name        string
		in          string
		wantStrip   int
		wantDropped int
		check       func(t *testing.T, out []byte)
	}{
		{
			name:      "tool call message keeps tool_calls",
			in:        `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"","reasoning_content":"[reasoning unavailable]","tool_calls":[{"id":"call_1","type":"function","function":{"name":"Read","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"ok"}]}`,
			wantStrip: 1,
			check: func(t *testing.T, out []byte) {
				if n := len(ojson.GetBytes(out, "messages").Array()); n != 3 {
					t.Fatalf("messages len = %d, want 3; body=%s", n, out)
				}
				if ojson.GetBytes(out, "messages.1.reasoning_content").Exists() {
					t.Fatalf("reasoning_content not stripped: %s", out)
				}
				if ojson.GetBytes(out, "messages.1.tool_calls.0.id").String() != "call_1" {
					t.Fatalf("tool_calls lost: %s", out)
				}
			},
		},
		{
			name:      "text content message keeps content",
			in:        `{"messages":[{"role":"assistant","content":"answer","reasoning_content":"why"}]}`,
			wantStrip: 1,
			check: func(t *testing.T, out []byte) {
				if ojson.GetBytes(out, "messages.0.content").String() != "answer" {
					t.Fatalf("content lost: %s", out)
				}
				if ojson.GetBytes(out, "messages.0.reasoning_content").Exists() {
					t.Fatalf("reasoning_content not stripped: %s", out)
				}
			},
		},
		{
			name:      "array content with non-empty text or multimodal part is kept",
			in:        `{"messages":[{"role":"assistant","content":[{"type":"text","text":"hello"}],"reasoning_content":"r1"},{"role":"assistant","content":[{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}],"reasoning_content":"r2"}]}`,
			wantStrip: 2,
			check: func(t *testing.T, out []byte) {
				if n := len(ojson.GetBytes(out, "messages").Array()); n != 2 {
					t.Fatalf("messages len = %d, want 2; body=%s", n, out)
				}
				if ojson.GetBytes(out, "messages.0.reasoning_content").Exists() || ojson.GetBytes(out, "messages.1.reasoning_content").Exists() {
					t.Fatalf("reasoning_content not stripped: %s", out)
				}
			},
		},
		{
			name:        "reasoning-only messages including empty text array parts are dropped",
			in:          `{"messages":[{"role":"user","content":"a"},{"role":"assistant","content":"","reasoning_content":"r1"},{"role":"user","content":"b"},{"role":"assistant","content":null,"reasoning_content":"r2"},{"role":"assistant","content":[],"reasoning_content":"r3"},{"role":"assistant","content":[{"type":"text","text":"   "}],"reasoning_content":"r4"},{"role":"assistant","reasoning_content":"r5"},{"role":"user","content":"c"}]}`,
			wantStrip:   5,
			wantDropped: 5,
			check: func(t *testing.T, out []byte) {
				msgs := ojson.GetBytes(out, "messages").Array()
				if len(msgs) != 3 {
					t.Fatalf("messages len = %d, want 3; body=%s", len(msgs), out)
				}
				for i, want := range []string{"a", "b", "c"} {
					if msgs[i].Get("role").String() != "user" || msgs[i].Get("content").String() != want {
						t.Fatalf("messages[%d] = %s, want user %q; body=%s", i, msgs[i].Raw, want, out)
					}
				}
			},
		},
		{
			name:      "legacy function_call message is kept",
			in:        `{"messages":[{"role":"assistant","content":null,"reasoning_content":"r","function_call":{"name":"f","arguments":"{}"}}]}`,
			wantStrip: 1,
			check: func(t *testing.T, out []byte) {
				if !ojson.GetBytes(out, "messages.0.function_call").Exists() {
					t.Fatalf("function_call message dropped: %s", out)
				}
				if ojson.GetBytes(out, "messages.0.reasoning_content").Exists() {
					t.Fatalf("reasoning_content not stripped: %s", out)
				}
			},
		},
		{
			name: "non-assistant and clean messages untouched",
			in:   `{"messages":[{"role":"user","content":"hi","reasoning_content":"user-side"},{"role":"assistant","content":"ok"}]}`,
			check: func(t *testing.T, out []byte) {
				if ojson.GetBytes(out, "messages.0.reasoning_content").String() != "user-side" {
					t.Fatalf("non-assistant message modified: %s", out)
				}
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			doc := ojson.NewDoc([]byte(tt.in))
			stripped, dropped := stripAssistantReasoningContentDoc(doc)
			if stripped != tt.wantStrip || dropped != tt.wantDropped {
				t.Fatalf("stripped/dropped = %d/%d, want %d/%d", stripped, dropped, tt.wantStrip, tt.wantDropped)
			}
			tt.check(t, doc.Bytes())
		})
	}
}

func TestSanitizeAssistantReasoningContentForUpstreamDocGatesOnHost(t *testing.T) {
	const body = `{"model":"m","reasoning_effort":"max","messages":[{"role":"assistant","content":"","reasoning_content":"[reasoning unavailable]","tool_calls":[{"id":"c","type":"function","function":{"name":"f","arguments":"{}"}}]}]}`

	doc := ojson.NewDoc([]byte(body))
	SanitizeAssistantReasoningContentForUpstreamDoc(context.Background(), doc, "https://api.deepseek.com/v1")
	if !ojson.GetBytes(doc.Bytes(), "messages.0.reasoning_content").Exists() {
		t.Fatalf("non-Mistral upstream must keep reasoning_content: %s", doc.Bytes())
	}

	doc = ojson.NewDoc([]byte(body))
	SanitizeAssistantReasoningContentForUpstreamDoc(context.Background(), doc, "https://api.mistral.ai/v1")
	if ojson.GetBytes(doc.Bytes(), "messages.0.reasoning_content").Exists() {
		t.Fatalf("Mistral upstream must strip reasoning_content: %s", doc.Bytes())
	}
	if ojson.GetBytes(doc.Bytes(), "reasoning_effort").String() != "max" {
		t.Fatalf("top-level reasoning_effort must be preserved: %s", doc.Bytes())
	}
}
