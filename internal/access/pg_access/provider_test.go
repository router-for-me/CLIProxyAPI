package pgaccess

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// stubLookup is an in-memory Lookup used to exercise the provider without a
// real Postgres connection.
type stubLookup struct {
	keys map[string]*store.APIKey
	err  error
}

func (s *stubLookup) LookupByHash(_ context.Context, hash string) (*store.APIKey, *store.Policy, error) {
	if s.err != nil {
		return nil, nil, s.err
	}
	if k, ok := s.keys[hash]; ok {
		return k, nil, nil
	}
	return nil, nil, store.ErrAPIKeyNotFound
}

func newReq(t *testing.T, target string, headers map[string]string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return req
}

func newKey(id, secret, status string) (*store.APIKey, string) {
	hash := store.HashSecret(secret)
	return &store.APIKey{
		ID: id, Name: id, KeyHash: hash, KeyPrefix: "test", Status: status,
	}, secret
}

func TestProviderNilIsNoOp(t *testing.T) {
	p := New(nil)
	if p != nil {
		t.Fatal("New(nil) should return nil")
	}
}

func TestProviderAcceptsBearerHeader(t *testing.T) {
	key, secret := newKey("k1", "sk-secret-abcdef1234", store.APIKeyStatusActive)
	lookup := &stubLookup{keys: map[string]*store.APIKey{key.KeyHash: key}}
	p := New(lookup)
	req := newReq(t, "/", map[string]string{"Authorization": "Bearer " + secret})
	res, err := p.Authenticate(context.Background(), req)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if res.Principal != secret {
		t.Errorf("Principal = %q; want %q", res.Principal, secret)
	}
	if res.Metadata["key_id"] != "k1" {
		t.Errorf("key_id metadata = %q; want k1", res.Metadata["key_id"])
	}
}

func TestProviderAcceptsXAPIKeyHeader(t *testing.T) {
	key, secret := newKey("k2", "anthropic-style-secret-12345", store.APIKeyStatusActive)
	lookup := &stubLookup{keys: map[string]*store.APIKey{key.KeyHash: key}}
	p := New(lookup)
	req := newReq(t, "/", map[string]string{"X-Api-Key": secret})
	res, err := p.Authenticate(context.Background(), req)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if res.Principal != secret {
		t.Errorf("Principal = %q; want %q", res.Principal, secret)
	}
}

func TestProviderAcceptsQueryParams(t *testing.T) {
	key, secret := newKey("k3", "sk-query-secret-abcdef", store.APIKeyStatusActive)
	lookup := &stubLookup{keys: map[string]*store.APIKey{key.KeyHash: key}}
	p := New(lookup)
	req := newReq(t, "/?key="+secret, nil)
	res, err := p.Authenticate(context.Background(), req)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if res == nil {
		t.Fatal("expected Result, got nil")
	}
	if res.Metadata["source"] != "query-key" {
		t.Errorf("source = %q; want query-key", res.Metadata["source"])
	}
}

func TestProviderRejectsDisabledKey(t *testing.T) {
	key, secret := newKey("k4", "sk-disabled-abcdef123", store.APIKeyStatusDisabled)
	lookup := &stubLookup{keys: map[string]*store.APIKey{key.KeyHash: key}}
	p := New(lookup)
	req := newReq(t, "/", map[string]string{"Authorization": "Bearer " + secret})
	_, err := p.Authenticate(context.Background(), req)
	if err == nil {
		t.Fatal("expected invalid credential error for disabled key")
	}
	if !sdkaccess.IsAuthErrorCode(err, sdkaccess.AuthErrorCodeInvalidCredential) {
		t.Fatalf("expected InvalidCredential; got %v", err)
	}
}

func TestProviderReturnsNotHandledForUnknownCredentials(t *testing.T) {
	// Unknown credential should defer to the next provider via NotHandled
	// (so config-inline and pg-store can coexist).
	lookup := &stubLookup{keys: map[string]*store.APIKey{}}
	p := New(lookup)
	req := newReq(t, "/", map[string]string{"Authorization": "Bearer unknown-secret"})
	_, err := p.Authenticate(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for unknown credential")
	}
	if !sdkaccess.IsAuthErrorCode(err, sdkaccess.AuthErrorCodeNotHandled) {
		t.Fatalf("expected NotHandled for unknown credential; got %v", err)
	}
}

func TestProviderReturnsNoCredentialsWhenAbsent(t *testing.T) {
	lookup := &stubLookup{keys: map[string]*store.APIKey{}}
	p := New(lookup)
	req := newReq(t, "/", nil)
	_, err := p.Authenticate(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for missing credentials")
	}
	if !sdkaccess.IsAuthErrorCode(err, sdkaccess.AuthErrorCodeNoCredentials) {
		t.Fatalf("expected NoCredentials; got %v", err)
	}
}

func TestProviderInfraErrorBubbles(t *testing.T) {
	lookup := &stubLookup{err: errors.New("db down")}
	p := New(lookup)
	req := newReq(t, "/", map[string]string{"Authorization": "Bearer x"})
	_, err := p.Authenticate(context.Background(), req)
	if err == nil {
		t.Fatal("expected internal error")
	}
	if !sdkaccess.IsAuthErrorCode(err, sdkaccess.AuthErrorCodeInternal) {
		t.Fatalf("expected Internal; got %v", err)
	}
}

func TestProviderTriesAllCandidateSources(t *testing.T) {
	// When the Authorization header is present but doesn't match, the
	// provider should still try X-Api-Key and succeed if it matches a key.
	unknownBearer := "Bearer unknown-but-present"
	key, secret := newKey("k5", "sk-secondary-abc12", store.APIKeyStatusActive)
	lookup := &stubLookup{keys: map[string]*store.APIKey{key.KeyHash: key}}
	p := New(lookup)
	req := newReq(t, "/", map[string]string{
		"Authorization": unknownBearer,
		"X-Api-Key":     secret,
	})
	res, err := p.Authenticate(context.Background(), req)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil result for X-Api-Key fallback")
	}
}

func TestExtractBearerToken(t *testing.T) {
	cases := map[string]string{
		"Bearer abc": "abc",
		"bearer abc": "abc",
		"abc":        "abc",
		"":           "",
		"Basic xyz":  "Basic xyz", // non-bearer left untouched
	}
	for in, want := range cases {
		if got := extractBearerToken(in); got != want {
			t.Errorf("extractBearerToken(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestProviderRegisterUnregistersWhenNil(t *testing.T) {
	// Ensure Register(nil) does not panic and removes the provider from
	// the global registry. We cannot easily assert side effects on the
	// shared registry from a unit test, but we can at least exercise the
	// call path.
	Register(nil)
	// Registering with a real lookup should also be safe.
	key, _ := newKey("k6", "sk-register-abcdef1", store.APIKeyStatusActive)
	lookup := &stubLookup{keys: map[string]*store.APIKey{key.KeyHash: key}}
	Register(lookup)
}
