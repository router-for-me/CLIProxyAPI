package responses

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

// Compare full requests and SSE transcripts with the baseline (self-terminating SSE frames, issue #6161).
func TestResponsesCompatibilityDigest(t *testing.T) {
	requests := [][]byte{
		responsesPerfRequest(0), responsesPerfRequest(10), responsesPerfRequest(100),
		[]byte(`{"model":"test","tools":[{"type":"namespace","name":"editor","tools":[{"type":"custom","name":"patch"},{"type":"function","name":"read"}]}],"input":[]}`),
		[]byte(`{"model":"test","tools":[{"type":"function","name":"editor__patch"}],"input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"editor","tools":[{"type":"custom","name":"patch"}]}]}]}`),
		[]byte(fmt.Sprintf(`{"model":"test","tools":[{"type":"namespace","name":"%s","tools":[{"type":"function","name":"read"}]},{"type":"namespace","name":"other","tools":[{"type":"function","name":"read"}]}],"input":[]}`, strings.Repeat("namespace", 10))),
	}
	hash := sha256.New()
	ctx := context.Background()
	for _, request := range requests {
		body1, errConvert1 := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("test", request, true)
		if errConvert1 != nil {
			t.Fatal(errConvert1)
		}
		hash.Write(body1)
		for _, name := range []string{"editor__read", "read", "editor__patch", "patch", "unknown"} {
			var state any
			chunks := []string{
				`{"id":"r-test","created":1,"choices":[{"index":0,"delta":{"content":"hello"}}]}`,
				fmt.Sprintf(`{"id":"r-test","created":1,"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-test","function":{"name":%q,"arguments":""}}]}}]}`, name),
				`{"id":"r-test","created":1,"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"input\":\"hello\"}"}}]}}]}`,
				`{"id":"r-test","created":1,"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":100,"completion_tokens":10,"total_tokens":110}}`,
				`[DONE]`,
			}
			for _, chunk := range chunks {
				for _, event := range ConvertOpenAIChatCompletionsResponseToOpenAIResponses(ctx, "test", request, request, []byte("data: "+chunk), &state) {
					hash.Write(event)
				}
			}
		}
	}
	got := fmt.Sprintf("%x", hash.Sum(nil))
	const want = "be3fc19eade4e6aff373fdfdd0192d586b5aa83e01a6b22884456396443f8411"
	if got != want {
		t.Fatalf("compatibility digest: got %s, want %s", got, want)
	}
}
