package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// replayInput builds the transcript one rotated turn replays: an opening user
// turn, one reasoning item, the answer that item framed, and the new user turn.
func replayInput(reasoning map[string]any) []any {
	return []any{
		map[string]any{"type": "message", "role": "user", "content": "opening question"},
		reasoning,
		map[string]any{"type": "message", "role": "assistant", "content": "framed answer"},
		map[string]any{"type": "message", "role": "user", "content": "following question"},
	}
}

// projectionRequest builds one after-credential call for the named lane.
func projectionRequest(t *testing.T, requestedModel, model string, input []any) pluginapi.RequestInterceptRequest {
	t.Helper()
	return pluginapi.RequestInterceptRequest{
		RequestID: "projection-request", TraceID: "projection-trace",
		RequestedModel: requestedModel, Model: model,
		SourceFormat: "openai-response", ToFormat: "codex",
		Body: marshalBody(t, map[string]any{"model": model, "input": input}),
	}
}

// observeReasoningItem feeds one finished reasoning item as the response of the
// named lane, binding that item to the lane the plugin dispatched.
func observeReasoningItem(t *testing.T, model string, item map[string]any) {
	t.Helper()
	chunk := pluginapi.StreamChunkInterceptRequest{
		RequestID: "provenance-request", RequestedModel: "Iterative-Model", Model: model,
		SourceFormat: "openai-response", ChunkIndex: 0,
		Body: marshalBody(t, map[string]any{"type": "response.output_item.done", "item": item}),
	}
	if _, errHandle := handleMethod(pluginabi.MethodResponseInterceptStreamChunk, marshalBody(t, chunk)); errHandle != nil {
		t.Fatal(errHandle)
	}
}

// afterCredentialBody runs the after-credential hook and returns the replacement
// body it answered with, which is empty when the projection withheld nothing.
func afterCredentialBody(t *testing.T, req pluginapi.RequestInterceptRequest) []byte {
	t.Helper()
	result, errHandle := handleMethod(pluginabi.MethodRequestInterceptAfter, marshalBody(t, req))
	if errHandle != nil {
		t.Fatal(errHandle)
	}
	var reply envelope
	if errDecode := json.Unmarshal(result, &reply); errDecode != nil {
		t.Fatal(errDecode)
	}
	if !reply.OK {
		t.Fatalf("after-credential hook failed: %s", result)
	}
	var projection pluginapi.RequestInterceptResponse
	if errDecode := json.Unmarshal(reply.Result, &projection); errDecode != nil {
		t.Fatal(errDecode)
	}
	return projection.Body
}

// claudeReasoningItem names the reasoning the claude position produced.
func claudeReasoningItem() map[string]any {
	return map[string]any{
		"id": "rs-claude-1", "type": "reasoning", "status": "completed",
		"encrypted_content": "claude-private-blob",
	}
}

// TestLaneProjectionWithholdsForeignReasoning verifies one lane's reasoning
// leaves the transcript another lane receives while every other item survives
// byte for byte, so both providers' answers stay interleaved in one thread.
func TestLaneProjectionWithholdsForeignReasoning(t *testing.T) {
	installTestPlugin(t, newTestRuntime(t))
	reasoning := claudeReasoningItem()
	observeReasoningItem(t, "opus(high)", reasoning)

	body := afterCredentialBody(t, projectionRequest(t, "Iterative-Model", "terra", replayInput(reasoning)))
	if len(body) == 0 {
		t.Fatal("the codex lane received the claude reasoning item")
	}
	var projected map[string]any
	if errDecode := json.Unmarshal(body, &projected); errDecode != nil {
		t.Fatal(errDecode)
	}
	items, isArray := projected["input"].([]any)
	if !isArray {
		t.Fatalf("projected input = %#v, want an array", projected["input"])
	}
	if len(items) != 3 {
		t.Fatalf("projected items = %d, want 3", len(items))
	}
	original := replayInput(reasoning)
	for index, want := range []any{original[0], original[2], original[3]} {
		if string(marshalBody(t, items[index])) != string(marshalBody(t, want)) {
			t.Fatalf("item %d = %s, want %s", index, marshalBody(t, items[index]), marshalBody(t, want))
		}
	}
	if projected["model"] != "terra" {
		t.Fatalf("projected model = %#v, want terra", projected["model"])
	}
}

// TestLaneProjectionReturnsReasoningToItsProducer verifies the lane that framed
// one reasoning item keeps that item on every later turn it serves.
func TestLaneProjectionReturnsReasoningToItsProducer(t *testing.T) {
	installTestPlugin(t, newTestRuntime(t))
	reasoning := claudeReasoningItem()
	observeReasoningItem(t, "opus(high)", reasoning)

	body := afterCredentialBody(t, projectionRequest(t, "Iterative-Model", "opus(high)", replayInput(reasoning)))
	if len(body) != 0 {
		t.Fatalf("the producing lane lost its own reasoning: %s", body)
	}
}

// TestLaneProjectionAnswersEachEvidenceState verifies that an unrecorded owner,
// an item carrying no join key, a model outside the configured aliases, and a
// transcript holding no reasoning each reach their own outcome.
func TestLaneProjectionAnswersEachEvidenceState(t *testing.T) {
	plainTurn := map[string]any{"type": "message", "role": "user", "content": "opening question"}
	cases := []struct {
		name           string
		requestedModel string
		model          string
		input          []any
		wantProjection bool
	}{
		{
			name: "unrecorded owner", requestedModel: "Iterative-Model", model: "terra",
			input:          replayInput(map[string]any{"id": "rs-unrecorded", "type": "reasoning", "status": "completed"}),
			wantProjection: true,
		},
		{
			name: "item without identifier", requestedModel: "Iterative-Model", model: "terra",
			input:          replayInput(map[string]any{"type": "reasoning", "status": "completed"}),
			wantProjection: false,
		},
		{
			name: "model outside the configured aliases", requestedModel: "unrelated-model", model: "terra",
			input:          replayInput(map[string]any{"id": "rs-unrecorded", "type": "reasoning", "status": "completed"}),
			wantProjection: false,
		},
		{
			name: "transcript holding no reasoning", requestedModel: "Iterative-Model", model: "terra",
			input:          []any{plainTurn},
			wantProjection: false,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			installTestPlugin(t, newTestRuntime(t))
			body := afterCredentialBody(t, projectionRequest(t, testCase.requestedModel, testCase.model, testCase.input))
			if (len(body) != 0) != testCase.wantProjection {
				t.Fatalf("projection answered %d body bytes, want a projection = %v", len(body), testCase.wantProjection)
			}
		})
	}
}

// TestReasoningOwnerSweepReleasesOnlyExpiredOwners verifies that the shared
// sweep releases an expired ownership record while preserving a live one.
func TestReasoningOwnerSweepReleasesOnlyExpiredOwners(t *testing.T) {
	now := time.Unix(100, 0)
	store := newReasoningOwnerStore(func() time.Time { return now })
	ttl := time.Minute
	departed := reasoningOwnerKey{Generation: 1, ItemID: "rs-departed"}
	active := reasoningOwnerKey{Generation: 1, ItemID: "rs-active"}
	claudeLane := reasoningLane{Provider: "claude", Model: "claude-opus-5"}
	codexLane := reasoningLane{Provider: "codex", Model: "gpt-5.6-sol"}

	store.record(departed, claudeLane, ttl)
	now = now.Add(2 * time.Minute)
	store.record(active, codexLane, ttl)

	cleanupExpiredStores(store)

	if size := store.size(); size != 1 {
		t.Fatalf("owners held after sweep = %d, want 1", size)
	}
	if lane, recorded := store.renewLane(departed, ttl); recorded {
		t.Fatalf("the swept item still names lane %s", lane)
	}
	if lane, recorded := store.renewLane(active, ttl); !recorded || lane != codexLane {
		t.Fatalf("live owner = %s recorded = %v, want %s", lane, recorded, codexLane)
	}
}

// installClockedTestPlugin installs the test runtime with an ownership store
// reading the returned instant, which a test advances between turns.
func installClockedTestPlugin(t *testing.T) (*runtimeState, *time.Time) {
	t.Helper()
	now := time.Unix(100, 0)
	runtime := newTestRuntime(t)
	runtime.owners = newReasoningOwnerStore(func() time.Time { return now })
	installTestPlugin(t, runtime)
	return runtime, &now
}

// TestLaneProjectionRenewsOwnershipWhileConversationContinues verifies every
// projection extends the ownership record it consults, withheld or kept, so
// reasoning older than session_ttl keeps its lane while turns keep arriving.
func TestLaneProjectionRenewsOwnershipWhileConversationContinues(t *testing.T) {
	runtime, now := installClockedTestPlugin(t)
	reasoning := claudeReasoningItem()
	observeReasoningItem(t, "opus(high)", reasoning)
	foreignTurn := projectionRequest(t, "Iterative-Model", "terra", replayInput(reasoning))
	owningTurn := projectionRequest(t, "Iterative-Model", "opus(high)", replayInput(reasoning))

	// Each step stays inside one session_ttl while the two together exceed it,
	// so only a renewal at the first step keeps the owner live at the second.
	step := runtime.loadedConfig().SessionTTL * 5 / 6
	*now = now.Add(step)
	if body := afterCredentialBody(t, foreignTurn); len(body) == 0 {
		t.Fatal("the codex lane received the claude reasoning item after one step")
	}
	*now = now.Add(step)
	if body := afterCredentialBody(t, owningTurn); len(body) != 0 {
		t.Fatalf("the producing lane lost its renewed reasoning after two steps: %s", body)
	}
	if body := afterCredentialBody(t, foreignTurn); len(body) == 0 {
		t.Fatal("the codex lane received the claude reasoning item after two steps")
	}
	cleanupExpiredStores(runtime.owners)
	if size := runtime.owners.size(); size != 1 {
		t.Fatalf("owners held after sweep = %d, want the renewed owner", size)
	}
}

// TestLaneProjectionReleasesOwnershipAfterIdleTTL verifies an ownership record
// no turn consulted for session_ttl lapses, withholds its item even from the
// producing lane, and stays lapsed rather than reviving on that lookup.
func TestLaneProjectionReleasesOwnershipAfterIdleTTL(t *testing.T) {
	runtime, now := installClockedTestPlugin(t)
	reasoning := claudeReasoningItem()
	observeReasoningItem(t, "opus(high)", reasoning)

	*now = now.Add(runtime.loadedConfig().SessionTTL + time.Minute)
	if body := afterCredentialBody(t, projectionRequest(t, "Iterative-Model", "opus(high)", replayInput(reasoning))); len(body) == 0 {
		t.Fatal("the producing lane kept reasoning whose ownership lapsed")
	}
	cleanupExpiredStores(runtime.owners)
	if size := runtime.owners.size(); size != 0 {
		t.Fatalf("owners held after sweep = %d, want the lapsed owner released", size)
	}
}
