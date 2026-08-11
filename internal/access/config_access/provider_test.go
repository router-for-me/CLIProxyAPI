package configaccess

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestProviderAuthenticateIncludesClientKeyMetadataForAllCredentialSources(t *testing.T) {
	const apiKey = "client-key"
	provider := newProvider("", []string{apiKey}, map[string]sdkconfig.ClientAPIKeyMetadata{
		" client-key ": {ID: " tenant-a ", Alias: " Team A "},
	})

	tests := []struct {
		name       string
		url        string
		headerName string
		header     string
		wantSource string
	}{
		{name: "authorization", url: "http://example.test/v1/models", headerName: "Authorization", header: "Bearer " + apiKey, wantSource: "authorization"},
		{name: "google header", url: "http://example.test/v1/models", headerName: "X-Goog-Api-Key", header: apiKey, wantSource: "x-goog-api-key"},
		{name: "anthropic header", url: "http://example.test/v1/models", headerName: "X-Api-Key", header: apiKey, wantSource: "x-api-key"},
		{name: "query key", url: "http://example.test/v1/models?key=" + apiKey, wantSource: "query-key"},
		{name: "query auth token", url: "http://example.test/v1/models?auth_token=" + apiKey, wantSource: "query-auth-token"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, test.url, nil)
			if test.headerName != "" {
				req.Header.Set(test.headerName, test.header)
			}

			result, authErr := provider.Authenticate(context.Background(), req)
			if authErr != nil {
				t.Fatalf("Authenticate() error = %v", authErr)
			}
			if result.Principal != apiKey {
				t.Fatalf("Principal = %q, want original API key", result.Principal)
			}
			if got := result.Metadata["source"]; got != test.wantSource {
				t.Fatalf("source = %q, want %q", got, test.wantSource)
			}
			if got := result.Metadata[sdkaccess.MetadataClientKeyID]; got != "tenant-a" {
				t.Fatalf("client key ID = %q, want tenant-a", got)
			}
			if got := result.Metadata[sdkaccess.MetadataClientKeyAlias]; got != "Team A" {
				t.Fatalf("client key alias = %q, want Team A", got)
			}
		})
	}
}

func TestProviderAuthenticateUsesFallbackIDForLegacyKey(t *testing.T) {
	provider := newProvider("", []string{"legacy-key"}, nil)
	req := httptest.NewRequest(http.MethodGet, "http://example.test/v1/models", nil)
	req.Header.Set("Authorization", "Bearer legacy-key")

	result, authErr := provider.Authenticate(context.Background(), req)
	if authErr != nil {
		t.Fatalf("Authenticate() error = %v", authErr)
	}
	if got, want := result.Metadata[sdkaccess.MetadataClientKeyID], sdkaccess.FallbackClientKeyID("legacy-key"); got != want {
		t.Fatalf("client key ID = %q, want %q", got, want)
	}
	if _, exists := result.Metadata[sdkaccess.MetadataClientKeyAlias]; exists {
		t.Fatal("legacy key unexpectedly has an alias")
	}
}

func TestProviderDuplicateRawKeyKeepsFirstStableID(t *testing.T) {
	provider := newProvider("", []string{"client-key", " client-key "}, map[string]sdkconfig.ClientAPIKeyMetadata{
		"client-key": {ID: "tenant-a", Alias: "Team A"},
	})
	req := httptest.NewRequest(http.MethodGet, "http://example.test/v1/models", nil)
	req.Header.Set("Authorization", "Bearer client-key")

	result, authErr := provider.Authenticate(context.Background(), req)
	if authErr != nil {
		t.Fatalf("Authenticate() error = %v", authErr)
	}
	if got := result.Metadata[sdkaccess.MetadataClientKeyID]; got != "tenant-a" {
		t.Fatalf("client key ID = %q, want tenant-a", got)
	}
	if len(provider.keys) != 1 {
		t.Fatalf("provider key count = %d, want 1", len(provider.keys))
	}
}

func TestProviderDisabledKeyDoesNotAuthenticate(t *testing.T) {
	provider := newProvider("", []string{"active-key", "disabled-key"}, map[string]sdkconfig.ClientAPIKeyMetadata{
		"disabled-key": {Disabled: true},
	})

	disabledReq := httptest.NewRequest(http.MethodGet, "http://example.test/v1/models", nil)
	disabledReq.Header.Set("Authorization", "Bearer disabled-key")
	if _, authErr := provider.Authenticate(context.Background(), disabledReq); !sdkaccess.IsAuthErrorCode(authErr, sdkaccess.AuthErrorCodeInvalidCredential) {
		t.Fatalf("disabled key error = %#v, want invalid credential", authErr)
	}

	missingReq := httptest.NewRequest(http.MethodGet, "http://example.test/v1/models", nil)
	if _, authErr := provider.Authenticate(context.Background(), missingReq); !sdkaccess.IsAuthErrorCode(authErr, sdkaccess.AuthErrorCodeNoCredentials) {
		t.Fatalf("missing key error = %#v, want no credentials", authErr)
	}
}

func TestRegisterAllDisabledKeysKeepsDenyCapableProvider(t *testing.T) {
	sdkaccess.UnregisterProvider(sdkaccess.AccessProviderTypeConfigAPIKey)
	defer sdkaccess.UnregisterProvider(sdkaccess.AccessProviderTypeConfigAPIKey)

	Register(&sdkconfig.SDKConfig{
		APIKeys: []string{"disabled-key"},
		APIKeyMetadata: map[string]sdkconfig.ClientAPIKeyMetadata{
			"disabled-key": {Disabled: true},
		},
	})

	var registered sdkaccess.Provider
	for _, candidate := range sdkaccess.RegisteredProviders() {
		if candidate.Identifier() == sdkaccess.DefaultAccessProviderName {
			registered = candidate
			break
		}
	}
	if registered == nil {
		t.Fatal("all-disabled configuration unregistered the inline access provider")
	}

	missingReq := httptest.NewRequest(http.MethodGet, "http://example.test/v1/models", nil)
	if _, authErr := registered.Authenticate(context.Background(), missingReq); !sdkaccess.IsAuthErrorCode(authErr, sdkaccess.AuthErrorCodeNoCredentials) {
		t.Fatalf("missing key error = %#v, want no credentials", authErr)
	}

	disabledReq := httptest.NewRequest(http.MethodGet, "http://example.test/v1/models", nil)
	disabledReq.Header.Set("Authorization", "Bearer disabled-key")
	if _, authErr := registered.Authenticate(context.Background(), disabledReq); !sdkaccess.IsAuthErrorCode(authErr, sdkaccess.AuthErrorCodeInvalidCredential) {
		t.Fatalf("disabled key error = %#v, want invalid credential", authErr)
	}
}
