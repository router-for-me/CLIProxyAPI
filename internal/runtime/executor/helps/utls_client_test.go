package helps

import (
	"context"
	"net/http"
	"testing"
	"time"

	tls "github.com/refraction-networking/utls"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestNewCodexFingerprintHTTPClientFingerprintsOfficialHosts(t *testing.T) {
	t.Parallel()

	client := NewCodexFingerprintHTTPClient(context.Background(), &config.Config{}, nil, 5*time.Second)

	transport, ok := client.Transport.(*fallbackRoundTripper)
	if !ok {
		t.Fatalf("transport = %T, want *fallbackRoundTripper", client.Transport)
	}
	if transport.utls == nil {
		t.Fatal("expected utls transport")
	}
	if transport.utls.clientHello != tls.HelloChrome_133 {
		t.Fatalf("clientHello = %+v, want Chrome 133", transport.utls.clientHello)
	}
	if transport.fallback == nil {
		t.Fatal("expected fallback transport")
	}
	if _, ok := transport.fingerprintedHosts["chatgpt.com"]; !ok {
		t.Fatal("expected chatgpt.com to use fingerprint transport")
	}
	if _, ok := transport.fingerprintedHosts["api.openai.com"]; !ok {
		t.Fatal("expected api.openai.com to use fingerprint transport")
	}
	if _, ok := transport.fingerprintedHosts["api.anthropic.com"]; ok {
		t.Fatal("codex client should not inherit Claude-only fingerprint hosts")
	}
	if client.Timeout != 5*time.Second {
		t.Fatalf("Timeout = %s, want %s", client.Timeout, 5*time.Second)
	}
}

func TestNewCodexFingerprintHTTPClientPreservesContextRoundTripper(t *testing.T) {
	t.Parallel()

	expected := &roundTripperSpy{}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(expected))

	client := NewCodexFingerprintHTTPClient(
		ctx,
		&config.Config{},
		&cliproxyauth.Auth{ProxyURL: "http://auth-proxy.example.com:8080"},
		0,
	)

	if client.Transport != expected {
		t.Fatalf("transport = %T %v, want context round tripper", client.Transport, client.Transport)
	}
}

func TestNewCodexFingerprintHTTPClientUsesFingerprintWithConfiguredProxyDespiteContextRoundTripper(t *testing.T) {
	t.Parallel()

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(&roundTripperSpy{}))
	cfg := &config.Config{}
	cfg.ProxyURL = "http://proxy.example.com:8080"

	client := NewCodexFingerprintHTTPClient(ctx, cfg, nil, 0)

	if _, ok := client.Transport.(*fallbackRoundTripper); !ok {
		t.Fatalf("transport = %T, want *fallbackRoundTripper", client.Transport)
	}
}

func TestNewCodexFingerprintHTTPClientHonorsClientHelloOverride(t *testing.T) {
	t.Setenv(codexTLSClientHelloEnvVar, "chrome-131")

	client := NewCodexFingerprintHTTPClient(context.Background(), &config.Config{}, nil, 0)

	transport, ok := client.Transport.(*fallbackRoundTripper)
	if !ok {
		t.Fatalf("transport = %T, want *fallbackRoundTripper", client.Transport)
	}
	if transport.utls.clientHello != tls.HelloChrome_131 {
		t.Fatalf("clientHello = %+v, want Chrome 131", transport.utls.clientHello)
	}
}

func TestNewCodexFingerprintHTTPClientReusesCachedClientAndTransport(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	cfg.ProxyURL = "http://proxy-cache.example.com:8080"

	first := NewCodexFingerprintHTTPClient(context.Background(), cfg, nil, 0)
	second := NewCodexFingerprintHTTPClient(context.Background(), cfg, nil, 0)

	if first != second {
		t.Fatal("expected codex fingerprint client cache reuse")
	}
	if first.Transport != second.Transport {
		t.Fatal("expected codex fingerprint transport reuse")
	}
}

func TestNewCodexFingerprintHTTPClientSharesTransportAcrossTimeouts(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	cfg.ProxyURL = "http://proxy-timeout.example.com:8080"

	first := NewCodexFingerprintHTTPClient(context.Background(), cfg, nil, 0)
	second := NewCodexFingerprintHTTPClient(context.Background(), cfg, nil, 5*time.Second)

	if first == second {
		t.Fatal("expected different cached clients for different timeouts")
	}
	if first.Transport != second.Transport {
		t.Fatal("expected shared fingerprint transport across timeout variants")
	}
}

func TestNewUtlsHTTPClientReusesCachedClientAndTransport(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	cfg.ProxyURL = "http://proxy-utls.example.com:8080"

	first := NewUtlsHTTPClient(cfg, nil, 0)
	second := NewUtlsHTTPClient(cfg, nil, 0)

	if first != second {
		t.Fatal("expected claude utls client cache reuse")
	}
	if first.Transport != second.Transport {
		t.Fatal("expected claude utls transport reuse")
	}
}

func TestParseTLSClientHelloIDFallsBackForUnknownValue(t *testing.T) {
	if got := parseTLSClientHelloID("unknown", tls.HelloChrome_120); got != tls.HelloChrome_120 {
		t.Fatalf("clientHello = %+v, want fallback Chrome 120", got)
	}
}
