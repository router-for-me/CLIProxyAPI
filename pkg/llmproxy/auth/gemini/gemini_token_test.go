package gemini

import "testing"

func TestGeminiTokenStorage_SaveTokenToFileRejectsTraversalPath(t *testing.T) {
	ts := &GeminiTokenStorage{}
	if err := ts.SaveTokenToFile("/tmp/../gemini-escape.json"); err == nil {
		t.Fatal("expected traversal path to be rejected")
	}
}
