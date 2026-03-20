package auth

import (
	"context"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

func BenchmarkManagerPickNextAndMarkResult1000Parallel(b *testing.B) {
	manager, _, model := benchmarkManagerSetup(b, 1000, false, false)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		ctx := context.Background()
		opts := cliproxyexecutor.Options{}
		for pb.Next() {
			auth, _, errPick := manager.pickNext(ctx, "gemini", model, opts, nil)
			if errPick != nil || auth == nil {
				b.Fatalf("pickNext failed: auth=%v err=%v", auth, errPick)
			}
			manager.MarkResult(ctx, Result{AuthID: auth.ID, Provider: "gemini", Model: model, Success: true})
		}
	})
}
