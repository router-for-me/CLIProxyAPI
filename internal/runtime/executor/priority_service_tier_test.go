package executor

import (
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

func TestApplyPriorityServiceTierCompatibility_FromFastAliasMetadata(t *testing.T) {
	payload := []byte(`{"model":"gpt-5.4"}`)
	metadata := map[string]any{
		cliproxyexecutor.OriginalRequestedModelMetadataKey: "gpt-5.4-high-fast",
	}

	out := applyPriorityServiceTierCompatibility(payload, metadata)

	if got := gjson.GetBytes(out, "service_tier").String(); got != "priority" {
		t.Fatalf("service_tier = %q, want %q", got, "priority")
	}
}

func TestApplyPriorityServiceTierCompatibility_ExplicitFlagOverridesExistingTier(t *testing.T) {
	payload := []byte(`{"model":"gpt-5.4","service_tier":"default"}`)
	metadata := map[string]any{
		cliproxyexecutor.PriorityServiceTierRequestedMetadataKey: true,
	}

	out := applyPriorityServiceTierCompatibility(payload, metadata)

	if got := gjson.GetBytes(out, "service_tier").String(); got != "priority" {
		t.Fatalf("service_tier = %q, want %q", got, "priority")
	}
}

func TestApplyPriorityServiceTierCompatibility_NonFastRequestUnchanged(t *testing.T) {
	payload := []byte(`{"model":"gpt-5.4"}`)
	metadata := map[string]any{
		cliproxyexecutor.OriginalRequestedModelMetadataKey: "gpt-5.4",
	}

	out := applyPriorityServiceTierCompatibility(payload, metadata)

	if gjson.GetBytes(out, "service_tier").Exists() {
		t.Fatalf("service_tier should not be set, payload=%s", string(out))
	}
}
