package helps

import (
	"reflect"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestSanitizeMistralChatRequestDocUpstreamBoundary(t *testing.T) {
	const input = `{"reasoning_effort":"high","stream_options":{"include_usage":true},"messages":[{"role":"assistant","content":"answer","reasoning_content":"plan"}]}`
	for _, tc := range []struct {
		baseURL string
		mistral bool
	}{
		{"https://api.mistral.ai/v1", true},
		{"https://API.MISTRAL.AI/v1/", true},
		{"https://mistral.ai/v1", true},
		{"https://api.mistral.ai./v1", true},
		{"https://eu.api.mistral.ai:443/v1", true},
		{"https://api.deepseek.com/v1", false},
		{"https://api.moonshot.ai/v1", false},
		{"https://openrouter.ai/api/v1", false},
		{"https://mistral.internal/v1", false},
		{"https://notmistral.ai/v1", false},
		{"https://mistral.ai.example.com/v1", false},
		{"https://example.com/mistral.ai/v1", false},
		{"", false},
		{"://invalid", false},
	} {
		t.Run(tc.baseURL, func(t *testing.T) {
			doc := ojson.NewDocString(input)
			SanitizeMistralChatRequestDoc(doc, tc.baseURL)
			body := doc.Bytes()
			if !tc.mistral {
				if string(body) != input {
					t.Fatalf("unrelated upstream payload changed: %s", body)
				}
				return
			}
			want := ojson.Parse(`{"reasoning_effort":"high","messages":[{"role":"assistant","content":"answer"}]}`).Value()
			if !reflect.DeepEqual(ojson.ParseBytes(body).Value(), want) {
				t.Fatalf("unexpected Mistral payload: %s", body)
			}
		})
	}
}

func TestSanitizeMistralChatRequestDocPreservesConversation(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{
			name: "tool call and result stay paired",
			in:   `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":null,"reasoning_content":"[reasoning unavailable]","tool_calls":[{"id":"call00001","type":"function","function":{"name":"terminal","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call00001","content":"ok"}]}`,
			want: `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":null,"tool_calls":[{"id":"call00001","type":"function","function":{"name":"terminal","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call00001","content":"ok"}]}`,
		},
		{
			name: "real reasoning removed from ordinary assistant",
			in:   `{"messages":[{"role":"assistant","content":"answer","reasoning_content":"actual plan"}]}`,
			want: `{"messages":[{"role":"assistant","content":"answer"}]}`,
		},
		{
			name: "consecutive reasoning only messages removed without reordering",
			in:   `{"messages":[{"role":"user","content":"first"},{"role":"assistant","content":"","reasoning_content":"r1"},{"role":"assistant","content":null,"reasoning_content":"r2"},{"role":"assistant","content":[],"reasoning_content":"r3"},{"role":"assistant","reasoning_content":"r4"},{"role":"user","content":"second"}]}`,
			want: `{"messages":[{"role":"user","content":"first"},{"role":"user","content":"second"}]}`,
		},
		{
			name: "explicit empty prefix retained",
			in:   `{"messages":[{"role":"assistant","content":"","prefix":true,"reasoning_content":"plan"}]}`,
			want: `{"messages":[{"role":"assistant","content":"","prefix":true}]}`,
		},
		{
			name: "whitespace prefix retained",
			in:   `{"messages":[{"role":"assistant","content":"\n","prefix":true,"reasoning_content":"plan"}]}`,
			want: `{"messages":[{"role":"assistant","content":"\n","prefix":true}]}`,
		},
		{
			name: "intentional whitespace retained without prefix flag",
			in:   `{"messages":[{"role":"assistant","content":" \n","reasoning_content":"plan"}]}`,
			want: `{"messages":[{"role":"assistant","content":" \n"}]}`,
		},
		{
			name: "multipart content retained",
			in:   `{"messages":[{"role":"assistant","content":[{"type":"text","text":""},{"type":"text","text":"answer"}],"reasoning_content":"plan"}]}`,
			want: `{"messages":[{"role":"assistant","content":[{"type":"text","text":""},{"type":"text","text":"answer"}]}]}`,
		},
		{
			name: "empty tool calls do not identify a synthetic message",
			in:   `{"messages":[{"role":"assistant","content":null,"tool_calls":[],"reasoning_content":"plan"}]}`,
			want: `{"messages":[{"role":"assistant","content":null,"tool_calls":[]}]}`,
		},
		{
			name: "only assistant reasoning field is removed",
			in:   `{"messages":[{"role":"user","content":"hi","reasoning_content":"user supplied"},{"role":"assistant","content":"ok","reasoning_content":null}]}`,
			want: `{"messages":[{"role":"user","content":"hi","reasoning_content":"user supplied"},{"role":"assistant","content":"ok"}]}`,
		},
		{
			name: "clean empty assistant is not removed",
			in:   `{"messages":[{"role":"assistant","content":""}]}`,
			want: `{"messages":[{"role":"assistant","content":""}]}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := ojson.NewDocString(tc.in)
			SanitizeMistralChatRequestDoc(doc, "https://api.mistral.ai/v1")
			body := doc.Bytes()
			if !reflect.DeepEqual(ojson.ParseBytes(body).Value(), ojson.Parse(tc.want).Value()) {
				t.Fatalf("payload = %s, want %s", body, tc.want)
			}
		})
	}
}
