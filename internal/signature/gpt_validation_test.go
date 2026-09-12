package signature

import (
	"encoding/base64"
	"strings"
	"testing"
)

func testGPTReasoningSignature() string {
	payload := make([]byte, 1+8+16+16+32)
	payload[0] = 0x80
	for i := 9; i < len(payload); i++ {
		payload[i] = byte(i)
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}

func TestDetectSignatureProvider_GPTReasoning(t *testing.T) {
	if got := DetectSignatureProvider(testGPTReasoningSignature()); got != SignatureProviderGPT {
		t.Fatalf("DetectSignatureProvider(GPT) = %q, want %q", got, SignatureProviderGPT)
	}
}

func TestInspectGPTReasoningSignatureRejectsUnicodeEllipsis(t *testing.T) {
	sig := testGPTReasoningSignature()
	polluted := sig[:20] + string(rune(0x2026)) + sig[20:]

	_, err := InspectGPTReasoningSignature(polluted)
	if err == nil {
		t.Fatal("expected invalid GPT reasoning signature")
	}
	if !strings.Contains(err.Error(), "non-base64url character U+2026") {
		t.Fatalf("error = %q, want U+2026 base64url detail", err.Error())
	}
}
