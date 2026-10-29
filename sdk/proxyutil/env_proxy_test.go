package proxyutil

import "testing"

func TestEnvironmentProxyURL(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://https.example:1")
	t.Setenv("HTTP_PROXY", "http://http.example:2")
	if got := EnvironmentProxyURL(); got != "http://https.example:1" {
		t.Fatalf("EnvironmentProxyURL() = %q, want HTTPS_PROXY value", got)
	}

	t.Setenv("HTTPS_PROXY", "")
	if got := EnvironmentProxyURL(); got != "http://http.example:2" {
		t.Fatalf("EnvironmentProxyURL() = %q, want HTTP_PROXY value", got)
	}

	t.Setenv("HTTP_PROXY", "")
	if got := EnvironmentProxyURL(); got != "" {
		t.Fatalf("EnvironmentProxyURL() = %q, want empty", got)
	}
}
