package proxyutil

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEnvironmentProxyURL(t *testing.T) {
	ClearProxyEnvironment()
	t.Setenv("HTTPS_PROXY", "http://https.example:1")
	t.Setenv("HTTP_PROXY", "http://http.example:2")
	if got := EnvironmentProxyURL(); got != "http://https.example:1" {
		t.Fatalf("EnvironmentProxyURL() = %q, want HTTPS_PROXY value", got)
	}

	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("https_proxy", "")
	if got := EnvironmentProxyURL(); got != "http://http.example:2" {
		t.Fatalf("EnvironmentProxyURL() = %q, want HTTP_PROXY value", got)
	}

	t.Setenv("HTTP_PROXY", "")
	t.Setenv("http_proxy", "")
	if got := EnvironmentProxyURL(); got != "" {
		t.Fatalf("EnvironmentProxyURL() = %q, want empty", got)
	}
}

func TestNormalizeProxyURLHostPort(t *testing.T) {
	got, err := NormalizeProxyURL("proxy.corp:8080")
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://proxy.corp:8080" {
		t.Fatalf("NormalizeProxyURL() = %q", got)
	}
}

func TestEnvironmentProxyFuncHonorsNoProxy(t *testing.T) {
	ClearProxyEnvironment()
	t.Setenv("HTTPS_PROXY", "http://proxy.example:8080")
	t.Setenv("NO_PROXY", "localhost")

	req, errReq := http.NewRequest(http.MethodGet, "http://localhost/test", nil)
	if errReq != nil {
		t.Fatal(errReq)
	}
	proxyURL, errProxy := EnvironmentProxyFunc()(req)
	if errProxy != nil {
		t.Fatal(errProxy)
	}
	if proxyURL != nil {
		t.Fatalf("NO_PROXY host should be direct, got proxy %v", proxyURL)
	}
}

func TestEnvironmentDialerHonorsNoProxy(t *testing.T) {
	ClearProxyEnvironment()
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("NO_PROXY", "127.0.0.1")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	dialer := EnvironmentDialer{}
	conn, errDial := dialer.DialContext(t.Context(), "tcp", server.Listener.Addr().String())
	if errDial != nil {
		t.Fatal(errDial)
	}
	if errClose := conn.Close(); errClose != nil {
		t.Fatal(errClose)
	}
}
