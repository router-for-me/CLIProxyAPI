package util

import "testing"

func TestRawJSON(t *testing.T) {
	if got := RawJSON(""); got != nil {
		t.Fatalf("RawJSON(\"\") = %q, want nil", string(got))
	}

	if got := RawJSON(`{"ok":true}`); string(got) != `{"ok":true}` {
		t.Fatalf("RawJSON object = %q, want %q", string(got), `{"ok":true}`)
	}
}
