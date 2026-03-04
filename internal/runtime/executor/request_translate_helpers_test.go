package executor

import (
	"bytes"
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

func TestTranslateRequestPair_UsesSingleTranslationForSameOriginal(t *testing.T) {
	from := sdktranslator.FromString("openai")
	to := sdktranslator.FromString("codex")
	payload := []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`)

	originalPayload, originalTranslated, translated := translateRequestPair(from, to, "gpt-5", payload, nil, false)
	want := sdktranslator.TranslateRequest(from, to, "gpt-5", payload, false)

	if !bytes.Equal(originalPayload, payload) {
		t.Fatalf("original payload mismatch\n got: %s\nwant: %s", string(originalPayload), string(payload))
	}
	if !bytes.Equal(translated, want) {
		t.Fatalf("translated payload mismatch\n got: %s\nwant: %s", string(translated), string(want))
	}
	if !bytes.Equal(originalTranslated, want) {
		t.Fatalf("original translated payload mismatch\n got: %s\nwant: %s", string(originalTranslated), string(want))
	}
}

func TestTranslateRequestPair_TranslatesOriginalAndRuntimeSeparately(t *testing.T) {
	from := sdktranslator.FromString("openai")
	to := sdktranslator.FromString("codex")
	runtimePayload := []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"runtime"}]}`)
	originalPayloadInput := []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"original"}]}`)

	originalPayload, originalTranslated, translated := translateRequestPair(from, to, "gpt-5", runtimePayload, originalPayloadInput, false)

	wantRuntime := sdktranslator.TranslateRequest(from, to, "gpt-5", runtimePayload, false)
	wantOriginal := sdktranslator.TranslateRequest(from, to, "gpt-5", originalPayloadInput, false)

	if !bytes.Equal(originalPayload, originalPayloadInput) {
		t.Fatalf("original payload mismatch\n got: %s\nwant: %s", string(originalPayload), string(originalPayloadInput))
	}
	if !bytes.Equal(translated, wantRuntime) {
		t.Fatalf("runtime translated payload mismatch\n got: %s\nwant: %s", string(translated), string(wantRuntime))
	}
	if !bytes.Equal(originalTranslated, wantOriginal) {
		t.Fatalf("original translated payload mismatch\n got: %s\nwant: %s", string(originalTranslated), string(wantOriginal))
	}
}
