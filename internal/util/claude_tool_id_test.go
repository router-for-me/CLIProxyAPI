package util

import (
	"regexp"
	"testing"
)

func TestSanitizeClaudeToolID_ReplacesInvalidCharacters(t *testing.T) {
	got := SanitizeClaudeToolID("fs.readFile:temp@1")
	if got != "fs_readFile_temp_1" {
		t.Fatalf("SanitizeClaudeToolID returned %q", got)
	}
}

func TestSanitizeClaudeToolID_GeneratesFallbackForEmptyResult(t *testing.T) {
	got := SanitizeClaudeToolID("!!!")
	if got == "" {
		t.Fatal("expected non-empty fallback id")
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9_-]+$`).MatchString(got) {
		t.Fatalf("fallback id %q does not match Claude regex", got)
	}
}

func TestEncodeClaudeToolID_RoundTripsInvalidIDs(t *testing.T) {
	original := "fs.readFile:temp@1"

	encoded := EncodeClaudeToolID(original)
	if encoded == original {
		t.Fatal("expected invalid id to be encoded")
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9_-]+$`).MatchString(encoded) {
		t.Fatalf("encoded id %q does not match Claude regex", encoded)
	}
	if got := DecodeClaudeToolID(encoded); got != original {
		t.Fatalf("DecodeClaudeToolID(%q) = %q, want %q", encoded, got, original)
	}
}

func TestEncodeClaudeToolID_ReEncodesEncodedLookingIDs(t *testing.T) {
	original := "toolu_enc0_Zm9v_8c736521"

	encoded := EncodeClaudeToolID(original)
	if encoded == original {
		t.Fatal("expected encoded-looking id to be re-encoded")
	}
	if got := DecodeClaudeToolID(encoded); got != original {
		t.Fatalf("DecodeClaudeToolID(%q) = %q, want %q", encoded, got, original)
	}
}
