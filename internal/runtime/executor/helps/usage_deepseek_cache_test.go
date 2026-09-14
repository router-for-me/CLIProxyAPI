package helps

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestParseOpenAIUsageDeepSeekCache(t *testing.T) {
	// DeepSeek includes cache hits in prompt_tokens; they must not be added to it.
	// https://api-docs.deepseek.com/api/create-chat-completion/
	tests := []struct {
		name   string
		fields string
		cached int64
	}{
		{"mixed cache", `"prompt_cache_hit_tokens":80,"prompt_cache_miss_tokens":20`, 80},
		{"all cached", `"prompt_cache_hit_tokens":100,"prompt_cache_miss_tokens":0`, 100},
		{"cache miss", `"prompt_cache_hit_tokens":0,"prompt_cache_miss_tokens":100`, 0},
		{"absent cache", `"prompt_cache_miss_tokens":100`, 0},
		{"chat field wins", `"prompt_tokens_details":{"cached_tokens":30},"prompt_cache_hit_tokens":80`, 30},
		{"chat zero wins", `"prompt_tokens_details":{"cached_tokens":0},"prompt_cache_hit_tokens":80`, 0},
		{"responses field wins", `"input_tokens_details":{"cached_tokens":40},"prompt_cache_hit_tokens":80`, 40},
		{"responses zero wins", `"input_tokens_details":{"cached_tokens":0},"prompt_cache_hit_tokens":80`, 0},
	}
	for _, tt := range tests {
		for _, stream := range []bool{false, true} {
			mode := "response"
			if stream {
				mode = "stream"
			}
			t.Run(tt.name+"/"+mode, func(t *testing.T) {
				payload := []byte(`{"usage":{"prompt_tokens":100,"completion_tokens":10,"total_tokens":110,` + tt.fields + `}}`)
				var detail usage.Detail
				if stream {
					var buffer StreamUsageBuffer
					buffer.ObserveOpenAIStream([]byte(`data: {"choices":[{"delta":{"content":"hello"}}]}`))
					buffer.ObserveOpenAIStream(append([]byte("data: "), payload...))
					buffer.ObserveOpenAIStream([]byte("data: [DONE]"))
					var ok bool
					detail, ok = buffer.Detail()
					if !ok {
						t.Fatal("final usage was not retained")
					}
				} else {
					detail = ParseOpenAIUsage(payload)
				}
				if detail.CachedTokens != tt.cached || detail.CacheReadTokens != tt.cached {
					t.Fatalf("cache counters = (%d, %d), want (%d, %d)", detail.CachedTokens, detail.CacheReadTokens, tt.cached, tt.cached)
				}
				if detail.InputTokens != 100 || detail.OutputTokens != 10 || detail.TotalTokens != 110 {
					t.Fatalf("cache hits changed authoritative totals: %+v", detail)
				}
				b := detail.TokenBreakdown
				if !b.Valid() || b.Quality != usage.TokenAccountingQualityComplete || b.Input.CacheReadTokens != tt.cached || b.Input.UncachedTokens != 100-tt.cached || b.TotalTokens != 110 {
					t.Fatalf("incorrect cache allocation: %+v", b)
				}
			})
		}
	}
}
