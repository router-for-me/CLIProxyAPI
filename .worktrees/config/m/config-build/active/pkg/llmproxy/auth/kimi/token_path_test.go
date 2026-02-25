package kimi

import (
	"strings"
	"testing"
)

func TestKimiTokenStorage_SaveTokenToFile_RejectsTraversalPath(t *testing.T) {
	ts := &KimiTokenStorage{AccessToken: "token"}
	badPath := t.TempDir() + "/../kimi-token.json"

	err := ts.SaveTokenToFile(badPath)
	if err == nil {
		t.Fatal("expected error for traversal path")
	}
	if !strings.Contains(err.Error(), "invalid token file path") {
		t.Fatalf("expected invalid path error, got %v", err)
	}
}
