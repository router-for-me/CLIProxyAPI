package helps

import (
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/cache"
)

const claudeWebSearchTestResults = `[{"type":"web_search_result","url":"https://example.com/a","title":"A","encrypted_content":"enc-a","page_age":null},{"type":"web_search_result","url":"https://example.com/b","title":"B","encrypted_content":"enc-b","page_age":null}]`

func resetClaudeWebSearchResults(t *testing.T) {
	t.Helper()
	cache.ClearClaudeWebSearchResultCache()
	t.Cleanup(cache.ClearClaudeWebSearchResultCache)
}

func claudeWebSearchRequestBody(resultContent string) []byte {
	return []byte(`{"model":"claude-opus-5-5","messages":[` +
		`{"role":"user","content":[{"type":"text","text":"search"}]},` +
		`{"role":"assistant","content":[` +
		`{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"q"}},` +
		`{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":` + resultContent + `},` +
		`{"type":"text","text":"answer"}]},` +
		`{"role":"user","content":[{"type":"text","text":"how many results?"}]}]}`)
}

func TestRecordClaudeWebSearchResultsFromStreamEventAndRestore(t *testing.T) {
	resetClaudeWebSearchResults(t)
	event := []byte(`data: {"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":` + claudeWebSearchTestResults + `}}`)
	RecordClaudeWebSearchResults("session-1", event, 30*time.Minute, 16)

	got := RestoreClaudeWebSearchResults(claudeWebSearchRequestBody(`[]`), "session-1", 30*time.Minute)
	if raw := ojson.GetBytes(got, "messages.1.content.1.content").Raw; raw != claudeWebSearchTestResults {
		t.Fatalf("restored content = %s, want original results", raw)
	}
	if ojson.GetBytes(got, "messages.1.content.2.text").String() != "answer" {
		t.Fatalf("neighbouring blocks changed: %s", got)
	}
}

func TestRecordClaudeWebSearchResultsFromMessageResponse(t *testing.T) {
	resetClaudeWebSearchResults(t)
	response := []byte(`{"id":"msg_1","type":"message","role":"assistant","content":[` +
		`{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"q"}},` +
		`{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":` + claudeWebSearchTestResults + `}]}`)
	RecordClaudeWebSearchResults("session-1", response, 30*time.Minute, 16)

	got := RestoreClaudeWebSearchResults(claudeWebSearchRequestBody(`[]`), "session-1", 30*time.Minute)
	if raw := ojson.GetBytes(got, "messages.1.content.1.content").Raw; raw != claudeWebSearchTestResults {
		t.Fatalf("restored content = %s, want original results", raw)
	}
}

func TestRestoreClaudeWebSearchResultsLeavesBodyUnchangedWhenNotApplicable(t *testing.T) {
	resetClaudeWebSearchResults(t)
	RecordClaudeWebSearchResults("session-1", []byte(`data: {"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":`+claudeWebSearchTestResults+`}}`), 30*time.Minute, 16)

	cases := map[string]struct {
		body       []byte
		sessionKey string
	}{
		"other session":           {claudeWebSearchRequestBody(`[]`), "session-2"},
		"no session":              {claudeWebSearchRequestBody(`[]`), ""},
		"content already present": {claudeWebSearchRequestBody(`[{"type":"web_search_result","url":"https://example.com/z","encrypted_content":"z"}]`), "session-1"},
		"unknown tool use":        {[]byte(`{"messages":[{"role":"assistant","content":[{"type":"web_search_tool_result","tool_use_id":"srvtoolu_9","content":[]}]}]}`), "session-1"},
		"user role is ignored":    {[]byte(`{"messages":[{"role":"user","content":[{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[]}]}]}`), "session-1"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := RestoreClaudeWebSearchResults(tc.body, tc.sessionKey, 30*time.Minute); string(got) != string(tc.body) {
				t.Fatalf("body changed:\n got %s\nwant %s", got, tc.body)
			}
		})
	}
}

func TestRecordClaudeWebSearchResultsIgnoresEmptyResults(t *testing.T) {
	resetClaudeWebSearchResults(t)
	RecordClaudeWebSearchResults("session-1", []byte(`data: {"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[]}}`), 30*time.Minute, 16)
	if _, ok := cache.GetClaudeWebSearchResult("session-1", "srvtoolu_1"); ok {
		t.Fatal("empty result content was cached")
	}
}

func TestRestoreClaudeWebSearchResultsDropsCitationsWhenResultRemainsEmpty(t *testing.T) {
	resetClaudeWebSearchResults(t)
	event := []byte(`data: {"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":` + claudeWebSearchTestResults + `}}`)
	RecordClaudeWebSearchResults("session-1", event, 30*time.Minute, 16)

	citedBody := []byte(`{"model":"claude-opus-5-5","messages":[` +
		`{"role":"user","content":[{"type":"text","text":"search"}]},` +
		`{"role":"assistant","content":[` +
		`{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"q"}},` +
		`{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[]},` +
		`{"type":"text","text":"answer","citations":[{"type":"web_search_result_location","url":"https://example.com/a","title":"A","cited_text":"answer","encrypted_index":"IDX_A"}]}]},` +
		`{"role":"user","content":[{"type":"text","text":"how many results?"}]}]}`)

	restored := RestoreClaudeWebSearchResults(citedBody, "session-1", 30*time.Minute)
	if got := ojson.GetBytes(restored, "messages.1.content.1.content").Raw; got != claudeWebSearchTestResults {
		t.Fatalf("restored content = %s, want %s", got, claudeWebSearchTestResults)
	}
	if got := ojson.GetBytes(restored, "messages.1.content.2.citations.0.encrypted_index").String(); got != "IDX_A" {
		t.Fatalf("restored turn citation encrypted_index = %q, want IDX_A: %s", got, restored)
	}

	for name, sessionKey := range map[string]string{
		"cache miss":  "session-2",
		"empty scope": "",
	} {
		t.Run(name, func(t *testing.T) {
			missed := RestoreClaudeWebSearchResults(citedBody, sessionKey, 30*time.Minute)
			if got := ojson.GetBytes(missed, "messages.1.content.2.text").String(); got != "answer" {
				t.Fatalf("text = %q, want answer", got)
			}
			if citations := ojson.GetBytes(missed, "messages.1.content.2.citations"); citations.Exists() {
				t.Fatalf("expected citations to be dropped on %s, got: %s", name, citations.Raw)
			}
		})
	}
}
