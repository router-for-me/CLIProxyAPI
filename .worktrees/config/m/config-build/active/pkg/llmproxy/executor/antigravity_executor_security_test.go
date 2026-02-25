package executor

import "testing"

func TestSanitizeAntigravityBaseURL_AllowsKnownHosts(t *testing.T) {
	t.Parallel()

	cases := []string{
		antigravityBaseURLDaily,
		antigravitySandboxBaseURLDaily,
		antigravityBaseURLProd,
	}
	for _, base := range cases {
		got, err := sanitizeAntigravityBaseURL(base)
		if err != nil {
			t.Fatalf("sanitizeAntigravityBaseURL(%q) error: %v", base, err)
		}
		if got != base {
			t.Fatalf("sanitizeAntigravityBaseURL(%q) = %q, want %q", base, got, base)
		}
	}
}

func TestSanitizeAntigravityBaseURL_RejectsUntrustedHost(t *testing.T) {
	t.Parallel()

	if _, err := sanitizeAntigravityBaseURL("https://127.0.0.1:8080"); err == nil {
		t.Fatal("expected error for untrusted antigravity base URL")
	}
}
