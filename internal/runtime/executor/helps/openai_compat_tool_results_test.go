package helps

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/tidwall/gjson"
)

func TestNormalizeOpenAIToolResultsTextOnly(t *testing.T) {
	input := []byte(`{"messages":[
        {"role":"assistant","content":[{"type":"text","text":"before"}]},
        {"role":"tool","tool_call_id":"call_1","content":[
            {"type":"text","text":"image inspected"},
            {"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}
        ]},
        {"role":"tool","tool_call_id":"call_2","content":"already text"},
        {"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/user.png"}}]}
    ]}`)

	got := NormalizeOpenAIToolResultsTextOnly(input)

	toolContent := gjson.GetBytes(got, "messages.1.content")
	if toolContent.Type != gjson.String {
		t.Fatalf("tool content type = %s, want string", toolContent.Type)
	}
	if toolContent.String() != "image inspected\n\n"+openAIToolResultImageOmittedText {
		t.Fatalf("tool content = %q", toolContent.String())
	}
	if gotContent := gjson.GetBytes(got, "messages.2.content"); gotContent.String() != "already text" {
		t.Fatalf("existing string tool content = %q", gotContent.String())
	}
	if !gjson.GetBytes(got, "messages.0.content").IsArray() {
		t.Fatal("assistant content array was unexpectedly changed")
	}
	if !gjson.GetBytes(got, "messages.3.content").IsArray() {
		t.Fatal("non-tool content array was unexpectedly changed")
	}
}

func TestNormalizeOpenAIToolResultsTextOnlyImageAndUnknownContent(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "image-only array",
			input: `{"messages":[{"role":"tool","content":[{"type":"image_url","image_url":{"url":"https://example.com/image.png"}}]}]}`,
			want:  openAIToolResultImageOmittedText,
		},
		{
			name:  "image object",
			input: `{"messages":[{"role":"tool","content":{"type":"image","source":{"type":"base64","data":"AA=="}}}]}`,
			want:  openAIToolResultImageOmittedText,
		},
		{
			name:  "unknown object",
			input: `{"messages":[{"role":"tool","content":[{"type":"custom","value":1}]}]}`,
			want:  `{"type":"custom","value":1}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeOpenAIToolResultsTextOnly([]byte(tt.input))
			if content := gjson.GetBytes(got, "messages.0.content").String(); content != tt.want {
				t.Fatalf("tool content = %q, want %q", content, tt.want)
			}
		})
	}
}

func TestShouldNormalizeOpenAIToolResultsForModel(t *testing.T) {
	compat := &config.OpenAICompatibility{Models: []config.OpenAICompatibilityModel{
		{Name: "upstream-text", Alias: "alias-text", InputModalities: []string{"text"}},
		{Name: "upstream-multimodal", Alias: "alias-multimodal", InputModalities: []string{"text", "image"}},
		{Name: "upstream-unspecified", Alias: "alias-unspecified"},
		{Name: "upstream-uppercase", Alias: "alias-uppercase", InputModalities: []string{"TEXT"}},
		{Name: "pool-text", Alias: "shared-alias", InputModalities: []string{"text"}},
		{Name: "pool-image", Alias: "shared-alias", InputModalities: []string{"text", "image"}},
	}}

	tests := []struct {
		name           string
		upstreamModel  string
		requestedModel string
		want           bool
	}{
		{name: "upstream text", upstreamModel: "upstream-text", want: true},
		{name: "upstream suffix", upstreamModel: "upstream-text(high)", want: true},
		{name: "requested alias", upstreamModel: "unknown", requestedModel: "alias-text", want: true},
		{name: "multimodal", upstreamModel: "upstream-multimodal", want: false},
		{name: "unspecified", upstreamModel: "upstream-unspecified", want: false},
		{name: "case insensitive modality", upstreamModel: "upstream-uppercase", want: true},
		{name: "mixed alias pool", upstreamModel: "unknown", requestedModel: "shared-alias", want: false},
		{name: "unknown", upstreamModel: "unknown", requestedModel: "missing", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ShouldNormalizeOpenAIToolResultsForModel(compat, tt.upstreamModel, tt.requestedModel); got != tt.want {
				t.Fatalf("normalize = %t, want %t", got, tt.want)
			}
		})
	}

	if ShouldNormalizeOpenAIToolResultsForModel(nil, "upstream-text", "alias-text") {
		t.Fatal("nil compatibility config unexpectedly enabled normalization")
	}
}

func TestEnsureOpenAICompatAssistantReasoningContent(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		wantReasoning string
		wantExists    bool
	}{
		{
			name:          "assistant tool_calls without reasoning_content gets fallback",
			input:         `{"messages":[{"role":"user","content":"read"},{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{}"}}]}]}`,
			wantReasoning: "[reasoning unavailable]",
			wantExists:    true,
		},
		{
			name:          "assistant tool_calls with empty reasoning_content gets fallback",
			input:         `{"messages":[{"role":"assistant","content":"","reasoning_content":"   ","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{}"}}]}]}`,
			wantReasoning: "[reasoning unavailable]",
			wantExists:    true,
		},
		{
			name:          "assistant tool_calls with existing reasoning_content preserved",
			input:         `{"messages":[{"role":"assistant","content":"","reasoning_content":"existing reasoning","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{}"}}]}]}`,
			wantReasoning: "existing reasoning",
			wantExists:    true,
		},
		{
			name:       "assistant text only without tool_calls has no reasoning_content injected",
			input:      `{"messages":[{"role":"assistant","content":"hello"}]}`,
			wantExists: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EnsureOpenAICompatAssistantReasoningContent([]byte(tt.input))
			var assistantMsg gjson.Result
			gjson.GetBytes(got, "messages").ForEach(func(_, m gjson.Result) bool {
				if m.Get("role").String() == "assistant" {
					assistantMsg = m
					return false
				}
				return true
			})
			reasoning := assistantMsg.Get("reasoning_content")
			if reasoning.Exists() != tt.wantExists {
				t.Fatalf("reasoning.Exists() = %t, want %t. Payload: %s", reasoning.Exists(), tt.wantExists, string(got))
			}
			if tt.wantExists && reasoning.String() != tt.wantReasoning {
				t.Fatalf("reasoning_content = %q, want %q. Payload: %s", reasoning.String(), tt.wantReasoning, string(got))
			}
		})
	}
}
