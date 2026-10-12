package auth

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func poolTestConfig() config.ClientConfig {
	reviewer, personal := []string{"reviewer", "shared"}, []string{"personal", "shared"}
	return config.ClientConfig{
		AccountPools: &[]config.ClientAccountPool{
			{Name: "reviewer", AuthIDs: []string{"reviewer"}},
			{Name: "shared", AuthIDs: []string{"shared"}},
			{Name: "personal", AuthIDs: []string{"personal"}},
		},
		KeyScopes: &[]config.ClientKeyScope{
			{KeyPrefix: "review-", Providers: []string{"claude"}, Pools: &reviewer},
			{KeyPrefix: "personal-", Providers: []string{"claude"}, Pools: &personal},
		},
	}
}

func poolTestContext(t *testing.T, cfg config.ClientConfig, key string) context.Context {
	t.Helper()
	policy, err := ResolveAccountPools(cfg, key)
	if err != nil {
		t.Fatal(err)
	}
	return WithAccountPools(context.Background(), policy)
}

// poolTestManager registers three Claude credentials. The one only the
// personal pool lists has the highest priority and the shared one the next,
// so pool order is visible wherever it beats priority.
func poolTestManager(t *testing.T, selector Selector) (*Manager, *authFallbackExecutor, string) {
	t.Helper()
	m := NewManager(nil, selector, nil)
	m.SetRetryConfig(0, 0, 0)
	executor := &authFallbackExecutor{id: "claude", executeErrors: make(map[string]error)}
	m.RegisterExecutor(executor)
	model := "pool-" + t.Name()
	for _, id := range []string{"reviewer", "personal", "shared"} {
		priority := "0"
		if id == "personal" {
			priority = "1000"
		}
		if id == "shared" {
			priority = "100"
		}
		a := &Auth{ID: id, Provider: "claude", Attributes: map[string]string{"priority": priority}}
		if _, err := m.Register(context.Background(), a); err != nil {
			t.Fatal(err)
		}
		registry.GetGlobalRegistry().RegisterClient(id, "claude", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	return m, executor, model
}

func TestAccountPoolsMembershipAndFailClosed(t *testing.T) {
	cfg := poolTestConfig()
	p, err := ResolveAccountPools(cfg, "review-key")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id, provider string
		want         bool
	}{
		{"reviewer", "claude", true}, {"shared", "claude", true}, {"personal", "claude", false}, {"reviewer", "codex", false},
	} {
		if got := p.Allows(&Auth{ID: tc.id, Provider: tc.provider}); got != tc.want {
			t.Fatalf("membership %v = %t", tc, got)
		}
	}
	unknown := []string{"missing"}
	(*cfg.KeyScopes)[0].Pools = &unknown
	if _, err = ResolveAccountPools(cfg, "review-key"); err == nil {
		t.Fatal("unknown pool must fail")
	}
	repeated := []string{"reviewer", "reviewer"}
	(*cfg.KeyScopes)[0].Pools = &repeated
	if _, err = ResolveAccountPools(cfg, "review-key"); err == nil {
		t.Fatal("repeated pool must fail")
	}
	empty := []string{}
	(*cfg.KeyScopes)[0].Pools = &empty
	p, err = ResolveAccountPools(cfg, "review-key")
	if err != nil || p == nil || p.Allows(&Auth{ID: "reviewer", Provider: "claude"}) {
		t.Fatal("empty chain must deny")
	}
	(*cfg.KeyScopes)[0].Pools = nil
	if p, err = ResolveAccountPools(cfg, "review-key"); err != nil || p != nil {
		t.Fatal("omitted pools must preserve routing")
	}
	if p, err = ResolveAccountPools(cfg, "unscoped-key"); err != nil || p != nil {
		t.Fatal("a key no scope applies to must stay unrestricted")
	}
	chain := []string{"reviewer"}
	noProviders := poolTestConfig()
	(*noProviders.KeyScopes)[0] = config.ClientKeyScope{KeyPrefix: "review-", Pools: &chain}
	if _, err = ResolveAccountPools(noProviders, "review-key"); err == nil {
		t.Fatal("a pooled scope without providers must fail")
	}
}

func TestAccountPoolsDedicatedThenSharedAndNoPersonalFallback(t *testing.T) {
	m, e, model := poolTestManager(t, &FillFirstSelector{})
	ctx := poolTestContext(t, poolTestConfig(), "review-key")
	req := cliproxyexecutor.Request{Model: model}
	resp, err := m.Execute(ctx, []string{"claude"}, req, cliproxyexecutor.Options{})
	if err != nil || string(resp.Payload) != "reviewer" {
		t.Fatalf("first choice: %s %v", resp.Payload, err)
	}
	e.executeCalls = nil
	e.executeErrors["reviewer"] = &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exceeded"}
	resp, err = m.Execute(ctx, []string{"claude"}, req, cliproxyexecutor.Options{})
	if err != nil || string(resp.Payload) != "shared" || !slices.Equal(e.executeCalls, []string{"reviewer", "shared"}) {
		t.Fatalf("fallback: %s %v calls=%v", resp.Payload, err, e.executeCalls)
	}
	e.executeCalls = nil
	e.executeErrors["shared"] = &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exceeded"}
	_, err = m.Execute(ctx, []string{"claude"}, req, cliproxyexecutor.Options{})
	if err == nil || slices.Contains(e.executeCalls, "personal") {
		t.Fatalf("escaped pool: err=%v calls=%v", err, e.executeCalls)
	}
	personal := poolTestContext(t, poolTestConfig(), "personal-key")
	resp, err = m.Execute(personal, []string{"claude"}, req, cliproxyexecutor.Options{})
	if err != nil || string(resp.Payload) != "personal" {
		t.Fatalf("personal route: %s %v", resp.Payload, err)
	}
}

func TestAccountPoolsPinnedAuthAndModelAvailability(t *testing.T) {
	m, _, model := poolTestManager(t, &RoundRobinSelector{})
	ctx := poolTestContext(t, poolTestConfig(), "review-key")
	_, err := m.SelectAuth(ctx, "claude", model, cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: "personal"}})
	if err == nil {
		t.Fatal("pinned personal credential escaped pool")
	}
	registry.GetGlobalRegistry().UnregisterClient("reviewer")
	a, err := m.SelectAuth(ctx, "claude", model, cliproxyexecutor.Options{})
	if err != nil || a.ID != "shared" {
		t.Fatalf("model fallback: %v %v", a, err)
	}
}

func TestAccountPoolsAffinityIsolationAndRevocation(t *testing.T) {
	selector := NewSessionAffinitySelector(&FillFirstSelector{})
	t.Cleanup(selector.Stop)
	m, _, model := poolTestManager(t, selector)
	cfg := poolTestConfig()
	opts := cliproxyexecutor.Options{Headers: http.Header{"X-Session-Id": []string{"identical-session"}}}
	for _, tc := range []struct{ key, want string }{{"review-key", "reviewer"}, {"personal-key", "personal"}} {
		a, err := m.SelectAuth(poolTestContext(t, cfg, tc.key), "claude", model, opts)
		if err != nil || a.ID != tc.want {
			t.Fatalf("affinity %s: %v %v", tc.key, a, err)
		}
	}
	(*cfg.AccountPools)[0].AuthIDs = nil
	a, err := m.SelectAuth(poolTestContext(t, cfg, "review-key"), "claude", model, opts)
	if err != nil || a.ID != "shared" {
		t.Fatalf("revocation: %v %v", a, err)
	}
}

// Two keys that share a pool member and a session ID keep separate session
// bindings: one key's binding never steers the other key's selection.
func TestAccountPoolsAffinityIsolatedByKey(t *testing.T) {
	selector := NewSessionAffinitySelector(&FillFirstSelector{})
	t.Cleanup(selector.Stop)
	m := NewManager(nil, selector, nil)
	m.RegisterExecutor(&authFallbackExecutor{id: "claude", executeErrors: make(map[string]error)})
	model := "pool-" + t.Name()
	for _, id := range []string{"pool-x", "pool-y"} {
		if _, err := m.Register(context.Background(), &Auth{ID: id, Provider: "claude"}); err != nil {
			t.Fatal(err)
		}
		registry.GetGlobalRegistry().RegisterClient(id, "claude", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	onlyY, both := []string{"only-y"}, []string{"both"}
	cfg := config.ClientConfig{
		AccountPools: &[]config.ClientAccountPool{
			{Name: "only-y", AuthIDs: []string{"pool-y"}},
			{Name: "both", AuthIDs: []string{"pool-x", "pool-y"}},
		},
		KeyScopes: &[]config.ClientKeyScope{
			{KeyPrefix: "a-", Providers: []string{"claude"}, Pools: &onlyY},
			{KeyPrefix: "b-", Providers: []string{"claude"}, Pools: &both},
		},
	}
	opts := cliproxyexecutor.Options{Headers: http.Header{"X-Session-Id": []string{"shared-session"}}}
	if a, err := m.SelectAuth(poolTestContext(t, cfg, "a-key"), "claude", model, opts); err != nil || a.ID != "pool-y" {
		t.Fatalf("key a: %v %v", a, err)
	}
	if a, err := m.SelectAuth(poolTestContext(t, cfg, "b-key"), "claude", model, opts); err != nil || a.ID != "pool-x" {
		t.Fatalf("key b followed key a's session binding: %v %v", a, err)
	}
}

func TestAccountPoolsSharedCooldownAndRecovery(t *testing.T) {
	selector := NewSessionAffinitySelector(&FillFirstSelector{})
	t.Cleanup(selector.Stop)
	m, _, model := poolTestManager(t, selector)
	cfg := poolTestConfig()
	setUnavailable := func(id string, unavailable bool) {
		t.Helper()
		a, _ := m.GetByID(id)
		a.Unavailable = unavailable
		a.NextRetryAfter = time.Time{}
		if unavailable {
			a.NextRetryAfter = time.Now().Add(time.Hour)
		}
		if _, err := m.Update(context.Background(), a); err != nil {
			t.Fatal(err)
		}
	}
	session := cliproxyexecutor.Options{Headers: http.Header{"X-Session-Id": []string{"cooldown-session"}}}
	setUnavailable("reviewer", true)
	setUnavailable("personal", true)
	for _, key := range []string{"review-key", "personal-key"} {
		a, err := m.SelectAuth(poolTestContext(t, cfg, key), "claude", model, session)
		if err != nil || a.ID != "shared" {
			t.Fatalf("shared %s: %v %v", key, a, err)
		}
	}
	setUnavailable("shared", true)
	for _, key := range []string{"review-key", "personal-key"} {
		if _, err := m.SelectAuth(poolTestContext(t, cfg, key), "claude", model, session); err == nil {
			t.Fatal("shared cooldown was not shared")
		}
	}
	// Pool order wins over session affinity: once the dedicated credential
	// recovers, the session bound to the shared one returns to it.
	setUnavailable("shared", false)
	setUnavailable("reviewer", false)
	if a, err := m.SelectAuth(poolTestContext(t, cfg, "review-key"), "claude", model, session); err != nil || a.ID != "reviewer" {
		t.Fatalf("recovered dedicated credential: %v %v", a, err)
	}
}

func TestAccountPoolsStreamAndCount(t *testing.T) {
	m, e, model := poolTestManager(t, &FillFirstSelector{})
	ctx := poolTestContext(t, poolTestConfig(), "review-key")
	req := cliproxyexecutor.Request{Model: model}
	resp, err := m.ExecuteCount(ctx, []string{"claude"}, req, cliproxyexecutor.Options{})
	if err != nil || string(resp.Payload) != "reviewer" {
		t.Fatalf("count: %s %v", resp.Payload, err)
	}
	e.streamFirstErrors = map[string]error{"reviewer": &Error{HTTPStatus: 429, Message: "quota exceeded"}}
	stream, err := m.ExecuteStream(ctx, []string{"claude"}, req, cliproxyexecutor.Options{Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	var payload string
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
		payload += string(chunk.Payload)
	}
	if payload != "shared" || slices.Contains(e.streamCalls, "personal") {
		t.Fatalf("stream: %s calls=%v", payload, e.streamCalls)
	}
}

func TestAccountPoolsZeroWeightFallsBack(t *testing.T) {
	for _, affinity := range []bool{false, true} {
		var selector Selector = &WeightedRoundRobinSelector{}
		if affinity {
			s := NewSessionAffinitySelector(selector)
			t.Cleanup(s.Stop)
			selector = s
		}
		m, _, model := poolTestManager(t, selector)
		a, _ := m.GetByID("reviewer")
		a.Attributes["weight"] = "0"
		if _, err := m.Update(context.Background(), a); err != nil {
			t.Fatal(err)
		}
		selected, err := m.SelectAuth(poolTestContext(t, poolTestConfig(), "review-key"), "claude", model, cliproxyexecutor.Options{})
		if err != nil || selected.ID != "shared" {
			t.Fatalf("zero weight fallback: %v %v", selected, err)
		}
	}
}

func TestAccountPoolsHomeFailsClosed(t *testing.T) {
	m := NewManager(nil, nil, nil)
	_, err := m.pickHomeDispatchSelection(poolTestContext(t, poolTestConfig(), "review-key"), "model", cliproxyexecutor.Options{})
	var authErr *Error
	if !errors.As(err, &authErr) || authErr.Code != "account_pool_unavailable" {
		t.Fatalf("Home dispatch with a pool policy: %v, want account_pool_unavailable", err)
	}
}

func TestAccountPoolsFileMembershipAndAuthKind(t *testing.T) {
	cfg := poolTestConfig()
	(*cfg.AccountPools)[0] = config.ClientAccountPool{Name: "reviewer", AuthFiles: []string{"reviewer.json"}, AuthKind: AuthKindOAuth}
	p, err := ResolveAccountPools(cfg, "review-key")
	if err != nil {
		t.Fatal(err)
	}
	a := &Auth{ID: "stable-id", Provider: "claude", FileName: "/auths/reviewer.json", Metadata: map[string]any{"access_token": "test-oauth-token"}}
	if !p.Allows(a) {
		t.Fatal("OAuth file membership missing")
	}
	a.Attributes = map[string]string{AttributeAPIKey: "test-api-key"}
	if p.Allows(a) {
		t.Fatal("API credential crossed OAuth-only pool")
	}
	(*cfg.AccountPools)[0].AuthFiles[0] = "changed.json"
	a.Attributes = nil
	if !p.Allows(a) {
		t.Fatal("policy snapshot changed under caller")
	}
}
