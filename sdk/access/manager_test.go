package access

import (
	"context"
	"net/http"
	"testing"
)

type mockProvider struct {
	id   string
	auth func(ctx context.Context, r *http.Request) (*Result, *AuthError)
}

func (m *mockProvider) Identifier() string { return m.id }
func (m *mockProvider) Authenticate(ctx context.Context, r *http.Request) (*Result, *AuthError) {
	return m.auth(ctx, r)
}

func TestManager_Authenticate(t *testing.T) {
	m := NewManager()

	// Test empty providers
	res, err := m.Authenticate(context.Background(), nil)
	if res != nil || err != nil {
		t.Error("expected nil result and error for empty manager")
	}

	p1 := &mockProvider{
		id: "p1",
		auth: func(ctx context.Context, r *http.Request) (*Result, *AuthError) {
			return nil, NewNotHandledError()
		},
	}
	p2 := &mockProvider{
		id: "p2",
		auth: func(ctx context.Context, r *http.Request) (*Result, *AuthError) {
			return &Result{Provider: "p2", Principal: "user"}, nil
		},
	}

	m.SetProviders([]Provider{p1, p2})

	// Test success
	res, err = m.Authenticate(context.Background(), nil)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if res == nil || res.Provider != "p2" {
		t.Errorf("expected result from p2, got %v", res)
	}

	// Test invalid
	p2.auth = func(ctx context.Context, r *http.Request) (*Result, *AuthError) {
		return nil, NewInvalidCredentialError()
	}
	res, err = m.Authenticate(context.Background(), nil)
	if err == nil || err.Code != AuthErrorCodeInvalidCredential {
		t.Errorf("expected invalid credential error, got %v", err)
	}

	// Test no credentials
	p2.auth = func(ctx context.Context, r *http.Request) (*Result, *AuthError) {
		return nil, NewNoCredentialsError()
	}
	res, err = m.Authenticate(context.Background(), nil)
	if err == nil || err.Code != AuthErrorCodeNoCredentials {
		t.Errorf("expected no credentials error, got %v", err)
	}
}

func TestManager_Providers(t *testing.T) {
	m := NewManager()
	p1 := &mockProvider{id: "p1"}
	m.SetProviders([]Provider{p1})

	providers := m.Providers()
	if len(providers) != 1 || providers[0].Identifier() != "p1" {
		t.Errorf("unexpected providers: %v", providers)
	}

	// Test snapshot
	m.SetProviders(nil)
	if len(providers) != 1 {
		t.Error("Providers() should return a snapshot")
	}
}
