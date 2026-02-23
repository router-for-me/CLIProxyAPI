package acp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestACPAdapterIsRegisteredAndAvailable verifies that NewTranslatorRegistry
// auto-registers the ACP adapter under the "acp" key.
// @trace FR-ADAPTERS-001
func TestACPAdapterIsRegisteredAndAvailable(t *testing.T) {
	registry := NewTranslatorRegistry()

	adapterExists := registry.HasTranslator("acp")

	assert.True(t, adapterExists, "ACP adapter not registered in translator registry")
}

// TestACPAdapterTransformsClaudeToACP verifies that a Claude/OpenAI-format request is
// correctly translated to ACP format by the registered adapter.
// @trace FR-ADAPTERS-001 FR-ADAPTERS-002
func TestACPAdapterTransformsClaudeToACP(t *testing.T) {
	registry := NewTranslatorRegistry()
	adapter := registry.GetTranslator("acp")
	require.NotNil(t, adapter)

	claudeReq := &ChatCompletionRequest{
		Model: "claude-opus-4-6",
		Messages: []Message{
			{Role: "user", Content: "Hello"},
		},
	}

	acpReq, err := adapter.Translate(context.Background(), claudeReq)

	require.NoError(t, err)
	require.NotNil(t, acpReq)
	assert.Equal(t, "claude-opus-4-6", acpReq.Model)
	assert.Len(t, acpReq.Messages, 1)
	assert.Equal(t, "user", acpReq.Messages[0].Role)
	assert.Equal(t, "Hello", acpReq.Messages[0].Content)
}

// TestACPAdapterRejectsNilRequest verifies that a nil request returns an error.
func TestACPAdapterRejectsNilRequest(t *testing.T) {
	adapter := NewACPAdapter("http://localhost:9000")

	_, err := adapter.Translate(context.Background(), nil)

	assert.Error(t, err)
}

// TestACPAdapterPreservesMultipleMessages verifies multi-turn conversation preservation.
// @trace FR-ADAPTERS-002
func TestACPAdapterPreservesMultipleMessages(t *testing.T) {
	adapter := NewACPAdapter("http://localhost:9000")

	req := &ChatCompletionRequest{
		Model: "claude-sonnet-4.6",
		Messages: []Message{
			{Role: "system", Content: "You are a helpful assistant."},
			{Role: "user", Content: "What is 2+2?"},
			{Role: "assistant", Content: "4"},
			{Role: "user", Content: "And 3+3?"},
		},
	}

	acpReq, err := adapter.Translate(context.Background(), req)

	require.NoError(t, err)
	assert.Len(t, acpReq.Messages, 4)
	assert.Equal(t, "system", acpReq.Messages[0].Role)
	assert.Equal(t, "assistant", acpReq.Messages[2].Role)
}
