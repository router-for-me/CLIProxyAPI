package executor

import "testing"

func TestNormalizeToolCallID(t *testing.T) {
	input := "call-cce860e6-ab07-414d-812c-785db35b17ca-4\nfc_d2335004-a95f-93b4-977b-e9eee6316be7_0"
	want := "call-cce860e6-ab07-414d-812c-785db35b17ca-4fc_d2335004-a95f-93b4-977b-e9eee6316be7_0"

	if got := normalizeToolCallID(input); got != want {
		t.Fatalf("normalizeToolCallID() = %q, want %q", got, want)
	}
}
