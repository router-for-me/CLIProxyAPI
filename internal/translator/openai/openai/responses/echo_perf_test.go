package responses

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func echoChunk(content string, extra string) []byte {
	return []byte(`data: {"id":"chatcmpl-1","created":1700000000,"object":"chat.completion.chunk","choices":[{"index":0,"delta":{` + extra + `}}]}`)
}

func runEchoStream(request []byte) []byte {
	chunks := [][]byte{
		echoChunk("", `"role":"assistant","content":""`),
		echoChunk("", `"reasoning_content":"think"`),
		echoChunk("", `"content":"Hel"`),
		echoChunk("", `"content":"lo"`),
		[]byte(`data: {"id":"chatcmpl-1","created":1700000000,"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"editor__read","arguments":"{\"path\":\"/tmp/x\"}"}}]}}]}`),
		[]byte(`data: {"id":"chatcmpl-1","created":1700000000,"object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":11,"completion_tokens":7}}`),
		[]byte(`data: [DONE]`),
	}
	var param any
	var out []byte
	for _, chunk := range chunks {
		out = append(out, bytes.Join(ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "test", request, request, chunk, &param), nil)...)
	}
	return out
}

func echoRequests() []struct {
	name string
	raw  []byte
} {
	history := strings.Repeat("context ", 4096)
	tools := `[{"type":"namespace","name":"editor","tools":[{"type":"function","name":"read","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}]}]`
	return []struct {
		name string
		raw  []byte
	}{
		{"nil", nil},
		{"empty-object", []byte(`{}`)},
		{"malformed", []byte(`{`)},
		{"full", []byte(`{"model":"gpt-5","instructions":"` + history + `","max_output_tokens":4096,"max_tool_calls":3,"parallel_tool_calls":false,"previous_response_id":"resp_prev","prompt_cache_key":"k1","reasoning":{"effort":"high","summary":"auto"},"safety_identifier":"user-1","service_tier":"priority","store":true,"temperature":1,"text":{"format":{"type":"json_schema","name":"x"}},"tool_choice":"auto","tools":` + tools + `,"top_logprobs":5,"top_p":0.5,"truncation":"auto","user":"u1","metadata":{"b":2,"a":1},"input":[{"role":"user","content":"hi"}]}`)},
		{"weird-types", []byte(`{"model":42,"instructions":null,"temperature":"hot","store":"yes","parallel_tool_calls":1,"max_output_tokens":"999","top_p":2,"tools":"none","metadata":[1,2],"user":{"id":7},"input":[]}`)},
		{"duplicate-keys", []byte(`{"model":"first","input":[],"model":"second","temperature":0.1,"temperature":0.9}`)},
		{"escaped-key", []byte("{\"mod\\u0065l\":\"esc-model\",\"input\":[],\"tools\":" + tools + "}")},
		{"max-tokens-only", []byte(`{"model":"gpt-5","max_tokens":777,"input":[]}`)},
	}
}

// Golden hashes captured from the pre-optimization implementation; the
// single-pass echo extraction must keep stream and non-stream outputs
// byte-identical.
var echoGoldenHashes = map[string][2]string{
	"nil":             {"12e174a662e5387285c18447eacd8f21802f391689b43a855718bd3a7962a8d2", "1c76155d2c138f38f71aeb380c3bfc873486725a0a3da29d1484461831b729cc"},
	"empty-object":    {"12e174a662e5387285c18447eacd8f21802f391689b43a855718bd3a7962a8d2", "1c76155d2c138f38f71aeb380c3bfc873486725a0a3da29d1484461831b729cc"},
	"malformed":       {"12e174a662e5387285c18447eacd8f21802f391689b43a855718bd3a7962a8d2", "1c76155d2c138f38f71aeb380c3bfc873486725a0a3da29d1484461831b729cc"},
	"full":            {"211b3a1fca607e01a1414e8cdefd2356369c5881284c09148ba2ac22df6b469f", "d9f230169b59ea6c2ab91910f9161656139cde3c44df55dee7c615798f5a687b"},
	"weird-types":     {"b09dc247fa502d2dab743a6a8eb0f09e0c99bf4da1b231f1db2843d25a93deee", "42ec2fafcf76c77924dafdfabef14d7df28da2166f4a50c20f1be5217a5ca907"},
	"duplicate-keys":  {"a446996117b660ec33d1f59b669295486de62d2b273f9bd9d7932f32a01fbbcf", "b65bee2aeb837e91af32ceecc962e92d06db59a1184461c4bd53fe337a892a4f"},
	"escaped-key":     {"c1183fc24b5774533d05e66db7c43725ab4757de1df267b858afd401f6fe3004", "290d5be1207223112672ee6d9cfe7abc4d6b6d9f03a3b05c46ff83a2e63d9fd2"},
	"max-tokens-only": {"6326707a3383eb6227783564451eb7efa40f0c2fa10fec8dc99f3ac24390f3f3", "aed55068cbdb234de859841ffcdbd87098938f4c59d62d03b12d510f4fca109c"},
}

func hashHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestResponsesEchoGoldenOutputs(t *testing.T) {
	for _, tc := range echoRequests() {
		want := echoGoldenHashes[tc.name]
		first := runEchoStream(tc.raw)
		if got := hashHex(first); got != want[0] {
			t.Errorf("%s stream: hash %s, want %s", tc.name, got, want[0])
		}
		resp := []byte(`{"id":"chatcmpl-9","created":1700000000,"model":"resp-model","choices":[{"message":{"role":"assistant","content":"done","reasoning_content":"why","tool_calls":[{"id":"call_9","type":"function","function":{"name":"editor__read","arguments":"{\"path\":\"/tmp/y\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":4}}`)
		var p1 any
		n1 := ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(context.Background(), "test", tc.raw, tc.raw, resp, &p1)
		if got := hashHex(n1); got != want[1] {
			t.Errorf("%s nonstream: hash %s, want %s", tc.name, got, want[1])
		}
	}
}

func BenchmarkResponsesEchoShortStreams(b *testing.B) {
	request := responsesPerfRequest(100)
	chunk := echoChunk("", `"content":"x"`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var param any
		ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "test", request, request, chunk, &param)
		ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "test", request, request, []byte("data: [DONE]"), &param)
	}
}

// Golden hashes for root-shape and reasoning-inclusion edge cases, also
// captured from the pre-optimization implementation.
func TestResponsesEchoGoldenEdgeCases(t *testing.T) {
	for name, req := range map[string][]byte{
		"array-root":  []byte(`[{"model":"x"}]`),
		"scalar-root": []byte(`"hello"`),
		"number-root": []byte(`42`),
	} {
		if got := hashHex(runEchoStream(req)); got != "12e174a662e5387285c18447eacd8f21802f391689b43a855718bd3a7962a8d2" {
			t.Errorf("%s stream: hash %s", name, got)
		}
		resp := []byte(`{"id":"c1","created":1700000000,"model":"m","choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
		var p any
		if got := hashHex(ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(context.Background(), "test", req, req, resp, &p)); got != "b934f12c0c7cda0dc4d68ce8a849c70627249fd9f21f3a08754cad2d775b40c4" {
			t.Errorf("%s nonstream: hash %s", name, got)
		}
	}
	respNoRC := []byte(`{"id":"c2","created":1700000000,"model":"m","choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	want := map[string]string{
		"req-with-reasoning":    "f6685c62154ff44ea0f90a1b5f1ee216dc27ddd20cae0d978f321b427c9727e6",
		"req-without-reasoning": "7465142d0300dcf9add250456693b76fbef1f1bb001aa57b7db47cc63ddc024d",
		"req-reasoning-null":    "1b4b449220acfdc810fa81e264e8559939f9b9ac79fa1097cd4ca09741665fde",
	}
	for name, req := range map[string][]byte{
		"req-with-reasoning":    []byte(`{"model":"gpt-5","reasoning":{"effort":"low"},"input":[]}`),
		"req-without-reasoning": []byte(`{"model":"gpt-5","input":[]}`),
		"req-reasoning-null":    []byte(`{"model":"gpt-5","reasoning":null,"input":[]}`),
	} {
		var p any
		if got := hashHex(ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(context.Background(), "test", req, req, respNoRC, &p)); got != want[name] {
			t.Errorf("%s nonstream: hash %s, want %s", name, got, want[name])
		}
	}
}
