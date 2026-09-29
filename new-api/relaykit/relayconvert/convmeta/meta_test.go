package convmeta

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValuesTypedNilMetaIsSafe(t *testing.T) {
	var values *Values
	var meta Meta = values

	assert.Empty(t, meta.GetOriginModelName())
	assert.Empty(t, meta.GetUpstreamModelName())
	assert.False(t, meta.HasChannelMeta())
	assert.Zero(t, meta.GetChannelID())
	assert.Zero(t, meta.GetChannelType())
	assert.False(t, meta.GetIsStream())
	assert.Empty(t, meta.GetReasoningEffort())
	assert.Nil(t, meta.ReasoningState())
	assert.Zero(t, meta.GetEstimatePromptTokens())
	assert.Zero(t, meta.GetSendResponseCount())

	require.NotPanics(t, func() {
		meta.SetReasoningEffort("high")
		meta.IncrSendResponseCount()
		meta.AppendRequestConversion(types.RelayFormatClaude)
	})

	convertInfo := meta.EnsureClaudeConvertInfo()
	require.NotNil(t, convertInfo)
	assert.Equal(t, LastMessageTypeNone, convertInfo.LastMessagesType)
	require.NotNil(t, meta.ConvOptions())
	require.NotNil(t, OptionsOf(meta))
	assert.Empty(t, UpstreamModelName(meta))
	assert.Zero(t, ChannelTypeOf(meta))
}
