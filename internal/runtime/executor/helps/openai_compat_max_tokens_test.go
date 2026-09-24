package helps

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/tidwall/gjson"
)

func TestShouldUseMaxCompletionTokensForModel(t *testing.T) {
	compat := &config.OpenAICompatibility{
		Models: []config.OpenAICompatibilityModel{
			{
				Name:                   "azure/o1-preview",
				Alias:                  "o1-preview",
				UseMaxCompletionTokens: true,
			},
			{
				Name:                   "deepseek/v3",
				Alias:                  "deepseek-chat",
				UseMaxCompletionTokens: false,
			},
			{
				Name:  "legacy/gpt-3.5",
				Alias: "gpt-3.5",
				// Omitted defaults to false
			},
		},
	}

	tests := []struct {
		name           string
		compat         *config.OpenAICompatibility
		upstreamModel  string
		requestedModel string
		want           bool
	}{
		{
			name:           "nil compat returns false",
			compat:         nil,
			upstreamModel:  "azure/o1-preview",
			requestedModel: "o1-preview",
			want:           false,
		},
		{
			name:           "match upstreamModel by name with true",
			compat:         compat,
			upstreamModel:  "azure/o1-preview",
			requestedModel: "other-alias",
			want:           true,
		},
		{
			name:           "match requestedModel by alias with true",
			compat:         compat,
			upstreamModel:  "unknown-upstream",
			requestedModel: "o1-preview",
			want:           true,
		},
		{
			name:           "match with thinking suffix on upstreamModel",
			compat:         compat,
			upstreamModel:  "azure/o1-preview(high)",
			requestedModel: "unknown-alias",
			want:           true,
		},
		{
			name:           "match with thinking suffix on requestedModel",
			compat:         compat,
			upstreamModel:  "unknown",
			requestedModel: "o1-preview(medium)",
			want:           true,
		},
		{
			name:           "case-insensitive match",
			compat:         compat,
			upstreamModel:  "AZURE/O1-PREVIEW",
			requestedModel: "O1-PREVIEW",
			want:           true,
		},
		{
			name:           "explicit false returns false",
			compat:         compat,
			upstreamModel:  "deepseek/v3",
			requestedModel: "deepseek-chat",
			want:           false,
		},
		{
			name:           "omitted default returns false",
			compat:         compat,
			upstreamModel:  "legacy/gpt-3.5",
			requestedModel: "gpt-3.5",
			want:           false,
		},
		{
			name:           "unmatched model returns false",
			compat:         compat,
			upstreamModel:  "completely-unknown",
			requestedModel: "unknown-alias",
			want:           false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ShouldUseMaxCompletionTokensForModel(tt.compat, tt.upstreamModel, tt.requestedModel)
			if got != tt.want {
				t.Fatalf("ShouldUseMaxCompletionTokensForModel() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNormalizeOpenAIMaxTokens(t *testing.T) {
	t.Run("useMaxCompletionTokens=true converts max_tokens", func(t *testing.T) {
		input := []byte(`{"model":"o1","max_tokens":1024,"messages":[]}`)
		got := NormalizeOpenAIMaxTokens(input, true)

		if v := gjson.GetBytes(got, "max_completion_tokens"); !v.Exists() || v.Int() != 1024 {
			t.Fatalf("max_completion_tokens = %v, want 1024; out=%s", v, string(got))
		}
		if gjson.GetBytes(got, "max_tokens").Exists() {
			t.Fatalf("max_tokens should be deleted; out=%s", string(got))
		}
	})

	t.Run("useMaxCompletionTokens=true preserves existing max_completion_tokens and removes max_tokens", func(t *testing.T) {
		input := []byte(`{"model":"o1","max_tokens":512,"max_completion_tokens":2048}`)
		got := NormalizeOpenAIMaxTokens(input, true)

		if v := gjson.GetBytes(got, "max_completion_tokens"); !v.Exists() || v.Int() != 2048 {
			t.Fatalf("max_completion_tokens = %v, want 2048; out=%s", v, string(got))
		}
		if gjson.GetBytes(got, "max_tokens").Exists() {
			t.Fatalf("max_tokens should be deleted; out=%s", string(got))
		}
	})

	t.Run("useMaxCompletionTokens=true keeps max_completion_tokens when max_tokens is absent", func(t *testing.T) {
		input := []byte(`{"model":"o1","max_completion_tokens":4096}`)
		got := NormalizeOpenAIMaxTokens(input, true)

		if v := gjson.GetBytes(got, "max_completion_tokens"); !v.Exists() || v.Int() != 4096 {
			t.Fatalf("max_completion_tokens = %v, want 4096; out=%s", v, string(got))
		}
		if gjson.GetBytes(got, "max_tokens").Exists() {
			t.Fatalf("max_tokens should be absent; out=%s", string(got))
		}
	})

	t.Run("useMaxCompletionTokens=false converts max_completion_tokens to max_tokens", func(t *testing.T) {
		input := []byte(`{"model":"deepseek","max_completion_tokens":1024,"messages":[]}`)
		got := NormalizeOpenAIMaxTokens(input, false)

		if v := gjson.GetBytes(got, "max_tokens"); !v.Exists() || v.Int() != 1024 {
			t.Fatalf("max_tokens = %v, want 1024; out=%s", v, string(got))
		}
		if gjson.GetBytes(got, "max_completion_tokens").Exists() {
			t.Fatalf("max_completion_tokens should be deleted; out=%s", string(got))
		}
	})

	t.Run("useMaxCompletionTokens=false preserves existing max_tokens and removes max_completion_tokens", func(t *testing.T) {
		input := []byte(`{"model":"deepseek","max_tokens":512,"max_completion_tokens":2048}`)
		got := NormalizeOpenAIMaxTokens(input, false)

		if v := gjson.GetBytes(got, "max_tokens"); !v.Exists() || v.Int() != 512 {
			t.Fatalf("max_tokens = %v, want 512; out=%s", v, string(got))
		}
		if gjson.GetBytes(got, "max_completion_tokens").Exists() {
			t.Fatalf("max_completion_tokens should be deleted; out=%s", string(got))
		}
	})

	t.Run("useMaxCompletionTokens=false keeps max_tokens when max_completion_tokens is absent", func(t *testing.T) {
		input := []byte(`{"model":"deepseek","max_tokens":4096}`)
		got := NormalizeOpenAIMaxTokens(input, false)

		if v := gjson.GetBytes(got, "max_tokens"); !v.Exists() || v.Int() != 4096 {
			t.Fatalf("max_tokens = %v, want 4096; out=%s", v, string(got))
		}
		if gjson.GetBytes(got, "max_completion_tokens").Exists() {
			t.Fatalf("max_completion_tokens should be absent; out=%s", string(got))
		}
	})

	t.Run("useMaxCompletionTokens=true preserves null value", func(t *testing.T) {
		input := []byte(`{"model":"o1","max_tokens":null}`)
		got := NormalizeOpenAIMaxTokens(input, true)

		if v := gjson.GetBytes(got, "max_completion_tokens"); !v.Exists() || v.Type != gjson.Null {
			t.Fatalf("max_completion_tokens = %v, want null; out=%s", v, string(got))
		}
		if gjson.GetBytes(got, "max_tokens").Exists() {
			t.Fatalf("max_tokens should be deleted; out=%s", string(got))
		}
	})

	t.Run("useMaxCompletionTokens=false preserves null value", func(t *testing.T) {
		input := []byte(`{"model":"deepseek","max_completion_tokens":null}`)
		got := NormalizeOpenAIMaxTokens(input, false)

		if v := gjson.GetBytes(got, "max_tokens"); !v.Exists() || v.Type != gjson.Null {
			t.Fatalf("max_tokens = %v, want null; out=%s", v, string(got))
		}
		if gjson.GetBytes(got, "max_completion_tokens").Exists() {
			t.Fatalf("max_completion_tokens should be deleted; out=%s", string(got))
		}
	})

	t.Run("payload without max tokens remains unchanged", func(t *testing.T) {
		input := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)

		gotTrue := NormalizeOpenAIMaxTokens(input, true)
		if gjson.GetBytes(gotTrue, "max_tokens").Exists() || gjson.GetBytes(gotTrue, "max_completion_tokens").Exists() {
			t.Fatalf("unexpected token fields emitted for true; out=%s", string(gotTrue))
		}

		gotFalse := NormalizeOpenAIMaxTokens(input, false)
		if gjson.GetBytes(gotFalse, "max_tokens").Exists() || gjson.GetBytes(gotFalse, "max_completion_tokens").Exists() {
			t.Fatalf("unexpected token fields emitted for false; out=%s", string(gotFalse))
		}
	})

	t.Run("empty payload returns empty", func(t *testing.T) {
		if got := NormalizeOpenAIMaxTokens(nil, true); len(got) != 0 {
			t.Fatalf("expected empty result, got %s", string(got))
		}
		if got := NormalizeOpenAIMaxTokens([]byte{}, false); len(got) != 0 {
			t.Fatalf("expected empty result, got %s", string(got))
		}
	})
}
