package configaccess

import (
	"context"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestProviderCredentialGroupsMetadata(t *testing.T) {
	provider := newProvider("", []string{"client-a", "client-b"}, map[string][]string{
		"client-a": {" team-a ", "team-a", "team-b", ""},
		"unknown":  {"ignored"},
	})

	request := httptest.NewRequest("GET", "/v1/models", nil)
	request.Header.Set("Authorization", "Bearer client-a")
	result, authErr := provider.Authenticate(context.Background(), request)
	if authErr != nil {
		t.Fatalf("Authenticate() error = %v", authErr)
	}
	want := map[string]string{"source": "authorization", "credential-groups": "team-a,team-b"}
	if !reflect.DeepEqual(result.Metadata, want) {
		t.Fatalf("Metadata = %#v, want %#v", result.Metadata, want)
	}
}

func TestProviderUnscopedKeyKeepsLegacyBehavior(t *testing.T) {
	provider := newProvider("", []string{"client-a", "client-b"}, map[string][]string{
		"client-a": {"team-a"},
	})

	request := httptest.NewRequest("GET", "/v1/models", nil)
	request.Header.Set("X-Api-Key", "client-b")
	result, authErr := provider.Authenticate(context.Background(), request)
	if authErr != nil {
		t.Fatalf("Authenticate() error = %v", authErr)
	}
	if _, exists := result.Metadata["credential-groups"]; exists {
		t.Fatalf("unscoped key unexpectedly received credential groups: %#v", result.Metadata)
	}
}

func TestProviderExplicitEmptyGroupsRemainRestricted(t *testing.T) {
	provider := newProvider("", []string{"client-a"}, map[string][]string{
		"client-a": {"", "   "},
	})

	request := httptest.NewRequest("GET", "/v1/models", nil)
	request.Header.Set("Authorization", "Bearer client-a")
	result, authErr := provider.Authenticate(context.Background(), request)
	if authErr != nil {
		t.Fatalf("Authenticate() error = %v", authErr)
	}
	groups, exists := result.Metadata["credential-groups"]
	if !exists || groups != "" {
		t.Fatalf("credential-groups = %q, exists = %t; want explicit empty restriction", groups, exists)
	}
}
