package helps

import (
	"bytes"
	"context"
	"testing"

	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

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
