package claude

import (
	"context"
	"strings"
	"testing"
)

func TestAntigravityClaudeRequestMetadata(t *testing.T) {
	for _, preinitialized := range []bool{false, true} {
		for _, original := range []string{`{}`, `{"tools":[{"type":"web_search_20250305","name":"web_search"}]}`} {
			for _, translated := range []string{`{}`, `{"request":{"tools":[{"googleSearch":{}}]},"model":"gemini-2.5-pro"}`} {
				var state any
				if preinitialized {
					state = &Params{}
				}
				chunk := []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"hello"}]}}]}}`)
				for i := 0; i < 3; i++ {
					ConvertAntigravityResponseToClaude(context.Background(), "", []byte(original), []byte(translated), chunk, &state)
					p := state.(*Params)
					if !p.RequestMetadataResolved || p.WebSearchStreamMode != shouldTranslateWebSearchGrounding([]byte(original), []byte(translated)) {
						t.Fatalf("unexpected metadata: %+v", p)
					}
					wantModel := ""
					if translated != "{}" {
						wantModel = "gemini-2.5-pro"
					}
					if p.ModelName != wantModel {
						t.Fatalf("model=%q want=%q", p.ModelName, wantModel)
					}
				}
				ConvertAntigravityResponseToClaude(context.Background(), "", []byte(original), []byte(translated), []byte("[DONE]"), &state)
			}
		}
	}
}

func BenchmarkAntigravityClaudeLongRequest(b *testing.B) {
	history := strings.Repeat("context ", 81920)
	original := []byte(`{"messages":[{"role":"user","content":"` + history + `"}],"tools":[{"type":"web_search_20250305","name":"web_search"}]}`)
	translated := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"` + history + `"}]}],"tools":[{"googleSearch":{}}]},"model":"gemini-2.5-pro"}`)
	chunk := []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"hello"}]}}]}}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var state any
		for j := 0; j < 128; j++ {
			ConvertAntigravityResponseToClaude(context.Background(), "", original, translated, chunk, &state)
		}
		ConvertAntigravityResponseToClaude(context.Background(), "", original, translated, []byte("[DONE]"), &state)
	}
}
