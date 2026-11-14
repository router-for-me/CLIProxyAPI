package helps

import (
	"bytes"
	"testing"

	"github.com/tidwall/gjson"
)

func TestNormalizeOpenAICompletionContent(t *testing.T) {
	for _, field := range []string{"message", "delta"} {
		t.Run(field, func(t *testing.T) {
			payload := []byte(`{"id":"chat-test","created":9007199254740993,"choices":[{"index":0,"` + field + `":{"role":"assistant","reasoning_content":"Earlier. ","content":[{"type":"thinking","thinking":[{"type":"text","text":"Inspect. "},{"type":"text","text":"Compare."}],"closed":true},{"type":"text","text":"purple "},{"type":"text","text":"triangle"}],"tool_calls":[{"index":0,"id":"call-test","type":"function","function":{"name":"lookup","arguments":"{\"id\":9007199254740993}"}}]},"finish_reason":"tool_calls"},{"index":1,"` + field + `":{"content":[{"type":"text","text":"second choice"}]},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":8,"total_tokens":28,"prompt_tokens_details":{"cached_tokens":5}}}`)
			got := NormalizeOpenAICompletionContent(payload)
			path := "choices.0." + field
			if text := gjson.GetBytes(got, path+".content"); text.Type != gjson.String || text.String() != "purple triangle" {
				t.Fatalf("answer = %s", text.Raw)
			}
			if reasoning := gjson.GetBytes(got, path+".reasoning_content").String(); reasoning != "Earlier. Inspect. Compare." {
				t.Fatalf("reasoning = %q", reasoning)
			}
			if text := gjson.GetBytes(got, "choices.1."+field+".content").String(); text != "second choice" {
				t.Fatalf("second choice = %q", text)
			}
			for _, preserved := range []string{"id", "created", "choices.0.index", path + ".role", path + ".tool_calls", "choices.0.finish_reason", "usage"} {
				if gjson.GetBytes(payload, preserved).Raw != gjson.GetBytes(got, preserved).Raw {
					t.Errorf("changed %s: %s", preserved, got)
				}
			}
			if twice := NormalizeOpenAICompletionContent(got); !bytes.Equal(got, twice) {
				t.Fatal("normalization is not idempotent")
			}
		})
	}
}

func TestNormalizeOpenAICompletionContentStreamTransition(t *testing.T) {
	frames := []string{
		`{"choices":[{"delta":{"content":[{"type":"thinking","thinking":[{"type":"text","text":"Inspect."}],"closed":false}]}}]}`,
		`{"choices":[{"delta":{"content":[{"type":"thinking","thinking":[],"closed":true},{"type":"text","text":"purple "}]}}]}`,
		`{"choices":[{"delta":{"content":"triangle"}}]}`,
	}
	var text, reasoning string
	for _, frame := range frames {
		got := NormalizeOpenAICompletionContent([]byte(frame))
		text += gjson.GetBytes(got, "choices.0.delta.content").String()
		reasoning += gjson.GetBytes(got, "choices.0.delta.reasoning_content").String()
	}
	if text != "purple triangle" || reasoning != "Inspect." {
		t.Fatalf("text=%q reasoning=%q", text, reasoning)
	}
}

func TestNormalizeOpenAICompletionContentPreservesOtherPayloads(t *testing.T) {
	for _, payload := range []string{
		`{"choices":[{"delta":{"content":"ordinary"}}]}`,
		`{"choices":[{"message":{"content":null}}]}`,
		`{"choices":[{"delta":{"content":[]}}]}`,
		`{"choices":[],"usage":{"prompt_tokens":10}}`,
		`{"error":{"message":"quota exhausted"}}`,
		`[DONE]`,
		`{"choices":`,
		`{"choices":[{"delta":{"content":[{"type":"text","text":"keep me"},{"type":"image_url","image_url":{"url":"https://example.com/image.png"}}]}}]}`,
		`{"choices":[{"delta":{"content":[{"type":"thinking","thinking":"unsupported"}]}}]}`,
		`{"choices":[{"delta":{"content":[{"type":"text","text":null}]}}]}`,
		`{"choices":[{"delta":{"content":[{"type":"thinking","thinking":[{"type":"image"}]}]}}]}`,
		`{"choices":[{"delta":{"reasoning_content":[],"content":[{"type":"thinking","thinking":[{"type":"text","text":"keep me"}]}]}}]}`,
	} {
		if got := NormalizeOpenAICompletionContent([]byte(payload)); string(got) != payload {
			t.Errorf("changed unsupported payload: %s", got)
		}
	}
}

func TestNormalizeOpenAICompletionContentExistingReasoning(t *testing.T) {
	payload := []byte(`{"choices":[{"message":{"reasoning":"Earlier. ","reasoning_content":null,"content":[{"type":"thinking","thinking":[{"type":"text","text":"Inspect."}]},{"type":"text","text":"triangle"}]}}]}`)
	got := NormalizeOpenAICompletionContent(payload)
	if reasoning := gjson.GetBytes(got, "choices.0.message.reasoning_content").String(); reasoning != "Earlier. Inspect." {
		t.Fatalf("reasoning = %q", reasoning)
	}
}
