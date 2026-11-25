package auth

import (
	"context"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestAuthSchedulingGroup(t *testing.T) {
	tests := []struct {
		name string
		auth *Auth
		want string
	}{
		{
			name: "openai compatibility compat name wins",
			auth: &Auth{Provider: "openai-compatibility-9router", Attributes: map[string]string{
				AttributeProviderKey:  "9router",
				AttributeProviderName: "ignored",
				"compat_name":         "9router",
				"base_url":            "http://9router:20128/v1",
			}},
			want: "openai-compatibility-9router|9router",
		},
		{
			name: "gemini group name is preserved",
			auth: &Auth{Provider: "gemini", Attributes: map[string]string{
				AttributeProviderName: "xu1142507",
			}},
			want: "gemini|xu1142507",
		},
		{
			name: "base url fallback",
			auth: &Auth{Provider: "gemini", Attributes: map[string]string{
				"base_url": "http://9router:20128",
			}},
			want: "gemini|http://9router:20128",
		},
		{
			name: "vertex compatibility separates by base url",
			auth: &Auth{Provider: "vertex", Attributes: map[string]string{
				AttributeProviderKey: "vertex",
				"base_url":           "HTTPS://Vertex-A.Example/",
			}},
			want: "vertex|https://vertex-a.example",
		},
		{
			name: "provider fallback",
			auth: &Auth{Provider: "vertex"},
			want: "vertex",
		},
		{
			name: "provider default endpoint",
			auth: &Auth{Provider: "claude"},
			want: "claude|https://api.anthropic.com",
		},
		{name: "nil auth", auth: nil, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := authSchedulingGroup(tt.auth); got != tt.want {
				t.Fatalf("authSchedulingGroup() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAuthSchedulingGroupCollapsesDefaultPorts(t *testing.T) {
	tests := []struct {
		name string
		a    string
		b    string
	}{
		{name: "https default port", a: "https://api.example.com", b: "https://api.example.com:443"},
		{name: "http default port", a: "http://api.example.com", b: "http://api.example.com:80"},
		{name: "trailing slash", a: "https://api.example.com", b: "https://api.example.com/"},
		{name: "host case", a: "https://api.example.com", b: "https://API.EXAMPLE.COM"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &Auth{ID: "a", Provider: "claude", Attributes: map[string]string{AttributeAPIKey: "a", "base_url": tt.a}}
			b := &Auth{ID: "b", Provider: "claude", Attributes: map[string]string{AttributeAPIKey: "b", "base_url": tt.b}}
			if got, want := authSchedulingGroup(a), authSchedulingGroup(b); got != want {
				t.Fatalf("authSchedulingGroup() = %q and %q, want one group", got, want)
			}
		})
	}
	nonDefault := &Auth{ID: "c", Provider: "claude", Attributes: map[string]string{AttributeAPIKey: "c", "base_url": "https://api.example.com:8443"}}
	if got, other := authSchedulingGroup(nonDefault), authSchedulingGroup(&Auth{ID: "d", Provider: "claude", Attributes: map[string]string{AttributeAPIKey: "d", "base_url": "https://api.example.com"}}); got == other {
		t.Fatalf("non-default port %q collapsed into %q", got, other)
	}
}

func TestAuthSchedulingGroupResolvesProviderDefaults(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		implicit *Auth
		explicit *Auth
	}{
		{
			name:     "claude default endpoint",
			provider: "claude",
			implicit: &Auth{ID: "a", Provider: "claude", Attributes: map[string]string{AttributeAPIKey: "a"}},
			explicit: &Auth{ID: "b", Provider: "claude", Attributes: map[string]string{AttributeAPIKey: "b", "base_url": "https://api.anthropic.com"}},
		},
		{
			name:     "claude default endpoint with trailing slash",
			provider: "claude",
			implicit: &Auth{ID: "a", Provider: "claude", Attributes: map[string]string{AttributeAPIKey: "a"}},
			explicit: &Auth{ID: "b", Provider: "claude", Attributes: map[string]string{AttributeAPIKey: "b", "base_url": "https://api.anthropic.com/"}},
		},
		{
			name:     "gemini default endpoint",
			provider: "gemini",
			implicit: &Auth{ID: "a", Provider: "gemini", Attributes: map[string]string{AttributeAPIKey: "a"}},
			explicit: &Auth{ID: "b", Provider: "gemini", Attributes: map[string]string{AttributeAPIKey: "b", "base_url": "https://generativelanguage.googleapis.com"}},
		},
		{
			name:     "meta default endpoint",
			provider: "meta",
			implicit: &Auth{ID: "a", Provider: "meta", Attributes: map[string]string{AttributeAPIKey: "a"}},
			explicit: &Auth{ID: "b", Provider: "meta", Attributes: map[string]string{AttributeAPIKey: "b", "base_url": "https://api.meta.ai/v1"}},
		},
		{
			name:     "gemini-interactions default endpoint",
			provider: "gemini-interactions",
			implicit: &Auth{ID: "a", Provider: "gemini-interactions", Attributes: map[string]string{AttributeAPIKey: "a"}},
			explicit: &Auth{ID: "b", Provider: "gemini-interactions", Attributes: map[string]string{AttributeAPIKey: "b", "base_url": "https://generativelanguage.googleapis.com"}},
		},
		{
			name:     "interactions default endpoint",
			provider: "interactions",
			implicit: &Auth{ID: "a", Provider: "interactions", Attributes: map[string]string{AttributeAPIKey: "a"}},
			explicit: &Auth{ID: "b", Provider: "interactions", Attributes: map[string]string{AttributeAPIKey: "b", "base_url": "https://generativelanguage.googleapis.com"}},
		},
		{
			name:     "compat name casing",
			provider: "openai-compatible-acme",
			implicit: &Auth{ID: "a", Provider: "openai-compatible-acme", Attributes: map[string]string{AttributeAPIKey: "a", "compat_name": "Acme"}},
			explicit: &Auth{ID: "b", Provider: "openai-compatible-acme", Attributes: map[string]string{AttributeAPIKey: "b", "compat_name": "acme"}},
		},
		{
			name:     "compat name whitespace",
			provider: "openai-compatible-acme",
			implicit: &Auth{ID: "a", Provider: "openai-compatible-acme", Attributes: map[string]string{AttributeAPIKey: "a", "compat_name": " acme "}},
			explicit: &Auth{ID: "b", Provider: "openai-compatible-acme", Attributes: map[string]string{AttributeAPIKey: "b", "compat_name": "Acme"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, want := authSchedulingGroup(tt.implicit), authSchedulingGroup(tt.explicit); got != want {
				t.Fatalf("authSchedulingGroup() = %q and %q, want one group", got, want)
			}
		})
	}
}

func TestAuthSchedulingGroupKeepsDistinctEndpoints(t *testing.T) {
	claudeDefault := &Auth{ID: "a", Provider: "claude", Attributes: map[string]string{AttributeAPIKey: "a"}}
	claudeGateway := &Auth{ID: "b", Provider: "claude", Attributes: map[string]string{AttributeAPIKey: "b", "base_url": "https://claude-gateway.example.com"}}
	if authSchedulingGroup(claudeDefault) == authSchedulingGroup(claudeGateway) {
		t.Fatalf("default Claude endpoint collapsed into a custom gateway group")
	}
	acme := &Auth{ID: "c", Provider: "openai-compatible-acme", Attributes: map[string]string{AttributeAPIKey: "c", "compat_name": "acme"}}
	other := &Auth{ID: "d", Provider: "openai-compatible-other", Attributes: map[string]string{AttributeAPIKey: "d", "compat_name": "other"}}
	if authSchedulingGroup(acme) == authSchedulingGroup(other) {
		t.Fatalf("distinct compatibility groups collapsed")
	}
}

func TestNoteProviderAttemptFailureExhaustsGroup(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.runtimeConfig.Store(&internalconfig.Config{MaxRetryCredentialsPerProvider: 2})

	auths := []*Auth{
		{ID: "gemini-a", Provider: "gemini", Attributes: map[string]string{AttributeAPIKey: "a", AttributeProviderName: "pool-a"}},
		{ID: "gemini-b", Provider: "gemini", Attributes: map[string]string{AttributeAPIKey: "b", AttributeProviderName: "pool-a"}},
		{ID: "gemini-c", Provider: "gemini", Attributes: map[string]string{AttributeAPIKey: "c", AttributeProviderName: "pool-b"}},
	}
	m.mu.Lock()
	m.auths = make(map[string]*Auth, len(auths))
	for _, auth := range auths {
		m.auths[auth.ID] = auth
	}
	m.mu.Unlock()

	budget := newProviderAttemptBudget(2)
	tried := make(map[string]struct{})

	m.noteProviderAttemptFailure(budget, auths[0], tried)
	if _, ok := tried["gemini-a"]; ok {
		t.Fatalf("first failure must not exhaust the provider group")
	}
	if budget.counts["gemini|pool-a"] != 1 {
		t.Fatalf("pool-a count = %d, want 1", budget.counts["gemini|pool-a"])
	}

	m.noteProviderAttemptFailure(budget, auths[1], tried)
	for _, id := range []string{"gemini-a", "gemini-b"} {
		if _, ok := tried[id]; !ok {
			t.Fatalf("group member %q not excluded after cap", id)
		}
	}
	if _, ok := tried["gemini-c"]; ok {
		t.Fatalf("unrelated provider group must not be excluded")
	}
	if _, ok := budget.exhausted["gemini-a"]; !ok {
		t.Fatalf("exhausted budget missing gemini-a")
	}
	if !providerAttemptBudgetExcludes(budget, auths[0]) {
		t.Fatalf("exhausted auth not rejected by post-pick fence")
	}
	if providerAttemptBudgetExcludes(budget, auths[2]) {
		t.Fatalf("unrelated auth rejected by post-pick fence")
	}
	// A credential added after the group was exhausted must still be rejected.
	newSibling := &Auth{ID: "gemini-new", Provider: "gemini", Attributes: map[string]string{AttributeAPIKey: "new", AttributeProviderName: "pool-a"}}
	if !providerAttemptBudgetExcludes(budget, newSibling) {
		t.Fatalf("new sibling of exhausted group not rejected")
	}

	// A budget seeded into a later retry round must keep the group excluded.
	triedNextRound := make(map[string]struct{})
	seedTriedFromProviderBudget(budget, triedNextRound)
	for _, id := range []string{"gemini-a", "gemini-b"} {
		if _, ok := triedNextRound[id]; !ok {
			t.Fatalf("seeded tried missing %q", id)
		}
	}
}

func TestNoteProviderAttemptFailureCountsDistinctCredentials(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.runtimeConfig.Store(&internalconfig.Config{MaxRetryCredentialsPerProvider: 2})

	authA := &Auth{ID: "gemini-a", Provider: "gemini", Attributes: map[string]string{AttributeAPIKey: "a", AttributeProviderName: "pool-a"}}
	authB := &Auth{ID: "gemini-b", Provider: "gemini", Attributes: map[string]string{AttributeAPIKey: "b", AttributeProviderName: "pool-a"}}
	m.mu.Lock()
	m.auths = map[string]*Auth{authA.ID: authA, authB.ID: authB}
	m.mu.Unlock()

	budget := newProviderAttemptBudget(2)
	tried := make(map[string]struct{})
	m.noteProviderAttemptFailure(budget, authA, tried)
	m.noteProviderAttemptFailure(budget, authA, tried)
	if budget.counts["gemini|pool-a"] != 1 {
		t.Fatalf("repeated failure of one credential counted twice: %d", budget.counts["gemini|pool-a"])
	}
	if len(budget.exhausted) != 0 {
		t.Fatalf("single credential exhausted a cap of two: %v", budget.exhausted)
	}
	m.noteProviderAttemptFailure(budget, authB, tried)
	if budget.counts["gemini|pool-a"] != 2 {
		t.Fatalf("distinct credentials count = %d, want 2", budget.counts["gemini|pool-a"])
	}
	if _, ok := budget.exhausted["gemini-a"]; !ok {
		t.Fatalf("group not exhausted after two distinct credentials")
	}
}

func TestProviderGroupExhaustionDoesNotExcludeOAuth(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.runtimeConfig.Store(&internalconfig.Config{MaxRetryCredentialsPerProvider: 1})

	apiKey := &Auth{ID: "claude-key", Provider: "claude", Attributes: map[string]string{AttributeAPIKey: "key"}}
	oauth := &Auth{ID: "claude-oauth", Provider: "claude", Metadata: map[string]any{"access_token": "token"}}
	m.mu.Lock()
	m.auths = map[string]*Auth{apiKey.ID: apiKey, oauth.ID: oauth}
	m.mu.Unlock()

	budget := newProviderAttemptBudget(1)
	tried := make(map[string]struct{})
	m.noteProviderAttemptFailure(budget, apiKey, tried)
	if _, ok := tried[oauth.ID]; ok {
		t.Fatalf("OAuth credential excluded by API-key provider group")
	}
	if providerAttemptBudgetExcludes(budget, oauth) {
		t.Fatalf("OAuth credential rejected by provider budget fence")
	}
	if _, ok := tried[apiKey.ID]; !ok {
		t.Fatalf("API-key credential not excluded after cap")
	}
}

func TestNoteProviderAttemptFailureSkipsOAuth(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.runtimeConfig.Store(&internalconfig.Config{MaxRetryCredentialsPerProvider: 1})

	oauth := &Auth{ID: "oauth-a", Provider: "claude", Metadata: map[string]any{"access_token": "token"}}
	m.mu.Lock()
	m.auths = map[string]*Auth{oauth.ID: oauth}
	m.mu.Unlock()

	budget := newProviderAttemptBudget(1)
	tried := make(map[string]struct{})
	m.noteProviderAttemptFailure(budget, oauth, tried)
	if len(budget.counts) != 0 || len(tried) != 0 {
		t.Fatalf("OAuth credential must not participate in provider caps: counts=%v tried=%v", budget.counts, tried)
	}
}

func TestProviderAttemptBudgetContextRoundTrip(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.runtimeConfig.Store(&internalconfig.Config{MaxRetryCredentialsPerProvider: 1})
	ctx := m.WithProviderAttemptBudget(context.Background())
	budget := providerAttemptBudgetFromContext(ctx)
	if budget == nil {
		t.Fatalf("WithProviderAttemptBudget() did not attach a budget")
	}
	if got := m.providerAttemptBudgetForRequest(ctx); got != budget {
		t.Fatalf("providerAttemptBudgetForRequest() = %p, want existing %p", got, budget)
	}
	original := m.WithProviderAttemptBudget(ctx)
	if got := providerAttemptBudgetFromContext(original); got != budget {
		t.Fatalf("WithProviderAttemptBudget() created a second budget")
	}
}

func TestProviderAttemptBudgetSnapshotsLimitAtCreation(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.runtimeConfig.Store(&internalconfig.Config{MaxRetryCredentialsPerProvider: 1})
	budget := m.providerAttemptBudgetForRequest(context.Background())
	if budget == nil || budget.limit != 1 {
		t.Fatalf("budget = %#v, want limit 1", budget)
	}
	m.runtimeConfig.Store(&internalconfig.Config{MaxRetryCredentialsPerProvider: 0})
	auth := &Auth{ID: "snapshot-a", Provider: "gemini", Attributes: map[string]string{AttributeAPIKey: "a", AttributeProviderName: "pool-a"}}
	m.mu.Lock()
	m.auths = map[string]*Auth{auth.ID: auth}
	m.mu.Unlock()
	tried := make(map[string]struct{})
	m.noteProviderAttemptFailure(budget, auth, tried)
	if _, ok := tried[auth.ID]; !ok {
		t.Fatalf("hot-reloaded cap changed the in-flight budget; tried=%v", tried)
	}
}

func TestDelayedFailureUsesAttemptTimeGroupAfterRegroup(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.runtimeConfig.Store(&internalconfig.Config{MaxRetryCredentialsPerProvider: 1})
	attempted := &Auth{ID: "gemini-regrouped", Provider: "gemini", Attributes: map[string]string{AttributeAPIKey: "a", AttributeProviderName: "pool-a"}}
	sibling := &Auth{ID: "gemini-regrouped-sibling", Provider: "gemini", Attributes: map[string]string{AttributeAPIKey: "b", AttributeProviderName: "pool-a"}}
	target := &Auth{ID: "gemini-regroup-target", Provider: "gemini", Attributes: map[string]string{AttributeAPIKey: "c", AttributeProviderName: "pool-b"}}
	m.mu.Lock()
	m.auths = map[string]*Auth{attempted.ID: attempted, sibling.ID: sibling, target.ID: target}
	m.mu.Unlock()

	ctx := m.WithProviderAttemptBudget(context.Background())
	budget := providerAttemptBudgetFromContext(ctx)
	providerAttemptBudgetRemember(budget, attempted)

	// The reload moves the in-flight credential into pool-b before it fails.
	moved := &Auth{ID: attempted.ID, Provider: "gemini", Attributes: map[string]string{AttributeAPIKey: "a", AttributeProviderName: "pool-b"}}
	m.mu.Lock()
	m.auths[attempted.ID] = moved
	m.mu.Unlock()

	m.NoteProviderAttemptFailure(ctx, attempted.ID, &Error{HTTPStatus: 500, Message: "late failure"})
	if budget.counts["gemini|pool-a"] != 1 {
		t.Fatalf("counts = %v, want the attempt-time group pool-a charged", budget.counts)
	}
	if _, exhausted := budget.exhaustedGroups["gemini|pool-b"]; exhausted {
		t.Fatalf("pool-b exhausted by a failure that belonged to pool-a")
	}
}

func TestOAuthAttemptsAreNeverRememberedOrCharged(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.runtimeConfig.Store(&internalconfig.Config{MaxRetryCredentialsPerProvider: 1})
	oauth := &Auth{
		ID:       "claude-oauth-remembered",
		Provider: "claude",
		Status:   StatusActive,
		Attributes: map[string]string{
			AttributeAuthKind:     AuthKindOAuth,
			AttributeProviderName: "shared",
		},
	}
	apiKey := &Auth{ID: "claude-key-remembered", Provider: "claude", Attributes: map[string]string{AttributeAPIKey: "k", AttributeProviderName: "shared"}}

	ctx := m.WithProviderAttemptBudget(context.Background())
	budget := providerAttemptBudgetFromContext(ctx)
	providerAttemptBudgetRemember(budget, oauth)
	if _, ok := budget.groups[oauth.ID]; ok {
		t.Fatalf("OAuth credential was remembered")
	}

	m.mu.Lock()
	m.auths = map[string]*Auth{apiKey.ID: apiKey}
	m.mu.Unlock()
	m.NoteProviderAttemptFailure(ctx, oauth.ID, &Error{HTTPStatus: 500, Message: "late oauth failure"})
	if len(budget.counts) != 0 || len(budget.exhaustedGroups) != 0 {
		t.Fatalf("OAuth failure charged the budget: counts=%v exhausted=%v", budget.counts, budget.exhaustedGroups)
	}
}

func TestDelayedFailureChargesRememberedGroup(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.runtimeConfig.Store(&internalconfig.Config{MaxRetryCredentialsPerProvider: 1})
	auth := &Auth{ID: "gemini-remembered", Provider: "gemini", Attributes: map[string]string{AttributeAPIKey: "a", AttributeProviderName: "pool-a"}}
	m.mu.Lock()
	m.auths = map[string]*Auth{auth.ID: auth}
	m.mu.Unlock()

	ctx := m.WithProviderAttemptBudget(context.Background())
	budget := providerAttemptBudgetFromContext(ctx)
	providerAttemptBudgetRemember(budget, auth)

	// Simulate a reload that drops the credential before its stream fails.
	m.mu.Lock()
	m.auths = map[string]*Auth{}
	m.mu.Unlock()

	m.NoteProviderAttemptFailure(ctx, auth.ID, &Error{HTTPStatus: 500, Message: "delayed stream failure"})
	if budget.counts["gemini|pool-a"] != 1 {
		t.Fatalf("remembered group counts = %v, want one charge for the serving group", budget.counts)
	}
	if !m.ProviderAttemptBudgetExhausted(ctx) {
		t.Fatalf("budget not exhausted after charging the remembered group")
	}
}

func TestDisabledCapSnapshotSurvivesReload(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.runtimeConfig.Store(&internalconfig.Config{MaxRetryCredentialsPerProvider: 0})
	ctx := m.WithProviderAttemptBudget(context.Background())
	if providerAttemptBudgetFromContext(ctx) != nil {
		t.Fatalf("budget bound while the cap is disabled")
	}

	// A reload enables the cap after the request was admitted.
	m.runtimeConfig.Store(&internalconfig.Config{MaxRetryCredentialsPerProvider: 1})
	ctx = m.WithProviderAttemptBudget(ctx)
	if budget := providerAttemptBudgetFromContext(ctx); budget != nil {
		t.Fatalf("mid-request reload created budget limit=%d, want the admitted snapshot (disabled)", budget.limit)
	}
}

func TestAlternativeTargetProbeSkipsExhaustedGroup(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.runtimeConfig.Store(&internalconfig.Config{MaxRetryCredentialsPerProvider: 1})
	first := &Auth{ID: "cap-alt-1", Provider: "codex", Status: StatusActive}
	second := &Auth{ID: "cap-alt-2", Provider: "codex", Status: StatusActive}
	m.mu.Lock()
	m.auths = map[string]*Auth{first.ID: first, second.ID: second}
	m.mu.Unlock()
	for _, a := range []*Auth{first, second} {
		registry.GetGlobalRegistry().RegisterClient(a.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
		id := a.ID
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}

	ctx := m.WithProviderAttemptBudget(context.Background())
	budget := providerAttemptBudgetFromContext(ctx)
	m.noteProviderAttemptFailure(budget, first, map[string]struct{}{})

	if m.HasAlternativeTargetForModel(ctx, []string{"codex"}, "gpt-5-codex", coreexecutor.Options{}, first.ID) {
		t.Fatalf("probe reported an alternative target from a cap-exhausted group")
	}
}

func TestRememberProviderAttemptGroupSurvivesRemoval(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.runtimeConfig.Store(&internalconfig.Config{MaxRetryCredentialsPerProvider: 1})
	auth := &Auth{ID: "meta-remember-exported", Provider: "meta", Attributes: map[string]string{AttributeAPIKey: "k", "base_url": "https://api.meta.ai/v1"}}
	m.mu.Lock()
	m.auths = map[string]*Auth{auth.ID: auth}
	m.mu.Unlock()

	ctx := m.WithProviderAttemptBudget(context.Background())
	m.RememberProviderAttemptGroup(ctx, auth)

	m.mu.Lock()
	m.auths = map[string]*Auth{}
	m.mu.Unlock()

	m.NoteProviderAttemptFailure(ctx, auth.ID, &Error{HTTPStatus: 429, Message: "late failure"})
	if !m.ProviderAttemptBudgetExhausted(ctx) {
		t.Fatalf("remembered group was not charged after the credential was removed")
	}
}
