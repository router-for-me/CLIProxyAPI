package kilo

import "testing"

func TestKiloTokenStorage_SaveTokenToFileRejectsTraversalPath(t *testing.T) {
	ts := &KiloTokenStorage{}
	if err := ts.SaveTokenToFile("/tmp/../kilo-escape.json"); err == nil {
		t.Fatal("expected traversal path to be rejected")
	}
}
