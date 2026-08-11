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

func TestShouldEnsureOpenAICompatAssistantReasoningContent(t *testing.T) {
	compat := &config.OpenAICompatibility{
		Models: []config.OpenAICompatibilityModel{
			{Name: "upstream-fill", Alias: "alias-fill", FillMissingReasoningHistory: true},
			{Name: "upstream-nofill", Alias: "alias-nofill", FillMissingReasoningHistory: false},
		},
	}

	compatProviderOptIn := &config.OpenAICompatibility{
		FillMissingReasoningHistory: true,
		Models: []config.OpenAICompatibilityModel{
			{Name: "model-a", Alias: "alias-a"},
		},
	}

	tests := []struct {
		name           string
		compat         *config.OpenAICompatibility
		upstreamModel  string
		requestedModel string
		want           bool
	}{
		{name: "model fill true", compat: compat, upstreamModel: "upstream-fill", want: true},
		{name: "model fill suffix", compat: compat, upstreamModel: "upstream-fill(high)", want: true},
		{name: "alias fill true", compat: compat, upstreamModel: "unknown", requestedModel: "alias-fill", want: true},
		{name: "model fill false", compat: compat, upstreamModel: "upstream-nofill", want: false},
		{name: "provider level opt in", compat: compatProviderOptIn, upstreamModel: "model-a", want: true},
		{name: "nil compat", compat: nil, upstreamModel: "upstream-fill", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ShouldEnsureOpenAICompatAssistantReasoningContent(tt.compat, tt.upstreamModel, tt.requestedModel); got != tt.want {
				t.Fatalf("fill = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestEnsureOpenAICompatAssistantReasoningContent(t *testing.T) {
	input := []byte(`{"messages":[
		{"role":"user","content":"hello"},
		{"role":"assistant","content":"text answer without reasoning"},
		{"role":"user","content":"next question"},
		{"role":"assistant","content":"answer with reasoning","reasoning_content":"existing reasoning"}
	]}`)

	got := EnsureOpenAICompatAssistantReasoningContent(input)

	res1 := gjson.GetBytes(got, "messages.1.reasoning_content")
	if !res1.Exists() || res1.String() != "[reasoning unavailable]" {
		t.Fatalf("messages.1.reasoning_content = %q, want [reasoning unavailable]", res1.String())
	}

	res3 := gjson.GetBytes(got, "messages.3.reasoning_content")
	if !res3.Exists() || res3.String() != "existing reasoning" {
		t.Fatalf("messages.3.reasoning_content = %q, want existing reasoning", res3.String())
	}
}
