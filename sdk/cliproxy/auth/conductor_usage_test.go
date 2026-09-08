package auth

import (
	"context"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestContextWithRequestedModelAliasIncludesStream(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "stream_false", true: "stream_true"}[stream], func(t *testing.T) {
			ctx := contextWithRequestedModelAlias(context.Background(), cliproxyexecutor.Options{
				Stream: stream,
			}, "fallback-model")

			if got := coreusage.StreamFromContext(ctx); got != stream {
				t.Fatalf("stream = %v, want %v", got, stream)
			}
		})
	}
}

func TestContextWithRequestedModelAliasIncludesReasoningEffort(t *testing.T) {
	ctx := contextWithRequestedModelAlias(context.Background(), cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.RequestedModelMetadataKey:  "client-model",
			cliproxyexecutor.ReasoningEffortMetadataKey: "medium",
			cliproxyexecutor.ServiceTierMetadataKey:     "auto",
			cliproxyexecutor.GenerateMetadataKey:        false,
		},
	}, "fallback-model")

	if got := coreusage.RequestedModelAliasFromContext(ctx); got != "client-model" {
		t.Fatalf("requested model alias = %q, want %q", got, "client-model")
	}
	if got := coreusage.ReasoningEffortFromContext(ctx); got != "medium" {
		t.Fatalf("reasoning effort = %q, want %q", got, "medium")
	}
	gotServiceTier := coreusage.ServiceTierFromContext(ctx)
	if gotServiceTier != "auto" {
		t.Fatalf("service tier = %q, want %q", gotServiceTier, "auto")
	}
	if got := coreusage.GenerateFromContext(ctx); got {
		t.Fatalf("generate = %v, want false", got)
	}
}

func TestContextWithRequestedModelAliasDefaultsGenerateTrue(t *testing.T) {
	ctx := contextWithRequestedModelAlias(context.Background(), cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.RequestedModelMetadataKey: "client-model",
		},
	}, "fallback-model")

	if got := coreusage.GenerateFromContext(ctx); !got {
		t.Fatalf("generate = %v, want true", got)
	}
}

func TestContextWithRequestedModelAliasPreservesExistingGenerateFalse(t *testing.T) {
	ctx := coreusage.WithGenerate(context.Background(), false)
	ctx = contextWithRequestedModelAlias(ctx, cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.RequestedModelMetadataKey: "client-model",
		},
	}, "fallback-model")

	if got := coreusage.GenerateFromContext(ctx); got {
		t.Fatalf("generate = %v, want false", got)
	}
}
