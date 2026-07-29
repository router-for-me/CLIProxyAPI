package main

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestConfigureRejectsLegacyHostSchema(t *testing.T) {
	raw, errMarshal := json.Marshal(lifecycleRequest{SchemaVersion: 1})
	if errMarshal != nil {
		t.Fatalf("marshal lifecycle request: %v", errMarshal)
	}
	if errConfigure := configure(raw); errConfigure == nil {
		t.Fatal("configure() error = nil for schema version 1")
	}
}

func TestConcurrencySlotReleasedByCompletion(t *testing.T) {
	resetState(pluginConfig{MaxConcurrency: 1})
	first := interceptForTest(t, pluginapi.RequestInterceptRequest{RequestID: "first", Body: []byte(`{"model":"test"}`)})
	if first.Terminate {
		t.Fatalf("first request was terminated: %#v", first)
	}
	second := interceptForTest(t, pluginapi.RequestInterceptRequest{RequestID: "second", Body: []byte(`{"model":"test"}`)})
	if !second.Terminate || second.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second response = %#v", second)
	}

	completionRaw, errMarshal := json.Marshal(pluginapi.RequestCompletion{RequestID: "first", Outcome: pluginapi.RequestCompletionSucceeded})
	if errMarshal != nil {
		t.Fatalf("marshal completion: %v", errMarshal)
	}
	completeRaw, errComplete := completeRequest(completionRaw)
	if errComplete != nil {
		t.Fatalf("completeRequest() error = %v", errComplete)
	}
	if len(completeRaw) == 0 {
		t.Fatal("completeRequest() response is empty")
	}
	third := interceptForTest(t, pluginapi.RequestInterceptRequest{RequestID: "third", Body: []byte(`{"model":"test"}`)})
	if third.Terminate {
		t.Fatalf("third request was terminated after release: %#v", third)
	}
}

func TestPolicyTerminationReturnsCustomResponse(t *testing.T) {
	resetState(pluginConfig{MaxConcurrency: 1, RejectKeyword: "blocked"})
	response := interceptForTest(t, pluginapi.RequestInterceptRequest{RequestID: "blocked", Body: []byte(`{"prompt":"blocked"}`)})
	if !response.Terminate || response.StatusCode != http.StatusForbidden {
		t.Fatalf("response = %#v", response)
	}
	if response.ResponseHeaders.Get("Content-Type") != "application/json" {
		t.Fatalf("response headers = %#v", response.ResponseHeaders)
	}
	if len(response.ResponseBody) == 0 {
		t.Fatal("response body is empty")
	}
}

func resetState(cfg pluginConfig) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.config = cfg
	state.active = make(map[string]struct{})
}

func interceptForTest(t *testing.T, req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	t.Helper()
	raw, errMarshal := json.Marshal(req)
	if errMarshal != nil {
		t.Fatalf("marshal request: %v", errMarshal)
	}
	rawEnvelope, errIntercept := interceptBeforeAuth(raw)
	if errIntercept != nil {
		t.Fatalf("interceptBeforeAuth() error = %v", errIntercept)
	}
	var env envelope
	if errUnmarshal := json.Unmarshal(rawEnvelope, &env); errUnmarshal != nil {
		t.Fatalf("unmarshal envelope: %v", errUnmarshal)
	}
	var response pluginapi.RequestInterceptResponse
	if errUnmarshal := json.Unmarshal(env.Result, &response); errUnmarshal != nil {
		t.Fatalf("unmarshal response: %v", errUnmarshal)
	}
	return response
}
