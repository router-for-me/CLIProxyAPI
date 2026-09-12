package executor

import (
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestNormalizeOpenAICompatClaudeSampling(t *testing.T) {
	tests := []struct {
		name       string
		from       sdktranslator.Format
		source     string
		translated string
		wantTopK   bool
	}{
		{
			name:       "claude adaptive thinking drops top_k",
			from:       sdktranslator.FormatClaude,
			source:     `{"thinking":{"type":"adaptive"},"top_k":40}`,
			translated: `{"reasoning_effort":"max","temperature":1,"top_k":40}`,
		},
		{
			name:       "claude enabled thinking drops top_k",
			from:       sdktranslator.FormatClaude,
			source:     `{"thinking":{"type":"enabled","budget_tokens":1024},"top_k":40}`,
			translated: `{"reasoning_effort":"low","top_k":40}`,
		},
		{
			name:       "claude effort activates thinking after translation and drops top_k",
			from:       sdktranslator.FormatClaude,
			source:     `{"output_config":{"effort":"max"},"top_k":40}`,
			translated: `{"reasoning_effort":"max","top_k":40}`,
		},
		{
			name:       "translated reasoning effort drops top_k",
			from:       sdktranslator.FormatClaude,
			source:     `{"messages":[],"top_k":40}`,
			translated: `{"reasoning_effort":"high","top_k":40}`,
		},
		{
			name:       "claude auto thinking drops top_k",
			from:       sdktranslator.FormatClaude,
			source:     `{"thinking":{"type":"auto"},"top_k":40}`,
			translated: `{"top_k":40}`,
		},
		{
			name:       "claude reasoning effort drops top_k",
			from:       sdktranslator.FormatClaude,
			source:     `{"reasoning":{"effort":"high"},"top_k":40}`,
			translated: `{"top_k":40}`,
		},
		{
			name:       "explicit none effort keeps top_k",
			from:       sdktranslator.FormatClaude,
			source:     `{"output_config":{"effort":"none"},"top_k":40}`,
			translated: `{"reasoning_effort":"none","top_k":40}`,
			wantTopK:   true,
		},
		{
			name:       "translated active effort overrides source disabled sentinel",
			from:       sdktranslator.FormatClaude,
			source:     `{"thinking":{"type":"disabled"},"top_k":40}`,
			translated: `{"reasoning_effort":"high","top_k":40}`,
		},
		{
			name:       "claude without thinking keeps top_k",
			from:       sdktranslator.FormatClaude,
			source:     `{"messages":[]}`,
			translated: `{"top_k":40}`,
			wantTopK:   true,
		},
		{
			name:       "non-claude source keeps top_k",
			from:       sdktranslator.FormatOpenAI,
			source:     `{"thinking":{"type":"adaptive"}}`,
			translated: `{"top_k":40}`,
			wantTopK:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeOpenAICompatClaudeSampling([]byte(tt.translated), []byte(tt.source), tt.from)
			if exists := gjson.GetBytes(got, "top_k").Exists(); exists != tt.wantTopK {
				t.Fatalf("top_k exists = %v, want %v; body=%s", exists, tt.wantTopK, got)
			}
			if temperature := gjson.GetBytes(got, "temperature"); temperature.Exists() && temperature.Int() != 1 {
				t.Fatalf("temperature changed unexpectedly: body=%s", got)
			}
		})
	}
}
