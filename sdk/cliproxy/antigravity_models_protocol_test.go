package cliproxy

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// TestAntigravityCatalogProbeUsesHTTP11AgainstHTTP2Server runs the real catalog
// probe against a TLS server that offers HTTP/2 and asserts the request arrives
// over HTTP/1.1, matching the executor wire profile.
func TestAntigravityCatalogProbeUsesHTTP11AgainstHTTP2Server(t *testing.T) {
	var protoMajor atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protoMajor.Store(int32(r.ProtoMajor))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":{}}`))
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	// Trust the test certificate through the default transport only; the probe's
	// inherit mode starts from http.DefaultTransport, so production code is unchanged.
	// The server's client transport sets ForceAttemptHTTP2, so a probe that keeps
	// the default protocol settings negotiates h2 via ALPN.
	previous := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = previous })

	svc := &Service{cfg: &config.Config{}}
	auth := &coreauth.Auth{ID: "protocol-probe", Metadata: map[string]any{"project_id": "project-protocol"}}
	_, _ = svc.probeAntigravityModelCapabilityHints(t.Context(), auth, []string{server.URL}, "", "token-protocol", "antigravity-test")

	got := protoMajor.Load()
	if got == 0 {
		t.Fatal("catalog probe did not reach the test server")
	}
	if got != 1 {
		t.Fatalf("catalog probe negotiated HTTP/%d, want HTTP/1.1", got)
	}
}
