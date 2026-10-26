package claude

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// GPT-5.x models answer with two message output items: a "commentary" preamble
// and the "final_answer". Only the final answer is a Claude text block; the
// preamble travels as thinking so a text consumer (or a TTS client) gets the
// answer once. The commentary thinking block carries the internal commentary
// marker as its signature so the request translator can restore it on replay.

type codexClaudeBlocks struct {
	text, thinking []string
	signatures     []string
}

func collectCodexClaudeBlocks(t *testing.T, chunks [][]byte) codexClaudeBlocks {
	t.Helper()
	ctx := context.Background()
	var param any
	var outputs [][]byte
	for _, chunk := range chunks {
		outputs = append(outputs, ConvertCodexResponseToClaude(ctx, "", []byte(`{"messages":[]}`), nil, chunk, &param)...)
	}
	var blocks codexClaudeBlocks
	var cur strings.Builder
	curType := ""
	for _, out := range outputs {
		for _, line := range strings.Split(string(out), "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := gjson.Parse(strings.TrimPrefix(line, "data: "))
			switch data.Get("type").String() {
			case "content_block_start":
				curType = data.Get("content_block.type").String()
				cur.Reset()
			case "content_block_delta":
				switch data.Get("delta.type").String() {
				case "text_delta":
					cur.WriteString(data.Get("delta.text").String())
				case "thinking_delta":
					cur.WriteString(data.Get("delta.thinking").String())
				case "signature_delta":
					blocks.signatures = append(blocks.signatures, data.Get("delta.signature").String())
				}
			case "content_block_stop":
				switch curType {
				case "text":
					blocks.text = append(blocks.text, cur.String())
				case "thinking":
					blocks.thinking = append(blocks.thinking, cur.String())
				}
				curType = ""
			}
		}
	}
	return blocks
}

func TestConvertCodexResponseToClaude_StreamCommentaryPhaseIsThinking(t *testing.T) {
	chunks := [][]byte{
		[]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5"}}`),
		[]byte(`data: {"type":"response.output_item.added","item":{"type":"message","role":"assistant","phase":"commentary"}}`),
		[]byte(`data: {"type":"response.content_part.added","part":{"type":"output_text"}}`),
		[]byte(`data: {"type":"response.output_text.delta","delta":"Checking the "}`),
		[]byte(`data: {"type":"response.output_text.delta","delta":"gate first."}`),
		[]byte(`data: {"type":"response.content_part.done","part":{"type":"output_text","text":"Checking the gate first."}}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"Checking the gate first."}]}}`),
		[]byte(`data: {"type":"response.output_item.added","item":{"type":"message","role":"assistant","phase":"final_answer"}}`),
		[]byte(`data: {"type":"response.content_part.added","part":{"type":"output_text"}}`),
		[]byte(`data: {"type":"response.output_text.delta","delta":"The merge was refused."}`),
		[]byte(`data: {"type":"response.content_part.done","part":{"type":"output_text","text":"The merge was refused."}}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"The merge was refused."}]}}`),
		[]byte(`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1}}}`),
	}
	got := collectCodexClaudeBlocks(t, chunks)
	if len(got.thinking) != 1 || got.thinking[0] != "Checking the gate first." {
		t.Fatalf("commentary should be one thinking block, got %q", got.thinking)
	}
	if len(got.text) != 1 || got.text[0] != "The merge was refused." {
		t.Fatalf("final_answer should be the only text block, got %q", got.text)
	}
	if len(got.signatures) != 1 || got.signatures[0] != codexCommentarySignatureMarker {
		t.Fatalf("a commentary block carries only the internal marker signature, got %v", got.signatures)
	}
}

func TestConvertCodexResponseToClaude_StreamCommentaryDoneFallbackIsThinking(t *testing.T) {
	// No deltas at all: both items arrive whole on output_item.done.
	chunks := [][]byte{
		[]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5"}}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"On it."}]}}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Done: two PRs merged."}]}}`),
		[]byte(`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1}}}`),
	}
	got := collectCodexClaudeBlocks(t, chunks)
	if len(got.thinking) != 1 || got.thinking[0] != "On it." {
		t.Fatalf("commentary should be one thinking block, got %q", got.thinking)
	}
	if len(got.text) != 1 || got.text[0] != "Done: two PRs merged." {
		t.Fatalf("final_answer should be the only text block, got %q", got.text)
	}
}

func TestConvertCodexResponseToClaude_StreamSecondMessageDoneFallbackAfterStreamedFirstMessage(t *testing.T) {
	// First message streams deltas; second message arrives whole on output_item.done
	// (both with and without a preceding output_item.added).
	for _, includeSecondAdded := range []bool{true, false} {
		chunks := [][]byte{
			[]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5"}}`),
			[]byte(`data: {"type":"response.output_item.added","item":{"type":"message","role":"assistant","phase":"commentary"}}`),
			[]byte(`data: {"type":"response.output_text.delta","delta":"Checking."}`),
			[]byte(`data: {"type":"response.output_item.done","item":{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"Checking."}]}}`),
		}
		if includeSecondAdded {
			chunks = append(chunks, []byte(`data: {"type":"response.output_item.added","item":{"type":"message","role":"assistant","phase":"final_answer"}}`))
		}
		chunks = append(chunks,
			[]byte(`data: {"type":"response.output_item.done","item":{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"All checks passed."}]}}`),
			[]byte(`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1}}}`),
		)
		got := collectCodexClaudeBlocks(t, chunks)
		if len(got.thinking) != 1 || got.thinking[0] != "Checking." {
			t.Fatalf("includeSecondAdded=%v: commentary should be one thinking block, got %q", includeSecondAdded, got.thinking)
		}
		if len(got.text) != 1 || got.text[0] != "All checks passed." {
			t.Fatalf("includeSecondAdded=%v: final_answer done fallback should be emitted after streamed commentary, got %q", includeSecondAdded, got.text)
		}
	}
}

func TestConvertCodexResponseToClaude_StreamCommentaryDoesNotTakeReasoningSignature(t *testing.T) {
	// reasoning item (with encrypted_content) -> commentary message -> final_answer:
	// the reasoning signature belongs to the reasoning block; the commentary block
	// carries only the internal commentary marker.
	chunks := [][]byte{
		[]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5"}}`),
		[]byte(`data: {"type":"response.output_item.added","item":{"type":"reasoning","encrypted_content":"pre"}}`),
		[]byte(`data: {"type":"response.reasoning_summary_part.added"}`),
		[]byte(`data: {"type":"response.reasoning_summary_text.delta","delta":"Let me think"}`),
		[]byte(`data: {"type":"response.reasoning_summary_part.done"}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"type":"reasoning","encrypted_content":"enc_sig"}}`),
		[]byte(`data: {"type":"response.output_item.added","item":{"type":"message","role":"assistant","phase":"commentary"}}`),
		[]byte(`data: {"type":"response.output_text.delta","delta":"Looking."}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"Looking."}]}}`),
		[]byte(`data: {"type":"response.output_item.added","item":{"type":"message","role":"assistant","phase":"final_answer"}}`),
		[]byte(`data: {"type":"response.output_text.delta","delta":"All green."}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"All green."}]}}`),
		[]byte(`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1}}}`),
	}
	got := collectCodexClaudeBlocks(t, chunks)
	if len(got.thinking) != 2 || got.thinking[0] != "Let me think" || got.thinking[1] != "Looking." {
		t.Fatalf("expected reasoning + commentary thinking blocks, got %q", got.thinking)
	}
	if len(got.signatures) != 2 || got.signatures[0] != "enc_sig" || got.signatures[1] != codexCommentarySignatureMarker {
		t.Fatalf("expected the reasoning signature plus the commentary marker, got %v", got.signatures)
	}
	if len(got.text) != 1 || got.text[0] != "All green." {
		t.Fatalf("final_answer should be the only text block, got %q", got.text)
	}

	// Also verify when a reasoning item's output_item.added parked encrypted_content
	// before any summary delta opened a block, a commentary block does not consume it.
	parkedChunks := [][]byte{
		[]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5"}}`),
		[]byte(`data: {"type":"response.output_item.added","item":{"type":"reasoning","encrypted_content":"parked_sig"}}`),
		[]byte(`data: {"type":"response.output_item.added","item":{"type":"message","role":"assistant","phase":"commentary"}}`),
		[]byte(`data: {"type":"response.output_text.delta","delta":"Checking."}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"Checking."}]}}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"type":"reasoning","summary":[{"type":"summary_text","text":"Thought."}],"encrypted_content":""}}`),
		[]byte(`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1}}}`),
	}
	parkedGot := collectCodexClaudeBlocks(t, parkedChunks)
	if len(parkedGot.thinking) != 2 || parkedGot.thinking[0] != "Checking." || parkedGot.thinking[1] != "Thought." {
		t.Fatalf("expected commentary + reasoning thinking blocks, got %q", parkedGot.thinking)
	}
	if len(parkedGot.signatures) != 2 || parkedGot.signatures[0] != codexCommentarySignatureMarker || parkedGot.signatures[1] != "parked_sig" {
		t.Fatalf("expected the commentary marker plus parked_sig for the reasoning block, got %v", parkedGot.signatures)
	}
}

func TestConvertCodexResponseToClaude_StreamUnclosedReasoningClosesBeforeCommentaryStarts(t *testing.T) {
	// If a reasoning item never emitted output_item.done before a commentary message
	// starts, its thinking block must still close (with its fallback signature)
	// before the commentary thinking block opens.
	chunks := [][]byte{
		[]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5"}}`),
		[]byte(`data: {"type":"response.output_item.added","item":{"type":"reasoning","encrypted_content":"enc_fallback"}}`),
		[]byte(`data: {"type":"response.reasoning_summary_part.added"}`),
		[]byte(`data: {"type":"response.reasoning_summary_text.delta","delta":"Reasoning step"}`),
		[]byte(`data: {"type":"response.output_item.added","item":{"type":"message","role":"assistant","phase":"commentary"}}`),
		[]byte(`data: {"type":"response.content_part.added","part":{"type":"output_text"}}`),
		[]byte(`data: {"type":"response.output_text.delta","delta":"Commentary step"}`),
		[]byte(`data: {"type":"response.content_part.done","part":{"type":"output_text","text":"Commentary step"}}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"Commentary step"}]}}`),
		[]byte(`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1}}}`),
	}
	got := collectCodexClaudeBlocks(t, chunks)
	if len(got.thinking) != 2 || got.thinking[0] != "Reasoning step" || got.thinking[1] != "Commentary step" {
		t.Fatalf("expected separate reasoning and commentary thinking blocks, got %q", got.thinking)
	}
	if len(got.signatures) != 2 || got.signatures[0] != "enc_fallback" || got.signatures[1] != codexCommentarySignatureMarker {
		t.Fatalf("expected fallback signature on reasoning block plus the commentary marker, got %v", got.signatures)
	}
}

func TestConvertCodexResponseToClaude_StreamFinalAnswerOnlyUnchanged(t *testing.T) {
	chunks := [][]byte{
		[]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5"}}`),
		[]byte(`data: {"type":"response.output_item.added","item":{"type":"message","role":"assistant","phase":"final_answer"}}`),
		[]byte(`data: {"type":"response.output_text.delta","delta":"Hi."}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Hi."}]}}`),
		[]byte(`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1}}}`),
	}
	got := collectCodexClaudeBlocks(t, chunks)
	if len(got.thinking) != 0 || len(got.text) != 1 || got.text[0] != "Hi." {
		t.Fatalf("unexpected blocks: thinking=%q text=%q", got.thinking, got.text)
	}
}

func TestConvertCodexResponseToClaudeNonStream_CommentaryPhaseIsThinking(t *testing.T) {
	raw := []byte(`{"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"gpt-5","output":[
		{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"Checking the gate first."}]},
		{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"The merge was refused."}]}
	],"usage":{"input_tokens":1,"output_tokens":1}}}`)
	var param any
	out := ConvertCodexResponseToClaudeNonStream(context.Background(), "", []byte(`{"messages":[]}`), nil, raw, &param)
	content := gjson.GetBytes(out, "content").Array()
	if len(content) != 2 {
		t.Fatalf("expected 2 content blocks, got %s", out)
	}
	if content[0].Get("type").String() != "thinking" || content[0].Get("thinking").String() != "Checking the gate first." {
		t.Fatalf("commentary should be a thinking block, got %s", content[0].Raw)
	}
	if got := content[0].Get("signature").String(); got != codexCommentarySignatureMarker {
		t.Fatalf("commentary thinking block should carry the internal marker signature, got %s", content[0].Raw)
	}
	if content[1].Get("type").String() != "text" || content[1].Get("text").String() != "The merge was refused." {
		t.Fatalf("final_answer should be a text block, got %s", content[1].Raw)
	}
}
