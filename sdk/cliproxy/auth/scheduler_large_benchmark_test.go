package auth

import (
	"context"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

func BenchmarkManagerPickNext2813(b *testing.B) {
	manager, _, model := benchmarkManagerSetup(b, 2813, false, false)
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

func BenchmarkManagerPickNextAndMarkResult2813(b *testing.B) {
	manager, _, model := benchmarkManagerSetup(b, 2813, false, false)
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

func BenchmarkManagerMarkResultStateChange1000(b *testing.B) {
	benchmarkManagerMarkResultStateChange(b, 1000)
}

func BenchmarkManagerMarkResultStateChange2813(b *testing.B) {
	benchmarkManagerMarkResultStateChange(b, 2813)
}

func BenchmarkManagerMarkResultCooldownWindow1000(b *testing.B) {
	benchmarkManagerMarkResultCooldownWindow(b, 1000, 128)
}

func BenchmarkManagerMarkResultCooldownWindow2813(b *testing.B) {
	benchmarkManagerMarkResultCooldownWindow(b, 2813, 128)
}

func benchmarkManagerMarkResultStateChange(b *testing.B, total int) {
	manager, _, model := benchmarkManagerSetup(b, total, false, false)
	ctx := context.Background()
	opts := cliproxyexecutor.Options{}
	tried := map[string]struct{}{}
	auth, _, errWarm := manager.pickNext(ctx, "gemini", model, opts, tried)
	if errWarm != nil || auth == nil {
		b.Fatalf("warmup pickNext error = %v auth=%v", errWarm, auth)
	}

	quotaErr := &Error{HTTPStatus: 429, Message: "quota"}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		auth, _, errPick := manager.pickNext(ctx, "gemini", model, opts, tried)
		if errPick != nil || auth == nil {
			b.Fatalf("pickNext failed: auth=%v err=%v", auth, errPick)
		}
		manager.MarkResult(ctx, Result{AuthID: auth.ID, Provider: "gemini", Model: model, Success: false, Error: quotaErr})
		manager.MarkResult(ctx, Result{AuthID: auth.ID, Provider: "gemini", Model: model, Success: true})
	}
}

func benchmarkManagerMarkResultCooldownWindow(b *testing.B, total int, window int) {
	manager, _, model := benchmarkManagerSetup(b, total, false, false)
	ctx := context.Background()
	opts := cliproxyexecutor.Options{}
	tried := map[string]struct{}{}
	if _, _, errWarm := manager.pickNext(ctx, "gemini", model, opts, tried); errWarm != nil {
		b.Fatalf("warmup pickNext error = %v", errWarm)
	}

	quotaErr := &Error{HTTPStatus: 429, Message: "quota"}
	blocked := make([]string, 0, window+1)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		auth, _, errPick := manager.pickNext(ctx, "gemini", model, opts, tried)
		if errPick != nil || auth == nil {
			b.Fatalf("pickNext failed: auth=%v err=%v", auth, errPick)
		}
		manager.MarkResult(ctx, Result{AuthID: auth.ID, Provider: "gemini", Model: model, Success: false, Error: quotaErr})
		blocked = append(blocked, auth.ID)
		if len(blocked) > window {
			recoverID := blocked[0]
			blocked = blocked[1:]
			manager.MarkResult(ctx, Result{AuthID: recoverID, Provider: "gemini", Model: model, Success: true})
		}
	}
}
