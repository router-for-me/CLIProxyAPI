package main

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

// TestNonResponsesClientIsNeverRewritten guards the plugin's blast radius:
// only a Responses client can send the client-executed tool_search protocol.
// A chat-completions client routed to a chat-completions upstream must pass
// through untouched, including its tool declarations.
func TestNonResponsesClientIsNeverRewritten(t *testing.T) {
	chatBody := []byte(`{
		"model":"gpt-6-luna",
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]
	}`)
	raw, errIntercept := interceptRequest("request_after", mustJSON(t, map[string]any{
		"RequestID":    "chat-client",
		"SourceFormat": "openai",
		"ToFormat":     "openai",
		"Body":         chatBody,
	}))
	if errIntercept != nil {
		t.Fatalf("interceptRequest() error = %v", errIntercept)
	}
	if body := decodeRequestBodyResult(t, raw); len(body) != 0 {
		t.Fatalf("chat-completions request was rewritten: %s", body)
	}

	claudeBody := []byte(`{
		"model":"claude-sonnet-4-6",
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{"name":"Read","input_schema":{"type":"object"}}]
	}`)
	raw, errIntercept = interceptRequest("request_after", mustJSON(t, map[string]any{
		"RequestID":    "claude-client",
		"SourceFormat": "claude",
		"ToFormat":     "openai",
		"Body":         claudeBody,
	}))
	if errIntercept != nil {
		t.Fatalf("interceptRequest() error = %v", errIntercept)
	}
	if body := decodeRequestBodyResult(t, raw); len(body) != 0 {
		t.Fatalf("claude request was rewritten: %s", body)
	}
}

// TestNonResponsesResponseIsNeverRewritten ensures a chat-completions client
// that happens to use a tool literally named tool_search is not hijacked.
func TestNonResponsesResponseIsNeverRewritten(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"tool_calls":[
		{"id":"call-1","type":"function","function":{"name":"tool_search","arguments":"{\"query\":\"x\"}"}}
	]}}]}`)
	raw, errIntercept := interceptResponse(mustJSON(t, map[string]any{
		"RequestID":    "chat-client",
		"SourceFormat": "openai",
		"Body":         body,
	}))
	if errIntercept != nil {
		t.Fatalf("interceptResponse() error = %v", errIntercept)
	}
	if out := decodeResponseBodyResult(t, raw); len(out) != 0 {
		t.Fatalf("chat-completions response was rewritten: %s", out)
	}
}

// TestSyntheticToolSearchRequiresSomethingToSearch makes sure the plugin never
// advertises tool_search for a request that has no deferred tools and no search
// history to look through.
func TestSyntheticToolSearchRequiresSomethingToSearch(t *testing.T) {
	body := []byte(`{
		"tools":[{"type":"function","name":"exec_command","parameters":{"type":"object"}}]
	}`)
	raw, errIntercept := interceptRequest("request_after", mustJSON(t, map[string]any{
		"RequestID":    "responses-without-search",
		"SourceFormat": "openai-response",
		"ToFormat":     "openai",
		"Body":         body,
	}))
	if errIntercept != nil {
		t.Fatalf("interceptRequest() error = %v", errIntercept)
	}
	if out := decodeRequestBodyResult(t, raw); len(out) != 0 {
		t.Fatalf("request without tool_search was rewritten: %s", out)
	}
}

// TestNestedNamespacePruningKeepsParentPrefix covers namespaces nested inside
// other namespaces, which used to lose the parent prefix and be dropped.
func TestNestedNamespacePruningKeepsParentPrefix(t *testing.T) {
	body := []byte(`{
		"tools":[
			{"type":"tool_search","execution":"client","parameters":{"type":"object"}},
			{"type":"namespace","name":"outer","tools":[
				{"type":"function","name":"direct","parameters":{"type":"object"}},
				{"type":"namespace","name":"inner","tools":[
					{"type":"function","name":"deep","parameters":{"type":"object"}}
				]}
			]}
		]
	}`)
	out, changed, errRewrite := rewriteRequestBody(body)
	if errRewrite != nil || !changed {
		t.Fatalf("rewriteRequestBody() = (_, %v, %v), want changed", changed, errRewrite)
	}
	var root struct {
		Tools []map[string]any `json:"tools"`
	}
	if errUnmarshal := json.Unmarshal(out, &root); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal() error = %v", errUnmarshal)
	}
	var outer map[string]any
	for _, tool := range root.Tools {
		if stringField(tool, "type") == namespaceToolType && stringField(tool, "name") == "outer" {
			outer = tool
			break
		}
	}
	if outer == nil {
		t.Fatalf("outer namespace missing: %#v", root.Tools)
	}
	entries, ok := outer[deferredToolsKey].([]any)
	if !ok || len(entries) != 2 {
		t.Fatalf("deferred entries = %#v", outer[deferredToolsKey])
	}
	found := map[string]string{}
	for _, rawEntry := range entries {
		entry, okEntry := rawEntry.(map[string]any)
		if !okEntry {
			continue
		}
		found[stringField(entry, "name")] = stringField(entry, "namespace")
	}
	if found["direct"] != "outer" {
		t.Fatalf("direct child namespace = %q, want outer", found["direct"])
	}
	if found["deep"] != "outer__inner" {
		t.Fatalf("nested child namespace = %q, want outer__inner", found["deep"])
	}
	if strings.Contains(string(out), `"type":"namespace","name":"inner"`) {
		t.Fatalf("nested namespace survived pruning: %s", out)
	}
}

// TestToolSearchHistoryWithoutExecutionIsBridged accepts history items that
// omit execution, which several Responses-compatible clients emit.
func TestToolSearchHistoryWithoutExecutionIsBridged(t *testing.T) {
	body := []byte(`{
		"tools":[{"type":"tool_search","parameters":{"type":"object"}}],
		"input":[
			{"type":"tool_search_call","call_id":"search-1","arguments":{"query":"github"}},
			{"type":"tool_search_output","call_id":"search-1","tools":[
				{"type":"function","name":"get_me","parameters":{"type":"object"}}
			]}
		]
	}`)
	out, changed, errRewrite := rewriteRequestBody(body)
	if errRewrite != nil || !changed {
		t.Fatalf("rewriteRequestBody() = (_, %v, %v), want changed", changed, errRewrite)
	}
	var root struct {
		Input []map[string]any `json:"input"`
	}
	if errUnmarshal := json.Unmarshal(out, &root); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal() error = %v", errUnmarshal)
	}
	if root.Input[0]["type"] != "function_call" || root.Input[0]["name"] != toolSearchName {
		t.Fatalf("search call = %#v", root.Input[0])
	}
	if root.Input[1]["type"] != "function_call_output" {
		t.Fatalf("search output = %#v", root.Input[1])
	}
}

// TestBigIntegerSurvivesBridgeRewrite protects request payload fidelity: the
// bridge must not round large integers through float64.
func TestBigIntegerSurvivesBridgeRewrite(t *testing.T) {
	body := []byte(`{
		"metadata":{"number":900719925474099312345},
		"tools":[{"type":"tool_search","execution":"client","parameters":{"type":"object"}}]
	}`)
	out, changed, errRewrite := rewriteRequestBody(body)
	if errRewrite != nil || !changed {
		t.Fatalf("rewriteRequestBody() = (_, %v, %v), want changed", changed, errRewrite)
	}
	if !strings.Contains(string(out), "900719925474099312345") {
		t.Fatalf("large integer was rewritten: %s", out)
	}
}

// TestStreamChunkUnchangedReturnsEmptyBody keeps the per-chunk hot path from
// forcing the host to clone the chunk on every delivery.
func TestStreamChunkUnchangedReturnsEmptyBody(t *testing.T) {
	raw, errIntercept := interceptStreamChunk(mustJSON(t, map[string]any{
		"RequestID":    "stream-unchanged",
		"SourceFormat": "openai-response",
		"ChunkIndex":   0,
		"Body":         []byte(`{"type":"response.output_text.delta","delta":"hello"}`),
	}))
	if errIntercept != nil {
		t.Fatalf("interceptStreamChunk() error = %v", errIntercept)
	}
	if body := decodeStreamChunkResult(t, raw); len(body) != 0 {
		t.Fatalf("unchanged chunk returned a body: %s", body)
	}
}

// TestCatalogFinalizeIsRaceFree exercises the shared catalog the way
// concurrent stream chunks do; run with -race to catch unsynchronized map
// writes.
// TestStaleRequestStateCannotWidenResponseRewrite guards the response path
// against request-ID reuse: the client protocol always wins over cached state.
func TestStaleRequestStateCannotWidenResponseRewrite(t *testing.T) {
	const requestID = "shared-request-id"
	if _, errRequest := interceptRequest("request_after", mustJSON(t, map[string]any{
		"RequestID":    requestID,
		"SourceFormat": "openai-response",
		"ToFormat":     "openai",
		"Body": []byte(`{"tools":[{"type":"namespace","name":"mcp__x","tools":[
			{"type":"function","name":"alpha","parameters":{}}
		]}]}`),
	})); errRequest != nil {
		t.Fatalf("interceptRequest() error = %v", errRequest)
	}

	raw, errResponse := interceptResponse(mustJSON(t, map[string]any{
		"RequestID":    requestID,
		"SourceFormat": "openai",
		"Body":         []byte(`{"type":"function_call","call_id":"c1","name":"mcp__x__alpha","arguments":"{}"}`),
	}))
	if errResponse != nil {
		t.Fatalf("interceptResponse() error = %v", errResponse)
	}
	if body := decodeResponseBodyResult(t, raw); len(body) != 0 {
		t.Fatalf("non-Responses response was rewritten from stale state: %s", body)
	}
}

func TestCatalogFinalizeIsRaceFree(t *testing.T) {
	catalog := extractToolCatalog([]byte(`{"tools":[{"type":"namespace","name":"mcp__x","tools":[
		{"type":"function","name":"alpha","parameters":{}},
		{"type":"function","name":"beta","parameters":{}}
	]}]}`))
	if catalog == nil {
		t.Fatal("extractToolCatalog() = nil")
	}
	var group sync.WaitGroup
	for index := 0; index < 32; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			name := "alpha"
			if index%2 == 1 {
				name = "beta"
			}
			if _, ok := catalog.resolve("mcp__x___" + name); !ok {
				t.Errorf("resolve(%q) failed", name)
			}
		}(index)
	}
	group.Wait()
}

// TestPanicIsContainedInABI verifies the plugin converts panics into errors
// instead of unwinding across the cgo boundary, which aborts the host process.
func TestPanicIsContainedInABI(t *testing.T) {
	var errClean error
	func() {
		defer containPanic(&errClean)
	}()
	if errClean != nil {
		t.Fatalf("containPanic() = %v, want nil for a clean call", errClean)
	}

	var errPanic error
	func() {
		defer containPanic(&errPanic)
		panic("boom")
	}()
	if errPanic == nil {
		t.Fatal("containPanic() swallowed a panic")
	}
	if !strings.Contains(errPanic.Error(), "boom") {
		t.Fatalf("containPanic() = %v, want the panic message", errPanic)
	}
}

// TestOversizedPanicMessageIsTruncated keeps a panic that carries request data
// from flooding the host log.
func TestOversizedPanicMessageIsTruncated(t *testing.T) {
	const limit = 200
	if got := panicMessage(strings.Repeat("x", 4096)); len(got) > limit+3 {
		t.Fatalf("panicMessage() length = %d, want <= %d", len(got), limit+3)
	}
}
