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

type funcTestProvider struct {
	id   string
	auth func(context.Context, *http.Request) (*Result, *AuthError)
}

func (p funcTestProvider) Identifier() string { return p.id }

func (p funcTestProvider) Authenticate(ctx context.Context, r *http.Request) (*Result, *AuthError) {
	return p.auth(ctx, r)
}

func TestManagerAuthenticateDeniedErrorEndsProviderChainAndPreservesStatus(t *testing.T) {
	laterCalled := false
	mgr := NewManager()
	mgr.SetProviders([]Provider{
		funcTestProvider{
			id: "denier",
			auth: func(context.Context, *http.Request) (*Result, *AuthError) {
				return nil, NewDeniedError("quota exhausted", http.StatusTooManyRequests)
			},
		},
		funcTestProvider{
			id: "fallback",
			auth: func(context.Context, *http.Request) (*Result, *AuthError) {
				laterCalled = true
				return &Result{Provider: "fallback", Principal: "user-1"}, nil
			},
		},
	})

	req, errReq := http.NewRequest(http.MethodPost, "http://example.test/v1/responses", nil)
	if errReq != nil {
		t.Fatalf("NewRequest() error = %v", errReq)
	}
	res, authErr := mgr.Authenticate(context.Background(), req)
	if res != nil {
		t.Fatalf("Authenticate() result = %#v, want nil", res)
	}
	if !IsAuthErrorCode(authErr, AuthErrorCodeDenied) {
		t.Fatalf("Authenticate() error = %v, want code %s", authErr, AuthErrorCodeDenied)
	}
	if authErr.HTTPStatusCode() != http.StatusTooManyRequests {
		t.Fatalf("HTTPStatusCode() = %d, want %d", authErr.HTTPStatusCode(), http.StatusTooManyRequests)
	}
	if authErr.Message != "quota exhausted" {
		t.Fatalf("Message = %q, want %q", authErr.Message, "quota exhausted")
	}
	if laterCalled {
		t.Fatal("expected later provider not to be called after AuthErrorCodeDenied")
	}
}
