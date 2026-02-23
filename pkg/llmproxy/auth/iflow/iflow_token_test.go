package iflow

import (
	"strings"
	"testing"
)

func TestIFlowTokenStorage_SaveTokenToFile_RejectsTraversalPath(t *testing.T) {
	ts := &IFlowTokenStorage{AccessToken: "token"}
	badPath := t.TempDir() + "/../iflow-token.json"

	err := ts.SaveTokenToFile(badPath)
	if err == nil {
		t.Fatal("expected error for traversal path")
	}
	if !strings.Contains(err.Error(), "invalid file path") {
		t.Fatalf("expected invalid path error, got %v", err)
	}
}
