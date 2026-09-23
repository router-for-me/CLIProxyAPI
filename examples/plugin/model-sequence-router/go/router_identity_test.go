package main

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

// TestRouteHoldsSequenceWhenPromptCacheKeyAppears verifies that a credential-affinity
// identifier appearing mid-conversation cannot restart that conversation's cursor.
func TestRouteHoldsSequenceWhenPromptCacheKeyAppears(t *testing.T) {
	runtime := newTestRuntime(t)

	opening := runtime.route(responsesRoute(t, "conv-alpha", "", 1))
	if opening.Target != "codex" {
		t.Fatalf("opening turn = %#v, want the first sequence position", opening)
	}
	runtime.route(responsesRoute(t, "conv-alpha", "cache-lane", 2))
	runtime.route(responsesRoute(t, "conv-alpha", "cache-lane", 3))

	fourth := runtime.route(responsesRoute(t, "conv-alpha", "cache-lane", 4))
	if fourth.Target != "claude" {
		t.Fatalf("sequence restarted when the prompt cache key appeared: %#v", fourth)
	}
}

// TestRouteKeepsConversationsSharingOneCacheLaneApart verifies that one cache
// affinity lane cannot merge the cursors of two conversations.
func TestRouteKeepsConversationsSharingOneCacheLaneApart(t *testing.T) {
	runtime := newTestRuntime(t)
	for turn := 1; turn <= 3; turn++ {
		runtime.route(responsesRoute(t, "conv-alpha", "shared-lane", turn))
	}

	other := runtime.route(responsesRoute(t, "conv-beta", "shared-lane", 1))
	if other.Target != "codex" {
		t.Fatalf("second conversation inherited the first conversation's position: %#v", other)
	}
}

// TestRouteReplaysObjectInteractionsInput verifies that one Interactions turn sent as
// an object and the equivalent single-element array share one observed turn before a
// longer transcript advances.
func TestRouteReplaysObjectInteractionsInput(t *testing.T) {
	runtime := newTestRuntime(t)
	logs := captureRouteLogs(runtime)
	opening := `{"role":"user","steps":[{"type":"user_input","content":[{"text":"object opening"}]}]}`
	answer := `{"role":"assistant","steps":[{"type":"model_output","content":[{"text":"answer"}]}]}`
	second := `{"role":"user","steps":[{"type":"user_input","content":[{"text":"second turn"}]}]}`
	request := pluginapi.ModelRouteRequest{
		RequestedModel:     "Iterative-Model",
		SourceFormat:       string(translator.FormatInteractions),
		AvailableProviders: []string{"codex", "claude"},
		Body:               []byte(`{"input":` + opening + `}`),
	}

	runtime.route(request)
	runtime.route(request)
	request.Body = []byte(`{"input":[` + opening + `]}`)
	runtime.route(request)
	request.Body = []byte(`{"input":[` + opening + `,` + answer + `,` + second + `]}`)
	runtime.route(request)

	assertRouteRecords(t, logs, []routeRecordExpectation{
		{outcome: "advanced", sequenceIndex: 0},
		{outcome: "replayed", sequenceIndex: 0},
		{outcome: "replayed", sequenceIndex: 0},
		{outcome: "advanced", sequenceIndex: 1},
	})
}

// TestRouteWalksSequenceAcrossIncrementalContinuation verifies that the turns of one
// long-lived session walk the configured sequence while the provider holds the
// transcript and each request carries only its newest items.
func TestRouteWalksSequenceAcrossIncrementalContinuation(t *testing.T) {
	runtime := newTestRuntime(t)
	logs := captureRouteLogs(runtime)
	opening := responsesRoute(t, "ws-conv", "", 1)
	second := opening
	second.Body = []byte(`{"previous_response_id":"resp-1","input":[{"role":"user","content":"second turn"}]}`)
	third := opening
	third.Body = []byte(`{"previous_response_id":"resp-2","input":[{"role":"user","content":"third turn"}]}`)
	toolOutput := opening
	toolOutput.Body = []byte(`{"previous_response_id":"resp-3","input":[{"type":"function_call_output","call_id":"call-1","output":"done"}]}`)

	runtime.route(opening)
	observeResponse(runtime, opening, "resp-1")
	runtime.route(second)
	observeResponse(runtime, second, "resp-2")
	runtime.route(second)
	runtime.route(third)
	observeResponse(runtime, third, "resp-3")
	runtime.route(toolOutput)

	assertRouteRecords(t, logs, []routeRecordExpectation{
		{outcome: "advanced", sequenceIndex: 0},
		{outcome: "advanced", sequenceIndex: 1},
		{outcome: "replayed", sequenceIndex: 1},
		{outcome: "advanced", sequenceIndex: 2},
		{outcome: "advanced", sequenceIndex: 3},
	})
}

// TestRouteReplaysScalarResponsesInput verifies that scalar and equivalent
// array input share one observed turn before an extended transcript advances.
func TestRouteReplaysScalarResponsesInput(t *testing.T) {
	runtime := newTestRuntime(t)
	logs := captureRouteLogs(runtime)
	scalar := responsesRoute(t, "scalar", "", 1)
	scalar.Body = []byte(`{"input":"scalar opening"}`)

	runtime.route(scalar)
	runtime.route(scalar)
	runtime.route(responsesRoute(t, "scalar", "", 1))
	runtime.route(responsesRoute(t, "scalar", "", 2))

	assertRouteRecords(t, logs, []routeRecordExpectation{
		{outcome: "advanced", sequenceIndex: 0},
		{outcome: "replayed", sequenceIndex: 0},
		{outcome: "replayed", sequenceIndex: 0},
		{outcome: "advanced", sequenceIndex: 1},
	})
}
