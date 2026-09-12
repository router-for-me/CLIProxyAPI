package responses

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertClaudeResponseToOpenAIResponsesNonStreamKeepsZeroUsageDefaults(t *testing.T) {
	input := []byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`)

	output := ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "", nil, nil, input, nil)

	for _, path := range []string{"usage.input_tokens", "usage.input_tokens_details.cached_tokens", "usage.output_tokens", "usage.total_tokens"} {
		value := gjson.GetBytes(output, path)
		if !value.Exists() || value.Int() != 0 {
			t.Fatalf("%s = %s, want zero", path, value.Raw)
		}
	}
}
