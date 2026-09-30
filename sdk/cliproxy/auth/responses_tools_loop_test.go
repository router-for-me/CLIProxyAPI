package auth

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type loopRequest struct {
	request cliproxyexecutor.Request
	opts    cliproxyexecutor.Options
}

func loopTurn(model, body string) loopRequest {
	return loopRequest{
		request: cliproxyexecutor.Request{Model: model, Payload: []byte(body)},
		opts:    cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatCodex, OriginalRequest: []byte(body)},
	}
}

func assertNoSearchBuiltin(t *testing.T, wire []byte) {
	t.Helper()
	text := string(wire)
	if strings.Contains(text, "\"type\":\"tool_search\"") || strings.Contains(text, "\"type\": \"tool_search\"") {
		t.Fatalf("search built-in leaked upstream: %s", text)
	}
	if strings.Contains(text, "tool_search_call") || strings.Contains(text, "tool_search_output") {
		t.Fatalf("search protocol item leaked upstream: %s", text)
	}
}

// TestResponsesToolsFullSearchLoop runs the complete client search loop
// through the real Manager and executor boundary: the mock upstream never
// sees search built-ins, discovery activates tools under budget, calls
// restore to client identities, and history replays align with aliases.
func TestResponsesToolsFullSearchLoop(t *testing.T) {
	mgr := responsesToolsTestManager()
	responsesToolsTestAuth(t, mgr)
	exec := &fakeResponsesExecutor{provider: "codex", toFormat: sdktranslator.FormatCodex}
	mgr.RegisterExecutor(exec)
	ctx := context.Background()

	turn1 := "{\"model\": \"gpt-5.6-sol\", \"tools\": [{\"type\": \"tool_search\"}, {\"type\": \"namespace\", \"name\": \"fs\", \"tools\": [{\"type\": \"function\", \"name\": \"read\", \"defer_loading\": true, \"parameters\": {\"type\": \"object\"}}]}], \"input\": [{\"type\": \"message\", \"role\": \"user\", \"content\": \"read the file\"}]}"
	req1 := loopTurn("gpt-5.6-sol", turn1)
	exec.response = []byte("{\"output\": [{\"type\": \"function_call\", \"id\": \"fc_call_search_1\", \"name\": \"tool_search\", \"call_id\": \"cs_1\", \"arguments\": \"{\\\"query\\\":\\\"read file\\\"}\"}]}")
	resp1, err := mgr.Execute(ctx, []string{"codex"}, req1.request, req1.opts)
	if err != nil {
		t.Fatalf("turn1: %v", err)
	}
	assertNoSearchBuiltin(t, exec.wire[len(exec.wire)-1])
	if !strings.Contains(string(resp1.Payload), "tool_search_call") {
		t.Fatalf("turn1 not restored: %s", resp1.Payload)
	}
	// The client stores the restored item verbatim, so turn2 replays the id
	// the proxy actually emitted rather than a hand-written one.
	var decoded1 map[string]any
	if err := json.Unmarshal(resp1.Payload, &decoded1); err != nil {
		t.Fatalf("turn1 decode: %v", err)
	}
	restoredID, _ := decoded1["output"].([]any)[0].(map[string]any)["id"].(string)
	if !strings.HasPrefix(restoredID, "tsc_") {
		t.Fatalf("turn1 id = %q, want the tool_search_call namespace", restoredID)
	}

	turn2 := "{\"model\": \"gpt-5.6-sol\", \"tools\": [{\"type\": \"tool_search\"}], \"input\": [{\"type\": \"tool_search_call\", \"id\": \"" + restoredID + "\", \"call_id\": \"cs_1\", \"arguments\": {\"query\": \"read file\"}}, {\"type\": \"tool_search_output\", \"id\": \"tso_out_1\", \"call_id\": \"cs_1\", \"tools\": [{\"type\": \"function\", \"name\": \"read\", \"namespace\": \"fs\", \"parameters\": {\"type\": \"object\"}}]}]}"
	req2 := loopTurn("gpt-5.6-sol", turn2)
	exec.response = []byte("{\"output\": [{\"type\": \"function_call\", \"name\": \"fs__read\", \"call_id\": \"call_2\", \"arguments\": \" {}\"}]}")
	resp2, err := mgr.Execute(ctx, []string{"codex"}, req2.request, req2.opts)
	if err != nil {
		t.Fatalf("turn2: %v", err)
	}
	assertNoSearchBuiltin(t, exec.wire[len(exec.wire)-1])
	var decoded2 map[string]any
	if err := json.Unmarshal(resp2.Payload, &decoded2); err != nil {
		t.Fatalf("turn2 decode: %v", err)
	}
	item2 := decoded2["output"].([]any)[0].(map[string]any)
	if item2["name"] != "read" || item2["namespace"] != "fs" {
		t.Fatalf("call identity not restored: %v", item2)
	}

	turn3 := "{\"model\": \"gpt-5.6-sol\", \"tools\": [{\"type\": \"tool_search\"}], \"input\": [{\"type\": \"function_call\", \"name\": \"read\", \"namespace\": \"fs\", \"call_id\": \"call_2\", \"arguments\": \" {}\"},{\"type\": \"function_call_output\", \"call_id\": \"call_2\", \"output\": \"contents\"}]}"
	req3 := loopTurn("gpt-5.6-sol", turn3)
	exec.response = []byte("{\"output\": [{\"type\": \"message\", \"content\": \"done\"}]}")
	resp3, err := mgr.Execute(ctx, []string{"codex"}, req3.request, req3.opts)
	if err != nil {
		t.Fatalf("turn3: %v", err)
	}
	if !strings.Contains(string(resp3.Payload), "done") {
		t.Fatalf("final answer missing: %s", resp3.Payload)
	}
}
