package auth

import (
	"context"
	"net/http"
	"sync"
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

// affinityCaptureExecutor records the execution options and call count per auth so
// tests can prove that session-affinity metadata survives the whole mixed pipeline.
type affinityCaptureExecutor struct {
	mu       sync.Mutex
	provider string
	lastOpts cliproxyexecutor.Options
	calls    map[string]int
}

func (e *affinityCaptureExecutor) Identifier() string { return e.provider }
func (e *affinityCaptureExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls[auth.ID]++
	e.lastOpts = opts
	return cliproxyexecutor.Response{Payload: []byte(`{"choices":[{"message":{"content":"ok"}}]}`)}, nil
}
func (e *affinityCaptureExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}
func (e *affinityCaptureExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}
func (e *affinityCaptureExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e *affinityCaptureExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *affinityCaptureExecutor) captured() (cliproxyexecutor.Options, map[string]int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	calls := make(map[string]int, len(e.calls))
	for id, n := range e.calls {
		calls[id] = n
	}
	return e.lastOpts, calls
}

// TestMixedAffinityNamespaceSurvivesExecution proves that after a successful mixed
// execution with a caller-supplied exclusion (nonempty tried set), the session
// affinity selector records Result.Options under the "mixed" namespace so a
// subsequent same-session request reuses the same auth instead of re-picking.
// Regression: execOpts must derive from pickOpts (stamped namespace), not opts.
func TestMixedAffinityNamespaceSurvivesExecution(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, nil, nil)
	execClaude := &affinityCaptureExecutor{provider: "claude", calls: make(map[string]int)}
	execOpenAI := &affinityCaptureExecutor{provider: "openai", calls: make(map[string]int)}
	manager.RegisterExecutor(execClaude)
	manager.RegisterExecutor(execOpenAI)

	model := "affinity-mixed-model"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("affinity-claude-1", "claude", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient("affinity-openai-1", "openai", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient("affinity-claude-1")
		reg.UnregisterClient("affinity-openai-1")
	})
	if _, err := manager.Register(ctx, &Auth{ID: "affinity-claude-1", Provider: "claude", FileName: "affinity-claude-1.json", Status: StatusActive}); err != nil {
		t.Fatalf("Register(claude) error = %v", err)
	}
	if _, err := manager.Register(ctx, &Auth{ID: "affinity-openai-1", Provider: "openai", FileName: "affinity-openai-1.json", Status: StatusActive}); err != nil {
		t.Fatalf("Register(openai) error = %v", err)
	}

	affinity := NewSessionAffinitySelector(&WeightedRoundRobinSelector{})
	manager.SetSelector(affinity)

	req := cliproxyexecutor.Request{Model: model}
	headers := http.Header{}
	headers.Set("X-Claude-Code-Session-Id", "affinity-session-1")
	opts := cliproxyexecutor.Options{
		Headers: headers,
		Metadata: map[string]any{
			cliproxyexecutor.ExcludedAuthIDsMetadataKey: map[string]struct{}{
				"affinity-claude-1": {},
			},
		},
	}

	// First request: claude is caller-excluded, openai must win.
	if _, err := manager.Execute(ctx, []string{"claude", "openai"}, req, opts); err != nil {
		t.Fatalf("first Execute() error = %v", err)
	}

	lastOpts, calls := execOpenAI.captured()
	if got := calls["affinity-openai-1"]; got != 1 {
		t.Fatalf("openai executed %d times on first request, want 1", got)
	}
	if got := calls["affinity-claude-1"]; got != 0 {
		t.Fatalf("caller-excluded claude executed %d times, want 0", got)
	}
	providerMeta, ok := lastOpts.Metadata[cliproxyexecutor.SessionAffinityProviderMetadataKey].(string)
	if !ok || providerMeta != "mixed" {
		t.Fatalf("SessionAffinityProviderMetadataKey = %q, %v; want \"mixed\", true (Result.Options must keep pickOpts namespace)", providerMeta, ok)
	}
	modelMeta, ok := lastOpts.Metadata[cliproxyexecutor.SessionAffinityModelMetadataKey].(string)
	if !ok || modelMeta != model {
		t.Fatalf("SessionAffinityModelMetadataKey = %q, %v; want %q, true", modelMeta, ok, model)
	}

	// Second same-session request without exclusions must hit the mixed binding.
	if _, err := manager.Execute(ctx, []string{"claude", "openai"}, req, cliproxyexecutor.Options{Headers: headers}); err != nil {
		t.Fatalf("second Execute() error = %v", err)
	}
	_, callsAfter := execOpenAI.captured()
	if got := callsAfter["affinity-openai-1"]; got != 2 {
		t.Fatalf("openai executed %d times across both same-session requests, want 2 (mixed cache reuse)", got)
	}
}
