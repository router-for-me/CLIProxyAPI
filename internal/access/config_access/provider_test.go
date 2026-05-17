package configaccess

import (
	"context"
	"net/http/httptest"
	"testing"

	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

func TestAuthenticateRejectsBlockedSampleAPIKeys(t *testing.T) {
	p := newProvider("test", []string{"your-api-key-1", "your-api-key-2", "your-api-key-3", "real-key"})

	tests := []struct {
		name   string
		header string
	}{
		{name: "bearer", header: "Bearer your-api-key-1"},
		{name: "plain authorization", header: "your-api-key-2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			req.Header.Set("Authorization", tt.header)

			result, authErr := p.Authenticate(context.Background(), req)
			if result != nil {
				t.Fatalf("Authenticate() result = %#v, want nil", result)
			}
			if !sdkaccess.IsAuthErrorCode(authErr, sdkaccess.AuthErrorCodeInvalidCredential) {
				t.Fatalf("Authenticate() error = %#v, want invalid credential", authErr)
			}
		})
	}
}

func TestAuthenticateAllowsCustomAPIKey(t *testing.T) {
	p := newProvider("test", []string{"your-api-key-1", "real-key"})
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer real-key")

	result, authErr := p.Authenticate(context.Background(), req)
	if authErr != nil {
		t.Fatalf("Authenticate() error = %v, want nil", authErr)
	}
	if result == nil {
		t.Fatal("Authenticate() result = nil, want result")
	}
	if result.Principal != "real-key" {
		t.Fatalf("Authenticate() principal = %q, want real-key", result.Principal)
	}
}

func TestAuthenticateTrimsCandidateAPIKey(t *testing.T) {
	p := newProvider("test", []string{"real-key"})
	req := httptest.NewRequest("GET", "/?key=%20real-key%20", nil)

	result, authErr := p.Authenticate(context.Background(), req)
	if authErr != nil {
		t.Fatalf("Authenticate() error = %v, want nil", authErr)
	}
	if result == nil {
		t.Fatal("Authenticate() result = nil, want result")
	}
	if result.Principal != "real-key" {
		t.Fatalf("Authenticate() principal = %q, want trimmed real-key", result.Principal)
	}
	if got := result.Metadata["source"]; got != "query-key" {
		t.Fatalf("Authenticate() source = %q, want query-key", got)
	}
}
