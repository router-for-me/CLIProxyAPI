package claude

import (
	"context"
	"testing"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestVisiblePublicCommentaryPreservesReasoningAndFinalAnswer(t *testing.T) {
	t.Setenv("CPA_VISIBLE_CODEX_COMMENTARY", "1")
	chunks := [][]byte{
		[]byte(`data: {"type":"response.created","response":{"id":"r","model":"gpt-6.1-sol"}}`),
		[]byte(`data: {"type":"response.output_item.added","item":{"type":"reasoning","encrypted_content":"secret-signature"}}`),
		[]byte(`data: {"type":"response.reasoning_summary_part.added"}`),
		[]byte(`data: {"type":"response.reasoning_summary_text.delta","delta":"Private reasoning summary"}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"type":"reasoning","encrypted_content":"secret-signature"}}`),
		[]byte(`data: {"type":"response.output_item.added","item":{"type":"message","role":"assistant","phase":"commentary"}}`),
		[]byte(`data: {"type":"response.output_text.delta","delta":"Two agents are working."}`),
		[]byte(`data: {"type":"response.content_part.done","part":{"type":"output_text"}}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"type":"message","role":"assistant","phase":"commentary"}}`),
		[]byte(`data: {"type":"response.output_item.added","item":{"type":"message","role":"assistant","phase":"final_answer"}}`),
		[]byte(`data: {"type":"response.output_text.delta","delta":"Done."}`),
		[]byte(`data: {"type":"response.content_part.done","part":{"type":"output_text"}}`),
		[]byte(`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1}}}`),
	}
	got := collectCodexClaudeBlocks(t, chunks)
	if len(got.text) != 2 || got.text[0] != "Two agents are working." || got.text[1] != "Done." {
		t.Fatalf("public commentary and final answer must be visible once: %q", got.text)
	}
	if len(got.thinking) != 2 || got.thinking[0] != "Private reasoning summary" || got.signatures[1] != codexCommentarySignatureMarker {
		t.Fatalf("reasoning and replay marker must be preserved: %#v", got)
	}
}

func TestVisibleCommentaryDoneOnlyIsMirrored(t *testing.T) {
	t.Setenv("CPA_VISIBLE_CODEX_COMMENTARY", "1")
	got := collectCodexClaudeBlocks(t, [][]byte{
		[]byte(`data: {"type":"response.created","response":{"id":"r","model":"gpt-6.1-sol"}}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"Progress"}]}}`),
		[]byte(`data: {"type":"response.completed","response":{"usage":{}}}`),
	})
	if len(got.text) != 1 || got.text[0] != "Progress" || len(got.thinking) != 1 {
		t.Fatalf("unexpected blocks: %#v", got)
	}
}

func TestVisibleCommentaryNonStreamContext(t *testing.T) {
	t.Setenv("CPA_VISIBLE_CODEX_COMMENTARY", "")
	ctx := translatorcommon.WithVisibleCodexCommentary(context.Background(), true)
	raw := []byte(`{"type":"response.completed","response":{"id":"r","model":"gpt-6.1-sol","output":[{"type":"reasoning","summary":[{"type":"summary_text","text":"Reasoning summary"}],"encrypted_content":"sig"},{"type":"message","phase":"commentary","content":[{"type":"output_text","text":"Public progress"}]},{"type":"message","phase":"final_answer","content":[{"type":"output_text","text":"Final answer"}]}],"usage":{"input_tokens":5,"output_tokens":7}}}`)
	var param any
	out := ConvertCodexResponseToClaudeNonStream(ctx, "", []byte(`{"messages":[]}`), nil, raw, &param)
	content := gjson.GetBytes(out, "content").Array()
	if len(content) != 4 || content[0].Get("type").String() != "thinking" || content[1].Get("signature").String() != codexCommentarySignatureMarker || content[2].Get("text").String() != "Public progress" || content[3].Get("text").String() != "Final answer" {
		t.Fatalf("non-stream progress/reasoning/final blocks changed: %s", out)
	}
	if gjson.GetBytes(out, "usage.output_tokens").Int() != 7 {
		t.Fatalf("mirroring must not inflate model token accounting: %s", out)
	}
}

func TestVisibleCommentaryReplayDefaultPreservesRepeatedFinal(t *testing.T) {
	t.Setenv("CPA_VISIBLE_CODEX_COMMENTARY", "")
	input := []byte(`{"model":"m","messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"same","signature":"cpa:commentary:v1"},{"type":"text","text":"same"}]}]}`)
	out, err := ConvertClaudeRequestToCodex("gpt-6.1-sol", input, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(gjson.GetBytes(out, "input").Array()) != 2 {
		t.Fatalf("default mode changed existing replay semantics: %s", out)
	}
}

func TestVisibleCommentaryReplayContextPreservesAdjacentFinalText(t *testing.T) {
	t.Setenv("CPA_VISIBLE_CODEX_COMMENTARY", "")
	ctx := translatorcommon.WithVisibleCodexCommentary(context.Background(), true)
	input := []byte(`{"model":"m","messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"Progress","signature":"cpa:commentary:v1"},{"type":"text","text":"Progress"},{"type":"text","text":"Actual final answer"}]},{"role":"user","content":"What are the agents doing?"}]}`)
	result := sdktranslator.TranslateRequestEnvelope(ctx, sdktranslator.FormatClaude, sdktranslator.FormatCodex,
		sdktranslator.RequestEnvelope{Format: sdktranslator.FormatClaude, Model: "gpt-6.1-sol", Body: input})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	items := gjson.GetBytes(result.Body, "input").Array()
	if len(items) != 3 || items[0].Get("phase").String() != "commentary" || items[1].Get("content.0.text").String() != "Actual final answer" || items[2].Get("content.0.text").String() != "What are the agents doing?" {
		t.Fatalf("mirror removal lost final text or user steering: %s", result.Body)
	}
}

func TestVisibleCommentaryReplayAcrossAssistantMessageBoundary(t *testing.T) {
	t.Setenv("CPA_VISIBLE_CODEX_COMMENTARY", "1")
	input := []byte(`{"model":"m","messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"Progress","signature":"cpa:commentary:v1"}]},{"role":"assistant","content":[{"type":"text","text":"Progress"},{"type":"text","text":"Final"}]}]}`)
	out, err := ConvertClaudeRequestToCodex("gpt-6.1-sol", input, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(gjson.GetBytes(out, "input").Array()) != 2 || gjson.GetBytes(out, "input.1.content.0.text").String() != "Final" {
		t.Fatalf("split native history replay did not preserve exactly one progress: %s", out)
	}
}

func TestVisibleCommentaryReplayKeepsPhaseWithoutDuplicate(t *testing.T) {
	t.Setenv("CPA_VISIBLE_CODEX_COMMENTARY", "1")
	input := []byte(`{"model":"gpt-6.1-sol","messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"Two agents are working.","signature":"cpa:commentary:v1"},{"type":"text","text":"Two agents are working."},{"type":"tool_use","id":"call_1","name":"Bash","input":{"command":"pwd"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"/tmp"},{"type":"text","text":"User asks: what are the agents doing?"}]}]}`)
	body, err := ConvertClaudeRequestToCodexWithCompat("gpt-6.1-sol", input, false)
	if err != nil {
		t.Fatal(err)
	}
	items := gjson.GetBytes(body, "input").Array()
	if len(items) != 4 || items[0].Get("phase").String() != "commentary" || items[1].Get("type").String() != "function_call" || items[3].Get("content.0.text").String() != "User asks: what are the agents doing?" {
		t.Fatalf("replay lost phase, tool call, or user steering: %s", body)
	}
}
