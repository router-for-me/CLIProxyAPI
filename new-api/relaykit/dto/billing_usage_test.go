package dto

import (
	"testing"

	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewGeminiChatBillingUsageRequiresTokenContent(t *testing.T) {
	require.Nil(t, NewGeminiChatBillingUsage(nil))
	require.Nil(t, NewGeminiChatBillingUsage(&GeminiUsageMetadata{}))

	billingUsage := NewGeminiChatBillingUsage(&GeminiUsageMetadata{PromptTokenCount: 1})
	require.NotNil(t, billingUsage)
	require.NotNil(t, billingUsage.GeminiUsageMetadata)
	assert.Equal(t, BillingUsageSourceGeminiChat, billingUsage.Source)
	assert.Equal(t, BillingUsageSemanticGemini, billingUsage.Semantic)
	assert.False(t, billingUsage.Estimated)
}

func TestNewClaudeMessagesBillingUsageRequiresTokenContent(t *testing.T) {
	require.Nil(t, NewClaudeMessagesBillingUsage(nil))
	require.Nil(t, NewClaudeMessagesBillingUsage(&ClaudeUsage{}))
	require.Nil(t, NewClaudeMessagesBillingUsage(&ClaudeUsage{CacheCreation: &ClaudeCacheCreationUsage{}}))

	billingUsage := NewClaudeMessagesBillingUsage(&ClaudeUsage{InputTokens: 1})
	require.NotNil(t, billingUsage)
	require.NotNil(t, billingUsage.ClaudeUsage)
	assert.Equal(t, BillingUsageSourceClaudeMessages, billingUsage.Source)
	assert.Equal(t, BillingUsageSemanticAnthropic, billingUsage.Semantic)

	cacheOnly := NewClaudeMessagesBillingUsage(&ClaudeUsage{
		CacheCreation: &ClaudeCacheCreationUsage{Ephemeral5mInputTokens: 4},
	})
	require.NotNil(t, cacheOnly)
}

func TestNewOpenAIChatBillingUsageRequiresTokenContent(t *testing.T) {
	require.Nil(t, NewOpenAIChatBillingUsage(nil))
	require.Nil(t, NewOpenAIChatBillingUsage(&Usage{}))

	billingUsage := NewOpenAIChatBillingUsage(&Usage{PromptTokens: 1})
	require.NotNil(t, billingUsage)
	require.NotNil(t, billingUsage.OpenAIUsage)
	assert.Equal(t, BillingUsageSourceOAIChat, billingUsage.Source)
	assert.Equal(t, BillingUsageSemanticOpenAI, billingUsage.Semantic)
	assert.Equal(t, 1, billingUsage.OpenAIUsage.PromptTokens)
}

func TestImageCacheDetailsSurviveUsageSnapshots(t *testing.T) {
	var original Usage
	require.NoError(t, kitutil.Unmarshal([]byte(`{"input_tokens":1000,"input_tokens_details":{"cached_tokens":300,"image_tokens":600,"cached_tokens_details":{"image_tokens":200,"text_tokens":100}}}`), &original))
	billing := NewOpenAIResponsesBillingUsage(&original)
	require.NotNil(t, billing)
	*original.InputTokensDetails.CachedTokensDetails.ImageTokens = 99
	canonical, ok := billing.CanonicalUsage()
	require.True(t, ok)
	require.NotNil(t, canonical.PromptTokensDetails.CachedTokensDetails)
	assert.Equal(t, 200, *canonical.PromptTokensDetails.CachedTokensDetails.ImageTokens)
	*canonical.PromptTokensDetails.CachedTokensDetails.ImageTokens = 42
	assert.Equal(t, 200, *billing.OpenAIUsage.InputTokensDetails.CachedTokensDetails.ImageTokens)

	var incoming Usage
	require.NoError(t, kitutil.Unmarshal([]byte(`{"input_tokens_details":{"cached_tokens_details":{"image_tokens":0}}}`), &incoming))
	merged := MergeUsageNonZero(canonical, &incoming)
	require.NotNil(t, merged.InputTokensDetails.CachedTokensDetails.ImageTokens)
	assert.Zero(t, *merged.InputTokensDetails.CachedTokensDetails.ImageTokens)
	assert.Equal(t, 100, *merged.InputTokensDetails.CachedTokensDetails.TextTokens)
	*incoming.InputTokensDetails.CachedTokensDetails.ImageTokens = 9
	assert.Zero(t, *merged.InputTokensDetails.CachedTokensDetails.ImageTokens)
	encoded, err := kitutil.Marshal(merged)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"cached_tokens_details":{"text_tokens":100,"image_tokens":0}`)
	assert.NotContains(t, string(encoded), `"audio_tokens":null`)
}

func TestNewEstimatedGeminiChatBillingUsage(t *testing.T) {
	billingUsage := NewEstimatedGeminiChatBillingUsage(&Usage{
		PromptTokens:     11,
		CompletionTokens: 7,
	})

	require.NotNil(t, billingUsage)
	require.NotNil(t, billingUsage.GeminiUsageMetadata)
	assert.True(t, billingUsage.Estimated)
	assert.Equal(t, 11, billingUsage.GeminiUsageMetadata.PromptTokenCount)
	assert.Equal(t, 7, billingUsage.GeminiUsageMetadata.CandidatesTokenCount)
	assert.Equal(t, 18, billingUsage.GeminiUsageMetadata.TotalTokenCount)
}

func TestCanonicalGeminiUsageClampsNegativeCompletionFromTotalMinusPrompt(t *testing.T) {
	usage, ok := NewGeminiChatBillingUsage(&GeminiUsageMetadata{
		PromptTokenCount: 50,
		TotalTokenCount:  30,
	}).CanonicalUsage()
	require.True(t, ok)
	assert.Equal(t, 0, usage.CompletionTokens)
}

func TestCanonicalOpenAIUsageMergesInputTokenDetailsFieldwise(t *testing.T) {
	usage, ok := NewOpenAIResponsesBillingUsage(&Usage{
		PromptTokens: 10,
		PromptTokensDetails: InputTokenDetails{
			CachedTokens: 8,
			TextTokens:   12,
			ImageTokens:  4,
			AudioTokens:  3,
		},
		InputTokensDetails: &InputTokenDetails{
			CachedTokens:         5,
			CachedCreationTokens: 7,
			TextTokens:           2,
		},
	}).CanonicalUsage()
	require.True(t, ok)
	assert.Equal(t, 8, usage.PromptTokensDetails.CachedTokens)
	assert.Equal(t, 12, usage.PromptTokensDetails.TextTokens)
	assert.Equal(t, 4, usage.PromptTokensDetails.ImageTokens)
	assert.Equal(t, 3, usage.PromptTokensDetails.AudioTokens)
	assert.Equal(t, 7, usage.PromptTokensDetails.CachedCreationTokens)
}

func TestBillingUsageJSONUsesProtocolNamedFields(t *testing.T) {
	billingUsage := &BillingUsage{
		OpenAIUsage:         &Usage{PromptTokens: 1, BillingUsage: NewClaudeMessagesBillingUsage(&ClaudeUsage{InputTokens: 9})},
		ClaudeUsage:         &ClaudeUsage{InputTokens: 2, BillingUsage: NewOpenAIChatBillingUsage(&Usage{PromptTokens: 8})},
		GeminiUsageMetadata: &GeminiUsageMetadata{PromptTokenCount: 3, BillingUsage: NewOpenAIChatBillingUsage(&Usage{PromptTokens: 7})},
	}

	data, err := kitutil.Marshal(billingUsage)
	require.NoError(t, err)

	assert.Contains(t, string(data), `"openai_usage"`)
	assert.Contains(t, string(data), `"claude_usage"`)
	assert.Contains(t, string(data), `"gemini_usage_metadata"`)
	assert.NotContains(t, string(data), `"usage":`)
	assert.NotContains(t, string(data), `"usage_metadata"`)

	clone := CloneBillingUsage(billingUsage)
	require.NotNil(t, clone.OpenAIUsage)
	require.NotNil(t, clone.ClaudeUsage)
	require.NotNil(t, clone.GeminiUsageMetadata)
	assert.Nil(t, clone.OpenAIUsage.BillingUsage)
	assert.Nil(t, clone.ClaudeUsage.BillingUsage)
	assert.Nil(t, clone.GeminiUsageMetadata.BillingUsage)
}
