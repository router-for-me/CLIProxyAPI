package helps

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestStripVertexToolCallIDsReusesPayloadWithoutIDs(t *testing.T) {
	input := []byte(`{"contents":[{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{"id":9007199254740993}}}]}]}`)
	output := StripVertexOpenAIResponsesToolCallIDs(input, "openai-response")
	if &output[0] != &input[0] {
		t.Fatal("payload without tool call IDs was copied")
	}
}

func TestStripVertexToolCallIDsRebuildsContentsOnce(t *testing.T) {
	input := []byte(`{"contents":[{"role":"model","parts":[{"functionCall":{"id":"call_1","name":"lookup","args":{"id":9007199254740993}}}]},{"role":"user","parts":[{"functionResponse":{"id":"call_1","name":"lookup","response":{"id":"keep"}}}]}]}`)
	output := StripVertexOpenAIResponsesToolCallIDs(input, "openai-response")
	if gjson.GetBytes(output, "contents.0.parts.0.functionCall.id").Exists() {
		t.Fatal("functionCall.id was not removed")
	}
	if gjson.GetBytes(output, "contents.1.parts.0.functionResponse.id").Exists() {
		t.Fatal("functionResponse.id was not removed")
	}
	if got := gjson.GetBytes(output, "contents.1.parts.0.functionResponse.response.id").String(); got != "keep" {
		t.Fatalf("nested response id = %q, want keep", got)
	}
	if got := gjson.GetBytes(output, "contents.0.parts.0.functionCall.args.id").Raw; got != "9007199254740993" {
		t.Fatalf("large integer = %s, want exact original value", got)
	}
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
