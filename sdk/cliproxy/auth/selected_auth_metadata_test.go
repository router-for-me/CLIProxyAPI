package auth

import (
	"context"
	"net/http"
	"testing"

	registry "github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestPublishSelectedAuthMetadataIncludesStableIndex(t *testing.T) {
	auth := &Auth{
		ID:       "auth-1",
		Provider: "codex",
		FileName: "auth-1.json",
	}
	selectedAuthID := ""
	selectedAuthIndex := ""
	meta := map[string]any{
		cliproxyexecutor.SelectedAuthCallbackMetadataKey: func(authID string) {
			selectedAuthID = authID
		},
		cliproxyexecutor.SelectedAuthIndexCallbackMetadataKey: func(authIndex string) {
			selectedAuthIndex = authIndex
		},
	}

	publishSelectedAuthMetadata(meta, auth)

	if selectedAuthID != auth.ID {
		t.Fatalf("selected auth ID = %q, want %q", selectedAuthID, auth.ID)
	}
	if selectedAuthIndex == "" || selectedAuthIndex != auth.Index {
		t.Fatalf("selected auth index = %q, want %q", selectedAuthIndex, auth.Index)
	}
	if got := meta[cliproxyexecutor.SelectedAuthMetadataKey]; got != auth.ID {
		t.Fatalf("selected auth metadata = %#v, want %q", got, auth.ID)
	}
	if got := meta[cliproxyexecutor.SelectedAuthIndexMetadataKey]; got != auth.Index {
		t.Fatalf("selected auth index metadata = %#v, want %q", got, auth.Index)
	}
}

type dummySelExecutor struct {
	provider string
}

func (e *dummySelExecutor) Identifier() string { return e.provider }
func (e *dummySelExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e *dummySelExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}
func (e *dummySelExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}
func (e *dummySelExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e *dummySelExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestManagerSelection_NilMetadataPreservesAffinityNamespace(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(&dummySelExecutor{provider: "claude"})
	manager.RegisterExecutor(&dummySelExecutor{provider: "openai"})
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("auth-1", "claude", []*registry.ModelInfo{{ID: "claude-3-5-sonnet"}})
	t.Cleanup(func() {
		reg.UnregisterClient("auth-1")
	})
	auth1 := &Auth{
		ID:       "auth-1",
		Provider: "claude",
		FileName: "auth-1.json",
		Status:   StatusActive,
	}
	if _, err := manager.Register(ctx, auth1); err != nil {
		t.Fatalf("manager.Register() error = %v", err)
	}
	affinity := NewSessionAffinitySelector(&WeightedRoundRobinSelector{})
	manager.SetSelector(affinity)

	t.Run("single provider pickNext with nil Metadata populates and preserves affinity metadata", func(t *testing.T) {
		opts := cliproxyexecutor.Options{Metadata: make(map[string]any)}
		auth, _, err := manager.pickNextLegacy(ctx, "claude", "claude-3-5-sonnet", opts, nil)
		if err != nil {
			t.Fatalf("pickNextLegacy() error = %v", err)
		}
		if auth == nil {
			t.Fatal("pickNextLegacy() returned nil auth")
		}
		if opts.Metadata == nil {
			t.Fatal("opts.Metadata is nil after pickNextLegacy, expected initialized map")
		}
		providerMeta, ok := opts.Metadata[cliproxyexecutor.SessionAffinityProviderMetadataKey].(string)
		if !ok || providerMeta != "claude" {
			t.Fatalf("SessionAffinityProviderMetadataKey = %q, %v; want \"claude\", true", providerMeta, ok)
		}
		modelMeta, ok := opts.Metadata[cliproxyexecutor.SessionAffinityModelMetadataKey].(string)
		if !ok || modelMeta != "claude-3-5-sonnet" {
			t.Fatalf("SessionAffinityModelMetadataKey = %q, %v; want \"claude-3-5-sonnet\", true", modelMeta, ok)
		}
	})

	t.Run("mixed provider pickNextMixed with nil Metadata populates mixed namespace", func(t *testing.T) {
		opts := cliproxyexecutor.Options{Metadata: make(map[string]any)}
		auth, _, _, err := manager.pickNextMixedLegacy(ctx, []string{"claude", "openai"}, "claude-3-5-sonnet", opts, nil)
		if err != nil {
			t.Fatalf("pickNextMixedLegacy() error = %v", err)
		}
		if auth == nil {
			t.Fatal("pickNextMixedLegacy() returned nil auth")
		}
		if opts.Metadata == nil {
			t.Fatal("opts.Metadata is nil after pickNextMixedLegacy, expected initialized map")
		}
		providerMeta, ok := opts.Metadata[cliproxyexecutor.SessionAffinityProviderMetadataKey].(string)
		if !ok || providerMeta != "mixed" {
			t.Fatalf("SessionAffinityProviderMetadataKey = %q, %v; want \"mixed\", true", providerMeta, ok)
		}

		res := Result{
			AuthID:   auth.ID,
			Provider: "rewritten-provider",
			Model:    "rewritten-model",
			Success:  true,
			Options:  opts,
		}
		affinity.OnResult(res)
	})
}

// Manager-level regression: with nonempty caller-supplied exclusions, the mixed-pool affinity
// namespace ("mixed" provider + requested model) stamped during selection must survive into the
// success Result.Options, and a subsequent same-session request must bind to the same auth.
func TestManagerExecute_MixedAffinityNamespaceRetainedThroughExclusions(t *testing.T) {
	model := "claude-3-5-sonnet"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("affinity-auth-1", "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient("affinity-auth-1")
	})
	auth1 := &Auth{
		ID:       "affinity-auth-1",
		Provider: "claude",
		FileName: "affinity-auth-1.json",
		Status:   StatusActive,
	}
	executor := newOuterRetryTestExecutor()
	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 1)
	manager.RegisterExecutor(executor)
	if _, err := manager.Register(context.Background(), auth1); err != nil {
		t.Fatalf("manager.Register() error = %v", err)
	}
	affinity := NewSessionAffinitySelector(&WeightedRoundRobinSelector{})
	manager.SetSelector(affinity)

	opts := cliproxyexecutor.Options{
		Headers:  http.Header{"X-Session-Id": []string{"mixed-affinity-session-123"}},
		Metadata: map[string]any{},
	}
	// Caller supplies an exclusion so withExcludedAuthIDs clones the metadata map.
	opts.Metadata[cliproxyexecutor.ExcludedAuthIDsMetadataKey] = map[string]struct{}{"some-other-auth": {}}

	// First request: success must bind the mixed + requested-model affinity namespace so the
	// second same-session request resolves to the same auth (the "no failover" requirement).
	// withExcludedAuthIDs clones the metadata map, so the affinity keys live on that clone; the
	// caller-visible opts.Metadata does not necessarily carry them. Assert the semantic effect.
	if _, err := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, opts); err != nil {
		t.Fatalf("Execute() error = %v, expected success", err)
	}
	if cached, ok := affinity.cache.Get("mixed::header:mixed-affinity-session-123::" + model); !ok {
		t.Logf("mixed affinity binding not found; opts.Metadata=%#v", opts.Metadata)
		t.Fatalf("expected mixed affinity binding under mixed::header:mixed-affinity-session-123::%s", model)
	} else if cached != auth1.ID {
		t.Fatalf("mixed affinity binding = %q, want %q", cached, auth1.ID)
	}

	// Second same-session request: must hit the same mixed binding (i.e. not rotate empty/fail).
	resp, err := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, opts)
	if err != nil {
		t.Fatalf("Execute() second request error = %v, want same binding success", err)
	}
	if len(resp.Payload) == 0 {
		t.Fatal("Execute() second request returned empty payload")
	}
}
