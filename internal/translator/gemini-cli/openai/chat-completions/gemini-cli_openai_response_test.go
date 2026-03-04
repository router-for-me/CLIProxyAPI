package chat_completions

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertCliResponseToOpenAI_MapFinishReasonLength(t *testing.T) {
	in := []byte(`{
		"response":{
			"responseId":"resp_1",
			"modelVersion":"gemini-2.5-pro",
			"candidates":[
				{
					"content":{"parts":[{"text":"hello"}]},
					"finishReason":"MAX_TOKENS"
				}
			]
		}
	}`)

	var param any
	out := ConvertCliResponseToOpenAI(context.Background(), "gemini-2.5-pro", nil, nil, in, &param)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got=%d", len(out))
	}
	root := gjson.Parse(out[0])
	if got := root.Get("choices.0.finish_reason").String(); got != "length" {
		t.Fatalf("finish_reason should map to length, got=%q output=%s", got, out[0])
	}
	if got := root.Get("choices.0.native_finish_reason").String(); got != "max_tokens" {
		t.Fatalf("native_finish_reason mismatch, got=%q output=%s", got, out[0])
	}
}

func TestConvertCliResponseToOpenAI_MultiCandidate(t *testing.T) {
	in := []byte(`{
		"response":{
			"responseId":"resp_1",
			"modelVersion":"gemini-2.5-pro",
			"candidates":[
				{
					"content":{"parts":[{"text":"first"}]},
					"finishReason":"STOP"
				},
				{
					"content":{"parts":[{"text":"second"}]},
					"finishReason":"STOP"
				}
			]
		}
	}`)

	var param any
	out := ConvertCliResponseToOpenAI(context.Background(), "gemini-2.5-pro", nil, nil, in, &param)
	if len(out) != 2 {
		t.Fatalf("expected 2 chunks, got=%d", len(out))
	}

	first := gjson.Parse(out[0])
	if got := first.Get("choices.0.index").Int(); got != 0 {
		t.Fatalf("first candidate index mismatch, got=%d output=%s", got, out[0])
	}
	if got := first.Get("choices.0.delta.content").String(); got != "first" {
		t.Fatalf("first candidate content mismatch, got=%q output=%s", got, out[0])
	}

	second := gjson.Parse(out[1])
	if got := second.Get("choices.0.index").Int(); got != 1 {
		t.Fatalf("second candidate index mismatch, got=%d output=%s", got, out[1])
	}
	if got := second.Get("choices.0.delta.content").String(); got != "second" {
		t.Fatalf("second candidate content mismatch, got=%q output=%s", got, out[1])
	}
}
