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
// answer once.

type codexClaudeBlocks struct {
	text, thinking []string
	signatures     int
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
					blocks.signatures++
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
	if got.signatures != 0 {
		t.Fatalf("a commentary block carries no signature, got %d signature deltas", got.signatures)
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

func TestConvertCodexResponseToClaude_StreamCommentaryDoesNotTakeReasoningSignature(t *testing.T) {
	// reasoning item (with encrypted_content) → commentary message → final_answer:
	// the signature belongs to the reasoning block, never to the commentary block.
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
	if got.signatures != 1 {
		t.Fatalf("exactly one signature (the reasoning item's), got %d", got.signatures)
	}
	if len(got.text) != 1 || got.text[0] != "All green." {
		t.Fatalf("final_answer should be the only text block, got %q", got.text)
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
	if content[1].Get("type").String() != "text" || content[1].Get("text").String() != "The merge was refused." {
		t.Fatalf("final_answer should be a text block, got %s", content[1].Raw)
	}
}
