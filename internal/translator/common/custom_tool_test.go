package common

import "testing"

func TestCustomToolNamesFromRequest(t *testing.T) {
	req := []byte(`{"tools":[
		{"type":"function","name":"shell"},
		{"type":"custom","name":"apply_patch","description":"x"},
		{"type":"web_search","name":"web_search"}
	]}`)
	got := CustomToolNamesFromRequest(req)
	if _, ok := got["apply_patch"]; !ok {
		t.Fatalf("expected apply_patch in custom set, got %v", got)
	}
	if _, ok := got["shell"]; ok {
		t.Fatalf("shell must NOT be in custom set, got %v", got)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 custom tool, got %d: %v", len(got), got)
	}
}

func TestCustomToolNamesFromRequest_NoTools(t *testing.T) {
	if got := CustomToolNamesFromRequest([]byte(`{}`)); len(got) != 0 {
		t.Fatalf("expected empty set, got %v", got)
	}
}

func TestUnwrapCustomToolInput(t *testing.T) {
	patch := "*** Begin Patch\n*** Add File: /tmp/x.txt\n+hi\n*** End Patch"
	wrapped := WrapCustomToolInput(patch)
	// round-trip
	if got := UnwrapCustomToolInput(wrapped); got != patch {
		t.Fatalf("round-trip mismatch:\n want %q\n got  %q", patch, got)
	}
	// direct wrapper
	if got := UnwrapCustomToolInput(`{"input":"hello"}`); got != "hello" {
		t.Fatalf("expected hello, got %q", got)
	}
	// fallback: not the expected shape -> return raw
	if got := UnwrapCustomToolInput(`raw text not json`); got != "raw text not json" {
		t.Fatalf("fallback mismatch, got %q", got)
	}
	// fallback: json object without input key -> return raw
	if got := UnwrapCustomToolInput(`{"other":"v"}`); got != `{"other":"v"}` {
		t.Fatalf("fallback (no input key) mismatch, got %q", got)
	}
}

func TestCustomToolDescription(t *testing.T) {
	orig := "Use the `apply_patch` tool to edit files. This is a FREEFORM tool, so do not wrap the patch in JSON."
	got := CustomToolDescription(orig)
	if got == "" {
		t.Fatal("description must not be empty")
	}
	if contains(got, "do not wrap the patch in JSON") {
		t.Fatalf("contradictory sentence must be stripped, got %q", got)
	}
	if !contains(got, CustomToolInputKey) {
		t.Fatalf("description must mention the input arg, got %q", got)
	}
}

func TestCustomToolFunctionSchema(t *testing.T) {
	s := string(CustomToolFunctionSchema())
	if !contains(s, `"input"`) || !contains(s, `"required"`) {
		t.Fatalf("schema missing input/required: %s", s)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
