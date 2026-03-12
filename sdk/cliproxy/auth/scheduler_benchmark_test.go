package auth

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

type schedulerBenchmarkExecutor struct {
	id string
}

func (e schedulerBenchmarkExecutor) Identifier() string { return e.id }

func (e schedulerBenchmarkExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e schedulerBenchmarkExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e schedulerBenchmarkExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e schedulerBenchmarkExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e schedulerBenchmarkExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

func benchmarkManagerSetup(b *testing.B, total int, mixed bool, withPriority bool) (*Manager, []string, string) {
	b.Helper()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	providers := []string{"gemini"}
	manager.executors["gemini"] = schedulerBenchmarkExecutor{id: "gemini"}
	if mixed {
		providers = []string{"gemini", "claude"}
		manager.executors["claude"] = schedulerBenchmarkExecutor{id: "claude"}
	}

	reg := registry.GetGlobalRegistry()
	model := "bench-model"
	for index := 0; index < total; index++ {
		provider := providers[0]
		if mixed && index%2 == 1 {
			provider = providers[1]
		}
		auth := &Auth{ID: fmt.Sprintf("bench-%s-%04d", provider, index), Provider: provider}
		if withPriority {
			priority := "0"
			if index%2 == 0 {
				priority = "10"
			}
			auth.Attributes = map[string]string{"priority": priority}
		}
		_, errRegister := manager.Register(context.Background(), auth)
		if errRegister != nil {
			b.Fatalf("Register(%s) error = %v", auth.ID, errRegister)
		}
		reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}})
	}
	manager.syncScheduler()
	b.Cleanup(func() {
		for index := 0; index < total; index++ {
			provider := providers[0]
			if mixed && index%2 == 1 {
				provider = providers[1]
			}
			reg.UnregisterClient(fmt.Sprintf("bench-%s-%04d", provider, index))
		}
	})

	return manager, providers, model
}

func BenchmarkManagerPickNext500(b *testing.B) {
	manager, _, model := benchmarkManagerSetup(b, 500, false, false)
	ctx := context.Background()
	opts := cliproxyexecutor.Options{}
	tried := map[string]struct{}{}
	if _, _, errWarm := manager.pickNext(ctx, "gemini", model, opts, tried); errWarm != nil {
		b.Fatalf("warmup pickNext error = %v", errWarm)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		auth, exec, errPick := manager.pickNext(ctx, "gemini", model, opts, tried)
		if errPick != nil || auth == nil || exec == nil {
			b.Fatalf("pickNext failed: auth=%v exec=%v err=%v", auth, exec, errPick)
		}
	}
}

func BenchmarkManagerPickNext1000(b *testing.B) {
	manager, _, model := benchmarkManagerSetup(b, 1000, false, false)
	ctx := context.Background()
	opts := cliproxyexecutor.Options{}
	tried := map[string]struct{}{}
	if _, _, errWarm := manager.pickNext(ctx, "gemini", model, opts, tried); errWarm != nil {
		b.Fatalf("warmup pickNext error = %v", errWarm)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		auth, exec, errPick := manager.pickNext(ctx, "gemini", model, opts, tried)
		if errPick != nil || auth == nil || exec == nil {
			b.Fatalf("pickNext failed: auth=%v exec=%v err=%v", auth, exec, errPick)
		}
	}
}

func BenchmarkManagerPickNextPriority500(b *testing.B) {
	manager, _, model := benchmarkManagerSetup(b, 500, false, true)
	ctx := context.Background()
	opts := cliproxyexecutor.Options{}
	tried := map[string]struct{}{}
	if _, _, errWarm := manager.pickNext(ctx, "gemini", model, opts, tried); errWarm != nil {
		b.Fatalf("warmup pickNext error = %v", errWarm)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		auth, exec, errPick := manager.pickNext(ctx, "gemini", model, opts, tried)
		if errPick != nil || auth == nil || exec == nil {
			b.Fatalf("pickNext failed: auth=%v exec=%v err=%v", auth, exec, errPick)
		}
	}
}

func BenchmarkManagerPickNextPriority1000(b *testing.B) {
	manager, _, model := benchmarkManagerSetup(b, 1000, false, true)
	ctx := context.Background()
	opts := cliproxyexecutor.Options{}
	tried := map[string]struct{}{}
	if _, _, errWarm := manager.pickNext(ctx, "gemini", model, opts, tried); errWarm != nil {
		b.Fatalf("warmup pickNext error = %v", errWarm)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		auth, exec, errPick := manager.pickNext(ctx, "gemini", model, opts, tried)
		if errPick != nil || auth == nil || exec == nil {
			b.Fatalf("pickNext failed: auth=%v exec=%v err=%v", auth, exec, errPick)
		}
	}
}

func BenchmarkManagerPickNextMixed500(b *testing.B) {
	manager, providers, model := benchmarkManagerSetup(b, 500, true, false)
	ctx := context.Background()
	opts := cliproxyexecutor.Options{}
	tried := map[string]struct{}{}
	if _, _, _, errWarm := manager.pickNextMixed(ctx, providers, model, opts, tried); errWarm != nil {
		b.Fatalf("warmup pickNextMixed error = %v", errWarm)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		auth, exec, provider, errPick := manager.pickNextMixed(ctx, providers, model, opts, tried)
		if errPick != nil || auth == nil || exec == nil || provider == "" {
			b.Fatalf("pickNextMixed failed: auth=%v exec=%v provider=%q err=%v", auth, exec, provider, errPick)
		}
	}
}

func BenchmarkManagerPickNextMixedPriority500(b *testing.B) {
	manager, providers, model := benchmarkManagerSetup(b, 500, true, true)
	ctx := context.Background()
	opts := cliproxyexecutor.Options{}
	tried := map[string]struct{}{}
	if _, _, _, errWarm := manager.pickNextMixed(ctx, providers, model, opts, tried); errWarm != nil {
		b.Fatalf("warmup pickNextMixed error = %v", errWarm)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		auth, exec, provider, errPick := manager.pickNextMixed(ctx, providers, model, opts, tried)
		if errPick != nil || auth == nil || exec == nil || provider == "" {
			b.Fatalf("pickNextMixed failed: auth=%v exec=%v provider=%q err=%v", auth, exec, provider, errPick)
		}
	}
}

func BenchmarkManagerPickNextAndMarkResult1000(b *testing.B) {
	manager, _, model := benchmarkManagerSetup(b, 1000, false, false)
	ctx := context.Background()
	opts := cliproxyexecutor.Options{}
	tried := map[string]struct{}{}
	if _, _, errWarm := manager.pickNext(ctx, "gemini", model, opts, tried); errWarm != nil {
		b.Fatalf("warmup pickNext error = %v", errWarm)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		auth, _, errPick := manager.pickNext(ctx, "gemini", model, opts, tried)
		if errPick != nil || auth == nil {
			b.Fatalf("pickNext failed: auth=%v err=%v", auth, errPick)
		}
		manager.MarkResult(ctx, Result{AuthID: auth.ID, Provider: "gemini", Model: model, Success: true})
	}
}

func BenchmarkManagerPickNextAndMarkResultParallel1000(b *testing.B) {
	manager, _, model := benchmarkManagerSetup(b, 1000, false, false)
	ctx := context.Background()
	opts := cliproxyexecutor.Options{}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		tried := map[string]struct{}{}
		for pb.Next() {
			auth, _, errPick := manager.pickNext(ctx, "gemini", model, opts, tried)
			if errPick != nil || auth == nil {
				b.Fatalf("pickNext failed: auth=%v err=%v", auth, errPick)
			}
			manager.MarkResult(ctx, Result{AuthID: auth.ID, Provider: "gemini", Model: model, Success: true})
		}
	})
}

func BenchmarkManagerPickNextMixedAndMarkResultParallel1000(b *testing.B) {
	manager, providers, model := benchmarkManagerSetup(b, 1000, true, false)
	ctx := context.Background()
	opts := cliproxyexecutor.Options{}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		tried := map[string]struct{}{}
		for pb.Next() {
			auth, _, provider, errPick := manager.pickNextMixed(ctx, providers, model, opts, tried)
			if errPick != nil || auth == nil || provider == "" {
				b.Fatalf("pickNextMixed failed: auth=%v provider=%q err=%v", auth, provider, errPick)
			}
			manager.MarkResult(ctx, Result{AuthID: auth.ID, Provider: provider, Model: model, Success: true})
		}
	})
}
