package responses

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// --- L1 direction A: Claude server tool blocks -> Responses web_search_call ---

func claudeWebSearchStreamChunks() [][]byte {
	return [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_ws","usage":{"input_tokens":1,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{}}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"lindorm vector\"}"}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[{"type":"web_search_result","title":"Lindorm Vector","url":"https://example.com/a","encrypted_content":"ENC_A","page_age":"1 day"},{"type":"web_search_result","title":"Docs","url":"https://example.com/b","encrypted_content":"ENC_B"}]}}`),
		[]byte(`data: {"type":"content_block_stop","index":1}`),
		[]byte(`data: {"type":"message_stop"}`),
	}
}

func TestClaudeWebSearchBlocksBecomeWebSearchCallItem(t *testing.T) {
	outputs := translateClaudeResponsesStreamThroughRegistry(claudeWebSearchStreamChunks())

	var completed gjson.Result
	for _, o := range outputs {
		if ev, data := parseClaudeResponsesSSEEvent(t, o); ev == "response.completed" {
			completed = data
		}
	}
	items := completed.Get("response.output").Array()
	if len(items) != 1 {
		t.Fatalf("output items = %d, want 1: %s", len(items), completed.Get("response.output").Raw)
	}
	item := items[0]
	if got := item.Get("type").String(); got != "web_search_call" {
		t.Fatalf("item type = %q, want web_search_call", got)
	}
	if got := item.Get("action.query").String(); got != "lindorm vector" {
		t.Fatalf("action.query = %q, want %q", got, "lindorm vector")
	}
	if got := item.Get("status").String(); got != "completed" {
		t.Fatalf("status = %q, want completed", got)
	}
	if got := item.Get("results.#").Int(); got != 2 {
		t.Fatalf("results = %d, want 2: %s", got, item.Raw)
	}
	if got := item.Get("results.0.url").String(); got != "https://example.com/a" {
		t.Fatalf("results[0].url = %q", got)
	}
	// Anthropic validates encrypted_content on replay, so it must survive the hop.
	if got := item.Get("results.0.encrypted_content").String(); got != "ENC_A" {
		t.Fatalf("results[0].encrypted_content = %q, want ENC_A", got)
	}
}

func TestClaudeWebSearchBlocksBecomeWebSearchCallItemNonStream(t *testing.T) {
	var lines []string
	for _, chunk := range claudeWebSearchStreamChunks() {
		lines = append(lines, string(chunk))
	}
	out := ConvertClaudeResponseToOpenAIResponsesNonStream(
		context.Background(), "claude-test", nil, nil, []byte(strings.Join(lines, "\n")), nil)

	items := gjson.GetBytes(out, "output").Array()
	if len(items) != 1 || items[0].Get("type").String() != "web_search_call" {
		t.Fatalf("output = %s", gjson.GetBytes(out, "output").Raw)
	}
	if got := items[0].Get("action.query").String(); got != "lindorm vector" {
		t.Fatalf("action.query = %q", got)
	}
	if got := items[0].Get("results.#").Int(); got != 2 {
		t.Fatalf("results = %d, want 2", got)
	}
}

// --- L1 direction B: Responses web_search_call -> Claude server tool blocks ---

func TestWebSearchCallItemReplaysAsClaudeServerToolBlocks(t *testing.T) {
	raw := responsesRequestFromItems(`{
		"type":"web_search_call","id":"ws_srvtoolu_1","status":"completed",
		"action":{"type":"search","query":"lindorm vector"},
		"results":[{"title":"Lindorm Vector","url":"https://example.com/a","encrypted_content":"ENC_A"}]
	}`)
	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)

	if got := claudeAssistantBlockTypes(t, out); len(got) != 2 ||
		got[0] != "server_tool_use" || got[1] != "web_search_tool_result" {
		t.Fatalf("assistant blocks = %v, want [server_tool_use web_search_tool_result]", got)
	}
	blocks := gjson.GetBytes(out, "messages.0.content")
	use, result := blocks.Get("0"), blocks.Get("1")
	if got := use.Get("id").String(); got != "srvtoolu_1" {
		t.Fatalf("server_tool_use id = %q, want srvtoolu_1 (ws_ prefix stripped)", got)
	}
	if got := use.Get("name").String(); got != "web_search" {
		t.Fatalf("server_tool_use name = %q", got)
	}
	if got := use.Get("input.query").String(); got != "lindorm vector" {
		t.Fatalf("server_tool_use input.query = %q", got)
	}
	if got := result.Get("tool_use_id").String(); got != "srvtoolu_1" {
		t.Fatalf("web_search_tool_result tool_use_id = %q", got)
	}
	if got := result.Get("content.0.url").String(); got != "https://example.com/a" {
		t.Fatalf("result url = %q", got)
	}
	if got := result.Get("content.0.encrypted_content").String(); got != "ENC_A" {
		t.Fatalf("result encrypted_content = %q, want ENC_A", got)
	}
}

// Anthropic rejects a web_search_result without genuine encrypted_content and
// rejects a server_tool_use with no result block at all, but accepts an empty
// result list. A client that drops the field must therefore degrade, not break.
func TestWebSearchCallWithoutEncryptedContentReplaysEmptyResults(t *testing.T) {
	raw := responsesRequestFromItems(`{
		"type":"web_search_call","id":"ws_srvtoolu_1","status":"completed",
		"action":{"type":"search","query":"q"},
		"results":[{"title":"T","url":"https://example.com/a"}]
	}`)
	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	if got := claudeAssistantBlockTypes(t, out); len(got) != 2 {
		t.Fatalf("assistant blocks = %v, want the server_tool_use/result pair", got)
	}
	if got := gjson.GetBytes(out, "messages.0.content.1.content.#").Int(); got != 0 {
		t.Fatalf("result content entries = %d, want 0", got)
	}
}

func TestOutputTextAnnotationsReplayAsClaudeCitations(t *testing.T) {
	raw := responsesRequestFromItems(`{
		"type":"message","role":"assistant",
		"content":[{"type":"output_text","text":"Answer.","annotations":[
			{"type":"web_search_result_location","url":"https://example.com/a","title":"A","cited_text":"Answer","encrypted_index":"IDX_A"}
		]}]
	}`)
	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	block := gjson.GetBytes(out, "messages.0.content.0")
	if got := block.Get("type").String(); got != "text" {
		t.Fatalf("block type = %q", got)
	}
	if got := block.Get("citations.#").Int(); got != 1 {
		t.Fatalf("citations = %d, want 1: %s", got, block.Raw)
	}
	if got := block.Get("citations.0.url").String(); got != "https://example.com/a" {
		t.Fatalf("citation url = %q", got)
	}
	// encrypted_index is mandatory on replay; the annotation must ride through verbatim.
	if got := block.Get("citations.0.encrypted_index").String(); got != "IDX_A" {
		t.Fatalf("citation encrypted_index = %q, want IDX_A", got)
	}
}

func TestAnnotationsWithoutEncryptedIndexAreNotReplayedAsCitations(t *testing.T) {
	raw := responsesRequestFromItems(`{
		"type":"message","role":"assistant",
		"content":[{"type":"output_text","text":"Answer.","annotations":[
			{"type":"url_citation","url":"https://example.com/a","title":"A"}
		]}]
	}`)
	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	if gjson.GetBytes(out, "messages.0.content.0.citations").Exists() {
		t.Fatal("citations without encrypted_index must be dropped, not sent")
	}
}

func TestRefusalPartReplaysAsClaudeText(t *testing.T) {
	raw := responsesRequestFromItems(`{
		"type":"message","role":"assistant",
		"content":[{"type":"refusal","refusal":"I cannot help with that."}]
	}`)
	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	block := gjson.GetBytes(out, "messages.0.content")
	text := block.Get("0.text").String()
	if block.Type == gjson.String {
		text = block.String()
	}
	if text != "I cannot help with that." {
		t.Fatalf("refusal text = %q, raw=%s", text, block.Raw)
	}
}

// --- L3 contract: every reachable Claude block survives a round trip ---

func TestRoundTripPreservesReachableClaudeBlocks(t *testing.T) {
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_rt","usage":{"input_tokens":1,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"ponder"}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"` + mustTestSignature(t) + `"}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Researching."}}`),
		[]byte(`data: {"type":"content_block_stop","index":1}`),
		[]byte(`data: {"type":"content_block_start","index":2,"content_block":{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{}}}`),
		[]byte(`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"q\"}"}}`),
		[]byte(`data: {"type":"content_block_stop","index":2}`),
		[]byte(`data: {"type":"content_block_start","index":3,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[{"type":"web_search_result","title":"T","url":"https://example.com/a"}]}}`),
		[]byte(`data: {"type":"content_block_stop","index":3}`),
		[]byte(`data: {"type":"content_block_start","index":4,"content_block":{"type":"thinking","thinking":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":4,"delta":{"type":"thinking_delta","thinking":"more"}}`),
		[]byte(`data: {"type":"content_block_delta","index":4,"delta":{"type":"signature_delta","signature":"` + mustTestSignature(t) + `"}}`),
		[]byte(`data: {"type":"content_block_stop","index":4}`),
		[]byte(`data: {"type":"content_block_start","index":5,"content_block":{"type":"tool_use","id":"toolu_1","name":"exec","input":{}}}`),
		[]byte(`data: {"type":"content_block_delta","index":5,"delta":{"type":"input_json_delta","partial_json":"{\"cmd\":\"pwd\"}"}}`),
		[]byte(`data: {"type":"content_block_stop","index":5}`),
		[]byte(`data: {"type":"message_stop"}`),
	}

	outputs := translateClaudeResponsesStreamThroughRegistry(chunks)
	var completed gjson.Result
	for _, o := range outputs {
		if ev, data := parseClaudeResponsesSSEEvent(t, o); ev == "response.completed" {
			completed = data
		}
	}
	var items []json.RawMessage
	completed.Get("response.output").ForEach(func(_, v gjson.Result) bool {
		items = append(items, json.RawMessage(v.Raw))
		return true
	})
	req, _ := json.Marshal(map[string]any{"model": "claude-test", "input": items})
	out := ConvertOpenAIResponsesRequestToClaude("claude-test", req, false)

	got := claudeAssistantBlockTypes(t, out)
	want := []string{"thinking", "text", "server_tool_use", "web_search_tool_result", "thinking", "tool_use"}
	if len(got) != len(want) {
		t.Fatalf("round trip blocks = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("round trip blocks = %v, want %v", got, want)
		}
	}
}

// --- boundary and error cases ------------------------------------------------

// Anthropic constrains server tool ids to ^srvtoolu_[a-zA-Z0-9_]+$. Responses
// items that never came from Claude (a native OpenAI web_search_call) carry ids
// that violate it, so the id must be normalised instead of trusted.
func TestWebSearchCallIDNormalisedToClaudeServerToolPattern(t *testing.T) {
	for _, tc := range []struct {
		name        string
		responsesID string
		want        string
	}{
		{"claude round trip keeps the original id", "ws_srvtoolu_abc123", "srvtoolu_abc123"},
		{"native OpenAI id gains the required prefix", "ws_00112233aabb", "srvtoolu_00112233aabb"},
		{"characters outside the pattern are replaced", "ws_00112233-aabb.cc", "srvtoolu_00112233_aabb_cc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := responsesRequestFromItems(`{"type":"web_search_call","id":"` + tc.responsesID + `","status":"completed","action":{"type":"search","query":"q"}}`)
			out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
			blocks := gjson.GetBytes(out, "messages.0.content")
			if got := blocks.Get("0.id").String(); got != tc.want {
				t.Fatalf("server_tool_use id = %q, want %q", got, tc.want)
			}
			if got := blocks.Get("1.tool_use_id").String(); got != tc.want {
				t.Fatalf("tool_use_id = %q, want %q (must pair with the use block)", got, tc.want)
			}
		})
	}
}

func TestWebSearchCallWithoutIDProducesNoBlocks(t *testing.T) {
	for _, id := range []string{``, `ws_`} {
		raw := responsesRequestFromItems(`{"type":"web_search_call","id":"` + id + `","status":"completed","action":{"type":"search","query":"q"}}`)
		out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
		if got := claudeAssistantBlockTypes(t, out); len(got) != 0 {
			t.Fatalf("id=%q produced %v; an unpairable server_tool_use is rejected by Anthropic", id, got)
		}
	}
}

// A turn can end after the search block when the upstream stream is cut short;
// the item must still be closed so the client does not see a dangling item.
func TestClaudeWebSearchWithoutResultBlockStillEmitsItem(t *testing.T) {
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_ws","usage":{"input_tokens":1,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{}}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"q\"}"}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"message_stop"}`),
	}
	outputs := translateClaudeResponsesStreamThroughRegistry(chunks)
	done, completed := 0, gjson.Result{}
	for _, o := range outputs {
		event, data := parseClaudeResponsesSSEEvent(t, o)
		if event == "response.output_item.done" && data.Get("item.type").String() == "web_search_call" {
			done++
		}
		if event == "response.completed" {
			completed = data
		}
	}
	if done != 1 {
		t.Fatalf("web_search_call output_item.done count = %d, want 1", done)
	}
	if got := completed.Get("response.output.0.results").Exists(); got {
		t.Fatal("no result block arrived, so results must be absent")
	}
}

func TestUnmappedServerToolProducesNoItem(t *testing.T) {
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_ws","usage":{"input_tokens":1,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_1","name":"code_execution","input":{}}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"done"}}`),
		[]byte(`data: {"type":"content_block_stop","index":1}`),
		[]byte(`data: {"type":"message_stop"}`),
	}
	outputs := translateClaudeResponsesStreamThroughRegistry(chunks)
	var completed gjson.Result
	for _, o := range outputs {
		if event, data := parseClaudeResponsesSSEEvent(t, o); event == "response.completed" {
			completed = data
		}
	}
	if got := completed.Get("response.output.#").Int(); got != 1 {
		t.Fatalf("output items = %d, want 1 (message only): %s", got, completed.Get("response.output").Raw)
	}
	if got := completed.Get("response.output.0.type").String(); got != "message" {
		t.Fatalf("output[0].type = %q, want message", got)
	}
}

func TestWebSearchResultWithoutMatchingUseIsIgnored(t *testing.T) {
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_ws","usage":{"input_tokens":1,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtoolu_missing","content":[]}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"message_stop"}`),
	}
	outputs := translateClaudeResponsesStreamThroughRegistry(chunks)
	for _, o := range outputs {
		if event, data := parseClaudeResponsesSSEEvent(t, o); event == "response.completed" {
			if got := data.Get("response.output.#").Int(); got != 0 {
				t.Fatalf("orphan result produced %d output items, want 0", got)
			}
		}
	}
}

// Native OpenAI clients emit `queries` alongside `query`, and use an `open_page`
// action with a url instead of a query; both shapes reach this translator when a
// session started on an OpenAI provider is resumed against Claude.
func TestWebSearchCallQueryAcceptsNativeOpenAIActionShapes(t *testing.T) {
	for _, tc := range []struct{ name, action, want string }{
		{"search with query", `{"type":"search","query":"go release","queries":["go release"]}`, "go release"},
		{"search with only queries", `{"type":"search","queries":["go release"]}`, "go release"},
		{"open_page falls back to the url", `{"type":"open_page","url":"https://go.dev/dl"}`, "https://go.dev/dl"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := responsesRequestFromItems(`{"type":"web_search_call","id":"ws_srvtoolu_1","status":"completed","action":` + tc.action + `}`)
			out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
			if got := gjson.GetBytes(out, "messages.0.content.0.input.query").String(); got != tc.want {
				t.Fatalf("input.query = %q, want %q", got, tc.want)
			}
		})
	}
}

// An empty result list is distinct from a missing one: Claude reported a search
// that returned nothing, which Anthropic accepts on replay.
func TestClaudeWebSearchWithEmptyResultsKeepsEmptyList(t *testing.T) {
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_ws","usage":{"input_tokens":1,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{}}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"q\"}"}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[]}}`),
		[]byte(`data: {"type":"content_block_stop","index":1}`),
		[]byte(`data: {"type":"message_stop"}`),
	}
	var completed gjson.Result
	for _, o := range translateClaudeResponsesStreamThroughRegistry(chunks) {
		if event, data := parseClaudeResponsesSSEEvent(t, o); event == "response.completed" {
			completed = data
		}
	}
	results := completed.Get("response.output.0.results")
	if !results.Exists() || results.Int() != 0 && len(results.Array()) != 0 {
		t.Fatalf("results = %s, want an empty array", results.Raw)
	}
}

func TestClaudeWebSearchErrorResultSurvivesRoundTrip(t *testing.T) {
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_ws_err","usage":{"input_tokens":1,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_err","name":"web_search","input":{}}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"err_query\"}"}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtoolu_err","content":[{"type":"web_search_tool_result_error","error_code":"rate_limited"}]}}`),
		[]byte(`data: {"type":"content_block_stop","index":1}`),
		[]byte(`data: {"type":"message_stop"}`),
	}
	var completed gjson.Result
	for _, o := range translateClaudeResponsesStreamThroughRegistry(chunks) {
		if event, data := parseClaudeResponsesSSEEvent(t, o); event == "response.completed" {
			completed = data
		}
	}
	outputItems := completed.Get("response.output").Array()
	if len(outputItems) != 1 || outputItems[0].Get("type").String() != "web_search_call" {
		t.Fatalf("output items = %s, want 1 web_search_call", completed.Get("response.output").Raw)
	}
	errBlock := outputItems[0].Get("results.0")
	if got := errBlock.Get("type").String(); got != "web_search_tool_result_error" {
		t.Fatalf("results[0].type = %q, want web_search_tool_result_error", got)
	}
	if got := errBlock.Get("error_code").String(); got != "rate_limited" {
		t.Fatalf("results[0].error_code = %q, want rate_limited", got)
	}

	// Replay back to Claude
	req, _ := json.Marshal(map[string]any{"model": "claude-test", "input": []json.RawMessage{json.RawMessage(outputItems[0].Raw)}})
	replayed := ConvertOpenAIResponsesRequestToClaude("claude-test", req, false)
	replayedBlocks := gjson.GetBytes(replayed, "messages.0.content")
	if got := replayedBlocks.Get("1.content.0.type").String(); got != "web_search_tool_result_error" {
		t.Fatalf("replayed error type = %q, want web_search_tool_result_error", got)
	}
	if got := replayedBlocks.Get("1.content.0.error_code").String(); got != "rate_limited" {
		t.Fatalf("replayed error_code = %q, want rate_limited", got)
	}
}

func TestTextSearchTextOrderPreservedInStreamingAndReplay(t *testing.T) {
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_order","usage":{"input_tokens":1,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Before search."}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"content_block_start","index":1,"content_block":{"type":"server_tool_use","id":"srvtoolu_order","name":"web_search","input":{}}}`),
		[]byte(`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"query\"}"}}`),
		[]byte(`data: {"type":"content_block_stop","index":1}`),
		[]byte(`data: {"type":"content_block_start","index":2,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtoolu_order","content":[{"type":"web_search_result","title":"T","url":"https://example.com/order","encrypted_content":"ENC_ORD"}]}}`),
		[]byte(`data: {"type":"content_block_stop","index":2}`),
		[]byte(`data: {"type":"content_block_delta","index":1,"delta":{"type":"citations_delta","citation":{"type":"web_search_result_location","cited_text":"After search.","url":"https://example.com/order","title":"T","encrypted_index":"IDX_ORD"}}}`),
		[]byte(`data: {"type":"content_block_start","index":3,"content_block":{"type":"text","text":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":3,"delta":{"type":"text_delta","text":"After search."}}`),
		[]byte(`data: {"type":"content_block_stop","index":3}`),
		[]byte(`data: {"type":"message_stop"}`),
	}
	var completed gjson.Result
	for _, o := range translateClaudeResponsesStreamThroughRegistry(chunks) {
		if event, data := parseClaudeResponsesSSEEvent(t, o); event == "response.completed" {
			completed = data
		}
	}
	items := completed.Get("response.output").Array()
	if len(items) != 3 {
		t.Fatalf("expected 3 output items, got %d: %s", len(items), completed.Get("response.output").Raw)
	}
	if items[0].Get("type").String() != "message" || items[0].Get("content.0.text").String() != "Before search." {
		t.Fatalf("item 0 must be message 'Before search.', got: %s", items[0].Raw)
	}
	if items[1].Get("type").String() != "web_search_call" {
		t.Fatalf("item 1 must be web_search_call, got: %s", items[1].Raw)
	}
	if items[2].Get("type").String() != "message" || items[2].Get("content.0.text").String() != "After search." {
		t.Fatalf("item 2 must be message 'After search.', got: %s", items[2].Raw)
	}
	if items[2].Get("content.0.annotations.0.encrypted_index").String() != "IDX_ORD" {
		t.Fatalf("item 2 must have citation with encrypted_index IDX_ORD, got: %s", items[2].Raw)
	}

	// Also verify non-streaming matches streaming behavior:
	var lines []string
	for _, ch := range chunks {
		lines = append(lines, string(ch))
	}
	nonStreamOut := ConvertClaudeResponseToOpenAIResponsesNonStream(
		context.Background(), "claude-test", nil, nil, []byte(strings.Join(lines, "\n")), nil)
	nonStreamItems := gjson.GetBytes(nonStreamOut, "output").Array()
	if len(nonStreamItems) != 3 {
		t.Fatalf("non-stream expected 3 output items, got %d: %s", len(nonStreamItems), gjson.GetBytes(nonStreamOut, "output").Raw)
	}
	if nonStreamItems[0].Get("content.0.text").String() != "Before search." {
		t.Fatalf("non-stream item 0 text = %q, want 'Before search.'", nonStreamItems[0].Get("content.0.text").String())
	}
	if nonStreamItems[1].Get("type").String() != "web_search_call" {
		t.Fatalf("non-stream item 1 type = %q, want web_search_call", nonStreamItems[1].Get("type").String())
	}
	if nonStreamItems[2].Get("content.0.text").String() != "After search." {
		t.Fatalf("non-stream item 2 text = %q, want 'After search.'", nonStreamItems[2].Get("content.0.text").String())
	}
	if nonStreamItems[2].Get("content.0.annotations.0.encrypted_index").String() != "IDX_ORD" {
		t.Fatalf("non-stream item 2 citation encrypted_index = %q, want IDX_ORD", nonStreamItems[2].Get("content.0.annotations.0.encrypted_index").String())
	}

	// Replay back to Claude and verify the block sequence matches original Claude order:
	// text -> server_tool_use -> web_search_tool_result -> text
	var rawItems []json.RawMessage
	for _, it := range items {
		rawItems = append(rawItems, json.RawMessage(it.Raw))
	}
	req, _ := json.Marshal(map[string]any{"model": "claude-test", "input": rawItems})
	replayed := ConvertOpenAIResponsesRequestToClaude("claude-test", req, false)
	replayedBlocks := claudeAssistantBlockTypes(t, replayed)
	wantBlocks := []string{"text", "server_tool_use", "web_search_tool_result", "text"}
	if strings.Join(replayedBlocks, ",") != strings.Join(wantBlocks, ",") {
		t.Fatalf("replayed block types = %v, want %v", replayedBlocks, wantBlocks)
	}
}
