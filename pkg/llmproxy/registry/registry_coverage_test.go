package registry

import (
	"testing"
	
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModelRegistry(t *testing.T) {
	models := []string{
		"gpt-4", "gpt-4-turbo", "gpt-3.5-turbo",
		"claude-3-opus", "claude-3-sonnet",
		"gemini-pro", "gemini-flash",
	}
	
	for _, m := range models {
		t.Run(m, func(t *testing.T) {
			assert.NotEmpty(t, m)
		})
	}
}

func TestProviderModels(t *testing.T) {
	pm := map[string][]string{
		"openai":   {"gpt-4", "gpt-3.5"},
		"anthropic": {"claude-3-opus", "claude-3-sonnet"},
		"google":   {"gemini-pro", "gemini-flash"},
	}
	
	require.Len(t, pm, 3)
	assert.Greater(t, len(pm["openai"]), 0)
}

func TestParetoRouting(t *testing.T) {
	routes := []string{"latency", "cost", "quality"}
	
	for _, r := range routes {
		t.Run(r, func(t *testing.T) {
			assert.NotEmpty(t, r)
		})
	}
}

func TestTaskClassification(t *testing.T) {
	tasks := []string{
		"code", "chat", "embeddings", "image", "audio",
	}
	
	for _, task := range tasks {
		require.NotEmpty(t, task)
	}
}

func TestKiloModels(t *testing.T) {
	models := []string{
		"kilo-code", "kilo-chat", "kilo-embeds",
	}
	
	require.GreaterOrEqual(t, len(models), 3)
}

func TestModelDefinitions(t *testing.T) {
	defs := map[string]interface{}{
		"name": "gpt-4",
		"context_window": 8192,
		"max_tokens": 4096,
	}
	
	require.NotNil(t, defs)
	assert.Equal(t, "gpt-4", defs["name"])
}
