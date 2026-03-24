package translator

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestTranslateRequestFallbackNormalizesModel(t *testing.T) {
	reg := NewRegistry()
	raw := []byte(`{"model":"team/gpt-5.4","input":"hello"}`)

	out := reg.TranslateRequest(Format("openai"), Format("openai"), "gpt-5.4", raw, false)

	if got := gjson.GetBytes(out, "model").String(); got != "gpt-5.4" {
		t.Fatalf("model = %q, want %q", got, "gpt-5.4")
	}
}

func TestTranslateRequestFallbackPreservesRawPayloadWhenModelMatches(t *testing.T) {
	reg := NewRegistry()
	raw := []byte(`{"model":"gpt-5.4","input":"hello"}`)

	out := reg.TranslateRequest(Format("openai"), Format("openai"), "gpt-5.4", raw, false)

	if string(out) != string(raw) {
		t.Fatalf("TranslateRequest modified payload unexpectedly: got %s want %s", string(out), string(raw))
	}
}
