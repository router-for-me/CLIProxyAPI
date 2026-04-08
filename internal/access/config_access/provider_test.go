package configaccess

import (
	"net/http/httptest"
	"testing"

	sdkaccess "github.com/router-for-me/CLIProxyAPI/v6/sdk/access"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestProviderAuthenticate_RateLimitsPerAPIKey(t *testing.T) {
	provider := newProvider("test-provider", []sdkconfig.APIKeyEntry{
		{APIKey: "test-key", RequestsPerSecond: 2},
	})

	req1 := httptest.NewRequest("GET", "/v1/models", nil)
	req1.Header.Set("Authorization", "Bearer test-key")
	if _, err := provider.Authenticate(req1.Context(), req1); err != nil {
		t.Fatalf("first Authenticate() error = %v, want nil", err)
	}

	req2 := httptest.NewRequest("GET", "/v1/models", nil)
	req2.Header.Set("Authorization", "Bearer test-key")
	if _, err := provider.Authenticate(req2.Context(), req2); err != nil {
		t.Fatalf("second Authenticate() error = %v, want nil", err)
	}

	req3 := httptest.NewRequest("GET", "/v1/models", nil)
	req3.Header.Set("Authorization", "Bearer test-key")
	_, err := provider.Authenticate(req3.Context(), req3)
	if err == nil {
		t.Fatal("third Authenticate() error = nil, want rate limit")
	}
	if !sdkaccess.IsAuthErrorCode(err, sdkaccess.AuthErrorCodeRateLimited) {
		t.Fatalf("third Authenticate() code = %q, want %q", err.Code, sdkaccess.AuthErrorCodeRateLimited)
	}
	if err.HTTPStatusCode() != 429 {
		t.Fatalf("third Authenticate() status = %d, want 429", err.HTTPStatusCode())
	}
}
