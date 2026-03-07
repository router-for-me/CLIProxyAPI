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
