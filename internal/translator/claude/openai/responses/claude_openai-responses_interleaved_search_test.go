package responses

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// Regression for a production incident: a Claude turn that interleaved thinking
// with server-side web searches used to lose the search blocks, leaving two
// adjacent reasoning items. Replaying those produced two adjacent Claude
// `thinking` blocks, which Anthropic rejects with
// "`thinking` ... blocks in the latest assistant message cannot be modified",
// killing the session mid-turn.
func TestInterleavedThinkingAndSearchSurvivesRoundTrip(t *testing.T) {
	signature := mustTestSignature(t)
	thinking := func(index int, text string) [][]byte {
		prefix := `data: {"type":"content_block_`
		return [][]byte{
			[]byte(prefix + `start","index":` + strconv.Itoa(index) + `,"content_block":{"type":"thinking","thinking":""}}`),
			[]byte(prefix + `delta","index":` + strconv.Itoa(index) + `,"delta":{"type":"thinking_delta","thinking":"` + text + `"}}`),
			[]byte(prefix + `delta","index":` + strconv.Itoa(index) + `,"delta":{"type":"signature_delta","signature":"` + signature + `"}}`),
			[]byte(prefix + `stop","index":` + strconv.Itoa(index) + `}`),
		}
	}
	search := func(index int, id string) [][]byte {
		prefix := `data: {"type":"content_block_`
		return [][]byte{
			[]byte(prefix + `start","index":` + strconv.Itoa(index) + `,"content_block":{"type":"server_tool_use","id":"` + id + `","name":"web_search","input":{}}}`),
			[]byte(prefix + `delta","index":` + strconv.Itoa(index) + `,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"lindorm\"}"}}`),
			[]byte(prefix + `stop","index":` + strconv.Itoa(index) + `}`),
			[]byte(prefix + `start","index":` + strconv.Itoa(index+1) + `,"content_block":{"type":"web_search_tool_result","tool_use_id":"` + id + `","content":[{"type":"web_search_result","title":"T","url":"https://example.com/` + id + `"}]}}`),
			[]byte(prefix + `stop","index":` + strconv.Itoa(index+1) + `}`),
		}
	}

	chunks := [][]byte{[]byte(`data: {"type":"message_start","message":{"id":"msg_x","usage":{"input_tokens":1,"output_tokens":0}}}`)}
	chunks = append(chunks, thinking(0, "first")...)
	chunks = append(chunks, []byte(`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Researching."}}`),
		[]byte(`data: {"type":"content_block_stop","index":1}`))
	chunks = append(chunks, search(2, "srvtoolu_a")...)
	chunks = append(chunks, thinking(6, "second")...)
	chunks = append(chunks, search(7, "srvtoolu_b")...)
	chunks = append(chunks, thinking(11, "third")...)
	chunks = append(chunks,
		[]byte(`data: {"type":"content_block_start","index":12,"content_block":{"type":"tool_use","id":"toolu_1","name":"exec","input":{}}}`),
		[]byte(`data: {"type":"content_block_delta","index":12,"delta":{"type":"input_json_delta","partial_json":"{\"cmd\":\"pwd\"}"}}`),
		[]byte(`data: {"type":"content_block_stop","index":12}`),
		[]byte(`data: {"type":"message_stop"}`))

	outputs := translateClaudeResponsesStreamThroughRegistry(chunks)
	var completed gjson.Result
	for _, output := range outputs {
		if event, data := parseClaudeResponsesSSEEvent(t, output); event == "response.completed" {
			completed = data
		}
	}

	var kinds []string
	var items []json.RawMessage
	completed.Get("response.output").ForEach(func(_, v gjson.Result) bool {
		kinds = append(kinds, v.Get("type").String())
		items = append(items, json.RawMessage(v.Raw))
		return true
	})
	for i := 1; i < len(kinds); i++ {
		if kinds[i] == "reasoning" && kinds[i-1] == "reasoning" {
			t.Fatalf("adjacent reasoning items leak the dropped search blocks: %v", kinds)
		}
	}
	if searches := strings.Count(strings.Join(kinds, ","), "web_search_call"); searches != 2 {
		t.Fatalf("web_search_call items = %d, want 2: %v", searches, kinds)
	}

	req, _ := json.Marshal(map[string]any{"model": "claude-test", "input": items})
	blocks := claudeAssistantBlockTypes(t, ConvertOpenAIResponsesRequestToClaude("claude-test", req, false))
	for i := 1; i < len(blocks); i++ {
		if blocks[i] == "thinking" && blocks[i-1] == "thinking" {
			t.Fatalf("Anthropic rejects adjacent thinking blocks: %v", blocks)
		}
	}
	if got := strings.Count(strings.Join(blocks, ","), "web_search_tool_result"); got != 2 {
		t.Fatalf("replayed web_search_tool_result blocks = %d, want 2: %v", got, blocks)
	}
}
