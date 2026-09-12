package claude

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	"github.com/tidwall/gjson"
)

const capturedGeminiThinkingSignature = "EjQKMgEMOdbHO0Gd+c9Mxk4ELwPGbpCEcp2mFfYYLix2UVtBH3fL8GECc4+JITVnHF4qZDsA"

func TestConvertClaudeRequestToGeminiWithCompat_SignatureCompatibility(t *testing.T) {
	tests := []struct {
		name          string
		signature     string
		wantSignature string
	}{
		{
			name:          "preserves valid gemini signature",
			signature:     "gemini#" + capturedGeminiThinkingSignature,
			wantSignature: capturedGeminiThinkingSignature,
		},
		{
			name:          "foreign claude signature maps to bypass sentinel",
			signature:     "claude#opaque-signature-12345",
			wantSignature: signature.GeminiSkipThoughtSignatureValidator,
		},
		{
			name:          "empty signature maps to bypass sentinel",
			signature:     "",
			wantSignature: signature.GeminiSkipThoughtSignatureValidator,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":"` + tt.signature + `"}]}]}`)
			withCompat := ConvertClaudeRequestToGeminiWithCompat("deepseek-v4", payload, false)
			part := gjson.GetBytes(withCompat, "contents.0.parts.0")
			if !part.Get("thought").Bool() || part.Get("text").String() != "reason" {
				t.Fatalf("compat translation missing thought part: %s", withCompat)
			}
			if got := part.Get("thoughtSignature").String(); got != tt.wantSignature {
				t.Fatalf("thoughtSignature = %q, want %q; output: %s", got, tt.wantSignature, withCompat)
			}
		})
	}
}

func TestConvertClaudeRequestToGeminiWithCompatPreservesEmptyThinking(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":""}]}]}`)

	withoutCompat := ConvertClaudeRequestToGemini("deepseek-v4", payload, false)
	if gjson.GetBytes(withoutCompat, "contents.0.parts.#").Int() != 0 {
		t.Fatalf("default translation preserved thinking: %s", withoutCompat)
	}

	withCompat := ConvertClaudeRequestToGeminiWithCompat("deepseek-v4", payload, false)
	part := gjson.GetBytes(withCompat, "contents.0.parts.0")
	if !part.Get("thought").Bool() || part.Get("text").String() != "reason" {
		t.Fatalf("compat translation missing thought part: %s", withCompat)
	}
	if !part.Get("thoughtSignature").Exists() || part.Get("thoughtSignature").String() != signature.GeminiSkipThoughtSignatureValidator {
		t.Fatalf("compat translation did not preserve bypass signature: %s", withCompat)
	}
}
