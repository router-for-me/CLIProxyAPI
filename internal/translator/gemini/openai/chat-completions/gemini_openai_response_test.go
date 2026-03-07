package chat_completions

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertGeminiResponseToOpenAI_StreamUsesCandidateFinishReason(t *testing.T) {
	raw := []byte(`{
		"responseId":"r1",
		"modelVersion":"m1",
		"candidates":[
			{"index":0,"finishReason":"STOP","content":{"parts":[{"text":"a"}]}},
			{"index":1,"finishReason":"MAX_TOKENS","content":{"parts":[{"text":"b"}]}}
		]
	}`)

	var param any
	out := ConvertGeminiResponseToOpenAI(context.Background(), "", nil, nil, raw, &param)
	if len(out) != 2 {
		t.Fatalf("unexpected chunk count: got=%d", len(out))
	}

	c0 := gjson.Parse(out[0])
	c1 := gjson.Parse(out[1])

	if c0.Get("choices.0.index").Int() != 0 || c0.Get("choices.0.finish_reason").String() != "stop" {
		t.Fatalf("candidate0 finish_reason mismatch: %s", out[0])
	}
	if c1.Get("choices.0.index").Int() != 1 || c1.Get("choices.0.finish_reason").String() != "length" {
		t.Fatalf("candidate1 finish_reason mismatch: %s", out[1])
	}
}

func TestConvertGeminiResponseToOpenAI_NonStreamMapMaxTokensToLength(t *testing.T) {
	raw := []byte(`{
		"responseId":"r1",
		"modelVersion":"m1",
		"candidates":[{"index":0,"finishReason":"MAX_TOKENS","content":{"parts":[{"text":"hello"}]}}]
	}`)

	out := ConvertGeminiResponseToOpenAINonStream(context.Background(), "", nil, nil, raw, nil)
	root := gjson.Parse(out)

	if got := root.Get("choices.0.finish_reason").String(); got != "length" {
		t.Fatalf("finish_reason should map to length, got=%q output=%s", got, out)
	}
	if got := root.Get("choices.0.native_finish_reason").String(); got != "max_tokens" {
		t.Fatalf("native_finish_reason mismatch: got=%q output=%s", got, out)
	}
}
