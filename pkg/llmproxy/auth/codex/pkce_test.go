package codex

import (
	"testing"
)

func TestGeneratePKCECodes(t *testing.T) {
	codes, err := GeneratePKCECodes()
	if err != nil {
		t.Fatalf("GeneratePKCECodes failed: %v", err)
	}

	if codes.CodeVerifier == "" {
		t.Error("expected non-empty CodeVerifier")
	}
	if codes.CodeChallenge == "" {
		t.Error("expected non-empty CodeChallenge")
	}

	// Verify challenge matches verifier
	expectedChallenge := generateCodeChallenge(codes.CodeVerifier)
	if codes.CodeChallenge != expectedChallenge {
		t.Errorf("CodeChallenge mismatch: expected %s, got %s", expectedChallenge, codes.CodeChallenge)
	}
}

func TestGenerateCodeVerifier(t *testing.T) {
	v1, err := generateCodeVerifier()
	if err != nil {
		t.Fatalf("generateCodeVerifier failed: %v", err)
	}
	v2, err := generateCodeVerifier()
	if err != nil {
		t.Fatalf("generateCodeVerifier failed: %v", err)
	}

	if v1 == v2 {
		t.Error("expected different verifiers")
	}

	if len(v1) < 43 || len(v1) > 128 {
		t.Errorf("invalid verifier length: %d", len(v1))
	}
}
