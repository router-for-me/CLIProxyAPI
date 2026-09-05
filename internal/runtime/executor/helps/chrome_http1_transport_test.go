package helps

import (
	"net/http"
	"reflect"
	"testing"

	tls "github.com/refraction-networking/utls"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestChromeHTTP1ClientHelloKeepsChromeProfileWithHTTP1ALPN(t *testing.T) {
	original, errOriginal := tls.UTLSIdToSpec(tls.HelloChrome_Auto)
	if errOriginal != nil {
		t.Fatal(errOriginal)
	}
	modified, errModified := chromeHTTP1ClientHelloSpec()
	if errModified != nil {
		t.Fatal(errModified)
	}

	if !reflect.DeepEqual(modified.CipherSuites, original.CipherSuites) {
		t.Fatal("Chrome cipher suites changed")
	}
	if !reflect.DeepEqual(modified.CompressionMethods, original.CompressionMethods) {
		t.Fatal("Chrome compression methods changed")
	}
	originalTypes := make(map[reflect.Type]int, len(original.Extensions))
	for _, extension := range original.Extensions {
		switch extension.(type) {
		case *tls.ApplicationSettingsExtension, *tls.ApplicationSettingsExtensionNew:
			continue
		default:
			originalTypes[reflect.TypeOf(extension)]++
		}
	}
	modifiedTypes := make(map[reflect.Type]int, len(modified.Extensions))
	var alpn []string
	for _, extension := range modified.Extensions {
		switch extension.(type) {
		case *tls.ApplicationSettingsExtension, *tls.ApplicationSettingsExtensionNew:
			t.Fatalf("HTTP/1.1 ClientHello contains h2 ALPS extension %T", extension)
		}
		modifiedTypes[reflect.TypeOf(extension)]++
		if typed, ok := extension.(*tls.ALPNExtension); ok {
			alpn = append(alpn, typed.AlpnProtocols...)
		}
	}
	if !reflect.DeepEqual(modifiedTypes, originalTypes) {
		t.Fatalf("Chrome extension types = %v, want %v", modifiedTypes, originalTypes)
	}
	if want := []string{"http/1.1"}; !reflect.DeepEqual(alpn, want) {
		t.Fatalf("ALPN = %v, want %v", alpn, want)
	}
}

func TestNewUtlsHTTPClientForceHTTP1IsOptIn(t *testing.T) {
	tests := []struct {
		name      string
		cfg       *config.Config
		wantHTTP1 bool
	}{
		{name: "nil config", cfg: nil, wantHTTP1: false},
		{name: "default", cfg: &config.Config{}, wantHTTP1: false},
		{name: "forced", cfg: &config.Config{Codex: config.CodexConfig{ForceHTTP1: true}}, wantHTTP1: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewUtlsHTTPClient(t.Context(), tt.cfg, nil, 0)
			router, ok := client.Transport.(*fallbackRoundTripper)
			if !ok {
				t.Fatalf("transport type = %T, want *fallbackRoundTripper", client.Transport)
			}

			if tt.wantHTTP1 {
				transport, ok := router.chrome.(*http.Transport)
				if !ok {
					t.Fatalf("ChatGPT transport type = %T, want *http.Transport", router.chrome)
				}
				if transport.ForceAttemptHTTP2 {
					t.Fatal("ForceAttemptHTTP2 = true, want false")
				}
				if !transport.DisableKeepAlives {
					t.Fatal("DisableKeepAlives = false, want one connection per request")
				}
				return
			}

			if _, ok := router.chrome.(*utlsRoundTripper); !ok {
				t.Fatalf("default ChatGPT transport type = %T, want *utlsRoundTripper", router.chrome)
			}
		})
	}
}
