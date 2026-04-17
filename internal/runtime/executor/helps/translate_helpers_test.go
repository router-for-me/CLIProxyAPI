package helps

import (
	"bytes"
	"testing"

	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

func TestTranslateRequestWithOriginal_SkipsOriginalWithoutPayloadRules(t *testing.T) {
	cfg := &config.Config{}
	from := sdktranslator.FromString("openai")
	to := sdktranslator.FromString("gemini")
	payload := []byte(`{"model":"gpt-4.1","messages":[{"role":"user","content":"hi"}]}`)

	translated, originalTranslated := TranslateRequestWithOriginal(cfg, from, to, "gpt-4.1", payload, payload, false)

	want := sdktranslator.TranslateRequest(from, to, "gpt-4.1", payload, false)
	if !bytes.Equal(translated, want) {
		t.Fatalf("translated payload mismatch\n got: %s\nwant: %s", translated, want)
	}
	if originalTranslated != nil {
		t.Fatalf("originalTranslated should be nil when payload rules are disabled")
	}
}

func TestTranslateRequestWithOriginal_ReusesTranslatedPayloadForIdenticalOriginal(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Default: []config.PayloadRule{{
				Models: []config.PayloadModelRule{{Name: "gpt-4.1"}},
				Params: map[string]any{"temperature": 0.2},
			}},
		},
	}
	from := sdktranslator.FromString("openai")
	to := sdktranslator.FromString("gemini")
	payload := []byte(`{"model":"gpt-4.1","messages":[{"role":"user","content":"hi"}]}`)

	translated, originalTranslated := TranslateRequestWithOriginal(cfg, from, to, "gpt-4.1", payload, payload, false)

	if !bytes.Equal(translated, originalTranslated) {
		t.Fatalf("originalTranslated should match translated payload when original matches request")
	}
	if len(translated) > 0 && len(originalTranslated) > 0 && &translated[0] == &originalTranslated[0] {
		t.Fatalf("originalTranslated should not alias translated payload")
	}
}

func TestTranslateRequestWithOriginal_TranslatesDistinctOriginal(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Default: []config.PayloadRule{{
				Models: []config.PayloadModelRule{{Name: "gpt-4.1"}},
				Params: map[string]any{"temperature": 0.2},
			}},
		},
	}
	from := sdktranslator.FromString("openai")
	to := sdktranslator.FromString("gemini")
	payload := []byte(`{"model":"gpt-4.1","messages":[{"role":"user","content":"hi"}]}`)
	original := []byte(`{"model":"gpt-4.1","temperature":0.5,"messages":[{"role":"user","content":"hi"}]}`)

	translated, originalTranslated := TranslateRequestWithOriginal(cfg, from, to, "gpt-4.1", payload, original, false)

	wantPayload := sdktranslator.TranslateRequest(from, to, "gpt-4.1", payload, false)
	wantOriginal := sdktranslator.TranslateRequest(from, to, "gpt-4.1", original, false)
	if !bytes.Equal(translated, wantPayload) {
		t.Fatalf("translated payload mismatch\n got: %s\nwant: %s", translated, wantPayload)
	}
	if !bytes.Equal(originalTranslated, wantOriginal) {
		t.Fatalf("originalTranslated mismatch\n got: %s\nwant: %s", originalTranslated, wantOriginal)
	}
}
