package executor

import (
	"net/http"
	"strings"
	"testing"
)

const chatBody = `{"model":"gpt-5.3-codex","stream":true,"temperature":0.2,"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}}],"messages":[{"role":"system","content":"You are a reviewer."},{"role":"user","content":"Review this diff: ..."},{"role":"assistant","content":"ok"},{"role":"user","content":"second turn"}]}`

func TestResolvePromptCacheKey_CallerKeyIsPassedThroughUntouched(t *testing.T) {
	body := `{"model":"gpt-5.3-codex","prompt_cache_key":"  fr-abc  ","messages":[{"role":"user","content":"hi"}]}`
	res := ResolvePromptCacheKey("codex", []byte(body), nil, nil)
	if res.Source != PromptCacheKeySourceCaller || res.Key != "fr-abc" {
		t.Fatalf("resolution = %+v, want caller fr-abc", res)
	}
	if res.ID != PromptCacheKeyID("fr-abc") || len(res.ID) != 16 {
		t.Fatalf("id = %q", res.ID)
	}
	meta := ApplyPromptCacheKeyMetadata(nil, res)
	if _, ok := meta["prompt_cache_key"]; ok {
		t.Fatalf("labels must never record the key: %#v", meta)
	}
	if PromptCacheKeySourceFromMetadata(meta) != PromptCacheKeySourceCaller || PromptCacheKeyIDFromMetadata(meta) != res.ID {
		t.Fatalf("metadata = %#v", meta)
	}
}

func TestResolvePromptCacheKey_SessionSourcesAreHashed(t *testing.T) {
	body := `{"model":"gpt-5.3-codex","messages":[{"role":"user","content":"hi"}]}`
	for name, tc := range map[string]struct {
		headers  http.Header
		metadata map[string]any
		payload  string
		wantKey  string
	}{
		"x-session-id": {headers: http.Header{"X-Session-Id": {"s1"}}, payload: body, wantKey: "s1"},
		// codex Session_id and xAI X-Grok-Conv-Id are vendor-native routing headers: source
		// caller, forwarded verbatim (TestResolvePromptCacheKey_NativeRoutingHeadersAreCallerKeys).
		"conversation_id header": {headers: http.Header{"Conversation_id": {"s2"}}, payload: body, wantKey: "s2"},
		"execution session":      {metadata: map[string]any{ExecutionSessionMetadataKey: "exec-1"}, payload: body, wantKey: "exec-1"},
		"claude code session":    {payload: `{"model":"m","metadata":{"user_id":"user_abc_account__session_0f0f0f0f-1"},"messages":[{"role":"user","content":"hi"}]}`, wantKey: "0f0f0f0f-1"},
		"conversation_id":        {payload: `{"model":"m","conversation_id":"conv-9","messages":[{"role":"user","content":"hi"}]}`, wantKey: "conv-9"},
		"responses conversation": {payload: `{"model":"m","conversation":{"id":"conv_10"},"input":"hi"}`, wantKey: "conv_10"},
	} {
		res := ResolvePromptCacheKey("codex", []byte(tc.payload), tc.headers, tc.metadata)
		if res.Source != PromptCacheKeySourceSession {
			t.Fatalf("%s: resolution = %+v, want session", name, res)
		}
		// The wire key is a hash of the id: stable, and never the raw identifier.
		if res.Key != hashedPromptCacheKey("session", tc.wantKey) || strings.Contains(res.Key, tc.wantKey) {
			t.Fatalf("%s: key = %q for id %q", name, res.Key, tc.wantKey)
		}
		if !strings.HasPrefix(res.Key, "pck-") || len(res.Key) != 36 {
			t.Fatalf("%s: key shape %q", name, res.Key)
		}
		if res.ID != PromptCacheKeyID(res.Key) {
			t.Fatalf("%s: id = %q", name, res.ID)
		}
	}
	// A per-request id is not a session: the prefix hash wins over X-Client-Request-Id.
	res := ResolvePromptCacheKey("codex", []byte(chatBody), http.Header{"X-Client-Request-Id": {"r1"}}, nil)
	if res.Source != PromptCacheKeySourceDerived {
		t.Fatalf("X-Client-Request-Id treated as a session: %+v", res)
	}
}

// Vendor-native routing headers are the caller's own routing key: source caller.
func TestResolvePromptCacheKey_NativeRoutingHeadersAreCallerKeys(t *testing.T) {
	for name, headers := range map[string]http.Header{
		"codex Session_id": {"Session_id": {"sess-codex-1"}},
		"codex Session-Id": {"Session-Id": {"sess-codex-2"}},
		"xai conv id":      {"X-Grok-Conv-Id": {"conv-xai-1"}},
	} {
		res := ResolvePromptCacheKey("codex", []byte(chatBody), headers, nil)
		if res.Source != PromptCacheKeySourceCaller {
			t.Fatalf("%s: source = %q, want caller", name, res.Source)
		}
		var want string
		for _, v := range headers {
			want = v[0]
		}
		if res.Key != want {
			t.Fatalf("%s: key = %q, want the header value %q verbatim", name, res.Key, want)
		}
		if res.ID != PromptCacheKeyID(want) {
			t.Fatalf("%s: id = %q", name, res.ID)
		}
	}
	// Body prompt_cache_key still wins over a native header.
	res := ResolvePromptCacheKey("codex", []byte(`{"model":"m","prompt_cache_key":"body-1","messages":[{"role":"user","content":"hi"}]}`), http.Header{"Session_id": {"sess"}}, nil)
	if res.Key != "body-1" {
		t.Fatalf("body key must win over a native header: %+v", res)
	}
}

func TestResolvePromptCacheKey_PassthroughDerivesNothing(t *testing.T) {
	// An explicit empty key beats every other source, including a session id.
	res := ResolvePromptCacheKey("codex", []byte(`{"model":"m","prompt_cache_key":"","messages":[{"role":"user","content":"hi"}]}`), http.Header{"X-Session-Id": {"s1"}}, map[string]any{ExecutionSessionMetadataKey: "exec-1"})
	if res.Source != PromptCacheKeySourcePassthrough || res.Key != "" || res.ID != "" {
		t.Fatalf("explicit empty key opt-out: %+v", res)
	}
	meta := ApplyPromptCacheKeyMetadata(map[string]any{"keep": 1}, res)
	if meta["keep"] != 1 || PromptCacheKeyIDFromMetadata(meta) != "" || PromptCacheKeySourceFromMetadata(meta) != PromptCacheKeySourcePassthrough {
		t.Fatalf("metadata = %#v", meta)
	}
	// Nothing to hash -> passthrough, never a key shared by every empty request.
	res = ResolvePromptCacheKey("codex", []byte(`{"model":"m","stream":true}`), nil, nil)
	if res.Source != PromptCacheKeySourcePassthrough {
		t.Fatalf("empty prompt: %+v", res)
	}
}

func TestDerivePromptCacheKey_DeterministicAcrossEncodings(t *testing.T) {
	a := DerivePromptCacheKey("codex", []byte(chatBody))
	if !strings.HasPrefix(a, "pck-") || len(a) != 4+32 {
		t.Fatalf("key shape %q", a)
	}
	// Same bytes -> same key.
	if b := DerivePromptCacheKey("codex", []byte(chatBody)); b != a {
		t.Fatalf("not deterministic: %q vs %q", a, b)
	}
	// Whitespace and object-key order inside tools, and volatile fields, do not change it.
	permuted := `{
	  "stream": false, "temperature": 0.9, "user": "req-7f3", "model": "gpt-5.3-codex",
	  "tools": [ {"function": {"parameters": {"properties": {"path": {"type": "string"}}, "type": "object"}, "name": "read_file"}, "type": "function"} ],
	  "messages": [
	    {"content": "You are a reviewer.", "role": "system"},
	    {"content": "Review this diff: ...", "role": "user"},
	    {"content": "ok", "role": "assistant"},
	    {"content": "a DIFFERENT second turn", "role": "user"}
	  ]
	}`
	if b := DerivePromptCacheKey("codex", []byte(permuted)); b != a {
		t.Fatalf("encoding/volatile fields changed the key: %q vs %q", a, b)
	}
	// Chat content as text parts hashes like the string form.
	parts := strings.Replace(chatBody, `"content":"Review this diff: ..."`, `"content":[{"type":"text","text":"Review this diff: ..."}]`, 1)
	if b := DerivePromptCacheKey("codex", []byte(parts)); b != a {
		t.Fatalf("text-part content changed the key: %q vs %q", a, b)
	}
}

// Pinned canonicalisation vectors: large integers keep their digits (no float64 round trip),
// HTML characters are not escaped, key order and whitespace are normalised. Changing these
// bytes changes every derived id, which breaks joins across a deploy.
func TestCanonicalJSON_PinnedVectors(t *testing.T) {
	for raw, want := range map[string]string{
		`[{"b": 1, "a": {"y": 2, "x": [3, 4]}}]`:                 `[{"a":{"x":[3,4],"y":2},"b":1}]`,
		`[{"max":9007199254740993,"min":-18446744073709551617}]`: `[{"max":9007199254740993,"min":-18446744073709551617}]`,
		`[{"desc":"a < b && c > d","f":1.50}]`:                   `[{"desc":"a < b && c > d","f":1.50}]`,
		"[ {\"n\" : \"read_file\" ,\n \"p\" : {} } ]":            `[{"n":"read_file","p":{}}]`,
	} {
		if got := canonicalJSON(raw); got != want {
			t.Fatalf("canonicalJSON(%s) = %s, want %s", raw, got, want)
		}
	}
	if canonicalJSON("not json") != "not json" {
		t.Fatalf("invalid JSON must pass through trimmed")
	}
}

func TestDerivePromptCacheKey_SensitiveToThePrefixOnly(t *testing.T) {
	base := DerivePromptCacheKey("codex", []byte(chatBody))
	for name, body := range map[string]string{
		"model":      strings.Replace(chatBody, `"gpt-5.3-codex"`, `"gpt-5.4"`, 1),
		"system":     strings.Replace(chatBody, "You are a reviewer.", "You are a poet.", 1),
		"tools":      strings.Replace(chatBody, `"name":"read_file"`, `"name":"write_file"`, 1),
		"first user": strings.Replace(chatBody, "Review this diff: ...", "Review this OTHER diff", 1),
		"provider":   chatBody,
	} {
		provider := "codex"
		if name == "provider" {
			provider = "xai"
		}
		if got := DerivePromptCacheKey(provider, []byte(body)); got == base {
			t.Fatalf("%s change did not flip the key", name)
		}
	}
	// Bytes past the first-user head are not part of the prefix: two seats sharing one
	// artifact head collide onto one key even if their tails differ.
	head := strings.Repeat("A", promptCacheFirstUserHeadSize)
	seat := func(tail string) string {
		return `{"model":"m","messages":[{"role":"system","content":"s"},{"role":"user","content":"` + head + tail + `"}]}`
	}
	if DerivePromptCacheKey("codex", []byte(seat("lens one"))) != DerivePromptCacheKey("codex", []byte(seat("lens two"))) {
		t.Fatalf("tails past the head changed the key")
	}
}

func TestDerivePromptCacheKey_ResponsesClaudeGeminiShapes(t *testing.T) {
	responses := `{"model":"gpt-5.4","instructions":"You are a reviewer.","input":[{"role":"user","content":[{"type":"input_text","text":"Review this diff: ..."}]}]}`
	responsesShort := `{"model":"gpt-5.4","instructions":"You are a reviewer.","input":"Review this diff: ..."}`
	if DerivePromptCacheKey("codex", []byte(responses)) != DerivePromptCacheKey("codex", []byte(responsesShort)) {
		t.Fatalf("Responses input string vs items must hash alike")
	}
	claude := `{"model":"c","system":[{"type":"text","text":"sys"}],"messages":[{"role":"user","content":[{"type":"text","text":"u"}]}]}`
	claudeStr := `{"model":"c","system":"sys","messages":[{"role":"user","content":"u"}]}`
	if DerivePromptCacheKey("codex", []byte(claude)) != DerivePromptCacheKey("codex", []byte(claudeStr)) {
		t.Fatalf("Claude system blocks vs string must hash alike")
	}
	gemini := `{"model":"g","systemInstruction":{"parts":[{"text":"sys"}]},"contents":[{"role":"user","parts":[{"text":"u"}]}]}`
	if DerivePromptCacheKey("codex", []byte(gemini)) == "" {
		t.Fatalf("Gemini shape derived nothing")
	}
	// Chained Responses turns (previous_response_id, tool-output-only input) carry no first
	// user message: turns 2..N share one key from instructions + tools. Turn 1 differs
	// (documented in the spec); nothing collapses onto a global "-" key.
	turn2 := `{"model":"gpt-5.4","instructions":"You are a reviewer.","previous_response_id":"resp_1","input":[{"type":"function_call_output","call_id":"c1","output":"ok"}]}`
	turn3 := `{"model":"gpt-5.4","instructions":"You are a reviewer.","previous_response_id":"resp_2","input":[{"type":"function_call_output","call_id":"c2","output":"more"}]}`
	if a, b := DerivePromptCacheKey("codex", []byte(turn2)), DerivePromptCacheKey("codex", []byte(turn3)); a == "" || a != b {
		t.Fatalf("chained turns must share a key: %q vs %q", a, b)
	}
	if DerivePromptCacheKey("codex", []byte(turn2)) == DerivePromptCacheKey("codex", []byte(`{"model":"gpt-5.4","instructions":"Other.","previous_response_id":"resp_9","input":[{"type":"function_call_output","call_id":"c","output":"x"}]}`)) {
		t.Fatalf("different instructions must not share a key")
	}
	if DerivePromptCacheKey("codex", []byte("not json")) != "" {
		t.Fatalf("invalid JSON must derive nothing")
	}
}

func TestApplyPromptCacheKeyMetadata_UpdatesInPlace(t *testing.T) {
	meta := map[string]any{"selected_auth_callback": "cb"}
	res := ResolvePromptCacheKey("codex", []byte(chatBody), nil, meta)
	out := ApplyPromptCacheKeyMetadata(meta, res)
	if res.Source != PromptCacheKeySourceDerived {
		t.Fatalf("resolution = %+v", res)
	}
	if PromptCacheKeySourceFromMetadata(meta) != PromptCacheKeySourceDerived || meta["selected_auth_callback"] != "cb" {
		t.Fatalf("caller's map not updated in place: %#v", meta)
	}
	if PromptCacheKeyIDFromMetadata(out) != PromptCacheKeyID(res.Key) {
		t.Fatalf("id mismatch: %#v", out)
	}
}
