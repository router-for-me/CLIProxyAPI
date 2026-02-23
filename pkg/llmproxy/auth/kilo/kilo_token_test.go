package kilo

import (
	"strings"
	"testing"
)

func TestKiloTokenStorage_SaveTokenToFile_RejectsTraversalPath(t *testing.T) {
	ts := &KiloTokenStorage{Token: "token"}
	badPath := t.TempDir() + "/../kilo-token.json"

	err := ts.SaveTokenToFile(badPath)
	if err == nil {
		t.Fatal("expected error for traversal path")
	}
	if !strings.Contains(err.Error(), "invalid token file path") {
		t.Fatalf("expected invalid path error, got %v", err)
	}
}
