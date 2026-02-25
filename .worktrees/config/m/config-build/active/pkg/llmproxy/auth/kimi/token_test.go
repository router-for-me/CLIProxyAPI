package kimi

import "testing"

func TestKimiTokenStorage_SaveTokenToFileRejectsTraversalPath(t *testing.T) {
	ts := &KimiTokenStorage{}
	if err := ts.SaveTokenToFile("/tmp/../kimi-escape.json"); err == nil {
		t.Fatal("expected traversal path to be rejected")
	}
}
