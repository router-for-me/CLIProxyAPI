//go:build darwin

package cursorcomposer

import "testing"

func TestBytesTrimSpace(t *testing.T) {
	if got := string(bytesTrimSpace([]byte("  hello  "))); got != "hello" {
		t.Fatalf("bytesTrimSpace() = %q, want hello", got)
	}
}
