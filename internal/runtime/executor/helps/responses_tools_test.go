package helps

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/responsestools"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
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

func TestValidateOutboundToolContractRecognizesTranslatedFunctionShapes(t *testing.T) {
	ctx := guardTestContext()
	payload := []byte(`{"tools":[{"type":"function","function":{"name":"tool_search","parameters":{"type":"object"}}},{"type":"function","function":{"name":"ccb_abc","parameters":{"type":"object"}}}],"input":[]}`)
	if err := ValidateOutboundToolContract(ctx, payload, 4096); err != nil {
		t.Fatalf("translated function declarations should retain their aliases: %v", err)
	}
}

func TestValidateOutboundToolContractRecognizesGeminiFunctionDeclarations(t *testing.T) {
	ctx := guardTestContext()
	for _, payload := range [][]byte{
		[]byte(`{"tools":[{"function_declarations":[{"name":"tool_search"},{"name":"ccb_abc"}]}],"input":[]}`),
		[]byte(`{"tools":[{"functionDeclarations":[{"name":"tool_search"},{"name":"ccb_abc"}]}],"input":[]}`),
	} {
		if err := ValidateOutboundToolContract(ctx, payload, WireContractByteLimit(ctx)); err != nil {
			t.Fatalf("Gemini function declarations must pass: %v", err)
		}
	}
}

func TestValidateOutboundToolContractRecognizesAntigravityRequestEnvelope(t *testing.T) {
	ctx := guardTestContext()
	payload := []byte(`{"request":{"tools":[{"functionDeclarations":[{"name":"tool_search"},{"name":"ccb_abc"}]}]}}`)
	if err := ValidateOutboundToolContract(ctx, payload, WireContractByteLimit(ctx)); err != nil {
		t.Fatalf("Antigravity request envelope must retain bridged aliases: %v", err)
	}
}

func TestValidateOutboundToolContractCountsAntigravityRequestEnvelope(t *testing.T) {
	ctx := cliproxyexecutor.WithWireContract(context.Background(), &cliproxyexecutor.WireContract{
		ActiveToolBytes:    10,
		MaxActiveToolBytes: 10,
	})
	payload := []byte(`{"request":{"tools":[{"functionDeclarations":[{"name":"apply_patch","description":"padding padding padding"}]}]}}`)
	err := ValidateOutboundToolContract(ctx, payload, WireContractByteLimit(ctx))
	if err == nil {
		t.Fatal("expected nested Antigravity tool declarations to count toward the outbound budget")
	}
	if status, ok := err.(interface{ StatusCode() int }); !ok || status.StatusCode() != http.StatusRequestEntityTooLarge {
		t.Fatalf("error = %v, want status 413", err)
	}
}

func TestValidateOutboundToolContractRequestUsesFinalBodyAndPreservesRequest(t *testing.T) {
	ctx := guardTestContext()
	payload := []byte(`{"request":{"tools":[{"functionDeclarations":[{"name":"tool_search"},{"name":"ccb_abc"}]}]}}`)
	request, err := http.NewRequest(http.MethodPost, "https://example.invalid", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateOutboundToolContractRequest(ctx, request, WireContractByteLimit(ctx)); err != nil {
		t.Fatalf("validate final request body: %v", err)
	}
	got, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatalf("read request after validation: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("request body changed during validation: got %s, want %s", got, payload)
	}
}

func TestValidateOutboundToolContractRecognizesNamespacedFunctions(t *testing.T) {
	ctx := cliproxyexecutor.WithWireContract(context.Background(), &cliproxyexecutor.WireContract{
		SearchAliases: []string{"fs__tool_search"},
		CustomAliases: []string{"fs__ccb_abc"},
	})
	payload := []byte(`{"tools":[{"type":"namespace","name":"fs","tools":[{"type":"function","name":"tool_search"},{"type":"function","name":"ccb_abc"}]}],"input":[]}`)
	if err := ValidateOutboundToolContract(ctx, payload, WireContractByteLimit(ctx)); err != nil {
		t.Fatalf("namespaced function declarations must pass: %v", err)
	}
}

func TestValidateOutboundToolContractRecognizesNestedNamespaceIdentity(t *testing.T) {
	ctx := cliproxyexecutor.WithWireContract(context.Background(), &cliproxyexecutor.WireContract{
		RequiredFunctionNames: []string{"fs__read"},
	})
	payload := []byte(`{"tools":[{"type":"namespace","name":"fs","tools":[{"type":"function","name":"read","parameters":{"type":"object"}}]}]}`)
	if err := ValidateOutboundToolContract(ctx, payload, WireContractByteLimit(ctx)); err != nil {
		t.Fatalf("nested namespace identity must be qualified when validating declarations: %v", err)
	}
}

// A namespaced tool whose local name merely starts with its namespace is not
// already qualified. The guard must accept the namespace-preserving wire shape
// that the Responses-family executors emit, instead of demanding a name the
// payload never carries.
func TestValidateOutboundToolContractRecognizesPrefixedNamespaceChild(t *testing.T) {
	ctx := cliproxyexecutor.WithWireContract(context.Background(), &cliproxyexecutor.WireContract{
		RequiredFunctionNames: []string{"fs__fs_read"},
	})
	payload := []byte(`{"tools":[{"type":"namespace","name":"fs","tools":[{"type":"function","name":"fs_read","parameters":{"type":"object"}}]}]}`)
	if err := ValidateOutboundToolContract(ctx, payload, WireContractByteLimit(ctx)); err != nil {
		t.Fatalf("namespaced tool sharing its namespace prefix must pass: %v", err)
	}
}

func TestQualifyOutboundToolNameUsesSeparatorBoundary(t *testing.T) {
	cases := []struct {
		namespace string
		name      string
		want      string
	}{
		{"fs", "fs_read", "fs__fs_read"},
		{"fs", "fs", "fs"},
		{"fs", "fs__read", "fs__read"},
		{"fs", "read", "fs__read"},
		{"mcp__acme", "mcp__acme__search", "mcp__acme__search"},
		{"mcp__acme__", "mcp__acme__search", "mcp__acme__search"},
		{"mcp__acme__", "search", "mcp__acme__search"},
		{"", "read", "read"},
	}
	for _, testCase := range cases {
		if got := qualifyOutboundToolName(testCase.namespace, testCase.name); got != testCase.want {
			t.Errorf("qualifyOutboundToolName(%q, %q) = %q, want %q", testCase.namespace, testCase.name, got, testCase.want)
		}
	}
}

func TestValidateOutboundToolContractRejectsReintroducedCustomAlias(t *testing.T) {
	ctx := guardTestContext()
	payload := []byte(`{"tools":[{"type":"custom","name":"tool_search"},{"type":"function","name":"ccb_abc"}],"input":[]}`)
	if err := ValidateOutboundToolContract(ctx, payload, WireContractByteLimit(ctx)); err == nil {
		t.Fatal("post rule reintroduced a bridged alias as a custom tool")
	}
}

func TestValidateOutboundToolContractRejectsReintroducedSearchBuiltin(t *testing.T) {
	ctx := guardTestContext()
	payload := []byte(`{"tools":[{"type":"tool_search"},{"type":"function","name":"tool_search"},{"type":"function","name":"ccb_abc"}],"input":[]}`)
	if err := ValidateOutboundToolContract(ctx, payload, WireContractByteLimit(ctx)); err == nil {
		t.Fatal("post rule reintroduced the bridged search built-in")
	}
}

func TestValidateOutboundToolContractRejectsSearchBuiltinOnHistoryOnlyBridge(t *testing.T) {
	ctx := cliproxyexecutor.WithWireContract(context.Background(), &cliproxyexecutor.WireContract{
		SearchBridgeActive: true,
		HistoryAliases:     []string{"tool_search"},
		HistoryCalls: []cliproxyexecutor.WireToolHistoryReference{{
			Name: "tool_search", CallID: "call_1",
		}},
	})
	payload := []byte(`{"tools":[{"type":"tool_search"}],"input":[{"type":"function_call","name":"tool_search","call_id":"call_1","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"done"}]}`)
	if err := ValidateOutboundToolContract(ctx, payload, WireContractByteLimit(ctx)); err == nil {
		t.Fatal("history-only search bridge reintroduced an unsupported native search declaration")
	}
}

func TestValidateOutboundToolContractRejectsDuplicateBridgedHistoryCalls(t *testing.T) {
	ctx := cliproxyexecutor.WithWireContract(context.Background(), &cliproxyexecutor.WireContract{
		CustomAliases: []string{"ccb_abc"},
		HistoryCalls: []cliproxyexecutor.WireToolHistoryReference{{
			Name: "ccb_abc", CallID: "call_1",
		}},
	})
	payload := []byte(`{"tools":[{"type":"function","name":"ccb_abc"}],"input":[{"type":"function_call","name":"ccb_abc","call_id":"call_1"},{"type":"function_call","name":"ccb_abc","call_id":"call_1"},{"type":"function_call_output","call_id":"call_1","output":"done"}]}`)
	if err := ValidateOutboundToolContract(ctx, payload, WireContractByteLimit(ctx)); err == nil {
		t.Fatal("post rule duplicated a bridged history call")
	}
}

func TestValidateOutboundToolContractRejectsDuplicateBridgedHistoryOutputs(t *testing.T) {
	ctx := cliproxyexecutor.WithWireContract(context.Background(), &cliproxyexecutor.WireContract{
		CustomAliases: []string{"ccb_abc"},
		HistoryCalls: []cliproxyexecutor.WireToolHistoryReference{{
			Name: "ccb_abc", CallID: "call_1",
		}},
	})
	payload := []byte(`{"tools":[{"type":"function","name":"ccb_abc"}],"input":[{"type":"function_call","name":"ccb_abc","call_id":"call_1"},{"type":"function_call_output","call_id":"call_1","output":"one"},{"type":"function_call_output","call_id":"call_1","output":"two"}]}`)
	if err := ValidateOutboundToolContract(ctx, payload, WireContractByteLimit(ctx)); err == nil {
		t.Fatal("post rule duplicated a bridged history output")
	}
}

func TestValidateOutboundToolContractProtectsHistoryOnlyBridgeWithoutDeclaration(t *testing.T) {
	ctx := cliproxyexecutor.WithWireContract(context.Background(), &cliproxyexecutor.WireContract{
		HistoryAliases: []string{"ccb_abc"},
		HistoryCalls: []cliproxyexecutor.WireToolHistoryReference{{
			Name: "ccb_abc", CallID: "call_1",
		}},
	})
	payload := []byte(`{"input":[{"type":"function_call","name":"ccb_abc","call_id":"call_1","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"done"}]}`)
	if err := ValidateOutboundToolContract(ctx, payload, WireContractByteLimit(ctx)); err != nil {
		t.Fatalf("history-only bridge must not require a current declaration: %v", err)
	}
}

func TestValidateOutboundToolContractAcceptsDeclaredNamespacedHistoryWireName(t *testing.T) {
	ctx := cliproxyexecutor.WithWireContract(context.Background(), &cliproxyexecutor.WireContract{
		RequiredFunctionNames: []string{"fs__ccb_abc"},
		HistoryAliases:        []string{"ccb_abc", "fs__ccb_abc"},
		HistoryCalls: []cliproxyexecutor.WireToolHistoryReference{{
			Name: "ccb_abc", AlternateNames: []string{"fs__ccb_abc"}, CallID: "call_1",
		}},
	})
	payload := []byte(`{"tools":[{"type":"function","function":{"name":"fs__ccb_abc","parameters":{"type":"object"}}}],"messages":[{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"fs__ccb_abc","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"done"}]}`)
	if err := ValidateOutboundToolContract(ctx, payload, WireContractByteLimit(ctx)); err != nil {
		t.Fatalf("provider-qualified history identity must match its declared namespace: %v", err)
	}
}

func TestValidateOutboundToolContractRejectsDifferentNamespacedHistoryIdentity(t *testing.T) {
	ctx := cliproxyexecutor.WithWireContract(context.Background(), &cliproxyexecutor.WireContract{
		RequiredFunctionNames: []string{"fs__ccb_abc"},
		HistoryAliases:        []string{"ccb_abc", "fs__ccb_abc"},
		HistoryCalls: []cliproxyexecutor.WireToolHistoryReference{{
			Name: "ccb_abc", AlternateNames: []string{"fs__ccb_abc"}, CallID: "call_1",
		}},
	})
	payload := []byte(`{"tools":[{"type":"function","function":{"name":"fs__ccb_abc","parameters":{"type":"object"}}}],"messages":[{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"other__ccb_abc","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"done"}]}`)
	if err := ValidateOutboundToolContract(ctx, payload, WireContractByteLimit(ctx)); err == nil {
		t.Fatal("post rule changed the namespace of a bridged history call")
	}
}

func TestValidateOutboundToolContractRejectsHistoryOnlyBridgeMutation(t *testing.T) {
	ctx := cliproxyexecutor.WithWireContract(context.Background(), &cliproxyexecutor.WireContract{
		HistoryAliases: []string{"ccb_abc"},
		HistoryCalls: []cliproxyexecutor.WireToolHistoryReference{{
			Name: "ccb_abc", CallID: "call_1",
		}},
	})
	payload := []byte(`{"input":[{"type":"function_call","name":"other","call_id":"call_1","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"done"}]}`)
	if err := ValidateOutboundToolContract(ctx, payload, WireContractByteLimit(ctx)); err == nil {
		t.Fatal("post rule changed a history-only bridged tool identity")
	}
}

func TestValidateOutboundToolContractDoesNotTreatBusinessDataAsHistory(t *testing.T) {
	ctx := cliproxyexecutor.WithWireContract(context.Background(), &cliproxyexecutor.WireContract{
		CustomAliases: []string{"ccb_abc"},
		HistoryCalls: []cliproxyexecutor.WireToolHistoryReference{{
			Name: "ccb_abc", CallID: "call_1",
		}},
	})
	payload := []byte(`{"tools":[{"type":"function","function":{"name":"ccb_abc","parameters":{"type":"object"}}}],"messages":[{"role":"user","id":"ordinary-record","function":{"name":"ccb_abc"},"content":"opaque user data"},{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"ccb_abc","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"done"}]}`)
	if err := ValidateOutboundToolContract(ctx, payload, WireContractByteLimit(ctx)); err != nil {
		t.Fatalf("ordinary business fields must not be interpreted as tool history: %v", err)
	}
}

func TestValidateOutboundToolContractRecognizesProviderHistoryShapes(t *testing.T) {
	ctx := cliproxyexecutor.WithWireContract(context.Background(), &cliproxyexecutor.WireContract{
		CustomAliases: []string{"ccb_abc"},
		HistoryCalls: []cliproxyexecutor.WireToolHistoryReference{{
			Name: "ccb_abc", CallID: "call_1",
		}},
	})
	for name, payload := range map[string][]byte{
		"responses":   []byte(`{"tools":[{"type":"function","name":"ccb_abc"}],"input":[{"type":"function_call","name":"ccb_abc","call_id":"call_1"},{"type":"function_call_output","call_id":"call_1","output":"done"}]}`),
		"chat":        []byte(`{"tools":[{"type":"function","function":{"name":"ccb_abc","parameters":{"type":"object"}}}],"messages":[{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"ccb_abc","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"done"}]}`),
		"claude":      []byte(`{"tools":[{"name":"ccb_abc","input_schema":{"type":"object"}}],"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"ccb_abc","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"done"}]}]}`),
		"gemini":      []byte(`{"tools":[{"functionDeclarations":[{"name":"ccb_abc"}]}],"contents":[{"role":"model","parts":[{"functionCall":{"id":"call_1","name":"ccb_abc","args":{}}}]},{"role":"user","parts":[{"functionResponse":{"id":"call_1","name":"ccb_abc","response":{"content":"done"}}}]}]}`),
		"antigravity": []byte(`{"request":{"tools":[{"functionDeclarations":[{"name":"ccb_abc"}]}],"contents":[{"role":"model","parts":[{"functionCall":{"id":"call_1","name":"ccb_abc","args":{}}}]},{"role":"user","parts":[{"functionResponse":{"id":"call_1","name":"ccb_abc","response":{"content":"done"}}}]}]}}`),
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateOutboundToolContract(ctx, payload, WireContractByteLimit(ctx)); err != nil {
				t.Fatalf("provider history must retain call identity and result linkage: %v", err)
			}
		})
	}
}

func TestWireContractErrorIsAvailabilityNeutralAndRequestScoped(t *testing.T) {
	err := wireError(422, "invalid tool contract")
	if !responsestools.IsRequestScopedError(err) {
		t.Fatal("wire contract error must be recognized as a request-scoped bridge error")
	}
	neutral, ok := err.(interface{ AvailabilityNeutral() bool })
	if !ok || !neutral.AvailabilityNeutral() {
		t.Fatal("wire contract error must be availability-neutral")
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
	ctx := cliproxyexecutor.WithWireContract(context.Background(), &cliproxyexecutor.WireContract{ActiveToolBytes: 10, MaxActiveToolBytes: 10})
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
