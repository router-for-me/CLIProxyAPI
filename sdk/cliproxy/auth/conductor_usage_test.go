package auth

import (
	"context"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestContextWithRequestedModelAliasIncludesReasoningEffort(t *testing.T) {
	ctx := contextWithRequestedModelAlias(context.Background(), cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.RequestedModelMetadataKey:  "client-model",
			cliproxyexecutor.ReasoningEffortMetadataKey: "medium",
			cliproxyexecutor.ServiceTierMetadataKey:     "auto",
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
	if gotRequestServiceTier := coreusage.RequestServiceTierFromContext(ctx); gotRequestServiceTier != "auto" {
		t.Fatalf("deprecated request service tier = %q, want %q", gotRequestServiceTier, "auto")
	}
}

func TestContextWithRequestedModelAliasAcceptsDeprecatedTierMetadata(t *testing.T) {
	ctx := contextWithRequestedModelAlias(context.Background(), cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.RequestServiceTierMetadataKey: "priority",
		},
	}, "fallback-model")

	if got := coreusage.ServiceTierFromContext(ctx); got != "priority" {
		t.Fatalf("service tier = %q, want %q", got, "priority")
	}
	if got := coreusage.RequestServiceTierFromContext(ctx); got != "priority" {
		t.Fatalf("request service tier = %q, want %q", got, "priority")
	}
}
