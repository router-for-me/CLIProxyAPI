package usage

import (
	"context"
	"strings"
	"testing"
)

func TestUpstreamResponseModelFromContextEmpty(t *testing.T) {
	ctx := BeginUpstreamResponseModelObservation(context.Background())
	if got := UpstreamResponseModelFromContext(ctx); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestObserveUpstreamResponseModelTruncatesRunes(t *testing.T) {
	ctx := BeginUpstreamResponseModelObservation(context.Background())
	name := strings.Repeat("模", 201)
	ObserveUpstreamResponseModel(ctx, "  "+name+"  ", true)
	got := []rune(UpstreamResponseModelFromContext(ctx))
	if len(got) != 200 {
		t.Fatalf("len=%d, want 200", len(got))
	}
}

func TestBeginReplacesObserver(t *testing.T) {
	ctx := BeginUpstreamResponseModelObservation(context.Background())
	ObserveUpstreamResponseModel(ctx, "model-a", true)
	ctx = BeginUpstreamResponseModelObservation(ctx)
	ObserveUpstreamResponseModel(ctx, "model-b", true)
	if got := UpstreamResponseModelFromContext(ctx); got != "model-b" {
		t.Fatalf("got %q, want model-b", got)
	}
}

func TestMapExecutorToExtractionFamily(t *testing.T) {
	if got := MapExecutorToExtractionFamily("claude", "ClaudeExecutor"); got != FamilyClaude {
		t.Fatalf("got %q, want claude", got)
	}
	if got := MapExecutorToExtractionFamily("antigravity", "AntigravityExecutor"); got != FamilyGemini {
		t.Fatalf("got %q, want gemini", got)
	}
	if got := MapExecutorToExtractionFamily("openai", "CodexExecutor"); got != FamilyOpenAI {
		t.Fatalf("got %q, want openai", got)
	}
}
