package helps

import (
	"context"
	"strings"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

type fakeCompactionExecutor struct {
	gotPayload []byte
}

func (f *fakeCompactionExecutor) Execute(_ context.Context, _ *cliproxyauth.Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	f.gotPayload = req.Payload
	return cliproxyexecutor.Response{Payload: []byte(`{"id":"resp_x","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"GOAL: finish.\nDONE: read a.go"}]}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`)}, nil
}

const triggerPayload = `{"model":"claude-fable-5-1","stream":true,"tools":[{"type":"function","name":"exec"}],"tool_choice":"auto","include":["reasoning.encrypted_content"],"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"do it"}]},{"type":"function_call","name":"exec","call_id":"c1","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":"ok"},{"type":"compaction_trigger"}]}`

func TestSyntheticCompactionRoundTrip(t *testing.T) {
	t.Parallel()
	opts := cliproxyexecutor.Options{Stream: true, SourceFormat: sdktranslator.FormatOpenAIResponse}
	if !SyntheticCompactionSupported([]byte(triggerPayload), opts) {
		t.Fatal("trigger payload not detected")
	}
	fake := &fakeCompactionExecutor{}
	result, err := ExecuteSyntheticCompactionStream(context.Background(), fake, nil, cliproxyexecutor.Request{Model: "claude-fable-5-1", Payload: []byte(triggerPayload)}, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gjson.GetBytes(fake.gotPayload, "tools").Exists() || gjson.GetBytes(fake.gotPayload, "stream").Bool() {
		t.Fatalf("summary request must be tool-free and non-streaming: %s", fake.gotPayload)
	}
	if strings.Contains(string(fake.gotPayload), "compaction_trigger") {
		t.Fatalf("summary request still carries the trigger: %s", fake.gotPayload)
	}
	var chunks [][]byte
	for chunk := range result.Chunks {
		chunks = append(chunks, chunk.Payload)
	}
	if len(chunks) != 5 {
		t.Fatalf("expected 5 SSE chunks, got %d", len(chunks))
	}
	last := string(chunks[4])
	if !strings.HasPrefix(last, "event: response.completed\ndata: ") {
		t.Fatalf("unexpected final chunk: %s", last)
	}
	completed := gjson.Parse(strings.TrimPrefix(last, "event: response.completed\ndata: "))
	output := completed.Get("response.output").Array()
	if len(output) != 1 || output[0].Get("type").String() != "compaction" {
		t.Fatalf("expected exactly one compaction item, got %s", completed.Get("response.output").Raw)
	}
	summary, ok := DecodeSyntheticCompaction(output[0].Get("encrypted_content").String())
	if !ok || !strings.Contains(summary, "DONE: read a.go") {
		t.Fatalf("summary not recoverable: %q %v", summary, ok)
	}

	replay := `{"model":"claude-fable-5-1","input":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"sys"}]},` + output[0].Raw + `,{"type":"compaction","id":"cmp_foreign","encrypted_content":"gAAAAABforeign"},{"type":"message","role":"user","content":[{"type":"input_text","text":"next"}]}]}`
	rewritten := RewriteSyntheticCompactionInput([]byte(replay))
	items := gjson.GetBytes(rewritten, "input").Array()
	if len(items) != 3 {
		t.Fatalf("expected 3 items after rewrite (foreign dropped), got %d: %s", len(items), rewritten)
	}
	if items[1].Get("type").String() != "message" || items[1].Get("role").String() != "user" || !strings.Contains(items[1].Get("content.0.text").String(), "DONE: read a.go") {
		t.Fatalf("compaction not restored as user message: %s", items[1].Raw)
	}
	if !InputHasSyntheticCompaction([]byte(gjson.Get(replay, "input").Raw)) {
		t.Fatal("InputHasSyntheticCompaction should detect the proxy item")
	}
}

func TestSyntheticCompactionNotSupportedForCompactAltOrOtherFormats(t *testing.T) {
	t.Parallel()
	if SyntheticCompactionSupported([]byte(triggerPayload), cliproxyexecutor.Options{Alt: "responses/compact", SourceFormat: sdktranslator.FormatOpenAIResponse}) {
		t.Fatal("compact alt must keep the native path")
	}
	if SyntheticCompactionSupported([]byte(triggerPayload), cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}) {
		t.Fatal("non-Responses sources must be ignored")
	}
	if SyntheticCompactionSupported([]byte(`{"input":[{"type":"message"}]}`), cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}) {
		t.Fatal("payload without trigger must be ignored")
	}
}
