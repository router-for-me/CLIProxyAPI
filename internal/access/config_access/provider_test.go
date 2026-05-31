package configaccess

import (
	"context"
	"net/http"
	"testing"
)

func TestProviderAuthenticateAcceptsConfiguredKeySources(t *testing.T) {
	const configuredKey = "configured-key"

	tests := []struct {
		name       string
		configure  func(*http.Request)
		wantSource string
	}{
		{
			name: "authorization bearer",
			configure: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer "+configuredKey)
			},
			wantSource: "authorization",
		},
		{
			name: "x-goog-api-key",
			configure: func(r *http.Request) {
				r.Header.Set("X-Goog-Api-Key", configuredKey)
			},
			wantSource: "x-goog-api-key",
		},
		{
			name: "x-api-key",
			configure: func(r *http.Request) {
				r.Header.Set("X-Api-Key", configuredKey)
			},
			wantSource: "x-api-key",
		},
		{
			name: "api-key",
			configure: func(r *http.Request) {
				r.Header.Set("api-key", configuredKey)
			},
			wantSource: "api-key",
		},
		{
			name: "query key",
			configure: func(r *http.Request) {
				query := r.URL.Query()
				query.Set("key", configuredKey)
				r.URL.RawQuery = query.Encode()
			},
			wantSource: "query-key",
		},
		{
			name: "query auth_token",
			configure: func(r *http.Request) {
				query := r.URL.Query()
				query.Set("auth_token", configuredKey)
				r.URL.RawQuery = query.Encode()
			},
			wantSource: "query-auth-token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request, err := http.NewRequest(http.MethodGet, "https://example.test/v1/models", nil)
			if err != nil {
				t.Fatalf("NewRequest() error = %v", err)
			}
			tt.configure(request)

			result, authErr := newProvider("", []string{configuredKey}).Authenticate(context.Background(), request)
			if authErr != nil {
				t.Fatalf("Authenticate() authErr = %v", authErr)
			}
			if result == nil {
				t.Fatal("Authenticate() result is nil")
			}
			if result.Principal != configuredKey {
				t.Fatalf("Authenticate() principal = %q, want %q", result.Principal, configuredKey)
			}
			if got := result.Metadata["source"]; got != tt.wantSource {
				t.Fatalf("Authenticate() source = %q, want %q", got, tt.wantSource)
			}
		})
	}
}
