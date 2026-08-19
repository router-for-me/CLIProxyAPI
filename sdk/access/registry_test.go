package access

import (
	"context"
	"net/http"
	"testing"
)

type testProvider struct {
	id string
}

func (p testProvider) Identifier() string {
	return p.id
}

func (p testProvider) Authenticate(context.Context, *http.Request) (*Result, *AuthError) {
	return &Result{Provider: p.id, Principal: p.id}, nil
}

func TestRegisteredProvidersReturnsOnlyExclusiveProvider(t *testing.T) {
	UnregisterProvider("test-a")
	UnregisterProvider("test-b")
	ClearExclusiveProvider()
	defer UnregisterProvider("test-a")
	defer UnregisterProvider("test-b")
	defer ClearExclusiveProvider()

	RegisterProvider("test-a", testProvider{id: "test-a"})
	RegisterProvider("test-b", testProvider{id: "test-b"})
	SetExclusiveProvider("test-b")

	providers := RegisteredProviders()
	if len(providers) != 1 {
		t.Fatalf("RegisteredProviders() len = %d, want 1", len(providers))
	}
	if providers[0].Identifier() != "test-b" {
		t.Fatalf("RegisteredProviders()[0] = %q, want test-b", providers[0].Identifier())
	}
}

func TestRegisteredProvidersRestoresAllProvidersAfterExclusiveCleared(t *testing.T) {
	UnregisterProvider("test-a")
	UnregisterProvider("test-b")
	ClearExclusiveProvider()
	defer UnregisterProvider("test-a")
	defer UnregisterProvider("test-b")
	defer ClearExclusiveProvider()

	RegisterProvider("test-a", testProvider{id: "test-a"})
	RegisterProvider("test-b", testProvider{id: "test-b"})
	SetExclusiveProvider("test-b")
	ClearExclusiveProvider()

	providers := RegisteredProviders()
	if len(providers) != 2 {
		t.Fatalf("RegisteredProviders() len = %d, want 2", len(providers))
	}
	if providers[0].Identifier() != "test-a" || providers[1].Identifier() != "test-b" {
		t.Fatalf("RegisteredProviders() = [%q, %q], want [test-a, test-b]", providers[0].Identifier(), providers[1].Identifier())
	}
}

func TestRegisteredProvidersIgnoresStaleExclusiveProvider(t *testing.T) {
	UnregisterProvider("test-a")
	UnregisterProvider("missing")
	ClearExclusiveProvider()
	defer UnregisterProvider("test-a")
	defer ClearExclusiveProvider()

	RegisterProvider("test-a", testProvider{id: "test-a"})
	SetExclusiveProvider("missing")

	providers := RegisteredProviders()
	if len(providers) != 1 {
		t.Fatalf("RegisteredProviders() len = %d, want 1", len(providers))
	}
	if providers[0].Identifier() != "test-a" {
		t.Fatalf("RegisteredProviders()[0] = %q, want test-a", providers[0].Identifier())
	}
}
