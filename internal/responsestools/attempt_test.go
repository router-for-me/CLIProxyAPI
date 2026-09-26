package responsestools

import (
	"strings"
	"testing"
)

func bridgeAttemptPolicy() RoutePolicy {
	return RoutePolicy{
		ClientSearch:  ClientSearchBridge,
		CustomTools:   CustomToolsFunction,
		CustomGrammar: CustomGrammarDescribe,
		Schema:        SchemaPolicy{LocalRefs: LocalRefsPreserve},
	}
}

func TestPrepareBridgeEndToEnd(t *testing.T) {
	body := []byte("{\"tools\": [{\"type\": \"tool_search\"}, {\"type\": \"custom\", \"name\": \"apply_patch\", \"description\": \"patch\"}], \"input\": []}")
	limiter := NewLimiter(DefaultLimits())
	prepared, err := Prepare(body, bridgeAttemptPolicy(), DefaultLimits(), limiter)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if prepared.Attempt == nil || !prepared.Attempt.Adapted() {
		t.Fatalf("expected adapted attempt")
	}
	if string(prepared.Body) == string(body) {
		t.Fatalf("expected rewritten body")
	}
	if strings.Contains(string(prepared.Body), "\"type\":\"custom\"") {
		t.Fatalf("custom declarations leaked: %s", prepared.Body)
	}
	prepared.Attempt.Close()
	if attempts, _ := limiter.Usage(); attempts != 0 {
		t.Fatalf("lease leaked")
	}
}

func TestPreparePassthroughReturnsOriginalSlice(t *testing.T) {
	body := []byte("{\"model\": \"x\", \"input\": \"hello\"}")
	limiter := NewLimiter(DefaultLimits())
	prepared, err := Prepare(body, bridgeAttemptPolicy(), DefaultLimits(), limiter)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if prepared.Attempt != nil {
		t.Fatalf("unexpected attempt")
	}
	if string(prepared.Body) != string(body) {
		t.Fatalf("body must pass through untouched")
	}
	if attempts, _ := limiter.Usage(); attempts != 0 {
		t.Fatalf("passthrough must not reserve state")
	}
}

func TestPrepareDisabledRejects(t *testing.T) {
	body := []byte("{\"tools\": [{\"type\": \"tool_search\"}], \"input\": []}")
	policy := RoutePolicy{ClientSearch: ClientSearchDisabled}
	_, err := Prepare(body, policy, DefaultLimits(), NewLimiter(DefaultLimits()))
	if err == nil || !IsRequestScopedError(err) {
		t.Fatalf("expected request-scoped rejection, got %v", err)
	}
}

func TestPrepareRejectCustomTools(t *testing.T) {
	body := []byte("{\"tools\": [{\"type\": \"tool_search\"}], \"input\": []}")
	_, err := Prepare(body, RoutePolicy{CustomTools: CustomToolsReject}, DefaultLimits(), NewLimiter(DefaultLimits()))
	if err != nil {
		t.Fatalf("no custom present, must pass: %v", err)
	}
	customBody := []byte("{\"tools\": [{\"type\": \"custom\", \"name\": \"x\"}], \"input\": []}")
	_, err = Prepare(customBody, RoutePolicy{CustomTools: CustomToolsReject}, DefaultLimits(), NewLimiter(DefaultLimits()))
	if err == nil {
		t.Fatalf("expected custom rejection")
	}
}

func TestAttemptResponseRestore(t *testing.T) {
	body := []byte("{\"tools\": [{\"type\": \"tool_search\"}], \"input\": []}")
	limiter := NewLimiter(DefaultLimits())
	prepared, err := Prepare(body, bridgeAttemptPolicy(), DefaultLimits(), limiter)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer prepared.Attempt.Close()
	resp := "{\"output\": [{\"type\": \"function_call\", \"name\": \"tool_search\", \"call_id\": \"c1\", \"arguments\": " + "{\"query\":\"x\"}" + "}]}"
	restored, err := prepared.Attempt.RewriteResponse([]byte(resp))
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !strings.Contains(string(restored), "tool_search_call") {
		t.Fatalf("search call not restored: %s", restored)
	}
}

func TestPrepareRetryRebuildsFromOriginal(t *testing.T) {
	body := []byte("{\"tools\": [{\"type\": \"tool_search\"}], \"input\": []}")
	limiter := NewLimiter(DefaultLimits())
	first, err := Prepare(body, bridgeAttemptPolicy(), DefaultLimits(), limiter)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	first.Attempt.Close()
	// A retry must rebuild from the original client contract, never by
	// re-wrapping an already normalized body.
	second, err := Prepare(body, bridgeAttemptPolicy(), DefaultLimits(), limiter)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	defer second.Attempt.Close()
	if string(first.Body) != string(second.Body) {
		t.Fatalf("retry produced different wire body")
	}
}
