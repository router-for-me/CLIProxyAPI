package management

import "testing"

func TestValidateCallbackForwarderTargetAllowsLoopbackAndLocalhost(t *testing.T) {
	cases := []string{
		"http://127.0.0.1:8080/callback",
		"https://localhost:9999/callback?state=abc",
		"http://[::1]:1455/callback",
	}
	for _, target := range cases {
		if _, err := validateCallbackForwarderTarget(target); err != nil {
			t.Fatalf("expected target %q to be allowed: %v", target, err)
		}
	}
}

func TestValidateCallbackForwarderTargetRejectsNonLocalTargets(t *testing.T) {
	cases := []string{
		"",
		"/relative/callback",
		"ftp://127.0.0.1/callback",
		"http://example.com/callback",
		"https://8.8.8.8/callback",
		"https://user:pass@127.0.0.1/callback",
	}
	for _, target := range cases {
		if _, err := validateCallbackForwarderTarget(target); err == nil {
			t.Fatalf("expected target %q to be rejected", target)
		}
	}
}
