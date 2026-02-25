package configaccess

import (
	"context"
	"net/http/httptest"
	"testing"

	sdkaccess "github.com/router-for-me/CLIProxyAPI/v6/sdk/access"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func findProvider() sdkaccess.Provider {
	providers := sdkaccess.RegisteredProviders()
	for _, p := range providers {
		if p.Identifier() == sdkaccess.DefaultAccessProviderName {
			return p
		}
	}
	return nil
}

func TestRegister(t *testing.T) {
	// Test nil config
	Register(nil)
	if findProvider() != nil {
		t.Errorf("expected provider to be unregistered for nil config")
	}

	// Test empty keys
	cfg := &sdkconfig.SDKConfig{APIKeys: []string{}}
	Register(cfg)
	if findProvider() != nil {
		t.Errorf("expected provider to be unregistered for empty keys")
	}

	// Test valid keys
	cfg.APIKeys = []string{"key1"}
	Register(cfg)
	p := findProvider()
	if p == nil {
		t.Fatalf("expected provider to be registered")
	}
	if p.Identifier() != sdkaccess.DefaultAccessProviderName {
		t.Errorf("expected identifier %q, got %q", sdkaccess.DefaultAccessProviderName, p.Identifier())
	}
}

func TestProvider_Authenticate(t *testing.T) {
	p := newProvider("test-provider", []string{"valid-key"})
	ctx := context.Background()

	tests := []struct {
		name       string
		headers    map[string]string
		query      string
		wantResult bool
		wantError  sdkaccess.AuthErrorCode
	}{
		{
			name:       "valid bearer token",
			headers:    map[string]string{"Authorization": "Bearer valid-key"},
			wantResult: true,
		},
		{
			name:       "valid plain token",
			headers:    map[string]string{"Authorization": "valid-key"},
			wantResult: true,
		},
		{
			name:       "valid google header",
			headers:    map[string]string{"X-Goog-Api-Key": "valid-key"},
			wantResult: true,
		},
		{
			name:       "valid anthropic header",
			headers:    map[string]string{"X-Api-Key": "valid-key"},
			wantResult: true,
		},
		{
			name:       "valid query key",
			query:      "?key=valid-key",
			wantResult: true,
		},
		{
			name:       "valid query auth_token",
			query:      "?auth_token=valid-key",
			wantResult: true,
		},
		{
			name:       "invalid token",
			headers:    map[string]string{"Authorization": "Bearer invalid-key"},
			wantResult: false,
			wantError:  sdkaccess.AuthErrorCodeInvalidCredential,
		},
		{
			name:       "no credentials",
			wantResult: false,
			wantError:  sdkaccess.AuthErrorCodeNoCredentials,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/"+tt.query, nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			res, err := p.Authenticate(ctx, req)
			if tt.wantResult {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if res == nil {
					t.Errorf("expected result, got nil")
				} else if res.Principal != "valid-key" {
					t.Errorf("expected principal valid-key, got %q", res.Principal)
				}
			} else {
				if err == nil {
					t.Errorf("expected error, got nil")
				} else if err.Code != tt.wantError {
					t.Errorf("expected error code %v, got %v", tt.wantError, err.Code)
				}
			}
		})
	}
}

func TestExtractBearerToken(t *testing.T) {
	cases := []struct {
		header string
		want   string
	}{
		{"", ""},
		{"valid-key", "valid-key"},
		{"Bearer valid-key", "valid-key"},
		{"bearer valid-key", "valid-key"},
		{"BEARER valid-key", "valid-key"},
		{"Bearer  valid-key ", "valid-key"},
		{"Other token", "Other token"},
	}
	for _, tc := range cases {
		got := extractBearerToken(tc.header)
		if got != tc.want {
			t.Errorf("extractBearerToken(%q) = %q, want %q", tc.header, got, tc.want)
		}
	}
}

func TestNormalizeKeys(t *testing.T) {
	cases := []struct {
		keys []string
		want []string
	}{
		{nil, nil},
		{[]string{}, nil},
		{[]string{" "}, nil},
		{[]string{" key1 ", "key2", "key1"}, []string{"key1", "key2"}},
	}
	for _, tc := range cases {
		got := normalizeKeys(tc.keys)
		if len(got) != len(tc.want) {
			t.Errorf("normalizeKeys(%v) length mismatch: got %v, want %v", tc.keys, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("normalizeKeys(%v)[%d] = %q, want %q", tc.keys, i, got[i], tc.want[i])
			}
		}
	}
}
