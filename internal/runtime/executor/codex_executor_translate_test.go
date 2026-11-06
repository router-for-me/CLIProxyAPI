package executor

import (
	"bytes"
	"sync/atomic"
	"testing"

	"github.com/MoeForge/moego/ojson"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestTranslateCodexRequestPairReusesEqualPayload(t *testing.T) {
	from := sdktranslator.Format("codex-test-from-equal")
	to := sdktranslator.Format("codex-test-to-equal")
	var calls int32
	sdktranslator.Register(from, to, func(model string, rawJSON []byte, stream bool) ([]byte, error) {
		atomic.AddInt32(&calls, 1)
		if model != "test-model" {
			t.Errorf("model = %q, want test-model", model)
		}
		if !stream {
			t.Error("stream = false, want true")
		}
		return append([]byte(nil), rawJSON...), nil
	}, sdktranslator.ResponseTransform{})

	payload := []byte(`{"model":"test-model","input":[{"role":"user"}]}`)
	originalTranslated, body := translateCodexRequestPair(from, to, "test-model", payload, bytes.Clone(payload), true)

	if gotCalls := atomic.LoadInt32(&calls); gotCalls != 1 {
		t.Fatalf("TranslateRequest calls = %d, want 1", gotCalls)
	}
	if !bytes.Equal(originalTranslated, body) {
		t.Fatalf("translated payloads differ: original=%s body=%s", originalTranslated, body)
	}
}

func TestTranslateCodexRequestPairTranslatesDifferentPayloads(t *testing.T) {
	from := sdktranslator.Format("codex-test-from-different")
	to := sdktranslator.Format("codex-test-to-different")
	var calls int32
	sdktranslator.Register(from, to, func(_ string, rawJSON []byte, _ bool) ([]byte, error) {
		atomic.AddInt32(&calls, 1)
		return append([]byte(nil), rawJSON...), nil
	}, sdktranslator.ResponseTransform{})

	originalPayload := []byte(`{"model":"test-model","input":[{"role":"system"}]}`)
	payload := []byte(`{"model":"test-model","input":[{"role":"user"}]}`)
	originalTranslated, body := translateCodexRequestPair(from, to, "test-model", originalPayload, payload, false)

	if gotCalls := atomic.LoadInt32(&calls); gotCalls != 2 {
		t.Fatalf("TranslateRequest calls = %d, want 2", gotCalls)
	}
	if !bytes.Equal(originalTranslated, originalPayload) {
		t.Fatalf("original translated = %s, want %s", originalTranslated, originalPayload)
	}
	if !bytes.Equal(body, payload) {
		t.Fatalf("body = %s, want %s", body, payload)
	}
}

func TestClampCodexMaxOutputTokensDoc(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    string
	}{
		{"claude code probe", `{"model":"m","max_output_tokens":1}`, "16"},
		{"zero", `{"model":"m","max_output_tokens":0}`, "16"},
		{"just below minimum", `{"model":"m","max_output_tokens":15}`, "16"},
		{"at minimum", `{"model":"m","max_output_tokens":16}`, "16"},
		{"above minimum", `{"model":"m","max_output_tokens":4096}`, "4096"},
		{"absent", `{"model":"m"}`, ""},
		{"null", `{"model":"m","max_output_tokens":null}`, "null"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := ojson.NewDocString(tt.payload)
			clampCodexMaxOutputTokensDoc(doc)
			if got := doc.Get("max_output_tokens").Raw; got != tt.want {
				t.Fatalf("max_output_tokens = %q, want %q; body=%s", got, tt.want, doc.Bytes())
			}
		})
	}
}

func TestClampCodexMaxOutputTokensDocClaudeProbe(t *testing.T) {
	payload := []byte(`{"model":"gpt-5.6-sol","max_tokens":1,"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]}]}`)
	_, body := translateCodexRequestPair(sdktranslator.FormatClaude, sdktranslator.FormatCodex, "gpt-5.6-sol", payload, bytes.Clone(payload), false)
	if got := ojson.GetBytesNoCopy(body, "max_output_tokens").Raw; got != "1" {
		t.Fatalf("translated max_output_tokens = %q, want 1; body=%s", got, body)
	}
	doc := ojson.NewDoc(body)
	clampCodexMaxOutputTokensDoc(doc)
	if got := doc.Get("max_output_tokens").Raw; got != "16" {
		t.Fatalf("max_output_tokens = %q, want 16; body=%s", got, doc.Bytes())
	}
}
