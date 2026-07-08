package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type thinkingLookupMetadataExecutor struct {
	id string

	mu           sync.Mutex
	lookupModels []string
}

func (e *thinkingLookupMetadataExecutor) Identifier() string { return e.id }

func (e *thinkingLookupMetadataExecutor) Execute(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.lookupModels = append(e.lookupModels, metadataString(opts.Metadata, cliproxyexecutor.ThinkingLookupModelMetadataKey))
	e.mu.Unlock()
	return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (e *thinkingLookupMetadataExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "ExecuteStream not implemented"}
}

func (e *thinkingLookupMetadataExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *thinkingLookupMetadataExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "CountTokens not implemented"}
}

func (e *thinkingLookupMetadataExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "HttpRequest not implemented"}
}

func (e *thinkingLookupMetadataExecutor) LookupModels() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.lookupModels))
	copy(out, e.lookupModels)
	return out
}

func TestExecuteSetsThinkingLookupModelFromRouteModel(t *testing.T) {
	ctx := context.Background()
	provider := "thinking-lookup-provider"
	authID := "thinking-lookup-auth"
	routeModel := "routed-model"
	originalRequestedModel := "original-model"

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, provider, []*registry.ModelInfo{{
		ID:      routeModel,
		Object:  "model",
		Created: time.Now().Unix(),
		OwnedBy: provider,
		Type:    "test",
	}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	mgr := NewManager(nil, nil, nil)
	exec := &thinkingLookupMetadataExecutor{id: provider}
	mgr.RegisterExecutor(exec)
	if _, err := mgr.Register(ctx, &Auth{
		ID:       authID,
		Provider: provider,
		Status:   StatusActive,
		Attributes: map[string]string{
			AttributeAuthKind: AuthKindAPIKey,
			AttributeAPIKey:   "test-key",
			AttributeSource:   "config:test[test-key]",
		},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	mgr.RefreshSchedulerEntry(authID)

	_, err := mgr.Execute(ctx, []string{provider}, cliproxyexecutor.Request{Model: routeModel}, cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.RequestedModelMetadataKey: originalRequestedModel,
		},
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	got := exec.LookupModels()
	if len(got) != 1 || got[0] != routeModel {
		t.Fatalf("thinking lookup model metadata = %v, want [%q]", got, routeModel)
	}
}

func TestExecuteSetsThinkingLookupModelFromRestoredExecutionModel(t *testing.T) {
	ctx := context.Background()
	provider := "thinking-lookup-selection-provider"
	authID := "thinking-lookup-selection-auth"
	selectionModel := "gemini-2.5-flash"
	executionModel := "agents/custom-model"

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, provider, []*registry.ModelInfo{{
		ID:      selectionModel,
		Object:  "model",
		Created: time.Now().Unix(),
		OwnedBy: provider,
		Type:    "test",
	}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	mgr := NewManager(nil, nil, nil)
	exec := &thinkingLookupMetadataExecutor{id: provider}
	mgr.RegisterExecutor(exec)
	if _, err := mgr.Register(ctx, &Auth{
		ID:       authID,
		Provider: provider,
		Status:   StatusActive,
		Attributes: map[string]string{
			AttributeAuthKind: AuthKindAPIKey,
			AttributeAPIKey:   "test-key",
			AttributeSource:   "config:test[test-key]",
		},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	mgr.RefreshSchedulerEntry(authID)

	_, err := mgr.Execute(ctx, []string{provider}, cliproxyexecutor.Request{Model: executionModel}, cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.AuthSelectionModelMetadataKey: selectionModel,
		},
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	got := exec.LookupModels()
	if len(got) != 1 || got[0] != executionModel {
		t.Fatalf("thinking lookup model metadata = %v, want [%q]", got, executionModel)
	}
}

func TestExecutePreservesAliasResolvedThinkingSuffix(t *testing.T) {
	ctx := context.Background()
	provider := "gemini"
	authID := "thinking-lookup-suffix-auth"
	routeModel := "g25f"
	clientModel := "g25f(high)"
	upstreamModel := "gemini-2.5-flash(low)"
	wantLookup := "g25f(low)"

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, provider, []*registry.ModelInfo{{
		ID:      routeModel,
		Object:  "model",
		Created: time.Now().Unix(),
		OwnedBy: provider,
		Type:    "test",
	}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{
		GeminiKey: []internalconfig.GeminiKey{{
			APIKey: "test-key",
			Models: []internalconfig.GeminiModel{{
				Name:  upstreamModel,
				Alias: routeModel,
			}},
		}},
	})
	exec := &thinkingLookupMetadataExecutor{id: provider}
	mgr.RegisterExecutor(exec)
	if _, err := mgr.Register(ctx, &Auth{
		ID:       authID,
		Provider: provider,
		Status:   StatusActive,
		Attributes: map[string]string{
			AttributeAuthKind: AuthKindAPIKey,
			AttributeAPIKey:   "test-key",
			AttributeSource:   "config:gemini[test-key]",
		},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	mgr.RefreshSchedulerEntry(authID)

	_, err := mgr.Execute(ctx, []string{provider}, cliproxyexecutor.Request{Model: clientModel}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	got := exec.LookupModels()
	if len(got) != 1 || got[0] != wantLookup {
		t.Fatalf("thinking lookup model metadata = %v, want [%q]", got, wantLookup)
	}
}

func metadataString(meta map[string]any, key string) string {
	if len(meta) == 0 {
		return ""
	}
	switch value := meta[key].(type) {
	case string:
		return value
	case []byte:
		return string(value)
	default:
		return ""
	}
}
