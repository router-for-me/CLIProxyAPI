package relayconvert

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	sharedclaude "github.com/QuantumNous/new-api/relaykit/relayconvert/internal/shared/claude"
	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/reasoning"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClaudeDefaultMaxTokensPresence(t *testing.T) {
	converters := []struct {
		name    string
		convert func(t *testing.T, meta convmeta.Meta, clientMaxTokens *uint) (*dto.ClaudeRequest, error)
	}{
		{
			name: "chat completions",
			convert: func(t *testing.T, meta convmeta.Meta, clientMaxTokens *uint) (*dto.ClaudeRequest, error) {
				t.Helper()
				return OpenAIChatRequestToClaudeMessages(context.Background(), meta, dto.GeneralOpenAIRequest{
					Model:     "claude-test",
					MaxTokens: clientMaxTokens,
					Messages: []dto.Message{
						{Role: "user", Content: "hello"},
					},
				})
			},
		},
		{
			name: "responses",
			convert: func(t *testing.T, meta convmeta.Meta, clientMaxTokens *uint) (*dto.ClaudeRequest, error) {
				t.Helper()
				return OpenAIResponsesRequestToClaudeMessages(context.Background(), meta, &dto.OpenAIResponsesRequest{
					Model:           "claude-test",
					Input:           []byte(`"hello"`),
					MaxOutputTokens: clientMaxTokens,
				})
			},
		},
	}

	for _, converter := range converters {
		t.Run(converter.name, func(t *testing.T) {
			t.Run("callback absent fails conversion", func(t *testing.T) {
				got, err := converter.convert(t, &convmeta.Values{}, nil)
				require.ErrorIs(t, err, sharedclaude.ErrMissingMaxTokens)
				assert.Nil(t, got)
			})

			t.Run("callback absent, client value wins", func(t *testing.T) {
				clientMaxTokens := uint(99)
				got, err := converter.convert(t, &convmeta.Values{}, &clientMaxTokens)
				require.NoError(t, err)
				require.NotNil(t, got.MaxTokens)
				assert.Equal(t, clientMaxTokens, *got.MaxTokens)
			})

			t.Run("configured zero", func(t *testing.T) {
				got, err := converter.convert(t, claudeDefaultsMeta(func(string) int { return 0 }), nil)
				require.NoError(t, err)
				require.NotNil(t, got.MaxTokens)
				assert.Zero(t, *got.MaxTokens)
			})

			t.Run("configured positive", func(t *testing.T) {
				got, err := converter.convert(t, claudeDefaultsMeta(func(string) int { return 512 }), nil)
				require.NoError(t, err)
				require.NotNil(t, got.MaxTokens)
				assert.Equal(t, uint(512), *got.MaxTokens)
			})

			t.Run("client nonzero wins", func(t *testing.T) {
				clientMaxTokens := uint(99)
				got, err := converter.convert(t, claudeDefaultsMeta(func(string) int { return 512 }), &clientMaxTokens)
				require.NoError(t, err)
				require.NotNil(t, got.MaxTokens)
				assert.Equal(t, clientMaxTokens, *got.MaxTokens)
			})

			t.Run("client zero same as absent, hook fills", func(t *testing.T) {
				clientMaxTokens := uint(0)
				got, err := converter.convert(t, claudeDefaultsMeta(func(string) int { return 512 }), &clientMaxTokens)
				require.NoError(t, err)
				require.NotNil(t, got.MaxTokens)
				assert.Equal(t, uint(512), *got.MaxTokens)
			})

			t.Run("client zero same as absent, no hook fails", func(t *testing.T) {
				clientMaxTokens := uint(0)
				got, err := converter.convert(t, &convmeta.Values{}, &clientMaxTokens)
				require.ErrorIs(t, err, sharedclaude.ErrMissingMaxTokens)
				assert.Nil(t, got)
			})
		})
	}
}

// The thinking adapter's max_tokens floor is an injection path of its own: a
// "-thinking" request without max_tokens must keep converting even when no
// DefaultMaxTokens hook is configured.
func TestClaudeThinkingAdapterSatisfiesMaxTokensWithoutCallback(t *testing.T) {
	_, intent, found, err := reasoning.ParseClaudeModelSuffix("claude-test-thinking", true)
	require.NoError(t, err)
	require.True(t, found)
	meta := &convmeta.Values{
		ReasoningConversion: reasoning.StateFromIntent(intent),
		Options: &convmeta.Options{
			Claude: convmeta.ClaudeOptions{
				ThinkingAdapterEnabled:                true,
				ThinkingAdapterBudgetTokensPercentage: 0.8,
			},
		},
	}
	got, err := OpenAIChatRequestToClaudeMessages(context.Background(), meta, dto.GeneralOpenAIRequest{
		Model: "claude-test-thinking",
		Messages: []dto.Message{
			{Role: "user", Content: "hello"},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, got.MaxTokens)
	assert.Equal(t, uint(1280), *got.MaxTokens)
}

func TestOpenAIChatRequestToClaudeMessagesOmitsEmptyTools(t *testing.T) {
	maxTokens := uint(16)
	tests := []struct {
		name      string
		request   dto.GeneralOpenAIRequest
		wantTools bool
	}{
		{
			name: "omitted tools",
			request: dto.GeneralOpenAIRequest{
				Model:     "claude-test",
				MaxTokens: &maxTokens,
				Messages:  []dto.Message{{Role: "user", Content: "hi"}},
			},
		},
		{
			name: "explicit empty tools",
			request: dto.GeneralOpenAIRequest{
				Model:     "claude-test",
				MaxTokens: &maxTokens,
				Messages:  []dto.Message{{Role: "user", Content: "hi"}},
				Tools:     []dto.ToolCallRequest{},
			},
		},
		{
			name: "function tool",
			request: dto.GeneralOpenAIRequest{
				Model:     "claude-test",
				MaxTokens: &maxTokens,
				Messages:  []dto.Message{{Role: "user", Content: "hi"}},
				Tools: []dto.ToolCallRequest{{
					Type: "function",
					Function: dto.FunctionRequest{
						Name:        "get_weather",
						Description: "Get weather by city",
						Parameters: map[string]any{
							"type":       "object",
							"properties": map[string]any{"city": map[string]any{"type": "string"}},
							"required":   []any{"city"},
						},
					},
				}},
			},
			wantTools: true,
		},
		{
			name: "web search only",
			request: dto.GeneralOpenAIRequest{
				Model:            "claude-test",
				MaxTokens:        &maxTokens,
				Messages:         []dto.Message{{Role: "user", Content: "hi"}},
				WebSearchOptions: &dto.WebSearchOptions{SearchContextSize: "low"},
			},
			wantTools: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := OpenAIChatRequestToClaudeMessages(context.Background(), &convmeta.Values{}, test.request)
			require.NoError(t, err)

			body, err := kitutil.Marshal(got)
			require.NoError(t, err)

			if test.wantTools {
				assert.NotNil(t, got.Tools)
				assert.Contains(t, string(body), `"tools":`)
				return
			}
			assert.Nil(t, got.Tools)
			assert.NotContains(t, string(body), `"tools":`)
		})
	}
}

func claudeDefaultsMeta(defaultMaxTokens func(string) int) convmeta.Meta {
	return &convmeta.Values{Options: &convmeta.Options{
		Claude: convmeta.ClaudeOptions{DefaultMaxTokens: defaultMaxTokens},
	}}
}

func TestClaudeManualThinkingBoundsAreCoerced(t *testing.T) {
	// OpenAI requests to a manual-thinking Claude model used to be rejected with
	// 400 when budget_tokens or max_tokens fell outside Anthropic's bounds. The
	// renderer now adjusts them and reports what changed.
	tests := []struct {
		name          string
		reasoning     string
		maxTokens     uint
		wantBudget    int
		wantMaxTokens uint
		wantCode      string
	}{
		{name: "budget below 1024 is raised", reasoning: `{"max_tokens":512}`, maxTokens: 4096, wantBudget: 1024, wantMaxTokens: 4096, wantCode: "claude_budget_adjusted"},
		{name: "budget at max_tokens is lowered", reasoning: `{"max_tokens":4096}`, maxTokens: 4096, wantBudget: 4095, wantMaxTokens: 4096, wantCode: "claude_budget_adjusted"},
		{name: "max_tokens too small for manual thinking is raised", reasoning: `{"max_tokens":2048}`, maxTokens: 1000, wantBudget: 2048, wantMaxTokens: 2049, wantCode: "claude_max_tokens_raised"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			maxTokens := tt.maxTokens
			request := dto.GeneralOpenAIRequest{
				Model:     "claude-3-7-sonnet",
				MaxTokens: &maxTokens,
				Reasoning: []byte(tt.reasoning),
				Messages:  []dto.Message{{Role: "user", Content: "hello"}},
			}
			result, err := ConvertRequest(context.Background(), &convmeta.Values{}, types.RelayFormatClaude, &request)
			require.NoError(t, err)
			converted, ok := result.Value.(*dto.ClaudeRequest)
			require.True(t, ok)
			require.NotNil(t, converted.Thinking)
			require.NotNil(t, converted.Thinking.BudgetTokens)
			assert.Equal(t, tt.wantBudget, *converted.Thinking.BudgetTokens)
			require.NotNil(t, converted.MaxTokens)
			assert.Equal(t, tt.wantMaxTokens, *converted.MaxTokens)
			codes := make([]string, 0, len(result.Diagnostics))
			for _, diagnostic := range result.Diagnostics {
				codes = append(codes, diagnostic.Code)
			}
			assert.Contains(t, codes, tt.wantCode)
		})
	}
}
