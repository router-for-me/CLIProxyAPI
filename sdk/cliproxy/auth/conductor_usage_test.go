package auth

import (
	"context"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestContextWithRequestedModelAliasIncludesReasoningEffort(t *testing.T) {
	ctx := contextWithRequestedModelAlias(context.Background(), cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.RequestedModelMetadataKey:  "client-model",
			cliproxyexecutor.ReasoningEffortMetadataKey: "medium",
		},
	}, "fallback-model", cliproxyexecutor.Request{})

	if got := coreusage.RequestedModelAliasFromContext(ctx); got != "client-model" {
		t.Fatalf("requested model alias = %q, want %q", got, "client-model")
	}
	if got := coreusage.ReasoningEffortFromContext(ctx); got != "medium" {
		t.Fatalf("reasoning effort = %q, want %q", got, "medium")
	}
}

func TestContextWithRequestedModelAliasInfersReasoningEffortFromOriginalRequest(t *testing.T) {
	ctx := contextWithRequestedModelAlias(context.Background(), cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"reasoning":{"effort":"high"}}`),
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
	}, "gpt-5.5", cliproxyexecutor.Request{})

	if got := coreusage.ReasoningEffortFromContext(ctx); got != "high" {
		t.Fatalf("reasoning effort = %q, want %q", got, "high")
	}
}

func TestContextWithRequestedModelAliasInfersReasoningEffortFromPayload(t *testing.T) {
	ctx := contextWithRequestedModelAlias(context.Background(), cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
	}, "gpt-5.5", cliproxyexecutor.Request{
		Payload: []byte(`{"reasoning":{"effort":"xhigh"}}`),
		Format:  sdktranslator.FormatOpenAIResponse,
	})

	if got := coreusage.ReasoningEffortFromContext(ctx); got != "xhigh" {
		t.Fatalf("reasoning effort = %q, want %q", got, "xhigh")
	}
}

func TestContextWithRequestedModelAliasInfersReasoningEffortWithoutSourceFormat(t *testing.T) {
	ctx := contextWithRequestedModelAlias(context.Background(), cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"reasoning":{"effort":"high"}}`),
	}, "gpt-5.5", cliproxyexecutor.Request{})

	if got := coreusage.ReasoningEffortFromContext(ctx); got != "high" {
		t.Fatalf("reasoning effort = %q, want %q", got, "high")
	}
}

func TestContextWithRequestedModelAliasUsesRequestFormatForPayload(t *testing.T) {
	ctx := contextWithRequestedModelAlias(context.Background(), cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
	}, "gemini-3-pro", cliproxyexecutor.Request{
		Payload: []byte(`{"generationConfig":{"thinkingConfig":{"thinkingLevel":"high"}}}`),
		Format:  sdktranslator.FormatGemini,
	})

	if got := coreusage.ReasoningEffortFromContext(ctx); got != "high" {
		t.Fatalf("reasoning effort = %q, want %q", got, "high")
	}
}

func TestContextWithRequestedModelAliasFallsBackToPayloadAfterOriginalRequest(t *testing.T) {
	ctx := contextWithRequestedModelAlias(context.Background(), cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"input":"hello"}`),
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
	}, "gemini-3-pro", cliproxyexecutor.Request{
		Payload: []byte(`{"generationConfig":{"thinkingConfig":{"thinkingLevel":"high"}}}`),
		Format:  sdktranslator.FormatGemini,
	})

	if got := coreusage.ReasoningEffortFromContext(ctx); got != "high" {
		t.Fatalf("reasoning effort = %q, want %q", got, "high")
	}
}
