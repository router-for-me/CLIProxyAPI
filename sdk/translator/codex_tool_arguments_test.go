package translator

import (
	"bytes"
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

// The canonicalizer runs on every Responses reply, so the unchanged case is
// measured too. On an M5 Max it costs roughly 450ns and 700B per document,
// which is negligible next to the upstream round trip; the scanner itself
// allocates nothing, the remainder is gjson/sjson bookkeeping.
func BenchmarkCanonicalizeNoop(b *testing.B) {
	body := []byte(`{"output":[{"type":"reasoning","summary":[]},{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"ls\",\"yield_time_ms\":2500}"}]}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := CanonicalizeCodexToolArguments(body, false); !bytes.Equal(got, body) {
			b.Fatalf("document changed: %s", got)
		}
	}
}

func BenchmarkCanonicalizeRewrite(b *testing.B) {
	body := []byte(`{"output":[{"type":"function_call","name":"exec_command","arguments":"{\"yield_time_ms\":2500.0,\"ratio\":0.5}"}]}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := CanonicalizeCodexToolArguments(body, false); bytes.Equal(got, body) {
			b.Fatal("expected the document to be rewritten")
		}
	}
}

func TestCanonicalizeIntegralFloatsRewritesOnlyIntegralFractions(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"integral fraction", `{"a":2500.0}`, `{"a":2500}`},
		{"integral fraction with padding", `{"a":-3.000}`, `{"a":-3}`},
		{"zero", `{"a":0.0}`, `{"a":0}`},
		{"negative zero", `{"a":-0.0}`, `{"a":-0}`},
		{"genuine float untouched", `{"a":1.5}`, `{"a":1.5}`},
		{"trailing non-zero untouched", `{"a":2500.001}`, `{"a":2500.001}`},
		{"exponent untouched", `{"a":1e3}`, `{"a":1e3}`},
		{"exponent after integral fraction untouched", `{"a":1.0e3}`, `{"a":1.0e3}`},
		{"signed exponent untouched", `{"a":2500.0E+2}`, `{"a":2500.0E+2}`},
		{"string value untouched", `{"a":"2500.0"}`, `{"a":"2500.0"}`},
		{"escaped quote before number", `{"a":"say \"hi\"","b":2500.0}`, `{"a":"say \"hi\"","b":2500}`},
		{"escaped backslash before number", `{"a":"x\\","b":2500.0}`, `{"a":"x\\","b":2500}`},
		{"multiple numbers", `{"a":2500.0,"b":1.5,"c":3000.00}`, `{"a":2500,"b":1.5,"c":3000}`},
		{"nested object", `{"a":{"b":[2500.0,{"c":2500.0}]}}`, `{"a":{"b":[2500,{"c":2500}]}}`},
		{"incomplete fraction untouched", `{"a":2500.}`, `{"a":2500.}`},
		{"no numbers", `{"a":"b"}`, `{"a":"b"}`},
		{"empty object", `{}`, `{}`},
		{"top level array", `[2500.0,1.5]`, `[2500,1.5]`},
		{"whitespace preserved", "{\"a\" : 2500.0 , \"b\" : 1 }", "{\"a\" : 2500 , \"b\" : 1 }"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := canonicalizeIntegralFloats([]byte(tc.in))
			if string(got) != tc.want {
				t.Fatalf("canonicalizeIntegralFloats(%s) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}

func TestCanonicalizeIntegralFloatsReturnsInputWhenUnchanged(t *testing.T) {
	src := []byte(`{"a":1.5,"b":"2500.0"}`)
	if got := canonicalizeIntegralFloats(src); !bytes.Equal(got, src) {
		t.Fatalf("canonicalizeIntegralFloats changed an untouched document: %s", got)
	}
}

func TestCanonicalizeIntegralFloatsDoesNotMutateInput(t *testing.T) {
	src := []byte(`{"a":2500.0}`)
	original := string(src)
	_ = canonicalizeIntegralFloats(src)
	if string(src) != original {
		t.Fatalf("input mutated: %s", src)
	}
}

func TestCanonicalizeIntegralFloatsIsIdempotent(t *testing.T) {
	once := canonicalizeIntegralFloats([]byte(`{"a":2500.0,"b":2500}`))
	twice := canonicalizeIntegralFloats(once)
	if !bytes.Equal(once, twice) {
		t.Fatalf("not idempotent: %s then %s", once, twice)
	}
}

func TestCanonicalizeCodexToolArgumentsStream(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "output item done",
			in:   `{"type":"response.output_item.done","item":{"type":"function_call","name":"exec_command","arguments":"{\"yield_time_ms\":2500.0}"}}`,
			want: `{"type":"response.output_item.done","item":{"type":"function_call","name":"exec_command","arguments":"{\"yield_time_ms\":2500}"}}`,
		},
		{
			name: "output item added",
			in:   `{"type":"response.output_item.added","item":{"type":"function_call","name":"exec_command","arguments":"{\"yield_time_ms\":2500.0}"}}`,
			want: `{"type":"response.output_item.added","item":{"type":"function_call","name":"exec_command","arguments":"{\"yield_time_ms\":2500}"}}`,
		},
		{
			name: "argument delta is not rewritten",
			in:   `{"type":"response.function_call_arguments.delta","delta":"{\"yield_time_ms\":2500.0}"}`,
			want: `{"type":"response.function_call_arguments.delta","delta":"{\"yield_time_ms\":2500.0}"}`,
		},
		{
			name: "message item untouched",
			in:   `{"type":"response.output_item.done","item":{"type":"message","content":[{"type":"output_text","text":"2500.0"}]}}`,
			want: `{"type":"response.output_item.done","item":{"type":"message","content":[{"type":"output_text","text":"2500.0"}]}}`,
		},
		{
			name: "custom tool call input",
			in:   `{"type":"response.output_item.done","item":{"type":"custom_tool_call","name":"apply_patch","input":"{\"count\":2500.0}"}}`,
			want: `{"type":"response.output_item.done","item":{"type":"custom_tool_call","name":"apply_patch","input":"{\"count\":2500}"}}`,
		},
		{
			name: "custom tool call untouched without integral float",
			in:   `{"type":"response.output_item.done","item":{"type":"custom_tool_call","name":"apply_patch","input":"{\"ratio\":0.5}"}}`,
			want: `{"type":"response.output_item.done","item":{"type":"custom_tool_call","name":"apply_patch","input":"{\"ratio\":0.5}"}}`,
		},
		{
			name: "custom input event is not rewritten",
			in:   `{"type":"response.custom_tool_call_input.done","input":"{\"count\":2500.0}"}`,
			want: `{"type":"response.custom_tool_call_input.done","input":"{\"count\":2500.0}"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CanonicalizeCodexToolArguments([]byte(tc.in), true)
			if string(got) != tc.want {
				t.Fatalf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestCanonicalizeCodexToolArgumentsNonStream(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "function call item",
			in:   `{"id":"resp_1","output":[{"type":"function_call","name":"exec_command","arguments":"{\"yield_time_ms\":2500.0}","call_id":"call_1"}]}`,
			want: `{"id":"resp_1","output":[{"type":"function_call","name":"exec_command","arguments":"{\"yield_time_ms\":2500}","call_id":"call_1"}]}`,
		},
		{
			name: "several items only function calls change",
			in:   `{"output":[{"type":"message","content":[]},{"type":"function_call","arguments":"{\"a\":2500.0}"},{"type":"function_call","arguments":"{\"b\":1.5}"}]}`,
			want: `{"output":[{"type":"message","content":[]},{"type":"function_call","arguments":"{\"a\":2500}"},{"type":"function_call","arguments":"{\"b\":1.5}"}]}`,
		},
		{
			name: "inline object arguments",
			in:   `{"output":[{"type":"function_call","arguments":{"yield_time_ms":2500.0}}]}`,
			want: `{"output":[{"type":"function_call","arguments":{"yield_time_ms":2500}}]}`,
		},
		{
			name: "custom tool call input",
			in:   `{"output":[{"type":"custom_tool_call","name":"apply_patch","input":"{\"count\":2500.0}"}]}`,
			want: `{"output":[{"type":"custom_tool_call","name":"apply_patch","input":"{\"count\":2500}"}]}`,
		},
		{
			name: "custom tool call inline object input",
			in:   `{"output":[{"type":"custom_tool_call","name":"apply_patch","input":{"count":2500.0}}]}`,
			want: `{"output":[{"type":"custom_tool_call","name":"apply_patch","input":{"count":2500}}]}`,
		},
		{
			name: "no output array",
			in:   `{"id":"resp_1"}`,
			want: `{"id":"resp_1"}`,
		},
		{
			name: "output not an array",
			in:   `{"output":"2500.0"}`,
			want: `{"output":"2500.0"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CanonicalizeCodexToolArguments([]byte(tc.in), false)
			if string(got) != tc.want {
				t.Fatalf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestCanonicalizeCodexToolArgumentsHandlesEmptyInput(t *testing.T) {
	if got := CanonicalizeCodexToolArguments(nil, false); got != nil {
		t.Fatalf("expected nil, got %s", got)
	}
	if got := CanonicalizeCodexToolArguments(nil, true); got != nil {
		t.Fatalf("expected nil, got %s", got)
	}
}

// codexArgumentResponse is the shape a translator emits for a Codex client.
const (
	codexNonStreamResponse = `{"output":[{"type":"function_call","name":"exec_command","arguments":"{\"yield_time_ms\":2500.0}"}]}`
	codexStreamResponse    = `{"type":"response.output_item.done","item":{"type":"function_call","name":"exec_command","arguments":"{\"yield_time_ms\":2500.0}"}}`
)

func registerCodexArgumentResponders(r *Registry, to Format) {
	r.Register(to, Format("upstream"), nil, ResponseTransform{
		NonStream: func(context.Context, string, []byte, []byte, []byte, *any) []byte {
			return []byte(codexNonStreamResponse)
		},
		Stream: func(context.Context, string, []byte, []byte, []byte, *any) [][]byte {
			return [][]byte{[]byte(codexStreamResponse)}
		},
	})
}

func TestRegistryCanonicalizesResponsesTargets(t *testing.T) {
	ctx := context.Background()
	// FormatOpenAIResponse is what the Codex client actually receives:
	// OpenAIResponsesAPIHandler serves both /v1/responses and
	// /backend-api/codex/responses and reports openai-response as its type.
	for _, format := range []Format{FormatOpenAIResponse, FormatCodex} {
		t.Run(format.String(), func(t *testing.T) {
			r := NewRegistry()
			registerCodexArgumentResponders(r, format)

			nonStream := r.TranslateNonStream(ctx, Format("upstream"), format, "model", nil, nil, []byte(`{}`), nil)
			if got := gjson.GetBytes(nonStream, `output.0.arguments`).String(); got != `{"yield_time_ms":2500}` {
				t.Fatalf("non-stream arguments = %s", got)
			}

			stream := r.TranslateStream(ctx, Format("upstream"), format, "model", nil, nil, []byte(`{}`), nil)
			if len(stream) != 1 {
				t.Fatalf("expected one event, got %d", len(stream))
			}
			if got := gjson.GetBytes(stream[0], "item.arguments").String(); got != `{"yield_time_ms":2500}` {
				t.Fatalf("stream arguments = %s", got)
			}
		})
	}
}

func TestRegistryLeavesNonCodexTargetsAlone(t *testing.T) {
	ctx := context.Background()
	r := NewRegistry()
	registerCodexArgumentResponders(r, FormatOpenAI)

	nonStream := r.TranslateNonStream(ctx, Format("upstream"), FormatOpenAI, "model", nil, nil, []byte(`{}`), nil)
	if !bytes.Equal(nonStream, []byte(codexNonStreamResponse)) {
		t.Fatalf("non-codex target was rewritten: %s", nonStream)
	}

	stream := r.TranslateStream(ctx, Format("upstream"), FormatOpenAI, "model", nil, nil, []byte(`{}`), nil)
	if len(stream) != 1 || !bytes.Equal(stream[0], []byte(codexStreamResponse)) {
		t.Fatalf("non-codex target stream was rewritten: %s", stream)
	}
}
