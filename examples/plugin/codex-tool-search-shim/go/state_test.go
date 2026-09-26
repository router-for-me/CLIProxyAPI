package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)

const stateSearchRequest = `{"tools":[{"type":"tool_search","execution":"client","parameters":{"type":"object"}}]}`

func TestCompletedRequestsDoNotEvictActiveBridgeState(t *testing.T) {
	t.Cleanup(clearRequestStates)
	const activeID = "state-active"
	interceptStateRequest(t, activeID, "openai-response", "openai")

	for index := 0; index < maxRequestStatesForTest()+100; index++ {
		requestID := "state-churn-" + string(rune('a'+index%26)) + "-" + jsonNumber(index)
		interceptStateRequest(t, requestID, "openai-response", "openai")
		completeStateRequest(t, requestID, pluginapiOutcomeSucceeded())
	}

	state, exists := loadRequestState(activeID)
	if !exists || state.catalog == nil || !state.catalog.searchBridge {
		t.Fatalf("active bridge state was evicted: exists=%v state=%#v", exists, state)
	}
}

func TestActiveStateCapacityRejectsNewRequest(t *testing.T) {
	t.Cleanup(clearRequestStates)
	t.Cleanup(func() { _ = applyPluginConfig(nil) })
	if errConfigure := applyPluginConfig([]byte("max_request_states: 1\n")); errConfigure != nil {
		t.Fatalf("applyPluginConfig() error = %v", errConfigure)
	}
	interceptStateRequest(t, "state-capacity-first", "openai-response", "openai")
	firstState, firstExists := loadRequestState("state-capacity-first")
	if !firstExists || firstState.catalog == nil {
		t.Fatalf("first state exists=%v state=%#v", firstExists, firstState)
	}

	raw, errIntercept := interceptRequest("request_after", mustJSON(t, map[string]any{
		"RequestID":    "state-capacity-second",
		"SourceFormat": "openai-response",
		"ToFormat":     "openai",
		"Body":         []byte(stateSearchRequest),
	}))
	if errIntercept != nil {
		t.Fatalf("interceptRequest() error = %v", errIntercept)
	}
	response := decodeRequestInterceptResult(t, raw)
	if !response.Terminate || response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("capacity response = %#v, want terminate 429", response)
	}
}

func TestActiveStateByteCapacityRejectsNewRequest(t *testing.T) {
	t.Cleanup(clearRequestStates)
	t.Cleanup(func() { _ = applyPluginConfig(nil) })
	if errConfigure := applyPluginConfig([]byte("max_state_bytes: 1\n")); errConfigure != nil {
		t.Fatalf("applyPluginConfig() error = %v", errConfigure)
	}
	raw, errIntercept := interceptRequest("request_after", mustJSON(t, map[string]any{
		"RequestID":    "state-byte-capacity",
		"SourceFormat": "openai-response",
		"ToFormat":     "openai",
		"Body":         []byte(stateSearchRequest),
	}))
	if errIntercept != nil {
		t.Fatalf("interceptRequest() error = %v", errIntercept)
	}
	response := decodeRequestInterceptResult(t, raw)
	if !response.Terminate || response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("byte capacity response = %#v, want terminate 429", response)
	}
}

func TestNonResponsesRequestDoesNotRetainState(t *testing.T) {
	t.Cleanup(clearRequestStates)
	raw, errIntercept := interceptRequest("request_after", mustJSON(t, map[string]any{
		"RequestID":    "non-responses-state",
		"SourceFormat": "openai",
		"ToFormat":     "openai",
		"Body":         []byte(`{"tools":[{"type":"function","name":"lookup","parameters":{}}]}`),
	}))
	if errIntercept != nil {
		t.Fatalf("interceptRequest() error = %v", errIntercept)
	}
	if response := decodeRequestInterceptResult(t, raw); response.Terminate {
		t.Fatalf("non-responses request terminated: %#v", response)
	}
	if _, exists := loadRequestState("non-responses-state"); exists {
		t.Fatal("non-responses request retained state")
	}
}

func TestNonResponsesStreamDoesNotCacheCatalog(t *testing.T) {
	t.Cleanup(clearRequestStates)
	_, errIntercept := interceptStreamChunk(mustJSON(t, map[string]any{
		"RequestID":    "non-responses-stream-state",
		"SourceFormat": "openai",
		"ChunkIndex":   -1,
		"OriginalRequest": []byte(`{"tools":[{"type":"namespace","name":"mcp__x","tools":[
			{"type":"function","name":"lookup","parameters":{}}
		]}]}`),
		"Body": []byte{},
	}))
	if errIntercept != nil {
		t.Fatalf("interceptStreamChunk() error = %v", errIntercept)
	}
	if _, exists := loadRequestState("non-responses-stream-state"); exists {
		t.Fatal("non Responses stream cached catalog state")
	}
}

func TestResponseRetainsRequestSourceFormatAcrossReconfigure(t *testing.T) {
	t.Cleanup(clearRequestStates)
	t.Cleanup(func() { _ = applyPluginConfig(nil) })
	if errConfigure := applyPluginConfig([]byte("extra_source_formats:\n  - custom-responses\n")); errConfigure != nil {
		t.Fatalf("applyPluginConfig() error = %v", errConfigure)
	}
	const requestID = "state-custom-source"
	interceptStateRequest(t, requestID, "custom-responses", "openai")
	if errReset := applyPluginConfig(nil); errReset != nil {
		t.Fatalf("applyPluginConfig(nil) error = %v", errReset)
	}

	upstream := []byte(`{"output":[{"type":"function_call","call_id":"c1","name":"tool_search","arguments":"{}"}]}`)
	raw, errResponse := interceptResponse(mustJSON(t, map[string]any{
		"RequestID":    requestID,
		"SourceFormat": "custom-responses",
		"Body":         upstream,
	}))
	if errResponse != nil {
		t.Fatalf("interceptResponse() error = %v", errResponse)
	}
	if body := decodeResponseBodyResult(t, raw); !strings.Contains(string(body), `"tool_search_call"`) {
		t.Fatalf("response body = %s, want tool_search_call after reconfigure", body)
	}

	rawOtherProtocol, errOther := interceptResponse(mustJSON(t, map[string]any{
		"RequestID":    requestID,
		"SourceFormat": "openai",
		"Body":         upstream,
	}))
	if errOther != nil {
		t.Fatalf("interceptResponse(openai) error = %v", errOther)
	}
	if body := decodeResponseBodyResult(t, rawOtherProtocol); len(body) != 0 {
		t.Fatalf("non Responses protocol used request snapshot: %s", body)
	}
}

func TestAllCompletionOutcomesReleaseRequestState(t *testing.T) {
	t.Cleanup(clearRequestStates)
	for _, outcome := range []string{"succeeded", "failed", "rejected", "canceled"} {
		t.Run(outcome, func(t *testing.T) {
			requestID := "state-completion-" + outcome
			interceptStateRequest(t, requestID, "openai-response", "openai")
			completeStateRequest(t, requestID, outcome)
			if _, exists := loadRequestState(requestID); exists {
				t.Fatalf("request state survived completion outcome %q", outcome)
			}
		})
	}
}

func TestMissingOrReplacedStateCannotRewriteResponse(t *testing.T) {
	t.Cleanup(clearRequestStates)
	const requestID = "state-replaced"
	interceptStateRequest(t, requestID, "openai-response", "openai")
	interceptStateRequest(t, requestID, "openai-response", "openai-response")

	raw, errResponse := interceptResponse(mustJSON(t, map[string]any{
		"RequestID":    requestID,
		"SourceFormat": "openai-response",
		"Body":         []byte(`{"type":"function_call","call_id":"c1","name":"tool_search","arguments":"{}"}`),
	}))
	if errResponse != nil {
		t.Fatalf("interceptResponse() error = %v", errResponse)
	}
	if body := decodeResponseBodyResult(t, raw); len(body) != 0 {
		t.Fatalf("replaced native state still rewrote response: %s", body)
	}
}

func interceptStateRequest(t *testing.T, requestID, sourceFormat, toFormat string) {
	t.Helper()
	raw, errIntercept := interceptRequest("request_after", mustJSON(t, map[string]any{
		"RequestID":    requestID,
		"SourceFormat": sourceFormat,
		"ToFormat":     toFormat,
		"Body":         []byte(stateSearchRequest),
	}))
	if errIntercept != nil {
		t.Fatalf("interceptRequest() error = %v", errIntercept)
	}
	if response := decodeRequestInterceptResult(t, raw); response.Terminate {
		t.Fatalf("interceptRequest() terminated: %#v", response)
	}
}

func completeStateRequest(t *testing.T, requestID, outcome string) {
	t.Helper()
	payload, errMarshal := json.Marshal(map[string]any{"RequestID": requestID, "Outcome": outcome})
	if errMarshal != nil {
		t.Fatalf("json.Marshal() error = %v", errMarshal)
	}
	raw, errCall := handleMethod(pluginabi.MethodRequestComplete, payload)
	if errCall != nil {
		t.Fatalf("handleMethod(request.complete) error = %v", errCall)
	}
	var envelopeResult struct {
		OK bool `json:"ok"`
	}
	if errUnmarshal := json.Unmarshal(raw, &envelopeResult); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal() error = %v", errUnmarshal)
	}
	if !envelopeResult.OK {
		t.Fatalf("request.complete envelope = %s", raw)
	}
}

func decodeRequestInterceptResult(t *testing.T, raw []byte) pluginapiRequestInterceptResult {
	t.Helper()
	var wrapped struct {
		Result pluginapiRequestInterceptResult `json:"result"`
	}
	if errUnmarshal := json.Unmarshal(raw, &wrapped); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal() error = %v", errUnmarshal)
	}
	return wrapped.Result
}

type pluginapiRequestInterceptResult struct {
	Body       []byte `json:"Body"`
	Terminate  bool   `json:"Terminate"`
	StatusCode int    `json:"StatusCode"`
}

func maxRequestStatesForTest() int {
	budget := configuredStateBudget()
	return budget.MaxStates
}

func jsonNumber(value int) string {
	if value == 0 {
		return "0"
	}
	var digits [20]byte
	position := len(digits)
	for value > 0 {
		position--
		digits[position] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[position:])
}

func pluginapiOutcomeSucceeded() string {
	return "succeeded"
}
