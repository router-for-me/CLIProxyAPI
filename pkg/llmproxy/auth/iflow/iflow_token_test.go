package iflow

import "testing"

func TestIFlowTokenStorage_SaveTokenToFileRejectsTraversalPath(t *testing.T) {
	ts := &IFlowTokenStorage{}
	if err := ts.SaveTokenToFile("/tmp/../iflow-escape.json"); err == nil {
		t.Fatal("expected traversal path to be rejected")
	}
}
