package executor

import (
	"fmt"
	"testing"
)

func TestCodexWebsocketRequestResetsUpstreamContextForLocalCompactionSummary(t *testing.T) {
	summary := "Another language model started to solve this problem and produced a summary of its thinking process. You also have access to the state of the tools that were used by that language model. Use this to build on the work that has already been done and avoid duplicating work. Here is the summary produced by the other language model, use the information in this summary to assist with your own analysis:\nCompressed transcript."
	input := fmt.Sprintf(`[
		{"type":"additional_tools","role":"developer","tools":[]},
		{"type":"message","role":"developer","content":"workspace context"},
		{"type":"message","role":"user","content":%q},
		{"type":"message","role":"user","content":"continue"}
	]`, summary)

	if !codexWebsocketRequestResetsUpstreamContext([]byte(fmt.Sprintf(`{"model":"gpt-5-codex","input":%s}`, input))) {
		t.Fatal("local compaction summary create must reset the upstream websocket")
	}
	if codexWebsocketRequestResetsUpstreamContext([]byte(fmt.Sprintf(`{"type":"response.create","previous_response_id":"resp-1","input":%s}`, input))) {
		t.Fatal("local compaction summary with previous_response_id must keep the upstream websocket")
	}
	if codexWebsocketRequestResetsUpstreamContext([]byte(fmt.Sprintf(`{"type":"response.append","input":%s}`, input))) {
		t.Fatal("local compaction summary append must keep the upstream websocket")
	}
	ordinary := []byte(`{"type":"response.create","input":[{"type":"message","role":"user","content":"Please create a compacted summary."}]}`)
	if codexWebsocketRequestResetsUpstreamContext(ordinary) {
		t.Fatal("ordinary user text must not reset the upstream websocket")
	}
}
