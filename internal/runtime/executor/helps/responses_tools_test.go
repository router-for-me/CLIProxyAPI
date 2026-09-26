package helps

import (
	"context"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func guardTestContext() context.Context {
	return cliproxyexecutor.WithWireContract(context.Background(), &cliproxyexecutor.WireContract{
		SearchAliases:   []string{"tool_search"},
		CustomAliases:   []string{"ccb_abc"},
		ActiveToolBytes: 4096,
	})
}

func TestValidateOutboundToolContractInactiveIsNoop(t *testing.T) {
	// No contract: invalid JSON and huge payloads pass without parsing cost.
	if err := ValidateOutboundToolContract(context.Background(), []byte("{invalid"), 10); err != nil {
		t.Fatalf("inactive guard must be no-op: %v", err)
	}
}

func TestValidateOutboundToolContractKeepsBridgeAliases(t *testing.T) {
	ctx := guardTestContext()
	payload := []byte("{\"tools\": [{\"type\": \"function\", \"name\": \"tool_search\"}, {\"type\": \"function\", \"name\": \"ccb_abc\"}], \"input\": []}")
	if err := ValidateOutboundToolContract(ctx, payload, WireContractByteLimit(ctx)); err != nil {
		t.Fatalf("valid wire must pass: %v", err)
	}
}

func TestValidateOutboundToolContractRejectsPostRuleDamage(t *testing.T) {
	ctx := guardTestContext()
	// A post rule renamed the bridged search alias away.
	payload := []byte("{\"tools\": [{\"type\": \"function\", \"name\": \"other_search\"}], \"input\": []}")
	err := ValidateOutboundToolContract(ctx, payload, WireContractByteLimit(ctx))
	if err == nil {
		t.Fatalf("expected rejection when bridge alias is gone")
	}
	if status, ok := err.(interface{ StatusCode() int }); !ok || status.StatusCode() != 422 {
		t.Fatalf("expected 422 typed error, got %v", err)
	}
}

func TestValidateOutboundToolContractRejectsGrowth(t *testing.T) {
	ctx := cliproxyexecutor.WithWireContract(context.Background(), &cliproxyexecutor.WireContract{ActiveToolBytes: 10})
	payload := []byte("{\"tools\": [{\"type\": \"function\", \"name\": \"tool_search\", \"parameters\": {\"type\": \"object\", \"properties\": {\"q\": {\"type\": \"string\", \"description\": \"padding padding padding padding\"}}}}], \"input\": []}")
	err := ValidateOutboundToolContract(ctx, payload, WireContractByteLimit(ctx))
	if err == nil {
		t.Fatalf("expected budget rejection after post-rule growth")
	}
}

func TestValidateOutboundToolContractIgnoresThinkingChanges(t *testing.T) {
	ctx := guardTestContext()
	// Normal model/thinking adjustments pass; only tool fields are checked.
	payload := []byte("{\"tools\": [{\"type\": \"function\", \"name\": \"tool_search\"}, {\"type\": \"function\", \"name\": \"ccb_abc\"}], \"model\": \"other-model\", \"reasoning\": {\"effort\": \"high\"}, \"input\": []}")
	if err := ValidateOutboundToolContract(ctx, payload, WireContractByteLimit(ctx)); err != nil {
		t.Fatalf("thinking/model changes must pass: %v", err)
	}
}
