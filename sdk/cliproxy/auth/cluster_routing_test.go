package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/cluster"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

type clusterRoutingExecutor struct {
	provider string
}

func (e clusterRoutingExecutor) Identifier() string { return e.provider }

func (e clusterRoutingExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{Payload: []byte(e.provider + ":" + auth.ID)}, nil
}

func (e clusterRoutingExecutor) ExecuteStream(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte(e.provider + ":" + auth.ID)}
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}

func (e clusterRoutingExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e clusterRoutingExecutor) CountTokens(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{Payload: []byte(e.provider + ":" + auth.ID)}, nil
}

func (e clusterRoutingExecutor) HttpRequest(_ context.Context, _ *Auth, _ *http.Request) (*http.Response, error) {
	return nil, nil
}

func registerClusterRoutingAuth(t *testing.T, mgr *Manager, auth *Auth, model string) {
	t.Helper()

	if _, err := mgr.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register(%s) error = %v", auth.ID, err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})
	mgr.RefreshSchedulerEntry(auth.ID)
}

func TestManagerClusterRoutingPreferLocalFalseIncludesRemoteProviders(t *testing.T) {
	localProvider := "gemini"
	remoteProvider := cluster.ProviderKey("node-b")
	model := "cluster-routing-open-model"

	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{
		Cluster: internalconfig.ClusterConfig{PreferLocal: false},
	})
	mgr.RegisterExecutor(clusterRoutingExecutor{provider: localProvider})
	mgr.RegisterExecutor(clusterRoutingExecutor{provider: remoteProvider})

	registerClusterRoutingAuth(t, mgr, &Auth{
		ID:       "local-auth",
		Provider: localProvider,
		Metadata: map[string]any{"email": "local@example.com"},
	}, model)
	registerClusterRoutingAuth(t, mgr, &Auth{
		ID:       cluster.RuntimeAuthID("node-b"),
		Provider: remoteProvider,
		Attributes: map[string]string{
			cluster.AttributeRuntimeOnly: "true",
		},
		Runtime: &cluster.PeerBinding{ConfiguredID: "node-b", NodeID: "node-b"},
	}, model)

	resp1, err := mgr.Execute(context.Background(), []string{localProvider, remoteProvider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute() first error = %v", err)
	}
	resp2, err := mgr.Execute(context.Background(), []string{localProvider, remoteProvider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute() second error = %v", err)
	}

	if got, want := string(resp1.Payload), "gemini:local-auth"; got != want {
		t.Fatalf("first payload = %q, want %q", got, want)
	}
	if got, want := string(resp2.Payload), remoteProvider+":"+cluster.RuntimeAuthID("node-b"); got != want {
		t.Fatalf("second payload = %q, want %q", got, want)
	}
}

func TestManagerClusterRoutingPreferLocalTrueKeepsReadyRemoteOut(t *testing.T) {
	localProvider := "gemini"
	remoteProvider := cluster.ProviderKey("node-b")
	model := "cluster-routing-prefer-local"

	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{
		Cluster: internalconfig.ClusterConfig{PreferLocal: true},
	})
	mgr.RegisterExecutor(clusterRoutingExecutor{provider: localProvider})
	mgr.RegisterExecutor(clusterRoutingExecutor{provider: remoteProvider})

	registerClusterRoutingAuth(t, mgr, &Auth{
		ID:       "local-auth",
		Provider: localProvider,
		Metadata: map[string]any{"email": "local@example.com"},
	}, model)
	registerClusterRoutingAuth(t, mgr, &Auth{
		ID:       cluster.RuntimeAuthID("node-b"),
		Provider: remoteProvider,
		Attributes: map[string]string{
			cluster.AttributeRuntimeOnly: "true",
		},
		Runtime: &cluster.PeerBinding{ConfiguredID: "node-b", NodeID: "node-b"},
	}, model)

	for attempt := 0; attempt < 2; attempt++ {
		resp, err := mgr.Execute(context.Background(), []string{localProvider, remoteProvider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
		if err != nil {
			t.Fatalf("Execute() #%d error = %v", attempt, err)
		}
		if got, want := string(resp.Payload), "gemini:local-auth"; got != want {
			t.Fatalf("payload #%d = %q, want %q", attempt, got, want)
		}
	}
}

func TestManagerClusterRoutingPreferLocalFallsBackWhenLocalUnavailable(t *testing.T) {
	localProvider := "gemini"
	remoteProvider := cluster.ProviderKey("node-b")
	model := "cluster-routing-local-cooldown"

	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{
		Cluster: internalconfig.ClusterConfig{PreferLocal: true},
	})
	mgr.RegisterExecutor(clusterRoutingExecutor{provider: localProvider})
	mgr.RegisterExecutor(clusterRoutingExecutor{provider: remoteProvider})

	registerClusterRoutingAuth(t, mgr, &Auth{
		ID:       "local-auth",
		Provider: localProvider,
		Metadata: map[string]any{"email": "local@example.com"},
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: time.Now().Add(time.Minute),
			},
		},
	}, model)
	registerClusterRoutingAuth(t, mgr, &Auth{
		ID:       cluster.RuntimeAuthID("node-b"),
		Provider: remoteProvider,
		Attributes: map[string]string{
			cluster.AttributeRuntimeOnly: "true",
		},
		Runtime: &cluster.PeerBinding{ConfiguredID: "node-b", NodeID: "node-b"},
	}, model)

	resp, err := mgr.Execute(context.Background(), []string{localProvider, remoteProvider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got, want := string(resp.Payload), remoteProvider+":"+cluster.RuntimeAuthID("node-b"); got != want {
		t.Fatalf("payload = %q, want %q", got, want)
	}
}

func TestManagerClusterRoutingPreferLocalIgnoresPinnedRemoteWhenLocalReady(t *testing.T) {
	localProvider := "gemini"
	remoteProvider := cluster.ProviderKey("node-b")
	model := "cluster-routing-pinned-remote"

	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{
		Cluster: internalconfig.ClusterConfig{PreferLocal: true},
	})
	mgr.RegisterExecutor(clusterRoutingExecutor{provider: localProvider})
	mgr.RegisterExecutor(clusterRoutingExecutor{provider: remoteProvider})

	registerClusterRoutingAuth(t, mgr, &Auth{
		ID:       "local-auth",
		Provider: localProvider,
		Metadata: map[string]any{"email": "local@example.com"},
	}, model)
	registerClusterRoutingAuth(t, mgr, &Auth{
		ID:       cluster.RuntimeAuthID("node-b"),
		Provider: remoteProvider,
		Attributes: map[string]string{
			cluster.AttributeRuntimeOnly: "true",
		},
		Runtime: &cluster.PeerBinding{ConfiguredID: "node-b", NodeID: "node-b"},
	}, model)

	resp, err := mgr.Execute(context.Background(), []string{localProvider, remoteProvider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{
		Metadata: map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: cluster.RuntimeAuthID("node-b")},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got, want := string(resp.Payload), "gemini:local-auth"; got != want {
		t.Fatalf("payload = %q, want %q", got, want)
	}
}

func TestManagerClusterRoutingForwardedRequestsStayLocalOnly(t *testing.T) {
	localProvider := "gemini"
	remoteProvider := cluster.ProviderKey("node-b")
	model := "cluster-routing-forwarded"

	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{
		Cluster: internalconfig.ClusterConfig{PreferLocal: false},
	})
	mgr.RegisterExecutor(clusterRoutingExecutor{provider: localProvider})
	mgr.RegisterExecutor(clusterRoutingExecutor{provider: remoteProvider})

	registerClusterRoutingAuth(t, mgr, &Auth{
		ID:       "local-auth",
		Provider: localProvider,
		Metadata: map[string]any{"email": "local@example.com"},
	}, model)
	registerClusterRoutingAuth(t, mgr, &Auth{
		ID:       cluster.RuntimeAuthID("node-b"),
		Provider: remoteProvider,
		Attributes: map[string]string{
			cluster.AttributeRuntimeOnly: "true",
		},
		Runtime: &cluster.PeerBinding{ConfiguredID: "node-b", NodeID: "node-b"},
	}, model)

	resp, err := mgr.Execute(context.Background(), []string{localProvider, remoteProvider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{
		Metadata: map[string]any{cliproxyexecutor.ClusterForwardedMetadataKey: true},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got, want := string(resp.Payload), "gemini:local-auth"; got != want {
		t.Fatalf("payload = %q, want %q", got, want)
	}
}

func TestManagerClusterRoutingForwardedRequestsFailInsteadOfReForwarding(t *testing.T) {
	remoteProvider := cluster.ProviderKey("node-b")
	model := "cluster-routing-forwarded-fail"

	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{
		Cluster: internalconfig.ClusterConfig{PreferLocal: false},
	})
	mgr.RegisterExecutor(clusterRoutingExecutor{provider: remoteProvider})

	registerClusterRoutingAuth(t, mgr, &Auth{
		ID:       cluster.RuntimeAuthID("node-b"),
		Provider: remoteProvider,
		Attributes: map[string]string{
			cluster.AttributeRuntimeOnly: "true",
		},
		Runtime: &cluster.PeerBinding{ConfiguredID: "node-b", NodeID: "node-b"},
	}, model)

	_, err := mgr.Execute(context.Background(), []string{remoteProvider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{
		Metadata: map[string]any{cliproxyexecutor.ClusterForwardedMetadataKey: true},
	})
	if err == nil {
		t.Fatal("Execute() error = nil, want cluster local-only failure")
	}
	authErr, ok := err.(*Error)
	if !ok {
		t.Fatalf("error type = %T, want *Error", err)
	}
	if authErr.Code != "cluster_local_only_unavailable" {
		t.Fatalf("error code = %q", authErr.Code)
	}
	if authErr.StatusCode() != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", authErr.StatusCode(), http.StatusBadGateway)
	}
}
