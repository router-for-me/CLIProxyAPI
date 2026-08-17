package executor

import (
	"bytes"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestTranslateCodexRequestPairReusesEqualPayload(t *testing.T) {
	from := sdktranslator.Format("codex-test-from-equal")
	to := sdktranslator.Format("codex-test-to-equal")
	var calls int32
	sdktranslator.Register(from, to, func(model string, rawJSON []byte, stream bool) []byte {
		atomic.AddInt32(&calls, 1)
		if model != "test-model" {
			t.Errorf("model = %q, want test-model", model)
		}
		if !stream {
			t.Error("stream = false, want true")
		}
		return append([]byte(nil), rawJSON...)
	}, sdktranslator.ResponseTransform{})

	payload := []byte(`{"model":"test-model","input":[{"role":"user"}]}`)
	originalTranslated, body, err := translateCodexRequestPair(from, to, "test-model", payload, bytes.Clone(payload), true)
	if err != nil {
		t.Fatalf("translateCodexRequestPair() error = %v", err)
	}

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
	sdktranslator.Register(from, to, func(_ string, rawJSON []byte, _ bool) []byte {
		atomic.AddInt32(&calls, 1)
		return append([]byte(nil), rawJSON...)
	}, sdktranslator.ResponseTransform{})

	originalPayload := []byte(`{"model":"test-model","input":[{"role":"system"}]}`)
	payload := []byte(`{"model":"test-model","input":[{"role":"user"}]}`)
	originalTranslated, body, err := translateCodexRequestPair(from, to, "test-model", originalPayload, payload, false)
	if err != nil {
		t.Fatalf("translateCodexRequestPair() error = %v", err)
	}

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

func TestTranslateCodexRequestPairRejectsLossyClaudePayload(t *testing.T) {
	tests := []struct {
		name     string
		original []byte
		payload  []byte
	}{
		{
			name:     "original payload",
			original: []byte(`{"messages":[{"role":"user","content":"hello"}],"stop_sequences":["END"]}`),
			payload:  []byte(`{"messages":[{"role":"user","content":"hello"}],"stop_sequences":["END"]}`),
		},
		{
			name:     "transformed payload",
			original: []byte(`{"messages":[{"role":"user","content":"hello"}]}`),
			payload:  []byte(`{"messages":[{"role":"user","content":[{"type":"audio","data":"ignored"}]}]}`),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := translateCodexRequestPair(sdktranslator.FormatClaude, sdktranslator.FormatCodex, "gpt-5.4", test.original, test.payload, true)
			if err == nil {
				t.Fatal("translateCodexRequestPair() error = nil, want rejection")
			}
			var requestScoped interface{ IsRequestScoped() bool }
			if !errors.As(err, &requestScoped) || !requestScoped.IsRequestScoped() {
				t.Fatalf("error %T is not request-scoped: %v", err, err)
			}
			var status interface{ StatusCode() int }
			if !errors.As(err, &status) || status.StatusCode() != http.StatusBadRequest {
				t.Fatalf("error status = %v, want %d", err, http.StatusBadRequest)
			}
		})
	}
}
