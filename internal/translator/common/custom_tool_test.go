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

func TestUnwrapCustomToolInput_RawControlCharsInsideString(t *testing.T) {
	// A backend emits the wrapper with literal (unescaped) newlines INSIDE the
	// "input" string value, which makes it invalid JSON. The control-char escape
	// fallback must repair it and recover the multi-line payload.
	raw := "{\"input\":\"line1\nline2\tend\"}"
	got := UnwrapCustomToolInput(raw)
	if got != "line1\nline2\tend" {
		t.Fatalf("expected recovered multi-line payload, got %q", got)
	}
}

func TestUnwrapCustomToolInput_PreservesStructuralWhitespace(t *testing.T) {
	// A pretty-printed wrapper (structural newlines/indent OUTSIDE string literals)
	// whose input value ALSO contains a raw, unescaped newline. The first
	// gjson.Valid fails (raw newline in the string), so the escape fallback runs.
	// The OLD global escape turned BOTH the in-string newline AND the structural
	// newlines into literal \n tokens -> {\n ... became invalid JSON -> fallback
	// failed and the raw blob leaked. The state-machine escape must escape ONLY
	// the in-string control chars and leave structural whitespace intact, so the
	// repaired JSON parses and the multi-line payload is recovered.
	pretty := "{\n  \"input\": \"line1\nline2\"\n}"
	got := UnwrapCustomToolInput(pretty)
	if got != "line1\nline2" {
		t.Fatalf("pretty wrapper with in-string newline must unwrap to multi-line payload, got %q", got)
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

func TestCustomToolDescription_CapitalizedVariants(t *testing.T) {
	// The contradictory instruction can appear with different capitalization.
	// All variants must be stripped, otherwise the downgraded function tool keeps
	// a "do not wrap in JSON" instruction that conflicts with the JSON-arguments form.
	cases := []string{
		"Use apply_patch. Do not wrap the patch in JSON.",
		"Use apply_patch. This is a freeform tool, so do not wrap the patch in JSON.",
	}
	for _, orig := range cases {
		got := CustomToolDescription(orig)
		lower := got
		if contains(lower, "wrap the patch in JSON") {
			t.Fatalf("capitalized contradictory sentence must be stripped, got %q", got)
		}
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
