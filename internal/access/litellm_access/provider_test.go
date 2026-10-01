package litellmaccess

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

type fakePG struct {
	mu        sync.Mutex
	byHash    map[string]*store.APIKey
	upserted  []store.APIKey
	upsertErr error
}

func (f *fakePG) LookupByHash(_ context.Context, hash string) (*store.APIKey, *store.Policy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if k, ok := f.byHash[hash]; ok {
		return k, nil, nil
	}
	return nil, nil, store.ErrAPIKeyNotFound
}

func (f *fakePG) Upsert(_ context.Context, k store.APIKey, _ *store.Policy) (store.APIKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.upsertErr != nil {
		return store.APIKey{}, f.upsertErr
	}
	f.upserted = append(f.upserted, k)
	return k, nil
}

type fakeLite struct {
	byID   map[string]*store.LiteLLMKey
	byHash map[string]*store.LiteLLMKey
}

func (f *fakeLite) LookupByID(_ context.Context, id string) (*store.LiteLLMKey, *store.LiteLLMPolicy, error) {
	if k, ok := f.byID[id]; ok {
		return k, nil, nil
	}
	return nil, nil, store.ErrLiteLLMKeyNotFound
}

func (f *fakeLite) LookupByHash(_ context.Context, hash string) (*store.LiteLLMKey, error) {
	if k, ok := f.byHash[hash]; ok {
		return k, nil
	}
	return nil, store.ErrLiteLLMKeyNotFound
}

type fakeLogs struct{ ch chan store.OnTheFlyLogEvent }

func (f *fakeLogs) RecordOnTheFly(_ context.Context, e store.OnTheFlyLogEvent) error {
	f.ch <- e
	return nil
}

func (f *fakeLogs) wait(t *testing.T) store.OnTheFlyLogEvent {
	t.Helper()
	select {
	case e := <-f.ch:
		return e
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for on-the-fly log event")
		return store.OnTheFlyLogEvent{}
	}
}

func secret() string { return "sk-litellm-abcdefghijklmnop" }

func newProvider(t *testing.T, pg PGLookup, lite LiteLLMLookup, logs LogStore, srv *httptest.Server, ttl time.Duration) *Provider {
	t.Helper()
	p := New(pg, lite, logs, func(context.Context) (Settings, error) {
		return Settings{Enabled: true, BaseURL: srv.URL, CacheTTL: ttl, Timeout: 2 * time.Second}, nil
	}, srv.Client())
	if p == nil {
		t.Fatal("New returned nil")
	}
	return p
}

func TestAuthenticateValidMatchedSyncsAndLogs(t *testing.T) {
	hash := store.HashSecret(secret())
	hashID := "token-hash-1"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/key/info" {
			t.Errorf("path = %q; want /key/info", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+secret() {
			t.Errorf("auth = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"key":"` + hashID + `","info":{"user_id":"u1","key_alias":"team-a","spend":1.5}}`))
	}))
	defer srv.Close()

	pg := &fakePG{byHash: map[string]*store.APIKey{}}
	lite := &fakeLite{byID: map[string]*store.LiteLLMKey{hashID: {
		ID: hashID, Name: "prod", KeyAlias: "team-a", UserID: "u1",
		Metadata: map[string]any{"team": "core"},
	}}}
	logs := &fakeLogs{ch: make(chan store.OnTheFlyLogEvent, 4)}
	p := newProvider(t, pg, lite, logs, srv, time.Minute)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+secret())
	res, authErr := p.Authenticate(context.Background(), req)
	if authErr != nil {
		t.Fatalf("Authenticate err: %v", authErr)
	}
	if res.Provider != Identifier || res.Principal != secret() {
		t.Fatalf("result = %+v", res)
	}
	if res.Metadata["key_id"] != hashID || res.Metadata["user_id"] != "u1" {
		t.Fatalf("metadata = %+v", res.Metadata)
	}
	if len(pg.upserted) != 1 {
		t.Fatalf("upserts = %d; want 1", len(pg.upserted))
	}
	up := pg.upserted[0]
	if up.ID != hashID || up.KeyHash != hash || up.UserID != "u1" || up.Status != store.APIKeyStatusActive {
		t.Fatalf("upserted key = %+v", up)
	}
	if _, ok := up.Metadata["litellm_info"]; !ok {
		t.Fatalf("metadata missing litellm_info: %+v", up.Metadata)
	}
	ev := logs.wait(t)
	if ev.Outcome != store.OnTheFlyOutcomeSynced || ev.KeyID != hashID || ev.KeyPrefix == "" {
		t.Fatalf("log event = %+v", ev)
	}
}

func TestAuthenticateValidUnmatchedRejects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"key":"unknown-token","info":{"user_id":"u9"}}`))
	}))
	defer srv.Close()
	pg := &fakePG{byHash: map[string]*store.APIKey{}}
	lite := &fakeLite{}
	logs := &fakeLogs{ch: make(chan store.OnTheFlyLogEvent, 4)}
	p := newProvider(t, pg, lite, logs, srv, time.Minute)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+secret())
	_, authErr := p.Authenticate(context.Background(), req)
	if authErr == nil || authErr.Code != "invalid_credential" {
		t.Fatalf("authErr = %+v; want invalid_credential", authErr)
	}
	if len(pg.upserted) != 0 {
		t.Fatalf("unexpected upserts: %d", len(pg.upserted))
	}
	if ev := logs.wait(t); ev.Outcome != store.OnTheFlyOutcomeUnmatched {
		t.Fatalf("log outcome = %q; want unmatched", ev.Outcome)
	}
}

func TestAuthenticateInvalidKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid"}`))
	}))
	defer srv.Close()
	pg := &fakePG{byHash: map[string]*store.APIKey{}}
	logs := &fakeLogs{ch: make(chan store.OnTheFlyLogEvent, 4)}
	p := newProvider(t, pg, &fakeLite{}, logs, srv, time.Minute)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+secret())
	_, authErr := p.Authenticate(context.Background(), req)
	if authErr == nil || authErr.Code != "invalid_credential" {
		t.Fatalf("authErr = %+v; want invalid_credential", authErr)
	}
	if ev := logs.wait(t); ev.Outcome != store.OnTheFlyOutcomeInvalid {
		t.Fatalf("log outcome = %q; want invalid", ev.Outcome)
	}
}

func TestAuthenticateLocalFirstSkipsRemote(t *testing.T) {
	hash := store.HashSecret(secret())
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	pg := &fakePG{byHash: map[string]*store.APIKey{hash: {
		ID: "local-1", KeyHash: hash, Status: store.APIKeyStatusActive, UserID: "u-local",
	}}}
	logs := &fakeLogs{ch: make(chan store.OnTheFlyLogEvent, 4)}
	p := newProvider(t, pg, &fakeLite{}, logs, srv, time.Minute)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+secret())
	res, authErr := p.Authenticate(context.Background(), req)
	if authErr != nil {
		t.Fatalf("Authenticate err: %v", authErr)
	}
	if res.Metadata["key_id"] != "local-1" {
		t.Fatalf("key_id = %q; want local-1", res.Metadata["key_id"])
	}
	if calls != 0 {
		t.Fatalf("remote calls = %d; want 0 (local-first)", calls)
	}
}

func TestAuthenticateDisabledIsNotHandled(t *testing.T) {
	pg := &fakePG{byHash: map[string]*store.APIKey{}}
	p := New(pg, &fakeLite{}, nil, func(context.Context) (Settings, error) {
		return Settings{Enabled: false, BaseURL: "http://example.invalid"}, nil
	}, nil)
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+secret())
	_, authErr := p.Authenticate(context.Background(), req)
	if authErr == nil || authErr.Code != "not_handled" {
		t.Fatalf("authErr = %+v; want not_handled", authErr)
	}
}

func TestAuthenticateNoCredentials(t *testing.T) {
	pg := &fakePG{byHash: map[string]*store.APIKey{}}
	p := New(pg, &fakeLite{}, nil, func(context.Context) (Settings, error) {
		return Settings{Enabled: true, BaseURL: "http://example.invalid"}, nil
	}, nil)
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	_, authErr := p.Authenticate(context.Background(), req)
	if authErr == nil || authErr.Code != "no_credentials" {
		t.Fatalf("authErr = %+v; want no_credentials", authErr)
	}
}
