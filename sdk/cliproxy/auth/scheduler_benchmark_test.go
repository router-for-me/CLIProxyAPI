package auth

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
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

func benchmarkAccessToken(subject string, expiresAt time.Time) string {
	encoding := base64.RawURLEncoding
	header := encoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payload := encoding.EncodeToString(fmt.Appendf(nil, `{"exp":%d,"sub":%q}`, expiresAt.Unix(), subject))
	signature := encoding.EncodeToString([]byte("signature-" + subject))
	return header + "." + payload + "." + signature
}

// benchmarkOAuthManagerSetup registers OAuth credentials carrying JWT access tokens, the shape of
// large Codex pools, so selection benchmarks include access token expiry evaluation.
func benchmarkOAuthManagerSetup(b *testing.B, total int, selector Selector) (*Manager, string) {
	b.Helper()
	manager := NewManager(nil, selector, nil)
	manager.executors["codex"] = schedulerBenchmarkExecutor{id: "codex"}

	reg := registry.GetGlobalRegistry()
	model := "bench-oauth-model"
	expiresAt := time.Now().Add(24 * time.Hour)
	authIDs := make([]string, 0, total)
	for index := 0; index < total; index++ {
		authID := fmt.Sprintf("bench-codex-%04d", index)
		auth := &Auth{
			ID:       authID,
			Provider: "codex",
			Metadata: map[string]any{"access_token": benchmarkAccessToken(authID, expiresAt)},
		}
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			b.Fatalf("Register(%s) error = %v", authID, errRegister)
		}
		reg.RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
		authIDs = append(authIDs, authID)
	}
	manager.syncScheduler()
	b.Cleanup(func() {
		for _, authID := range authIDs {
			reg.UnregisterClient(authID)
		}
	})

	return manager, model
}

func BenchmarkManagerPickNextOAuth1000(b *testing.B) {
	manager, model := benchmarkOAuthManagerSetup(b, 1000, &RoundRobinSelector{})
	ctx := context.Background()
	opts := cliproxyexecutor.Options{}
	tried := map[string]struct{}{}
	if _, _, errWarm := manager.pickNext(ctx, "codex", model, opts, tried); errWarm != nil {
		b.Fatalf("warmup pickNext error = %v", errWarm)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		auth, exec, errPick := manager.pickNext(ctx, "codex", model, opts, tried)
		if errPick != nil || auth == nil || exec == nil {
			b.Fatalf("pickNext failed: auth=%v exec=%v err=%v", auth, exec, errPick)
		}
	}
}

func BenchmarkManagerPickNextExhausted1000(b *testing.B) {
	manager, model := benchmarkOAuthManagerSetup(b, 1000, &RoundRobinSelector{})
	ctx := context.Background()
	retryAfter := time.Hour
	for _, auth := range manager.List() {
		manager.MarkResult(ctx, Result{
			AuthID:     auth.ID,
			Provider:   "codex",
			Model:      model,
			RouteModel: model,
			RetryAfter: &retryAfter,
			Error:      &Error{Code: "rate_limited", Message: "quota exhausted", HTTPStatus: http.StatusTooManyRequests},
		})
	}
	previousOutput := log.StandardLogger().Out
	log.SetOutput(io.Discard)
	b.Cleanup(func() { log.SetOutput(previousOutput) })

	opts := cliproxyexecutor.Options{}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, errPick := manager.pickNext(ctx, "codex", model, opts, nil); errPick == nil {
			b.Fatal("pickNext succeeded although every credential is cooling down")
		}
	}
}

func BenchmarkManagerPickNextMixedSessionAffinityOAuth1000(b *testing.B) {
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: &RoundRobinSelector{}, TTL: time.Hour})
	b.Cleanup(selector.Stop)
	manager, model := benchmarkOAuthManagerSetup(b, 1000, selector)
	previousOutput := log.StandardLogger().Out
	log.SetOutput(io.Discard)
	b.Cleanup(func() { log.SetOutput(previousOutput) })
	ctx := context.Background()
	providers := []string{"codex"}
	opts := cliproxyexecutor.Options{Headers: http.Header{"X-Session-ID": []string{"bench-session"}}}
	if _, _, _, errWarm := manager.pickNextMixed(ctx, providers, model, opts, nil); errWarm != nil {
		b.Fatalf("warmup pickNextMixed error = %v", errWarm)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		auth, exec, provider, errPick := manager.pickNextMixed(ctx, providers, model, opts, nil)
		if errPick != nil || auth == nil || exec == nil || provider == "" {
			b.Fatalf("pickNextMixed failed: auth=%v exec=%v provider=%q err=%v", auth, exec, provider, errPick)
		}
	}
}
