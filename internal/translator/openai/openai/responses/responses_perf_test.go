package responses

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func responsesPerfRequest(turns int) []byte {
	items := []string{`{"role":"user","content":"start"}`}
	for i := 0; i < turns; i++ {
		items = append(items,
			fmt.Sprintf(`{"type":"function_call","call_id":"c%d","namespace":"editor","name":"read","arguments":"{}"}`, i),
			fmt.Sprintf(`{"type":"function_call_output","call_id":"c%d","output":"%s"}`, i, strings.Repeat("x", 8192)))
	}
	return []byte(fmt.Sprintf(`{"model":"test","instructions":"%s","tools":[{"type":"namespace","name":"editor","tools":[{"type":"function","name":"read","parameters":{"type":"object"}}]}],"input":[%s]}`, strings.Repeat("x", 65536), strings.Join(items, ",")))
}

func BenchmarkResponsesLongHistory(b *testing.B) {
	for _, turns := range []int{10, 100} {
		b.Run(fmt.Sprint(turns), func(b *testing.B) {
			request := responsesPerfRequest(turns)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				ConvertOpenAIResponsesRequestToOpenAIChatCompletions("test", request, true)
			}
		})
	}
}

func BenchmarkResponsesStreamLongRequest(b *testing.B) {
	request := responsesPerfRequest(100)
	chunk := []byte(`data: {"id":"chatcmpl-test","created":1,"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"x"}}]}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var param any
		for j := 0; j < 256; j++ {
			ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "test", request, request, chunk, &param)
		}
		ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "test", request, request, []byte("data: [DONE]"), &param)
	}
}
