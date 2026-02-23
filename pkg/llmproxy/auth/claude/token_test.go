package claude

import "testing"

func TestClaudeTokenStorage_SaveTokenToFileRejectsTraversalPath(t *testing.T) {
	ts := &ClaudeTokenStorage{}
	if err := ts.SaveTokenToFile("/tmp/../claude-escape.json"); err == nil {
		t.Fatal("expected traversal path to be rejected")
	}
}
