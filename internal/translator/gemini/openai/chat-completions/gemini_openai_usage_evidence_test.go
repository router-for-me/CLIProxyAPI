package chat_completions

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestGeminiUsageEvidence(t *testing.T) {
	tests := []struct {
		name  string
		usage string
		cache string
	}{
		{"hit", `{"promptTokenCount":100,"candidatesTokenCount":5,"cachedContentTokenCount":80,"trafficType":"ON_DEMAND_FLEX"}`, "80"},
		{"zero", `{"promptTokenCount":100,"candidatesTokenCount":5,"cachedContentTokenCount":0,"trafficType":"ON_DEMAND"}`, "0"},
		{"missing", `{"promptTokenCount":100,"candidatesTokenCount":5}`, ""},
		{"null", `{"promptTokenCount":100,"candidatesTokenCount":5,"cachedContentTokenCount":null}`, ""},
		{"studio", `{"promptTokenCount":100,"candidatesTokenCount":5,"serviceTier":"flex"}`, ""},
		{"unknown-tier", `{"promptTokenCount":100,"candidatesTokenCount":5,"trafficType":"FUTURE_TIER"}`, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, stream := range []bool{false, true} {
				for _, candidates := range []string{"", `,"candidates":[{"index":0,"content":{"parts":[{"text":"hello"}]},"finishReason":"STOP"}]`} {
					raw := []byte(`{"usageMetadata":` + tc.usage + candidates + `}`)
					var param any
					var outputs [][]byte
					if stream {
						outputs = ConvertGeminiResponseToOpenAI(context.Background(), "gemini", nil, nil, raw, &param)
					} else {
						outputs = [][]byte{ConvertGeminiResponseToOpenAINonStream(context.Background(), "gemini", nil, nil, raw, &param)}
					}
					if len(outputs) != 1 {
						t.Fatalf("stream=%v: got %d output chunks", stream, len(outputs))
					}
					got := outputs[0]
					if gjson.GetBytes(got, "usageMetadata").Raw != tc.usage {
						t.Fatalf("stream=%v: upstream evidence changed: %s", stream, got)
					}
					if cache := gjson.GetBytes(got, "usage.prompt_tokens_details.cached_tokens"); cache.Raw != tc.cache {
						t.Fatalf("stream=%v: cache=%q, want %q", stream, cache.Raw, tc.cache)
					}
					if gjson.GetBytes(got, "usage.prompt_tokens").Int() != 100 || gjson.GetBytes(got, "usage.completion_tokens").Int() != 5 {
						t.Fatalf("stream=%v: existing token counts changed", stream)
					}
					if gjson.GetBytes(got, "service_tier").Exists() {
						t.Fatal("invented normalized service tier instead of preserving upstream evidence")
					}
				}
			}
		})
	}
}
