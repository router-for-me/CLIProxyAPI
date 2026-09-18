package helps

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestObserveOpenAITerminalWins(t *testing.T) {
	ctx := usage.BeginUpstreamResponseModelObservation(context.Background())
	usage.BindUpstreamResponseModelFamily(ctx, usage.FamilyOpenAI)
	ObserveUpstreamResponseBytes(ctx, []byte(`{"model":"first-model"}`))
	ObserveUpstreamResponseBytes(ctx, []byte(`{"type":"response.completed","response":{"model":"final-model"}}`))
	if got := usage.UpstreamResponseModelFromContext(ctx); got != "final-model" {
		t.Fatalf("got %q, want final-model", got)
	}
}

func TestObserveRejectsMalformedJSON(t *testing.T) {
	ctx := usage.BeginUpstreamResponseModelObservation(context.Background())
	usage.BindUpstreamResponseModelFamily(ctx, usage.FamilyOpenAI)
	ObserveUpstreamResponseBytes(ctx, []byte(`{"model":"x"`))
	if got := usage.UpstreamResponseModelFromContext(ctx); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestObserveClaudeKeepsFirst(t *testing.T) {
	ctx := usage.BeginUpstreamResponseModelObservation(context.Background())
	usage.BindUpstreamResponseModelFamily(ctx, usage.FamilyClaude)
	ObserveUpstreamResponseBytes(ctx, []byte(`{"message":{"model":"claude-first"}}`))
	ObserveUpstreamResponseBytes(ctx, []byte(`{"model":"claude-later"}`))
	if got := usage.UpstreamResponseModelFromContext(ctx); got != "claude-first" {
		t.Fatalf("got %q, want claude-first", got)
	}
}

func TestObserveGeminiLatestWins(t *testing.T) {
	ctx := usage.BeginUpstreamResponseModelObservation(context.Background())
	usage.BindUpstreamResponseModelFamily(ctx, usage.FamilyGemini)
	ObserveUpstreamResponseBytes(ctx, []byte(`{"modelVersion":"gemini-a"}`))
	ObserveUpstreamResponseBytes(ctx, []byte(`{"response":{"modelVersion":"gemini-b"}}`))
	if got := usage.UpstreamResponseModelFromContext(ctx); got != "gemini-b" {
		t.Fatalf("got %q, want gemini-b", got)
	}
}

func TestObserveSplitSSETerminal(t *testing.T) {
	ctx := usage.BeginUpstreamResponseModelObservation(context.Background())
	usage.BindUpstreamResponseModelFamily(ctx, usage.FamilyOpenAI)
	ObserveUpstreamResponseBytes(ctx, []byte("event: response.completed"))
	ObserveUpstreamResponseBytes(ctx, []byte(`data: {"response":{"model":"final-model"}}`))
	if got := usage.UpstreamResponseModelFromContext(ctx); got != "final-model" {
		t.Fatalf("got %q, want final-model", got)
	}
}
