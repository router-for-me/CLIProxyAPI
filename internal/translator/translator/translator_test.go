package translator

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
)

func TestRequest(t *testing.T) {
	// OpenAI to OpenAI is usually a pass-through or simple transformation
	input := []byte(`{"model": "gpt-3.5-turbo", "messages": [{"role": "user", "content": "hello"}]}`)
	got := Request("openai", "openai", "gpt-4o", input, false)
	if string(got) == "" {
		t.Errorf("got empty result")
	}
}

func TestNeedConvert(t *testing.T) {
	if NeedConvert("openai", "openai") {
		t.Errorf("openai to openai should not need conversion by default")
	}
}

func TestResponse(t *testing.T) {
	ctx := context.Background()
	got := Response("openai", "openai", ctx, "gpt-4o", nil, nil, []byte(`{"id":"1"}`), nil)
	if len(got) == 0 {
		t.Errorf("got empty response")
	}
}

func TestRegister(t *testing.T) {
	from := "unit_from"
	to := "unit_to"

	Request(from, to, "model", []byte(`{}`), false)

	calls := 0
	Register(from, to, func(_ string, rawJSON []byte, _ bool) []byte {
		calls++
		return append(append([]byte(`{"wrapped":`), rawJSON...), '}')
	}, interfaces.TranslateResponse{
		Stream: func(_ context.Context, model string, _, _, rawJSON []byte, _ *any) []string {
			calls++
			return []string{string(rawJSON) + "::" + model}
		},
		NonStream: func(_ context.Context, model string, _, _, rawJSON []byte, _ *any) string {
			calls++
			return string(rawJSON) + "::" + model
		},
	})

	gotReq := Request(from, to, "gpt-4o", []byte(`{"v":1}`), true)
	if string(gotReq) != `{"wrapped":{"v":1}}` {
		t.Fatalf("got request %q", string(gotReq))
	}
	if !NeedConvert(from, to) {
		t.Fatalf("expected conversion path to be registered")
	}
	if calls == 0 {
		t.Fatalf("expected register callbacks to be invoked")
	}
}

func TestResponseNonStream(t *testing.T) {
	from := "unit_from_nonstream"
	to := "unit_to_nonstream"

	Register(from, to, nil, interfaces.TranslateResponse{
		NonStream: func(_ context.Context, model string, _, _, rawJSON []byte, _ *any) string {
			return string(rawJSON) + "::" + model + "::nonstream"
		},
	})

	got := ResponseNonStream(to, from, context.Background(), "model-1", nil, nil, []byte("payload"), nil)
	if got != `payload::model-1::nonstream` {
		t.Fatalf("got %q, want %q", got, `payload::model-1::nonstream`)
	}
}

func TestResponseNonStreamFallback(t *testing.T) {
	got := ResponseNonStream("missing_from", "missing_to", context.Background(), "model-2", nil, nil, []byte("payload"), nil)
	if got != "payload" {
		t.Fatalf("got %q, want raw payload", got)
	}
}
