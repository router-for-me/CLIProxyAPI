package codexloopback

import (
	"context"
	"net"
	"net/http"
	"testing"

	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

func TestProviderAuthenticatesBearerOnlyFromSocketLoopback(t *testing.T) {
	provider := NewProvider()
	tests := []struct {
		name       string
		remoteAddr string
		auth       string
		xff        string
		wantOK     bool
		wantCode   sdkaccess.AuthErrorCode
	}{
		{name: "IPv4 loopback", remoteAddr: "127.0.0.1:50000", auth: "Bearer chatgpt-token", wantOK: true},
		{name: "IPv6 loopback", remoteAddr: "[::1]:50000", auth: "Bearer chatgpt-token", wantOK: true},
		{name: "mapped IPv4 loopback", remoteAddr: "[::ffff:127.0.0.1]:50000", auth: "Bearer chatgpt-token", wantOK: true},
		{name: "LAN denied despite spoofed XFF", remoteAddr: "192.168.1.20:50000", auth: "Bearer chatgpt-token", xff: "127.0.0.1", wantCode: sdkaccess.AuthErrorCodeNotHandled},
		{name: "loopback without bearer", remoteAddr: "127.0.0.1:50000", wantCode: sdkaccess.AuthErrorCodeNoCredentials},
		{name: "loopback basic auth denied", remoteAddr: "127.0.0.1:50000", auth: "Basic abc", wantCode: sdkaccess.AuthErrorCodeInvalidCredential},
		{name: "malformed remote denied", remoteAddr: "localhost", auth: "Bearer chatgpt-token", wantCode: sdkaccess.AuthErrorCodeNotHandled},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, "http://localhost/v1/responses", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.RemoteAddr = tt.remoteAddr
			req.Header.Set("Authorization", tt.auth)
			req.Header.Set("X-Forwarded-For", tt.xff)
			result, authErr := provider.Authenticate(context.Background(), req)
			if tt.wantOK {
				if authErr != nil || result == nil {
					t.Fatalf("Authenticate() = %#v, %v", result, authErr)
				}
				if result.Principal != LoopbackPrincipal || result.Principal == "chatgpt-token" {
					t.Fatalf("Principal = %q, want redacted fixed principal", result.Principal)
				}
				return
			}
			if authErr == nil || authErr.Code != tt.wantCode {
				t.Fatalf("Authenticate() error = %#v, want code %q", authErr, tt.wantCode)
			}
		})
	}
}

func TestValidateListenerAddr(t *testing.T) {
	for _, address := range []string{"127.0.0.1:8317", "[::1]:8317", "[::ffff:127.0.0.1]:8317"} {
		if err := ValidateListenerAddr(testAddr(address)); err != nil {
			t.Fatalf("ValidateListenerAddr(%q) error = %v", address, err)
		}
	}
	for _, address := range []string{"0.0.0.0:8317", "[::]:8317", "192.168.1.20:8317", "bad"} {
		if err := ValidateListenerAddr(testAddr(address)); err == nil {
			t.Fatalf("ValidateListenerAddr(%q) error = nil", address)
		}
	}
	if err := ValidateListenerAddr(nil); err == nil {
		t.Fatal("ValidateListenerAddr(nil) error = nil")
	}
}

type testAddr string

func (a testAddr) Network() string { return "tcp" }
func (a testAddr) String() string  { return string(a) }

var _ net.Addr = testAddr("")
