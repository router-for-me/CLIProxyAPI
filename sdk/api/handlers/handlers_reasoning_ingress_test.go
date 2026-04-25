package handlers

import (
	"bytes"
	"context"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
	"github.com/tidwall/gjson"
)

func ingressTestContext(method, path string) context.Context {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(method, path, nil)
	return context.WithValue(context.Background(), "gin", ginCtx)
}

func TestApplyIngressReasoningDefaults_OpenAIMissingOnlyInject(t *testing.T) {
	cfg := &sdkconfig.SDKConfig{
		DefaultReasoningOnIngressByFormat: map[string]internalconfig.ReasoningIngressDefault{
			internalconfig.ReasoningIngressFormatOpenAI: {
				Policy: internalconfig.ReasoningIngressPolicyMissingOnly,
				Mode:   internalconfig.ReasoningModeEffort,
				Value:  "xhigh",
			},
		},
	}

	ctx := ingressTestContext("POST", "/v1/chat/completions")
	body := []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"hello"}]}`)

	out, changed := applyIngressReasoningDefaults(ctx, cfg, "openai", body)
	if !changed {
		t.Fatalf("expected ingress reasoning defaults to be applied")
	}
	if got := gjson.GetBytes(out, "reasoning_effort").String(); got != "xhigh" {
		t.Fatalf("reasoning_effort = %q, want %q", got, "xhigh")
	}
	if got := gjson.GetBytes(out, "reasoning.effort").String(); got != "xhigh" {
		t.Fatalf("reasoning.effort = %q, want %q", got, "xhigh")
	}
}

func TestApplyIngressReasoningDefaults_OpenAIMissingOnlyKeepExplicit(t *testing.T) {
	cfg := &sdkconfig.SDKConfig{
		DefaultReasoningOnIngressByFormat: map[string]internalconfig.ReasoningIngressDefault{
			internalconfig.ReasoningIngressFormatOpenAI: {
				Policy: internalconfig.ReasoningIngressPolicyMissingOnly,
				Mode:   internalconfig.ReasoningModeEffort,
				Value:  "xhigh",
			},
		},
	}

	ctx := ingressTestContext("POST", "/v1/chat/completions")
	body := []byte(`{"model":"gpt-5","reasoning_effort":"low","reasoning":{"effort":"low"}}`)

	out, changed := applyIngressReasoningDefaults(ctx, cfg, "openai", body)
	if changed {
		t.Fatalf("expected ingress defaults not to override explicit request values")
	}
	if !bytes.Equal(out, body) {
		t.Fatalf("payload changed unexpectedly: %s", string(out))
	}
}

func TestApplyIngressReasoningDefaults_OpenAIForceOverride(t *testing.T) {
	cfg := &sdkconfig.SDKConfig{
		DefaultReasoningOnIngressByFormat: map[string]internalconfig.ReasoningIngressDefault{
			internalconfig.ReasoningIngressFormatOpenAI: {
				Policy: internalconfig.ReasoningIngressPolicyForceOverride,
				Mode:   internalconfig.ReasoningModeEffort,
				Value:  "high",
			},
		},
	}

	ctx := ingressTestContext("POST", "/v1/responses")
	body := []byte(`{"model":"gpt-5","reasoning_effort":"none","reasoning":{"effort":"none"}}`)

	out, changed := applyIngressReasoningDefaults(ctx, cfg, "openai-response", body)
	if !changed {
		t.Fatalf("expected ingress defaults to override explicit request values")
	}
	if got := gjson.GetBytes(out, "reasoning_effort").String(); got != "high" {
		t.Fatalf("reasoning_effort = %q, want %q", got, "high")
	}
	if got := gjson.GetBytes(out, "reasoning.effort").String(); got != "high" {
		t.Fatalf("reasoning.effort = %q, want %q", got, "high")
	}
}

func TestApplyIngressReasoningDefaults_ClaudeAdaptive(t *testing.T) {
	cfg := &sdkconfig.SDKConfig{
		DefaultReasoningOnIngressByFormat: map[string]internalconfig.ReasoningIngressDefault{
			internalconfig.ReasoningIngressFormatClaude: {
				Policy: internalconfig.ReasoningIngressPolicyMissingOnly,
				Mode:   internalconfig.ReasoningModeAdaptiveEffort,
				Value:  "high",
			},
		},
	}

	ctx := ingressTestContext("POST", "/v1/messages")
	body := []byte(`{"model":"claude-sonnet-4-5","thinking":{"budget_tokens":1024}}`)

	out, changed := applyIngressReasoningDefaults(ctx, cfg, "claude", body)
	if !changed {
		t.Fatalf("expected ingress defaults to be applied")
	}
	if got := gjson.GetBytes(out, "thinking.type").String(); got != "adaptive" {
		t.Fatalf("thinking.type = %q, want %q", got, "adaptive")
	}
	if got := gjson.GetBytes(out, "output_config.effort").String(); got != "high" {
		t.Fatalf("output_config.effort = %q, want %q", got, "high")
	}
	if gjson.GetBytes(out, "thinking.budget_tokens").Exists() {
		t.Fatalf("thinking.budget_tokens should be removed")
	}
}

func TestApplyIngressReasoningDefaults_ClaudeDisabledForceOverride(t *testing.T) {
	cfg := &sdkconfig.SDKConfig{
		DefaultReasoningOnIngressByFormat: map[string]internalconfig.ReasoningIngressDefault{
			internalconfig.ReasoningIngressFormatClaude: {
				Policy: internalconfig.ReasoningIngressPolicyForceOverride,
				Mode:   internalconfig.ReasoningModeDisabled,
				Value:  "disabled",
			},
		},
	}

	ctx := ingressTestContext("POST", "/v1/messages")
	body := []byte(`{"model":"claude-sonnet-4-5","thinking":{"type":"adaptive","budget_tokens":1024},"output_config":{"effort":"high"}}`)

	out, changed := applyIngressReasoningDefaults(ctx, cfg, "claude", body)
	if !changed {
		t.Fatalf("expected ingress defaults to be applied")
	}
	if got := gjson.GetBytes(out, "thinking.type").String(); got != "disabled" {
		t.Fatalf("thinking.type = %q, want %q", got, "disabled")
	}
	if gjson.GetBytes(out, "output_config.effort").Exists() {
		t.Fatalf("output_config.effort should be removed")
	}
	if gjson.GetBytes(out, "thinking.budget_tokens").Exists() {
		t.Fatalf("thinking.budget_tokens should be removed")
	}
}

func TestApplyIngressReasoningDefaults_GeminiLevel(t *testing.T) {
	cfg := &sdkconfig.SDKConfig{
		DefaultReasoningOnIngressByFormat: map[string]internalconfig.ReasoningIngressDefault{
			internalconfig.ReasoningIngressFormatGemini: {
				Policy: internalconfig.ReasoningIngressPolicyMissingOnly,
				Mode:   internalconfig.ReasoningModeLevel,
				Value:  "medium",
			},
		},
	}

	ctx := ingressTestContext("POST", "/v1beta/models/gemini-2.5-pro:streamGenerateContent")
	body := []byte(`{"generationConfig":{"thinkingConfig":{"thinkingLevel":"","thinkingBudget":1024,"thinking_budget":2048}}}`)

	out, changed := applyIngressReasoningDefaults(ctx, cfg, "gemini", body)
	if !changed {
		t.Fatalf("expected ingress defaults to be applied")
	}
	if got := gjson.GetBytes(out, "generationConfig.thinkingConfig.thinkingLevel").String(); got != "medium" {
		t.Fatalf("thinkingLevel = %q, want %q", got, "medium")
	}
	if gjson.GetBytes(out, "generationConfig.thinkingConfig.thinkingBudget").Exists() {
		t.Fatalf("thinkingBudget should be removed")
	}
	if gjson.GetBytes(out, "generationConfig.thinkingConfig.thinking_budget").Exists() {
		t.Fatalf("thinking_budget should be removed")
	}
}

func TestApplyIngressReasoningDefaults_ScopeNotMatched(t *testing.T) {
	cfg := &sdkconfig.SDKConfig{
		DefaultReasoningOnIngressByFormat: map[string]internalconfig.ReasoningIngressDefault{
			internalconfig.ReasoningIngressFormatOpenAI: {
				Policy: internalconfig.ReasoningIngressPolicyMissingOnly,
				Mode:   internalconfig.ReasoningModeEffort,
				Value:  "xhigh",
			},
		},
	}

	ctx := ingressTestContext("POST", "/v1/completions")
	body := []byte(`{"model":"gpt-5","prompt":"hello"}`)

	out, changed := applyIngressReasoningDefaults(ctx, cfg, "openai", body)
	if changed {
		t.Fatalf("expected ingress defaults not to be applied for unmatched route")
	}
	if !bytes.Equal(out, body) {
		t.Fatalf("payload changed unexpectedly: %s", string(out))
	}
}
