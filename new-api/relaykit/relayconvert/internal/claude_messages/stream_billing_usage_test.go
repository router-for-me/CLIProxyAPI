package claudemessages

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMessageStartZeroOutputSidecarRemainsRefreshable(t *testing.T) {
	t.Parallel()

	info := &ClaudeResponseInfo{Usage: &dto.Usage{}}
	ok := FormatClaudeResponseInfo(&dto.ClaudeResponse{
		Type: "message_start",
		Message: &dto.ClaudeMediaMessage{
			Id:    "msg_1",
			Model: "claude-test",
			Usage: &dto.ClaudeUsage{
				InputTokens:  10,
				OutputTokens: 0,
				BillingUsage: dto.NewClaudeMessagesBillingUsage(&dto.ClaudeUsage{
					InputTokens:  10,
					OutputTokens: 0,
				}),
			},
		},
	}, nil, info)
	require.True(t, ok)

	ok = FormatClaudeResponseInfo(&dto.ClaudeResponse{
		Type: "message_delta",
		Usage: &dto.ClaudeUsage{
			OutputTokens: 42,
		},
	}, nil, info)
	require.True(t, ok)
	require.NotNil(t, info.Usage.BillingUsage)
	require.NotNil(t, info.Usage.BillingUsage.ClaudeUsage)
	assert.Equal(t, 42, info.Usage.BillingUsage.ClaudeUsage.OutputTokens)
}

func TestTerminalSidecarRemainsAuthoritativeAgainstFinalize(t *testing.T) {
	t.Parallel()

	info := &ClaudeResponseInfo{Usage: &dto.Usage{}}
	ok := FormatClaudeResponseInfo(&dto.ClaudeResponse{
		Type: "message_delta",
		Usage: &dto.ClaudeUsage{
			InputTokens:  10,
			OutputTokens: 7,
			BillingUsage: dto.NewClaudeMessagesBillingUsage(&dto.ClaudeUsage{
				InputTokens:  10,
				OutputTokens: 7,
			}),
		},
	}, nil, info)
	require.True(t, ok)
	require.NotNil(t, info.Usage.BillingUsage)
	require.NotNil(t, info.Usage.BillingUsage.ClaudeUsage)

	info.Usage.CompletionTokens = 99
	FinalizeClaudeStreamBillingUsage(info)
	assert.Equal(t, 7, info.Usage.BillingUsage.ClaudeUsage.OutputTokens)
}
