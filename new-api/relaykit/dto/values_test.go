package dto

import (
	"testing"

	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntValueUnmarshalJSON(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		expected int
	}{
		{name: "integer", input: `1768488160`, expected: 1768488160},
		{name: "float", input: `1786588600.0`, expected: 1786588600},
		{name: "scientific", input: `1.76848816E9`, expected: 1768488160},
		{name: "string integer", input: `"42"`, expected: 42},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var v IntValue
			require.NoError(t, kitutil.UnmarshalJsonStr(tc.input, &v))
			assert.Equal(t, tc.expected, int(v))
		})
	}
}

func TestResponsesStreamResponseCreatedAtFloat(t *testing.T) {
	var resp ResponsesStreamResponse
	require.NoError(t, kitutil.UnmarshalJsonStr(`{"response":{"id":"resp_1","created_at":1786588600.0}}`, &resp))
	require.NotNil(t, resp.Response)
	assert.Equal(t, 1786588600, int(resp.Response.CreatedAt))

	encoded, err := kitutil.Marshal(resp.Response)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"created_at":1786588600`)
	assert.NotContains(t, string(encoded), `1786588600.0`)
}

func TestResponsesCompactionResponseCreatedAtFloat(t *testing.T) {
	var resp OpenAIResponsesCompactionResponse
	require.NoError(t, kitutil.UnmarshalJsonStr(`{"id":"resp_1","object":"response","created_at":1.76848816E9}`, &resp))
	assert.Equal(t, 1768488160, int(resp.CreatedAt))
}
