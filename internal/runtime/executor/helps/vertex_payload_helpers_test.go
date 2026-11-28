package helps

import (
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestStripVertexToolCallIDsReusesPayloadWithoutIDs(t *testing.T) {
	input := []byte(`{"contents":[{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{"id":9007199254740993}}}]}]}`)
	for _, format := range []string{"openai-response", "openai"} {
		output := StripVertexOpenAIResponsesToolCallIDs(input, format)
		if &output[0] != &input[0] {
			t.Fatalf("payload without tool call IDs was copied for format %q", format)
		}
	}
}

func TestStripVertexToolCallIDsRebuildsContentsOnce(t *testing.T) {
	input := []byte(`{"contents":[{"role":"model","parts":[{"functionCall":{"id":"call_1","name":"lookup","args":{"id":9007199254740993}}}]},{"role":"user","parts":[{"functionResponse":{"id":"call_1","name":"lookup","response":{"id":"keep"}}}]}]}`)
	for _, format := range []string{"openai-response", "openai", " OpenAI "} {
		t.Run(format, func(t *testing.T) {
			output := StripVertexOpenAIResponsesToolCallIDs(input, format)
			if ojson.GetBytes(output, "contents.0.parts.0.functionCall.id").Exists() {
				t.Fatal("functionCall.id was not removed")
			}
			if ojson.GetBytes(output, "contents.1.parts.0.functionResponse.id").Exists() {
				t.Fatal("functionResponse.id was not removed")
			}
			if got := ojson.GetBytes(output, "contents.1.parts.0.functionResponse.response.id").String(); got != "keep" {
				t.Fatalf("nested response id = %q, want keep", got)
			}
			if got := ojson.GetBytes(output, "contents.0.parts.0.functionCall.args.id").Raw; got != "9007199254740993" {
				t.Fatalf("large integer = %s, want exact original value", got)
			}
		})
	}
	t.Run("preserves_non_openai_source_format", func(t *testing.T) {
		output := StripVertexOpenAIResponsesToolCallIDs(input, "gemini")
		if &output[0] != &input[0] {
			t.Fatal("non-OpenAI payload should be reused unchanged")
		}
		if got := ojson.GetBytes(output, "contents.0.parts.0.functionCall.id").String(); got != "call_1" {
			t.Fatalf("functionCall.id = %q, want call_1", got)
		}
	})
}

var benchmarkVertexPayloadOutput []byte

func BenchmarkStripVertexToolCallIDsLargeNoopPayload(b *testing.B) {
	input := []byte(`{"contents":[{"role":"user","parts":[{"text":"` + strings.Repeat("x", 8<<20) + `"}]}]}`)
	b.ReportAllocs()
	b.SetBytes(int64(len(input)))
	b.ResetTimer()
	for b.Loop() {
		benchmarkVertexPayloadOutput = StripVertexOpenAIResponsesToolCallIDs(input, "openai-response")
	}
}
