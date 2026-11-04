package translator

import (
	"bytes"
	"context"
	"testing"

	"github.com/MoeForge/moego/ojson"
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
			name:       "chat effort leaves Claude display unspecified",
			from:       FormatOpenAI,
			to:         FormatClaude,
			input:      `{"reasoning_effort":"high"}`,
			translated: `{"thinking":{"type":"adaptive"}}`,
			path:       "thinking.display",
		},
		{
			name:       "chat explicit exclusion disables Claude summary",
			from:       FormatOpenAI,
			to:         FormatClaude,
			input:      `{"reasoning_effort":"high","reasoning":{"exclude":true}}`,
			translated: `{"thinking":{"type":"adaptive"}}`,
			path:       "thinking.display",
			want:       "omitted",
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
			registry.Register(test.from, test.to, func(_ string, _ []byte, _ bool) ([]byte, error) {
				return []byte(test.translated), nil
			}, ResponseTransform{})
			out := registry.TranslateRequest(test.from, test.to, "model", []byte(test.input), false)
			result := ojson.GetBytesNoCopy(out, test.path)
			if result.Exists() != test.wantExists {
				t.Fatalf("%s exists = %v, want %v; body=%s", test.path, result.Exists(), test.wantExists, out)
			}
			if test.wantExists && result.String() != test.want {
				t.Fatalf("%s = %q, want %q; body=%s", test.path, result.String(), test.want, out)
			}
		})
	}
}

func TestRegistryTranslateRequestActivatesClaudeForEnabledSummary(t *testing.T) {
	registry := NewRegistry()
	registry.Register(FormatOpenAIResponse, FormatClaude, func(_ string, _ []byte, _ bool) ([]byte, error) {
		return []byte(`{"model":"claude-opus-5","max_tokens":32000}`), nil
	}, ResponseTransform{})
	out := registry.TranslateRequest(
		FormatOpenAIResponse,
		FormatClaude,
		"claude-opus-5",
		[]byte(`{"reasoning":{"summary":"auto"},"input":"hi"}`),
		false,
	)
	if got := ojson.GetBytesNoCopy(out, "thinking.type").String(); got != "adaptive" {
		t.Fatalf("thinking.type = %q, want adaptive; body=%s", got, out)
	}
	if got := ojson.GetBytesNoCopy(out, "thinking.display").String(); got != "summarized" {
		t.Fatalf("thinking.display = %q, want summarized; body=%s", got, out)
	}
}

func TestRegistryTranslateRequestDoesNotActivateClaudeForDisabledSummary(t *testing.T) {
	registry := NewRegistry()
	registry.Register(FormatOpenAIResponse, FormatClaude, func(_ string, _ []byte, _ bool) ([]byte, error) {
		return []byte(`{"model":"claude-opus-5","max_tokens":32000}`), nil
	}, ResponseTransform{})
	out := registry.TranslateRequest(
		FormatOpenAIResponse,
		FormatClaude,
		"claude-opus-5",
		[]byte(`{"reasoning":{"summary":null},"input":"hi"}`),
		false,
	)
	if ojson.GetBytesNoCopy(out, "thinking").Exists() {
		t.Fatalf("disabled summary activated Claude thinking: %s", out)
	}
}

func TestRegistryTranslateRequestPreservesNativeClaudeMissingDisplay(t *testing.T) {
	registry := NewRegistry()
	body := []byte(`{"model":"claude-opus-5","thinking":{"type":"adaptive"}}`)
	out := registry.TranslateRequest(FormatClaude, FormatClaude, "claude-opus-5", body, true)
	if ojson.GetBytesNoCopy(out, "thinking.display").Exists() {
		t.Fatalf("native Claude request without display gained one: %s", out)
	}
}

func TestRegistryTranslateRequestDoesNotMixSummaryIntoFallback(t *testing.T) {
	registry := NewRegistry()
	body := []byte(`{"model":"gemini-3.6-flash","reasoning":{"summary":"auto"},"input":"hi"}`)
	out := registry.TranslateRequest(FormatOpenAIResponse, FormatGemini, "gemini-3.6-flash", body, false)
	if !bytes.Equal(out, body) {
		t.Fatalf("missing translator changed fallback body: got %s, want %s", out, body)
	}
	if ojson.GetBytesNoCopy(out, "generationConfig").Exists() {
		t.Fatalf("missing translator mixed Gemini fields into Responses body: %s", out)
	}
}

func TestRegistryTranslateRequestPluginMissDoesNotMixSummary(t *testing.T) {
	registry := NewRegistry()
	hooks := &fakePluginHooks{requestTranslateOK: false}
	registry.SetPluginHooks(hooks)
	body := []byte(`{"model":"gemini-3.6-flash","reasoning":{"summary":"auto"},"input":"hi"}`)
	out := registry.TranslateRequest(FormatOpenAIResponse, FormatGemini, "gemini-3.6-flash", body, false)
	if !bytes.Equal(out, body) {
		t.Fatalf("plugin translation miss changed fallback body: got %s, want %s", out, body)
	}
	if ojson.GetBytesNoCopy(out, "generationConfig").Exists() {
		t.Fatalf("plugin translation miss mixed Gemini fields into Responses body: %s", out)
	}
}

func TestRegistryTranslateRequestAppliesSummaryAfterPluginTranslation(t *testing.T) {
	registry := NewRegistry()
	hooks := &fakePluginHooks{
		requestTranslateBody: []byte(`{"generationConfig":{"thinkingConfig":{"thinkingLevel":"high"}}}`),
		requestTranslateOK:   true,
	}
	registry.SetPluginHooks(hooks)
	out := registry.TranslateRequest(
		FormatOpenAIResponse,
		FormatGemini,
		"gemini-3.6-flash",
		[]byte(`{"reasoning":{"summary":"auto"},"input":"hi"}`),
		false,
	)
	if !ojson.GetBytesNoCopy(out, "generationConfig.thinkingConfig.includeThoughts").Bool() {
		t.Fatalf("plugin-translated request lost canonical summary: %s", out)
	}
}

func TestRegistryTranslateRequestPluginNormalizerOwnsSourceSummaryIntent(t *testing.T) {
	tests := []struct {
		name       string
		normalize  func([]byte) []byte
		wantExists bool
		want       bool
	}{
		{
			name: "removed summary remains absent",
			normalize: func(body []byte) []byte {
				out, _ := ojson.DeleteBytes(body, "reasoning.summary")
				return out
			},
		},
		{
			name: "disabled summary replaces enabled intent",
			normalize: func(body []byte) []byte {
				out, _ := ojson.SetBytes(body, "reasoning.summary", nil)
				return out
			},
			wantExists: true,
			want:       false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := NewRegistry()
			hooks := &fakePluginHooks{
				normalizeRequest:     test.normalize,
				requestTranslateBody: []byte(`{"generationConfig":{"thinkingConfig":{"thinkingLevel":"high"}}}`),
				requestTranslateOK:   true,
			}
			registry.SetPluginHooks(hooks)

			out := registry.TranslateRequest(
				FormatOpenAIResponse,
				FormatGemini,
				"gemini-3.6-flash",
				[]byte(`{"reasoning":{"summary":"auto"},"input":"hi"}`),
				false,
			)
			result := ojson.GetBytesNoCopy(out, "generationConfig.thinkingConfig.includeThoughts")
			if result.Exists() != test.wantExists {
				t.Fatalf("includeThoughts exists = %v, want %v; body=%s", result.Exists(), test.wantExists, out)
			}
			if test.wantExists && result.Bool() != test.want {
				t.Fatalf("includeThoughts = %v, want %v; body=%s", result.Bool(), test.want, out)
			}
		})
	}
}

func TestRegistryTranslateRequestNormalizerOwnsFinalSummaryField(t *testing.T) {
	registry := NewRegistry()
	registry.Register(FormatOpenAIResponse, FormatGemini, func(_ string, _ []byte, _ bool) ([]byte, error) {
		return []byte(`{"generationConfig":{"thinkingConfig":{"thinkingLevel":"high"}}}`), nil
	}, ResponseTransform{})
	hooks := &fakePluginHooks{normalizeRequest: func(body []byte) []byte {
		if !ojson.GetBytesNoCopy(body, "generationConfig.thinkingConfig.includeThoughts").Bool() {
			t.Fatalf("normalizer did not receive canonical enabled summary: %s", body)
		}
		out, _ := ojson.DeleteBytes(body, "generationConfig.thinkingConfig.includeThoughts")
		return out
	}}
	registry.SetPluginHooks(hooks)

	out := registry.TranslateRequest(
		FormatOpenAIResponse,
		FormatGemini,
		"gemini-3.6-flash",
		[]byte(`{"reasoning":{"effort":"high","summary":"auto"},"input":"hi"}`),
		false,
	)
	if ojson.GetBytesNoCopy(out, "generationConfig.thinkingConfig.includeThoughts").Exists() {
		t.Fatalf("summary post-processing overrode request normalizer: %s", out)
	}
}

func TestRegistryTranslateRequestDetectsInPlaceConfigurationUpdateMutation(t *testing.T) {
	registry := NewRegistry()
	registry.SetPluginHooks(&fakePluginHooks{normalizeRequest: func(body []byte) []byte {
		for i := 0; i+4 <= len(body); i++ {
			if string(body[i:i+4]) == "auto" {
				copy(body[i:i+4], "none")
				break
			}
		}
		return body
	}})

	raw := []byte(`{"model":"gpt-5","input":[{"type":"configuration_update","reasoning":{"summary":"auto"}}]}`)
	env := registry.TranslateRequestEnvelope(context.Background(), FormatOpenAIResponse, FormatClaude, RequestEnvelope{
		Format: FormatOpenAIResponse,
		Model:  "gpt-5",
		Body:   raw,
	})
	if !env.ConfigurationUpdatesChanged {
		t.Fatal("expected ConfigurationUpdatesChanged to be true when normalizer mutates input buffer in-place")
	}
}
