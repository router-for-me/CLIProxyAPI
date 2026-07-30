package translator

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestRegistryTranslateRequestAppliesSummaryIntent(t *testing.T) {
	tests := []struct {
		name       string
		from       Format
		to         Format
		input      string
		translated string
		path       string
		want       string
		wantExists bool
	}{
		{
			name:       "chat effort enables Claude summary",
			from:       FormatOpenAI,
			to:         FormatClaude,
			input:      `{"reasoning_effort":"high"}`,
			translated: `{"thinking":{"type":"adaptive"}}`,
			path:       "thinking.display",
			want:       "summarized",
			wantExists: true,
		},
		{
			name:       "responses effort alone leaves Claude display absent",
			from:       FormatOpenAIResponse,
			to:         FormatClaude,
			input:      `{"reasoning":{"effort":"high"}}`,
			translated: `{"thinking":{"type":"adaptive"}}`,
			path:       "thinking.display",
		},
		{
			name:       "responses summary enables Claude summary",
			from:       FormatOpenAIResponse,
			to:         FormatClaude,
			input:      `{"reasoning":{"effort":"high","summary":"auto"}}`,
			translated: `{"thinking":{"type":"adaptive"}}`,
			path:       "thinking.display",
			want:       "summarized",
			wantExists: true,
		},
		{
			name:       "responses null summary disables Gemini summaries",
			from:       FormatOpenAIResponse,
			to:         FormatGemini,
			input:      `{"reasoning":{"effort":"high","summary":null}}`,
			translated: `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"high"}}}`,
			path:       "generationConfig.thinkingConfig.includeThoughts",
			want:       "false",
			wantExists: true,
		},
		{
			name:       "Google Chat extension overrides effort",
			from:       FormatOpenAI,
			to:         FormatGemini,
			input:      `{"reasoning_effort":"high","extra_body":{"google":{"thinking_config":{"include_thoughts":false}}}}`,
			translated: `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"high","includeThoughts":true}}}`,
			path:       "generationConfig.thinkingConfig.includeThoughts",
			want:       "false",
			wantExists: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := NewRegistry()
			registry.Register(test.from, test.to, func(_ string, _ []byte, _ bool) []byte {
				return []byte(test.translated)
			}, ResponseTransform{})
			out := registry.TranslateRequest(test.from, test.to, "model", []byte(test.input), false)
			result := gjson.GetBytes(out, test.path)
			if result.Exists() != test.wantExists {
				t.Fatalf("%s exists = %v, want %v; body=%s", test.path, result.Exists(), test.wantExists, out)
			}
			if test.wantExists && result.String() != test.want {
				t.Fatalf("%s = %q, want %q; body=%s", test.path, result.String(), test.want, out)
			}
		})
	}
}

func TestRegistryTranslateRequestMakesSummaryOnlyClaudeRequestValid(t *testing.T) {
	registry := NewRegistry()
	registry.Register(FormatOpenAIResponse, FormatClaude, func(_ string, _ []byte, _ bool) []byte {
		return []byte(`{"model":"claude-opus-5","max_tokens":32000}`)
	}, ResponseTransform{})
	out := registry.TranslateRequest(
		FormatOpenAIResponse,
		FormatClaude,
		"claude-opus-5",
		[]byte(`{"reasoning":{"summary":"auto"},"input":"hi"}`),
		false,
	)
	if got := gjson.GetBytes(out, "thinking.type").String(); got != "adaptive" {
		t.Fatalf("thinking.type = %q, want adaptive; body=%s", got, out)
	}
	if got := gjson.GetBytes(out, "thinking.display").String(); got != "summarized" {
		t.Fatalf("thinking.display = %q, want summarized; body=%s", got, out)
	}
}

func TestRegistryTranslateRequestPreservesNativeClaudeMissingDisplay(t *testing.T) {
	registry := NewRegistry()
	body := []byte(`{"model":"claude-opus-5","thinking":{"type":"adaptive"}}`)
	out := registry.TranslateRequest(FormatClaude, FormatClaude, "claude-opus-5", body, true)
	if gjson.GetBytes(out, "thinking.display").Exists() {
		t.Fatalf("native Claude request without display gained one: %s", out)
	}
}
