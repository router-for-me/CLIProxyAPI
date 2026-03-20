package auth

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
)

func benchmarkCooldownWaitSetup(b *testing.B, total int) (*Manager, []string, string) {
	b.Helper()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.SetRetryConfig(2, 0, 0)
	providers := []string{"gemini", "claude"}
	model := "bench-cooldown-model"
	reg := registry.GetGlobalRegistry()

	for index := 0; index < total; index++ {
		provider := providers[index%len(providers)]
		auth := &Auth{
			ID:       fmt.Sprintf("cooldown-%s-%04d", provider, index),
			Provider: provider,
		}
		if index%5 == 0 {
			auth.Metadata = map[string]any{"request_retry": 0}
		}
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			b.Fatalf("Register(%s) error = %v", auth.ID, errRegister)
		}
		reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}})
		retryAfter := 5*time.Second + time.Duration(index%11)*100*time.Millisecond
		manager.MarkResult(context.Background(), Result{
			AuthID:     auth.ID,
			Provider:   provider,
			Model:      model,
			Success:    false,
			Error:      &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"},
			RetryAfter: &retryAfter,
		})
	}

	b.Cleanup(func() {
		for index := 0; index < total; index++ {
			provider := providers[index%len(providers)]
			reg.UnregisterClient(fmt.Sprintf("cooldown-%s-%04d", provider, index))
		}
	})

	return manager, providers, model
}

func BenchmarkManagerClosestCooldownWait1000(b *testing.B) {
	manager, providers, model := benchmarkCooldownWaitSetup(b, 1000)
	if wait, found := manager.closestCooldownWait(providers, model, 0); !found || wait <= 0 {
		b.Fatalf("warmup closestCooldownWait failed: wait=%v found=%v", wait, found)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		wait, found := manager.closestCooldownWait(providers, model, 0)
		if !found || wait <= 0 {
			b.Fatalf("closestCooldownWait failed: wait=%v found=%v", wait, found)
		}
	}
}
