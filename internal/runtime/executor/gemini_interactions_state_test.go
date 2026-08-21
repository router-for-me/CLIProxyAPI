package executor

import (
	"context"
	"testing"

	internalcache "github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

func TestGoframeCacheAntigravityInteractionsState(t *testing.T) {
	internalcache.ClearAntigravityInteractionsCache()
	const model = "antigravity-preview-05-2026"
	opts := cliproxyexecutor.Options{}
	opts.Headers = map[string][]string{"Session-Id": {"sess-123"}}

	payload := []byte(`{"interaction":{"id":"inter_abc","environment_id":"env_def","status":"in_progress"}}`)
	goframeCacheAntigravityInteractionsState(context.Background(), model, nil, opts, payload)

	// The session key now comes from the shared ExtractSessionID extraction,
	// which prefixes explicit header ids with "codex:".
	state, ok := internalcache.GetAntigravityInteractionsState(context.Background(), model, "session:codex:sess-123")
	if !ok {
		t.Fatalf("expected cached state")
	}
	if state.InteractionID != "inter_abc" {
		t.Errorf("InteractionID = %q, want inter_abc", state.InteractionID)
	}
	if state.EnvironmentID != "env_def" {
		t.Errorf("EnvironmentID = %q, want env_def", state.EnvironmentID)
	}
}

func TestGoframeCacheIgnoresNonAntigravity(t *testing.T) {
	internalcache.ClearAntigravityInteractionsCache()
	opts := cliproxyexecutor.Options{}
	opts.Headers = map[string][]string{"Session-Id": {"sess"}, "namespace_key": {"x"}}
	payload := []byte(`{"interaction":{"id":"inter_x","environment_id":"env_x"}}`)
	goframeCacheAntigravityInteractionsState(context.Background(), "gemini-3.7-flash", nil, opts, payload)
	if _, ok := internalcache.GetAntigravityInteractionsState(context.Background(), "gemini-3.7-flash", "responses:sess"); ok {
		t.Fatalf("should not cache state for non-antigravity model")
	}
}

func TestGoframeApplyAntigravityInteractionsContinuation(t *testing.T) {
	internalcache.ClearAntigravityInteractionsCache()
	const model = "antigravity-preview-05-2026"
	internalcache.CacheAntigravityInteractionsState(model, "session:codex:sess-9", internalcache.AntigravityInteractionsState{
		InteractionID: "inter_cont",
		EnvironmentID: "env_cont",
	})

	opts := cliproxyexecutor.Options{}
	opts.Headers = map[string][]string{"Session-Id": {"sess-9"}, "namespace_key": {"x"}}

	// A translated continuation body that replays the full history:
	// user_input -> tool -> function_result. The proxy must keep ONLY the
	// function_result and attach previous_interaction_id + environment_id.
	body := []byte(`{
		"agent":"` + model + `",
		"input":[
			{"type":"user_input","content":[{"type":"text","text":"what is 42*17"}]},
			{"type":"model_output","content":[]},
			{"type":"function_result","name":"calculator","call_id":"c1","result":"714"}
		],
		"stream":true
	}`)

	out := goframeApplyAntigravityInteractionsContinuation(context.Background(), model, []byte(`{"model":"`+model+`"}`), opts, body)
	root := gjson.ParseBytes(out)

	if got := root.Get("previous_interaction_id").String(); got != "inter_cont" {
		t.Errorf("previous_interaction_id = %q, want inter_cont", got)
	}
	if got := root.Get("environment").String(); got != "env_cont" {
		t.Errorf("environment = %q, want env_cont (field must be literally 'environment')", got)
	}
	// Only function_result should survive in input.
	types := make([]string, 0, 1)
	root.Get("input").ForEach(func(_, item gjson.Result) bool {
		types = append(types, item.Get("type").String())
		return true
	})
	if len(types) != 1 || types[0] != "function_result" {
		t.Fatalf("input types = %v, want only [function_result]", types)
	}
	if got := root.Get("input.0.result").String(); got != "714" {
		t.Errorf("function_result.result = %q, want 714", got)
	}
}

func TestGoframeApplyNoContinuation(t *testing.T) {
	internalcache.ClearAntigravityInteractionsCache()
	const model = "antigravity-preview-05-2026"
	// No cached state -> body must be unchanged.
	opts := cliproxyexecutor.Options{}
	opts.Headers = map[string][]string{"Session-Id": {"sess-none"}, "namespace_key": {"x"}}

	body := []byte(`{"agent":"` + model + `","input":[{"type":"function_result","name":"f","call_id":"c","result":"1"}],"stream":true}`)
	out := goframeApplyAntigravityInteractionsContinuation(context.Background(), model, nil, opts, body)
	if string(out) != string(body) {
		t.Fatalf("expected unchanged body, got %s", string(out))
	}
}

// TestAntigravityConversationPrefixKeyFallback verifies the chat/completions
// fallback: a client that sends no Session-Id/session_id still gets a stable
// continuity key, because the tool-result turn shares the conversation prefix
// (everything up to the last user message) with the initial turn.
func TestAntigravityConversationPrefixKeyFallback(t *testing.T) {
	internalcache.ClearAntigravityInteractionsCache()
	const model = "antigravity-preview-05-2026"

	turn1 := []byte(`{"model":"antigravity","messages":[
		{"role":"system","content":"You are helpful."},
		{"role":"user","content":"Summarize /tmp/x"}
	]}`)
	turn2 := []byte(`{"model":"antigravity","messages":[
		{"role":"system","content":"You are helpful."},
		{"role":"user","content":"Summarize /tmp/x"},
		{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"/tmp/x\"}"}}]},
		{"role":"tool","tool_call_id":"c1","content":"file body"}
	]}`)

	opts := cliproxyexecutor.Options{} // no headers, no metadata
	key1 := antigravityExecSessionKey(opts, turn1)
	key2 := antigravityExecSessionKey(opts, turn2)
	if key1 == "" || key2 == "" {
		t.Fatalf("expected non-empty fallback keys, got %q and %q", key1, key2)
	}
	if key1 != key2 {
		t.Fatalf("prefix keys diverged: turn1=%q turn2=%q", key1, key2)
	}

	// Cache under turn1's key, then confirm continuation resolves via turn2.
	payload := []byte(`{"id":"inter_fb","environment_id":"env_fb"}`)
	goframeCacheAntigravityInteractionsState(context.Background(), model, turn1, opts, payload)

	body := []byte(`{
		"agent":"` + model + `",
		"input":[{"type":"function_result","name":"read_file","call_id":"c1","result":"file body"}]
	}`)
	out := goframeApplyAntigravityInteractionsContinuation(context.Background(), model, turn2, opts, body)
	root := gjson.ParseBytes(out)
	if got := root.Get("previous_interaction_id").String(); got != "inter_fb" {
		t.Errorf("previous_interaction_id = %q, want inter_fb (continuation must resolve via prefix key)", got)
	}
	if got := root.Get("environment").String(); got != "env_fb" {
		t.Errorf("environment = %q, want env_fb", got)
	}

	// A NEW user message must start a fresh key.
	turn3 := []byte(`{"model":"antigravity","messages":[
		{"role":"system","content":"You are helpful."},
		{"role":"user","content":"Different question"}
	]}`)
	if key3 := antigravityExecSessionKey(opts, turn3); key3 == key1 {
		t.Fatalf("new user turn must produce a different session key")
	}
}