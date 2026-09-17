package helps

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/tidwall/gjson"
)

func TestNormalizeOpenAIToolResultsTextOnlyRelayedImages(t *testing.T) {
	t.Run("synthetic relay message is dropped and tool is marked", func(t *testing.T) {
		input := []byte(`{"messages":[
			{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"inspect_image","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"image inspected"},
			{"role":"user","content":[
				{"type":"text","text":"Images returned by the preceding tool call(s):"},
				{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}
			]}
		]}`)

		got := NormalizeOpenAIToolResultsTextOnly(input)
		if strings.Contains(string(got), "image_url") {
			t.Fatalf("text-only normalization left image_url in payload: %s", string(got))
		}
		messages := gjson.GetBytes(got, "messages").Array()
		if len(messages) != 2 {
			t.Fatalf("expected 2 messages after dropping synthetic relay user message, got %d: %s", len(messages), string(got))
		}
		if gotContent := messages[1].Get("content").String(); gotContent != "image inspected\n\n"+openAIToolResultImageOmittedText {
			t.Fatalf("tool content = %q, want %q", gotContent, "image inspected\n\n"+openAIToolResultImageOmittedText)
		}
	})

	t.Run("placeholder tool content is replaced with omitted marker", func(t *testing.T) {
		input := []byte(`{"messages":[
			{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"inspect_image","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"[Tool returned image content; the images follow in the next user message.]"},
			{"role":"user","content":[
				{"type":"text","text":"Images returned by the preceding tool call(s):"},
				{"type":"image_url","image_url":{"url":"https://example.com/img.png"}}
			]}
		]}`)

		got := NormalizeOpenAIToolResultsTextOnly(input)
		if strings.Contains(string(got), "image_url") {
			t.Fatalf("text-only normalization left image_url in payload: %s", string(got))
		}
		messages := gjson.GetBytes(got, "messages").Array()
		if len(messages) != 2 {
			t.Fatalf("expected 2 messages, got %d: %s", len(messages), string(got))
		}
		if gotContent := messages[1].Get("content").String(); gotContent != openAIToolResultImageOmittedText {
			t.Fatalf("tool content = %q, want %q", gotContent, openAIToolResultImageOmittedText)
		}
	})

	t.Run("merged user message retains user prompt while stripping relay images", func(t *testing.T) {
		input := []byte(`{"messages":[
			{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"inspect_image","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"image inspected"},
			{"role":"user","content":[
				{"type":"text","text":"Images returned by the preceding tool call(s):"},
				{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}},
				{"type":"text","text":"What color is the car?"}
			]}
		]}`)

		got := NormalizeOpenAIToolResultsTextOnly(input)
		if strings.Contains(string(got), "image_url") {
			t.Fatalf("text-only normalization left image_url in payload: %s", string(got))
		}
		messages := gjson.GetBytes(got, "messages").Array()
		if len(messages) != 3 {
			t.Fatalf("expected 3 messages when user text is present, got %d: %s", len(messages), string(got))
		}
		if gotContent := messages[1].Get("content").String(); gotContent != "image inspected\n\n"+openAIToolResultImageOmittedText {
			t.Fatalf("tool content = %q, want %q", gotContent, "image inspected\n\n"+openAIToolResultImageOmittedText)
		}
		userContent := messages[2].Get("content").Raw
		if strings.Contains(userContent, claudeToolResultImageRelayNotice) {
			t.Fatalf("user content still has relay notice: %s", userContent)
		}
		if !strings.Contains(userContent, "What color is the car?") {
			t.Fatalf("user text was lost: %s", userContent)
		}
	})

	t.Run("multiple preceding tools where one is placeholder", func(t *testing.T) {
		input := []byte(`{"messages":[
			{"role":"assistant","content":"","tool_calls":[
				{"id":"call_1","type":"function","function":{"name":"tool1","arguments":"{}"}},
				{"id":"call_2","type":"function","function":{"name":"tool2","arguments":"{}"}}
			]},
			{"role":"tool","tool_call_id":"call_1","content":"text result only"},
			{"role":"tool","tool_call_id":"call_2","content":"[Tool returned image content; the images follow in the next user message.]"},
			{"role":"user","content":[
				{"type":"text","text":"Images returned by the preceding tool call(s):"},
				{"type":"image_url","image_url":{"url":"https://example.com/tool2.png"}}
			]}
		]}`)

		got := NormalizeOpenAIToolResultsTextOnly(input)
		if strings.Contains(string(got), "image_url") {
			t.Fatalf("text-only normalization left image_url in payload: %s", string(got))
		}
		messages := gjson.GetBytes(got, "messages").Array()
		if len(messages) != 3 {
			t.Fatalf("expected 3 messages, got %d: %s", len(messages), string(got))
		}
		if got1 := messages[1].Get("content").String(); got1 != "text result only" {
			t.Fatalf("tool 1 content = %q, want 'text result only'", got1)
		}
		if got2 := messages[2].Get("content").String(); got2 != openAIToolResultImageOmittedText {
			t.Fatalf("tool 2 content = %q, want %q", got2, openAIToolResultImageOmittedText)
		}
	})

	t.Run("multiple preceding tools where image tool precedes text tool", func(t *testing.T) {
		input := []byte(`{"messages":[
			{"role":"assistant","content":"","tool_calls":[
				{"id":"call_1","type":"function","function":{"name":"tool1","arguments":"{}"}},
				{"id":"call_2","type":"function","function":{"name":"tool2","arguments":"{}"}}
			]},
			{"role":"tool","tool_call_id":"call_1","content":"[Tool returned image content; the images follow in the next user message.]"},
			{"role":"tool","tool_call_id":"call_2","content":"text result only"},
			{"role":"user","content":[
				{"type":"text","text":"Images returned by the preceding tool call(s):"},
				{"type":"image_url","image_url":{"url":"https://example.com/tool1.png"}}
			]}
		]}`)

		got := NormalizeOpenAIToolResultsTextOnly(input)
		if strings.Contains(string(got), "image_url") {
			t.Fatalf("text-only normalization left image_url in payload: %s", string(got))
		}
		messages := gjson.GetBytes(got, "messages").Array()
		if len(messages) != 3 {
			t.Fatalf("expected 3 messages, got %d: %s", len(messages), string(got))
		}
		if got1 := messages[1].Get("content").String(); got1 != openAIToolResultImageOmittedText {
			t.Fatalf("tool 1 content = %q, want %q", got1, openAIToolResultImageOmittedText)
		}
		if got2 := messages[2].Get("content").String(); got2 != "text result only" {
			t.Fatalf("tool 2 content = %q, want 'text result only'", got2)
		}
	})
}

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
