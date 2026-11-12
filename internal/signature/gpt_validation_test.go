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

func TestInspectGPTReasoningSignature_RouterSuffixed(t *testing.T) {
	native := testGPTReasoningSignature()
	suffix := strings.Repeat("Ab3", 27)[:79]
	routed := native + "." + suffix

	info, err := InspectGPTReasoningSignature(routed)
	if err != nil {
		t.Fatalf("InspectGPTReasoningSignature(routed) error = %v", err)
	}
	if info.RouterSuffix != suffix {
		t.Fatalf("RouterSuffix = %q, want %q", info.RouterSuffix, suffix)
	}
	if got := DetectSignatureProvider(routed); got != SignatureProviderGPT {
		t.Fatalf("DetectSignatureProvider(routed) = %q, want %q", got, SignatureProviderGPT)
	}
	if got, ok := CompatibleSignatureForProvider(SignatureProviderGPT, routed); !ok || got != routed {
		t.Fatalf("CompatibleSignatureForProvider(GPT, routed) = (%q, %v), want verbatim routed value", got, ok)
	}

	// Malformed router suffixes must be rejected.
	for _, bad := range []string{native + ".", native + ".bad_char", native + ".a.b"} {
		if _, err := InspectGPTReasoningSignature(bad); err == nil {
			t.Fatalf("InspectGPTReasoningSignature(%q) unexpectedly succeeded", bad)
		}
	}
}
