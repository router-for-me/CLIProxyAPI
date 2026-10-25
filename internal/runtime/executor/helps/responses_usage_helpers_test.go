package helps

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestParseUsageCounter(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want int64
		ok   bool
	}{
		{name: "zero", raw: `0`, ok: true},
		{name: "integer", raw: `5`, want: 5, ok: true},
		{name: "string integer", raw: `"5"`, want: 5, ok: true},
		{name: "whitespace", raw: `" 5 "`, want: 5, ok: true},
		{name: "positive sign", raw: `"+5"`, want: 5, ok: true},
		{name: "decimal integer", raw: `"5.0"`, want: 5, ok: true},
		{name: "scientific integer", raw: `"5e2"`, want: 500, ok: true},
		{name: "numeric scientific integer", raw: `5e2`, want: 500, ok: true},
		{name: "negative zero", raw: `"-0.0"`, ok: true},
		{name: "large exact integer", raw: `"9007199254740993"`, want: 9007199254740993, ok: true},
		{name: "max int64 string", raw: `"9223372036854775807"`, want: 9223372036854775807, ok: true},
		{name: "max int64 number", raw: `9223372036854775807`, want: 9223372036854775807, ok: true},
		{name: "max int64 decimal", raw: `"9223372036854775807.0"`, want: 9223372036854775807, ok: true},
		{name: "large exact decimal", raw: `"9007199254740993.0"`, want: 9007199254740993, ok: true},
		{name: "largest float below limit", raw: `"9223372036854774784.0"`, want: 9223372036854774784, ok: true},
		{name: "fraction", raw: `"5.9"`},
		{name: "numeric fraction", raw: `5.9`},
		{name: "scientific fraction", raw: `"5e-1"`},
		{name: "rounded fraction", raw: `"5.0000000000000001"`},
		{name: "large rounded fraction", raw: `"9007199254740992.5"`},
		{name: "underflow", raw: `"1e-400"`},
		{name: "negative underflow", raw: `"-1e-400"`},
		{name: "negative integer", raw: `"-5"`},
		{name: "negative number", raw: `-5`},
		{name: "negative float", raw: `"-5.0"`},
		{name: "nan", raw: `"NaN"`},
		{name: "positive infinity", raw: `"+Inf"`},
		{name: "negative infinity", raw: `"-Inf"`},
		{name: "infinity", raw: `"Infinity"`},
		{name: "large exponent", raw: `"1e100"`},
		{name: "numeric large exponent", raw: `1e100`},
		{name: "float overflow", raw: `"1e309"`},
		{name: "int64 overflow", raw: `"9223372036854775808"`},
		{name: "numeric int64 overflow", raw: `9223372036854775808`},
		{name: "float upper bound", raw: `"9.223372036854776e18"`},
		{name: "empty string", raw: `""`},
		{name: "whitespace only", raw: `" "`},
		{name: "text", raw: `"invalid"`},
		{name: "boolean", raw: `false`},
		{name: "null", raw: `null`},
		{name: "object", raw: `{}`},
		{name: "array", raw: `[]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseUsageCounter(gjson.Parse(tt.raw))
			if got != tt.want || ok != tt.ok {
				t.Fatalf("parseUsageCounter(%s) = (%d, %t), want (%d, %t)", tt.raw, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestEnsureResponsesUsageDetails_InvalidCounters(t *testing.T) {
	invalid := []string{`"5.9"`, `"5.0000000000000001"`, `"1e-400"`, `"NaN"`, `"+Inf"`, `"-Inf"`, `"1e100"`, `"9223372036854775808"`, `"-5"`}
	for _, raw := range invalid {
		t.Run(raw, func(t *testing.T) {
			for _, fallback := range []string{`7`, `"7.0"`, `"NaN"`, `5.9`, `-1`, `1e100`, `null`} {
				t.Run(fallback, func(t *testing.T) {
					payload := []byte(fmt.Sprintf(`{"usage":{"output_tokens_details":{"reasoning_tokens":%s},"input_tokens_details":{"cached_tokens":%s},"completion_tokens_details":{"reasoning_tokens":%s},"prompt_tokens_details":{"cached_tokens":%s}}}`, raw, raw, fallback, fallback))
					got := EnsureResponsesUsageDetails(payload)
					var want int64
					if fallback == `7` || fallback == `"7.0"` {
						want = 7
					}
					for _, path := range []string{"usage.output_tokens_details.reasoning_tokens", "usage.input_tokens_details.cached_tokens"} {
						if node := gjson.GetBytes(got, path); node.Type != gjson.Number || node.Int() != want {
							t.Fatalf("%s = %s, want number %d (payload: %s)", path, node.Raw, want, got)
						}
					}
				})
			}
		})
	}
}

func TestEnsureResponsesUsageDetails_PreservesNumericCounters(t *testing.T) {
	raw := []byte(`{"usage":{"output_tokens_details":{"reasoning_tokens":5.0},"input_tokens_details":{"cached_tokens":3e2}}}`)
	if got := EnsureResponsesUsageDetails(raw); !bytes.Equal(got, raw) {
		t.Fatalf("existing numeric counters changed: got %s, want %s", got, raw)
	}
}

func TestEnsureResponsesUsageDetails_NonStreamJSON(t *testing.T) {
	raw := []byte(`{"id":"resp_1","object":"response","status":"completed","usage":{"input_tokens":84,"output_tokens":16,"total_tokens":100}}`)
	got := EnsureResponsesUsageDetails(raw)

	if !gjson.GetBytes(got, "usage.output_tokens_details").Exists() {
		t.Fatalf("expected usage.output_tokens_details to exist, got %s", string(got))
	}
	if gjson.GetBytes(got, "usage.output_tokens_details.reasoning_tokens").Int() != 0 {
		t.Fatalf("expected usage.output_tokens_details.reasoning_tokens == 0, got %d", gjson.GetBytes(got, "usage.output_tokens_details.reasoning_tokens").Int())
	}
	if !gjson.GetBytes(got, "usage.input_tokens_details").Exists() {
		t.Fatalf("expected usage.input_tokens_details to exist, got %s", string(got))
	}
	if gjson.GetBytes(got, "usage.input_tokens_details.cached_tokens").Int() != 0 {
		t.Fatalf("expected usage.input_tokens_details.cached_tokens == 0, got %d", gjson.GetBytes(got, "usage.input_tokens_details.cached_tokens").Int())
	}
}

func TestEnsureResponsesUsageDetails_NonStreamJSONWithDataSubstring(t *testing.T) {
	raw := []byte(`{"id":"resp_1","object":"response","status":"completed","output":[{"type":"message","content":[{"type":"text","text":"data:image/png;base64,iVBORw0KGgoAAAANSUhEUg"}]}],"usage":{"input_tokens":84,"output_tokens":16,"total_tokens":100}}`)
	got := EnsureResponsesUsageDetails(raw)

	if !gjson.GetBytes(got, "usage.output_tokens_details").Exists() {
		t.Fatalf("expected usage.output_tokens_details to exist, got %s", string(got))
	}
	if gjson.GetBytes(got, "usage.output_tokens_details.reasoning_tokens").Int() != 0 {
		t.Fatalf("expected usage.output_tokens_details.reasoning_tokens == 0, got %d", gjson.GetBytes(got, "usage.output_tokens_details.reasoning_tokens").Int())
	}
	if !gjson.GetBytes(got, "usage.input_tokens_details").Exists() {
		t.Fatalf("expected usage.input_tokens_details to exist, got %s", string(got))
	}
	if gjson.GetBytes(got, "usage.input_tokens_details.cached_tokens").Int() != 0 {
		t.Fatalf("expected usage.input_tokens_details.cached_tokens == 0, got %d", gjson.GetBytes(got, "usage.input_tokens_details.cached_tokens").Int())
	}
}

func TestEnsureResponsesUsageDetails_SSEData(t *testing.T) {
	raw := []byte(`data: {"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":10,"output_tokens":4,"total_tokens":14}}}`)
	got := EnsureResponsesUsageDetails(raw)

	if !bytes.HasPrefix(got, []byte("data: ")) {
		t.Fatalf("expected data: prefix preserved, got %s", string(got))
	}
	jsonBody := bytes.TrimPrefix(got, []byte("data: "))
	if !gjson.GetBytes(jsonBody, "response.usage.output_tokens_details").Exists() {
		t.Fatalf("expected response.usage.output_tokens_details to exist, got %s", string(got))
	}
	if gjson.GetBytes(jsonBody, "response.usage.output_tokens_details.reasoning_tokens").Int() != 0 {
		t.Fatalf("expected reasoning_tokens == 0, got %d", gjson.GetBytes(jsonBody, "response.usage.output_tokens_details.reasoning_tokens").Int())
	}
	if !gjson.GetBytes(jsonBody, "response.usage.input_tokens_details").Exists() {
		t.Fatalf("expected response.usage.input_tokens_details to exist, got %s", string(got))
	}
	if gjson.GetBytes(jsonBody, "response.usage.input_tokens_details.cached_tokens").Int() != 0 {
		t.Fatalf("expected cached_tokens == 0, got %d", gjson.GetBytes(jsonBody, "response.usage.input_tokens_details.cached_tokens").Int())
	}
}

func TestEnsureResponsesUsageDetails_SSEEventDataMultiLine(t *testing.T) {
	raw := []byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"usage\":{\"input_tokens\":84,\"output_tokens\":16,\"total_tokens\":100}}}\n\n")
	got := EnsureResponsesUsageDetails(raw)

	if !bytes.HasPrefix(got, []byte("event: response.completed\n")) {
		t.Fatalf("expected event header preserved, got %s", string(got))
	}

	for _, line := range bytes.Split(got, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("data: ")) {
			jsonBody := bytes.TrimPrefix(line, []byte("data: "))
			if !gjson.GetBytes(jsonBody, "response.usage.output_tokens_details").Exists() {
				t.Fatalf("expected response.usage.output_tokens_details to exist in multi-line frame, got %s", string(got))
			}
			if gjson.GetBytes(jsonBody, "response.usage.output_tokens_details.reasoning_tokens").Int() != 0 {
				t.Fatalf("expected reasoning_tokens == 0, got %d", gjson.GetBytes(jsonBody, "response.usage.output_tokens_details.reasoning_tokens").Int())
			}
			if !gjson.GetBytes(jsonBody, "response.usage.input_tokens_details").Exists() {
				t.Fatalf("expected response.usage.input_tokens_details to exist in multi-line frame, got %s", string(got))
			}
			if gjson.GetBytes(jsonBody, "response.usage.input_tokens_details.cached_tokens").Int() != 0 {
				t.Fatalf("expected cached_tokens == 0, got %d", gjson.GetBytes(jsonBody, "response.usage.input_tokens_details.cached_tokens").Int())
			}
		}
	}
}

func TestEnsureResponsesUsageDetails_PreservesExistingDetails(t *testing.T) {
	raw := []byte(`data: {"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":3},"output_tokens":4,"output_tokens_details":{"reasoning_tokens":2},"total_tokens":14}}}`)
	got := EnsureResponsesUsageDetails(raw)

	jsonBody := bytes.TrimPrefix(got, []byte("data: "))
	if gjson.GetBytes(jsonBody, "response.usage.output_tokens_details.reasoning_tokens").Int() != 2 {
		t.Fatalf("expected reasoning_tokens == 2, got %d", gjson.GetBytes(jsonBody, "response.usage.output_tokens_details.reasoning_tokens").Int())
	}
	if gjson.GetBytes(jsonBody, "response.usage.input_tokens_details.cached_tokens").Int() != 3 {
		t.Fatalf("expected cached_tokens == 3, got %d", gjson.GetBytes(jsonBody, "response.usage.input_tokens_details.cached_tokens").Int())
	}
}

func TestEnsureResponsesUsageDetails_HandlesNullOrEmptyDetails(t *testing.T) {
	raw := []byte(`{"id":"resp_1","usage":{"input_tokens":10,"input_tokens_details":null,"output_tokens":4,"output_tokens_details":{},"total_tokens":14}}`)
	got := EnsureResponsesUsageDetails(raw)

	if gjson.GetBytes(got, "usage.output_tokens_details.reasoning_tokens").Int() != 0 {
		t.Fatalf("expected reasoning_tokens == 0, got %d", gjson.GetBytes(got, "usage.output_tokens_details.reasoning_tokens").Int())
	}
	if gjson.GetBytes(got, "usage.input_tokens_details.cached_tokens").Int() != 0 {
		t.Fatalf("expected cached_tokens == 0, got %d", gjson.GetBytes(got, "usage.input_tokens_details.cached_tokens").Int())
	}
}

func TestEnsureResponsesUsageDetails_CoercesNonNumericCounters(t *testing.T) {
	raw := []byte(`{"usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":"3"},"output_tokens":4,"output_tokens_details":{"reasoning_tokens":"5"},"total_tokens":14}}`)
	got := EnsureResponsesUsageDetails(raw)
	if node := gjson.GetBytes(got, "usage.output_tokens_details.reasoning_tokens"); node.Type != gjson.Number || node.Int() != 5 {
		t.Fatalf("reasoning_tokens = %s, want number 5", got)
	}
	if node := gjson.GetBytes(got, "usage.input_tokens_details.cached_tokens"); node.Type != gjson.Number || node.Int() != 3 {
		t.Fatalf("cached_tokens = %s, want number 3", got)
	}

	invalid := []byte(`{"usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":false},"output_tokens":4,"output_tokens_details":{"reasoning_tokens":null},"completion_tokens_details":{"reasoning_tokens":7},"total_tokens":14}}`)
	got = EnsureResponsesUsageDetails(invalid)
	if node := gjson.GetBytes(got, "usage.output_tokens_details.reasoning_tokens"); node.Type != gjson.Number || node.Int() != 7 {
		t.Fatalf("null reasoning_tokens should fall back to 7, got %s", got)
	}
	if node := gjson.GetBytes(got, "usage.input_tokens_details.cached_tokens"); node.Type != gjson.Number || node.Int() != 0 {
		t.Fatalf("false cached_tokens should become 0, got %s", got)
	}
}

func TestEnsureResponsesUsageDetails_NonJSONAndDone(t *testing.T) {
	cases := [][]byte{
		[]byte("data: [DONE]"),
		[]byte("[DONE]"),
		[]byte(": keepalive"),
		[]byte(""),
		[]byte(`{"type":"response.output_item.added"}`),
	}
	for _, c := range cases {
		got := EnsureResponsesUsageDetails(c)
		if !bytes.Equal(got, c) {
			t.Fatalf("expected unchanged for %q, got %q", string(c), string(got))
		}
	}
}

func TestTranslateStreamWithClaudeInputTokens_OpenAICompatTranslation_PatchesResponsesUsage(t *testing.T) {
	ctx := context.Background()
	reqBody := []byte(`{"model":"deepseek-v4-flash","input":"hi","stream":true}`)
	translatedReq := []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"stream":true,"stream_options":{"include_usage":true}}`)

	chunk1 := []byte(`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"},"finish_reason":null}]}`)
	chunk2 := []byte(`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":84,"completion_tokens":16,"total_tokens":100}}`)
	chunk3 := []byte(`data: [DONE]`)

	var param any
	_ = TranslateStreamWithClaudeInputTokens(
		ctx,
		sdktranslator.FormatOpenAI,
		sdktranslator.FormatOpenAIResponse,
		"deepseek-v4-flash",
		reqBody,
		translatedReq,
		chunk1,
		&param,
		nil,
	)
	chunks2 := TranslateStreamWithClaudeInputTokens(
		ctx,
		sdktranslator.FormatOpenAI,
		sdktranslator.FormatOpenAIResponse,
		"deepseek-v4-flash",
		reqBody,
		translatedReq,
		chunk2,
		&param,
		nil,
	)
	chunks3 := TranslateStreamWithClaudeInputTokens(
		ctx,
		sdktranslator.FormatOpenAI,
		sdktranslator.FormatOpenAIResponse,
		"deepseek-v4-flash",
		reqBody,
		translatedReq,
		chunk3,
		&param,
		nil,
	)

	allChunks := append(chunks2, chunks3...)
	foundCompleted := false
	for _, ch := range allChunks {
		for _, line := range bytes.Split(ch, []byte("\n")) {
			if bytes.HasPrefix(line, []byte("data: ")) {
				payload := bytes.TrimPrefix(line, []byte("data: "))
				if gjson.GetBytes(payload, "type").String() == "response.completed" {
					foundCompleted = true
					if !gjson.GetBytes(payload, "response.usage.output_tokens_details").Exists() {
						t.Fatalf("expected output_tokens_details to exist on translated response.completed: %s", string(ch))
					}
					if gjson.GetBytes(payload, "response.usage.output_tokens_details.reasoning_tokens").Int() != 0 {
						t.Fatalf("expected reasoning_tokens == 0, got %d", gjson.GetBytes(payload, "response.usage.output_tokens_details.reasoning_tokens").Int())
					}
					if !gjson.GetBytes(payload, "response.usage.input_tokens_details").Exists() {
						t.Fatalf("expected input_tokens_details to exist on translated response.completed: %s", string(ch))
					}
					if gjson.GetBytes(payload, "response.usage.input_tokens_details.cached_tokens").Int() != 0 {
						t.Fatalf("expected cached_tokens == 0, got %d", gjson.GetBytes(payload, "response.usage.input_tokens_details.cached_tokens").Int())
					}
				}
			}
		}
	}
	if !foundCompleted {
		t.Fatalf("did not find response.completed chunk in stream translation output")
	}
}
