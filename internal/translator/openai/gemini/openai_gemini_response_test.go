package gemini

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponseToGemini_MultiCandidateStream(t *testing.T) {
	in := []byte(`{
		"model":"gpt-4o",
		"choices":[
			{"index":0,"delta":{"content":"first"}},
			{"index":1,"delta":{"content":"second"}}
		]
	}`)

	var param any
	out := ConvertOpenAIResponseToGemini(context.Background(), "gpt-4o", nil, nil, in, &param)
	if len(out) != 2 {
		t.Fatalf("expected 2 chunks, got=%d", len(out))
	}

	first := gjson.Parse(out[0])
	if got := first.Get("candidates.0.content.parts.0.text").String(); got != "first" {
		t.Fatalf("first candidate text mismatch, got=%q output=%s", got, out[0])
	}

	second := gjson.Parse(out[1])
	if got := second.Get("candidates.1.content.parts.0.text").String(); got != "second" {
		t.Fatalf("second candidate text mismatch, got=%q output=%s", got, out[1])
	}
}

func TestConvertOpenAIResponseToGeminiNonStream_MultiCandidate(t *testing.T) {
	in := []byte(`{
		"model":"gpt-4o",
		"choices":[
			{"index":0,"message":{"role":"assistant","content":"first"},"finish_reason":"stop"},
			{"index":1,"message":{"role":"assistant","content":"second"},"finish_reason":"stop"}
		]
	}`)

	out := ConvertOpenAIResponseToGeminiNonStream(context.Background(), "gpt-4o", nil, nil, in, nil)
	root := gjson.Parse(out)

	if got := root.Get("candidates.0.content.parts.0.text").String(); got != "first" {
		t.Fatalf("first candidate text mismatch, got=%q output=%s", got, out)
	}
	if got := root.Get("candidates.1.content.parts.0.text").String(); got != "second" {
		t.Fatalf("second candidate text mismatch, got=%q output=%s", got, out)
	}
	if got := root.Get("candidates.0.index").Int(); got != 0 {
		t.Fatalf("first candidate index mismatch, got=%d output=%s", got, out)
	}
	if got := root.Get("candidates.1.index").Int(); got != 1 {
		t.Fatalf("second candidate index mismatch, got=%d output=%s", got, out)
	}
}
