package configaccess

import (
	"context"
	"net/http"
	"testing"

	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestParseClientKey(t *testing.T) {
	tests := []struct {
		name             string
		input            string
		expectedKey      string
		expectedUpstream string
	}{
		{
			name:             "simple key without upstream",
			input:            "my-api-key",
			expectedKey:      "my-api-key",
			expectedUpstream: "",
		},
		{
			name:             "key with upstream",
			input:            "my-api-key|upstream-secret",
			expectedKey:      "my-api-key",
			expectedUpstream: "upstream-secret",
		},
		{
			name:             "key with empty upstream",
			input:            "my-api-key|",
			expectedKey:      "my-api-key",
			expectedUpstream: "",
		},
		{
			name:             "key with multiple pipes - uses last segment as upstream",
			input:            "my-api-key|middle|upstream",
			expectedKey:      "my-api-key|middle",
			expectedUpstream: "upstream",
		},
		{
			name:             "empty key",
			input:            "",
			expectedKey:      "",
			expectedUpstream: "",
		},
		{
			name:             "only pipe",
			input:            "|",
			expectedKey:      "",
			expectedUpstream: "",
		},
		{
			name:             "upstream with special characters",
			input:            "key123|sk-ant-api03-xxxx",
			expectedKey:      "key123",
			expectedUpstream: "sk-ant-api03-xxxx",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, upstream := parseClientKey(tt.input)
			if key != tt.expectedKey {
				t.Errorf("parseClientKey(%q) key = %q, want %q", tt.input, key, tt.expectedKey)
			}
			if upstream != tt.expectedUpstream {
				t.Errorf("parseClientKey(%q) upstream = %q, want %q", tt.input, upstream, tt.expectedUpstream)
			}
		})
	}
}

func TestProviderAuthenticateWithUpstreamKey(t *testing.T) {
	tests := []struct {
		name              string
		configuredKeys    []string
		authHeader        string
		expectSuccess     bool
		expectUpstreamKey string
		expectPrincipal   string
	}{
		{
			name:              "simple key authenticates",
			configuredKeys:    []string{"simple-key"},
			authHeader:        "Bearer simple-key",
			expectSuccess:     true,
			expectUpstreamKey: "",
			expectPrincipal:   "simple-key",
		},
		{
			name:              "client sends key with upstream - extracts upstream",
			configuredKeys:    []string{"client-key"},
			authHeader:        "Bearer client-key|upstream-key",
			expectSuccess:     true,
			expectUpstreamKey: "upstream-key",
			expectPrincipal:   "client-key",
		},
		{
			name:              "client sends key without upstream when config has key",
			configuredKeys:    []string{"client-key"},
			authHeader:        "Bearer client-key",
			expectSuccess:     true,
			expectUpstreamKey: "",
			expectPrincipal:   "client-key",
		},
		{
			name:              "wrong key fails",
			configuredKeys:    []string{"correct-key"},
			authHeader:        "Bearer wrong-key",
			expectSuccess:     false,
			expectUpstreamKey: "",
		},
		{
			name:              "wrong key with upstream fails",
			configuredKeys:    []string{"correct-key"},
			authHeader:        "Bearer wrong-key|upstream",
			expectSuccess:     false,
			expectUpstreamKey: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &sdkconfig.AccessProvider{
				Name:    "test",
				APIKeys: tt.configuredKeys,
			}
			p, err := newProvider(cfg, nil)
			if err != nil {
				t.Fatalf("newProvider() error = %v", err)
			}

			req, _ := http.NewRequest("GET", "/test", nil)
			req.Header.Set("Authorization", tt.authHeader)

			result, err := p.Authenticate(context.Background(), req)

			if tt.expectSuccess {
				if err != nil {
					t.Errorf("Authenticate() error = %v, expected success", err)
					return
				}
				if result == nil {
					t.Error("Authenticate() result is nil, expected non-nil")
					return
				}
				upstreamKey := result.Metadata["upstream_key"]
				if upstreamKey != tt.expectUpstreamKey {
					t.Errorf("Authenticate() upstream_key = %q, want %q", upstreamKey, tt.expectUpstreamKey)
				}
				if result.Principal != tt.expectPrincipal {
					t.Errorf("Authenticate() Principal = %q, want %q", result.Principal, tt.expectPrincipal)
				}
			} else {
				if err == nil {
					t.Errorf("Authenticate() succeeded, expected failure")
				}
			}
		})
	}
}

func TestProviderWithXApiKeyHeader(t *testing.T) {
	cfg := &sdkconfig.AccessProvider{
		Name:    "test",
		APIKeys: []string{"api-key"},
	}
	p, err := newProvider(cfg, nil)
	if err != nil {
		t.Fatalf("newProvider() error = %v", err)
	}

	req, _ := http.NewRequest("GET", "/test", nil)
	req.Header.Set("X-Api-Key", "api-key|upstream-secret")

	result, err := p.Authenticate(context.Background(), req)
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if result.Metadata["upstream_key"] != "upstream-secret" {
		t.Errorf("upstream_key = %q, want %q", result.Metadata["upstream_key"], "upstream-secret")
	}
	if result.Metadata["source"] != "x-api-key" {
		t.Errorf("source = %q, want %q", result.Metadata["source"], "x-api-key")
	}
	if result.Principal != "api-key" {
		t.Errorf("Principal = %q, want %q", result.Principal, "api-key")
	}
}
