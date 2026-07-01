package helps

import (
	"testing"

	tls "github.com/refraction-networking/utls"
)

func TestResolveTLSProfile(t *testing.T) {
	cases := []struct {
		name        string
		host        string
		disableNode bool
		wantOK      bool
		wantName    string
		wantHTTP2   bool
	}{
		{"anthropic_default_node", "api.anthropic.com", false, true, "node-h1", false},
		{"anthropic_escape_hatch_chrome", "api.anthropic.com", true, true, "chrome-h2", true},
		{"chatgpt_chrome", "chatgpt.com", false, true, "chrome-h2", true},
		{"case_and_space_insensitive", "  API.Anthropic.COM ", false, true, "node-h1", false},
		{"unprotected_host", "example.com", false, false, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, ok := resolveTLSProfile(tc.host, tc.disableNode)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if p.name != tc.wantName {
				t.Fatalf("name = %q, want %q", p.name, tc.wantName)
			}
			if p.http2 != tc.wantHTTP2 {
				t.Fatalf("http2 = %v, want %v", p.http2, tc.wantHTTP2)
			}
		})
	}
}

func TestNodeClientHelloSpecShape(t *testing.T) {
	// Arrange / Act
	spec := nodeClientHelloSpec()

	// Assert: cipher count and ALPN must match the captured Node.js fingerprint,
	// and a key_share must be present for a valid TLS 1.3 ClientHello.
	if len(spec.CipherSuites) != 17 {
		t.Fatalf("cipher suites = %d, want 17", len(spec.CipherSuites))
	}
	var alpnProtos []string
	hasKeyShare := false
	for _, ext := range spec.Extensions {
		switch e := ext.(type) {
		case *tls.ALPNExtension:
			alpnProtos = e.AlpnProtocols
		case *tls.KeyShareExtension:
			hasKeyShare = len(e.KeyShares) > 0
		}
	}
	if len(alpnProtos) != 1 || alpnProtos[0] != "http/1.1" {
		t.Fatalf("ALPN = %v, want [http/1.1]", alpnProtos)
	}
	if !hasKeyShare {
		t.Fatalf("expected a non-empty key_share extension")
	}
}
