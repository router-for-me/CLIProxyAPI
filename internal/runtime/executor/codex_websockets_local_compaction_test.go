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

func TestCodexWebsocketRequestResetsUpstreamContextForNormalizedMessageReplay(t *testing.T) {
	replayInput := `[
		{"type":"message","role":"developer","content":"workspace context"},
		{"type":"message","role":"user","content":"first turn"},
		{"type":"message","role":"user","content":"next turn"}
	]`

	if !codexWebsocketRequestResetsUpstreamContext([]byte(fmt.Sprintf(`{"model":"gpt-5-codex","stream":true,"input":%s}`, replayInput))) {
		t.Fatal("normalized type-less user/developer replay must reset the upstream websocket")
	}
	if codexWebsocketRequestResetsUpstreamContext([]byte(fmt.Sprintf(`{"model":"gpt-5-codex","previous_response_id":"resp-1","input":%s}`, replayInput))) {
		t.Fatal("normalized replay with previous_response_id must keep the upstream websocket")
	}
	if codexWebsocketRequestResetsUpstreamContext([]byte(fmt.Sprintf(`{"type":"response.append","model":"gpt-5-codex","input":%s}`, replayInput))) {
		t.Fatal("response.append must keep the upstream websocket")
	}
	if codexWebsocketRequestResetsUpstreamContext([]byte(fmt.Sprintf(`{"input":%s}`, replayInput))) {
		t.Fatal("ordinary model-less follow-up must keep the upstream websocket")
	}
	ordinary := []byte(`{"model":"gpt-5-codex","input":[{"type":"message","role":"user","content":"next turn"}]}`)
	if codexWebsocketRequestResetsUpstreamContext(ordinary) {
		t.Fatal("ordinary single-message follow-up must keep the upstream websocket")
	}
}
