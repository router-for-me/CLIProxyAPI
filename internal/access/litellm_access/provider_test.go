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
	mu          sync.Mutex
	byHash      map[string]*store.APIKey
	existingIDs map[string]bool
	upserted    []store.APIKey
	upsertErr   error
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
	if f.existingIDs != nil {
		f.existingIDs[k.ID] = true
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
	return newProviderWithTimeout(t, pg, lite, logs, srv, ttl, 2*time.Second)
}

func newProviderWithTimeout(t *testing.T, pg PGLookup, lite LiteLLMLookup, logs LogStore, srv *httptest.Server, ttl, timeout time.Duration) *Provider {
	t.Helper()
	p := New(pg, lite, logs, func(context.Context) (Settings, error) {
		return Settings{Enabled: true, BaseURL: srv.URL, CacheTTL: ttl, Timeout: timeout}, nil
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

func TestAuthenticateFallsThroughToSecondCandidate(t *testing.T) {
	hashID := "token-hash-2"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret() {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"key":"` + hashID + `","info":{"user_id":"u1"}}`))
	}))
	defer srv.Close()

	pg := &fakePG{byHash: map[string]*store.APIKey{}}
	lite := &fakeLite{byID: map[string]*store.LiteLLMKey{hashID: {
		ID: hashID, Name: "prod", UserID: "u1",
	}}}
	logs := &fakeLogs{ch: make(chan store.OnTheFlyLogEvent, 4)}
	p := newProvider(t, pg, lite, logs, srv, time.Minute)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer sk-not-litellm-000000000000")
	req.Header.Set("X-Api-Key", secret())
	res, authErr := p.Authenticate(context.Background(), req)
	if authErr != nil {
		t.Fatalf("Authenticate err: %v; want fall-through to second candidate", authErr)
	}
	if res.Metadata["key_id"] != hashID {
		t.Fatalf("key_id = %q; want %q", res.Metadata["key_id"], hashID)
	}
	if len(pg.upserted) != 1 {
		t.Fatalf("upserts = %d; want 1", len(pg.upserted))
	}
	// Two candidates are attempted: the first records an invalid outcome, the
	// second records a single synced outcome.
	var synced int
	for i := 0; i < 2; i++ {
		if ev := logs.wait(t); ev.Outcome == store.OnTheFlyOutcomeSynced {
			synced++
		}
	}
	if synced != 1 {
		t.Fatalf("synced events = %d; want 1", synced)
	}
}

func TestAuthenticatePlaintextNeverPersisted(t *testing.T) {
	hash := store.HashSecret(secret())
	hashID := "token-hash-3"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"key":"` + hashID + `","info":{"user_id":"u1"}}`))
	}))
	defer srv.Close()

	pg := &fakePG{byHash: map[string]*store.APIKey{}}
	lite := &fakeLite{byID: map[string]*store.LiteLLMKey{hashID: {
		ID: hashID, Name: "prod", UserID: "u1",
	}}}
	logs := &fakeLogs{ch: make(chan store.OnTheFlyLogEvent, 4)}
	p := newProvider(t, pg, lite, logs, srv, time.Minute)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+secret())
	if _, authErr := p.Authenticate(context.Background(), req); authErr != nil {
		t.Fatalf("Authenticate err: %v", authErr)
	}
	if len(pg.upserted) != 1 {
		t.Fatalf("upserts = %d; want 1", len(pg.upserted))
	}
	up := pg.upserted[0]
	if up.KeyHash != hash {
		t.Fatalf("KeyHash = %q; want %q", up.KeyHash, hash)
	}
	if up.KeyPrefix == secret() {
		t.Fatalf("plaintext persisted as KeyPrefix: %q", up.KeyPrefix)
	}
	if up.ID == secret() || up.Name == secret() || up.KeyAlias == secret() || up.UserID == secret() {
		t.Fatalf("plaintext persisted in APIKey: %+v", up)
	}
	ev := logs.wait(t)
	if ev.KeyPrefix == secret() {
		t.Fatalf("plaintext logged as KeyPrefix: %q", ev.KeyPrefix)
	}
	if ev.ErrorMessage == secret() || ev.UserID == secret() || ev.Source == secret() || ev.KeyID == secret() {
		t.Fatalf("plaintext logged in event: %+v", ev)
	}
}

func TestAuthenticateUpstream5xxReturnsInternalError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer srv.Close()

	pg := &fakePG{byHash: map[string]*store.APIKey{}}
	logs := &fakeLogs{ch: make(chan store.OnTheFlyLogEvent, 4)}
	p := newProvider(t, pg, &fakeLite{}, logs, srv, time.Minute)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+secret())
	_, authErr := p.Authenticate(context.Background(), req)
	if authErr == nil || authErr.Code != "internal_error" {
		t.Fatalf("authErr = %+v; want internal_error", authErr)
	}
	if len(pg.upserted) != 0 {
		t.Fatalf("upserts = %d; want 0", len(pg.upserted))
	}
	if ev := logs.wait(t); ev.Outcome != store.OnTheFlyOutcomeError {
		t.Fatalf("log outcome = %q; want error", ev.Outcome)
	}
}

func TestAuthenticateTransportTimeoutReturnsInternalError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	pg := &fakePG{byHash: map[string]*store.APIKey{}}
	logs := &fakeLogs{ch: make(chan store.OnTheFlyLogEvent, 4)}
	p := newProviderWithTimeout(t, pg, &fakeLite{}, logs, srv, time.Minute, 50*time.Millisecond)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+secret())
	_, authErr := p.Authenticate(context.Background(), req)
	if authErr == nil || authErr.Code != "internal_error" {
		t.Fatalf("authErr = %+v; want internal_error", authErr)
	}
	if len(pg.upserted) != 0 {
		t.Fatalf("upserts = %d; want 0", len(pg.upserted))
	}
	if ev := logs.wait(t); ev.Outcome != store.OnTheFlyOutcomeError {
		t.Fatalf("log outcome = %q; want error", ev.Outcome)
	}
}

func TestAuthenticateLookupByIDMissFallsBackToHash(t *testing.T) {
	hash := store.HashSecret(secret())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"key":"unknown-token","info":{"user_id":"u1"}}`))
	}))
	defer srv.Close()

	pg := &fakePG{byHash: map[string]*store.APIKey{}}
	// The local LiteLLM key is only reachable by hash, not by the token id
	// returned from /key/info, so the provider must fall back to LookupByHash.
	lite := &fakeLite{byHash: map[string]*store.LiteLLMKey{hash: {
		ID: "row-1", Name: "prod", UserID: "u1",
	}}}
	logs := &fakeLogs{ch: make(chan store.OnTheFlyLogEvent, 4)}
	p := newProvider(t, pg, lite, logs, srv, time.Minute)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+secret())
	res, authErr := p.Authenticate(context.Background(), req)
	if authErr != nil {
		t.Fatalf("Authenticate err: %v; want hash fallback success", authErr)
	}
	if res.Metadata["key_id"] != "unknown-token" {
		t.Fatalf("key_id = %q; want unknown-token", res.Metadata["key_id"])
	}
	if len(pg.upserted) != 1 {
		t.Fatalf("upserts = %d; want 1", len(pg.upserted))
	}
	if up := pg.upserted[0]; up.KeyHash != hash || up.UserID != "u1" {
		t.Fatalf("upserted key = %+v", up)
	}
	if ev := logs.wait(t); ev.Outcome != store.OnTheFlyOutcomeSynced {
		t.Fatalf("log outcome = %q; want synced", ev.Outcome)
	}
}

func TestAuthenticateNegativeCachePreventsSecondRemoteCall(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid"}`))
	}))
	defer srv.Close()

	pg := &fakePG{byHash: map[string]*store.APIKey{}}
	logs := &fakeLogs{ch: make(chan store.OnTheFlyLogEvent, 4)}
	p := newProvider(t, pg, &fakeLite{}, logs, srv, time.Minute)

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req.Header.Set("Authorization", "Bearer "+secret())
		_, authErr := p.Authenticate(context.Background(), req)
		if authErr == nil || authErr.Code != "invalid_credential" {
			t.Fatalf("call %d: authErr = %+v; want invalid_credential", i, authErr)
		}
	}
	if calls != 1 {
		t.Fatalf("server calls = %d; want 1 (negative cache)", calls)
	}
}

func TestAuthenticateUpsertOverwritesPreexistingRow(t *testing.T) {
	hash := store.HashSecret(secret())
	hashID := "token-hash-existing"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"key":"` + hashID + `","info":{"user_id":"u1"}}`))
	}))
	defer srv.Close()

	// The runtime api_keys table already holds a row with this id; the provider
	// must route through upsert semantics rather than failing on the conflict.
	pg := &fakePG{
		byHash:      map[string]*store.APIKey{},
		existingIDs: map[string]bool{hashID: true},
	}
	lite := &fakeLite{byID: map[string]*store.LiteLLMKey{hashID: {
		ID: hashID, Name: "prod", UserID: "u1",
	}}}
	logs := &fakeLogs{ch: make(chan store.OnTheFlyLogEvent, 4)}
	p := newProvider(t, pg, lite, logs, srv, time.Minute)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+secret())
	if _, authErr := p.Authenticate(context.Background(), req); authErr != nil {
		t.Fatalf("Authenticate err: %v", authErr)
	}
	if len(pg.upserted) != 1 {
		t.Fatalf("upserts = %d; want 1", len(pg.upserted))
	}
	if up := pg.upserted[0]; up.ID != hashID || up.KeyHash != hash {
		t.Fatalf("upserted key = %+v; want ID=%q KeyHash=%q", up, hashID, hash)
	}
}
