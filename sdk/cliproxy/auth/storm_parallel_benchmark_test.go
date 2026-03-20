package auth

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

type stormPoolExecutor struct {
	id string

	badAuths map[string]struct{}
	requests atomic.Int64
	mu       sync.Mutex
	models   []string
}

func (e *stormPoolExecutor) Identifier() string { return e.id }

func (e *stormPoolExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	_ = ctx
	_ = opts
	e.requests.Add(1)
	e.mu.Lock()
	e.models = append(e.models, req.Model)
	e.mu.Unlock()
	if _, bad := e.badAuths[auth.ID]; bad {
		return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota storm"}
	}
	return cliproxyexecutor.Response{Payload: []byte(req.Model)}, nil
}

func (e *stormPoolExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	_ = ctx
	_ = auth
	_ = req
	_ = opts
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "ExecuteStream not implemented"}
}

func (e *stormPoolExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *stormPoolExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.Execute(ctx, auth, req, opts)
}

func (e *stormPoolExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	_ = ctx
	_ = auth
	_ = req
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "HttpRequest not implemented"}
}

func benchmarkStormManagerSetup(b *testing.B, total int) (*Manager, string, *stormPoolExecutor) {
	b.Helper()
	const alias = "storm-model"
	executor := &stormPoolExecutor{
		id:       "pool",
		badAuths: make(map[string]struct{}, total),
	}
	m := NewManager(nil, &RoundRobinSelector{}, nil)
	m.SetConfig(&internalconfig.Config{
		OpenAICompatibility: []internalconfig.OpenAICompatibility{{
			Name: "pool",
			Models: []internalconfig.OpenAICompatibilityModel{
				{Name: "storm-a", Alias: alias},
				{Name: "storm-b", Alias: alias},
			},
		}},
	})
	m.RegisterExecutor(executor)
	m.SetRetryConfig(0, 0, 8)

	reg := registry.GetGlobalRegistry()
	for index := 0; index < total; index++ {
		authID := fmt.Sprintf("storm-auth-%04d", index)
		auth := &Auth{
			ID:       authID,
			Provider: "pool",
			Status:   StatusActive,
			Attributes: map[string]string{
				"api_key":      fmt.Sprintf("storm-key-%04d", index),
				"compat_name":  "pool",
				"provider_key": "pool",
			},
		}
		if _, err := m.Register(context.Background(), auth); err != nil {
			b.Fatalf("register %s: %v", authID, err)
		}
		reg.RegisterClient(authID, "pool", []*registry.ModelInfo{{ID: alias}})
		if index%5 != 4 {
			executor.badAuths[authID] = struct{}{}
		}
	}
	m.syncScheduler()
	b.Cleanup(func() {
		for index := 0; index < total; index++ {
			reg.UnregisterClient(fmt.Sprintf("storm-auth-%04d", index))
		}
	})
	return m, alias, executor
}

func BenchmarkManagerExecuteBadAuthStormParallel(b *testing.B) {
	manager, alias, executor := benchmarkStormManagerSetup(b, 1000)
	req := cliproxyexecutor.Request{Model: alias}
	opts := cliproxyexecutor.Options{}
	if _, errWarm := manager.Execute(context.Background(), []string{"pool"}, req, opts); errWarm != nil {
		b.Fatalf("warmup execute error = %v", errWarm)
	}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		ctx := context.Background()
		for pb.Next() {
			resp, errExec := manager.Execute(ctx, []string{"pool"}, req, opts)
			if errExec != nil {
				b.Fatalf("Execute failed: %v", errExec)
			}
			if len(resp.Payload) == 0 {
				b.Fatalf("Execute returned empty payload")
			}
		}
	})
	if got := executor.requests.Load(); got == 0 {
		b.Fatalf("storm executor saw no requests")
	}
}
