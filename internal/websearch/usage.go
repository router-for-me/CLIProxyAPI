package websearch

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// usageTotals accumulates token usage across fallback loop iterations so
// the client-visible response accounts for every upstream call.
type usageTotals struct {
	input  int64
	output int64
}

func (t *usageTotals) add(input, output int64) {
	t.input += input
	t.output += output
}

func (t usageTotals) empty() bool {
	return t.input == 0 && t.output == 0
}

// parseUsage extracts input/output token counts from a response body.
func parseUsage(format string, body []byte) (input, output int64) {
	switch normalizeFallbackFormat(format) {
	case FallbackFormatClaude:
		return gjson.GetBytes(body, "usage.input_tokens").Int(),
			gjson.GetBytes(body, "usage.output_tokens").Int()
	case FallbackFormatOpenAI:
		return gjson.GetBytes(body, "usage.prompt_tokens").Int(),
			gjson.GetBytes(body, "usage.completion_tokens").Int()
	case FallbackFormatOpenAIResponse, FallbackFormatCodex:
		return gjson.GetBytes(body, "usage.input_tokens").Int(),
			gjson.GetBytes(body, "usage.output_tokens").Int()
	case FallbackFormatGemini:
		return gjson.GetBytes(body, "usageMetadata.promptTokenCount").Int(),
			gjson.GetBytes(body, "usageMetadata.candidatesTokenCount").Int()
	default:
		return 0, 0
	}
}

// withUsage rewrites the response usage block with loop totals. Per-call
// detail objects are dropped because they cannot be meaningfully summed.
func withUsage(format string, body []byte, totals usageTotals) []byte {
	if totals.empty() {
		return body
	}
	out := body
	switch normalizeFallbackFormat(format) {
	case FallbackFormatClaude:
		out, _ = sjson.SetBytes(out, "usage.input_tokens", totals.input)
		out, _ = sjson.SetBytes(out, "usage.output_tokens", totals.output)
	case FallbackFormatOpenAI:
		out, _ = sjson.SetBytes(out, "usage.prompt_tokens", totals.input)
		out, _ = sjson.SetBytes(out, "usage.completion_tokens", totals.output)
		if gjson.GetBytes(out, "usage.total_tokens").Exists() {
			out, _ = sjson.SetBytes(out, "usage.total_tokens", totals.input+totals.output)
		}
		out, _ = sjson.DeleteBytes(out, "usage.prompt_tokens_details")
		out, _ = sjson.DeleteBytes(out, "usage.completion_tokens_details")
	case FallbackFormatOpenAIResponse, FallbackFormatCodex:
		out, _ = sjson.SetBytes(out, "usage.input_tokens", totals.input)
		out, _ = sjson.SetBytes(out, "usage.output_tokens", totals.output)
		if gjson.GetBytes(out, "usage.total_tokens").Exists() {
			out, _ = sjson.SetBytes(out, "usage.total_tokens", totals.input+totals.output)
		}
		out, _ = sjson.DeleteBytes(out, "usage.input_tokens_details")
		out, _ = sjson.DeleteBytes(out, "usage.output_tokens_details")
	case FallbackFormatGemini:
		out, _ = sjson.SetBytes(out, "usageMetadata.promptTokenCount", totals.input)
		out, _ = sjson.SetBytes(out, "usageMetadata.candidatesTokenCount", totals.output)
		if gjson.GetBytes(out, "usageMetadata.totalTokenCount").Exists() {
			out, _ = sjson.SetBytes(out, "usageMetadata.totalTokenCount", totals.input+totals.output)
		}
		out, _ = sjson.DeleteBytes(out, "usageMetadata.promptTokensDetails")
		out, _ = sjson.DeleteBytes(out, "usageMetadata.candidatesTokensDetails")
	}
	return out
}
