package access

import "testing"

func TestFallbackClientKeyID(t *testing.T) {
	const want = "key_8eb943e7040b69a9"

	if got := FallbackClientKeyID(" client-key "); got != want {
		t.Fatalf("FallbackClientKeyID() = %q, want %q", got, want)
	}
	if got := FallbackClientKeyID("client-key"); got != want {
		t.Fatalf("FallbackClientKeyID() without whitespace = %q, want %q", got, want)
	}
	if got := FallbackClientKeyID("   "); got != "" {
		t.Fatalf("FallbackClientKeyID() for empty key = %q, want empty", got)
	}
}
