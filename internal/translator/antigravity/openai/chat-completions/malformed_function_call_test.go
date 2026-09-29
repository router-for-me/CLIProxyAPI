package chat_completions

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func bashRequest() []byte {
	return []byte(`{"tools":[{"type":"function","function":{"name":"bash"}},{"type":"function","function":{"name":"shell"}}]}`)
}

func declaredBash() map[string]string {
	return map[string]string{"bash": "bash", "shell": "shell"}
}

func TestRecoverCallsParsesGeminiTextShape(t *testing.T) {
	text := "thought\nLet's check `airun-cursor:0` pane (read-only!) to see what `cursor` is currently doing.call:default_api:bash{command:tmux capture-pane -p -t airun-cursor:0 -S -65 2>/dev/null | tail -55 | sed 's/^/  /',timeout:10}"
	calls, ok := recoverCalls(text, declaredBash(), true)
	if !ok || len(calls) != 1 {
		t.Fatalf("recover = %v, %v", calls, ok)
	}
	if calls[0].Name != "bash" {
		t.Fatalf("name = %q", calls[0].Name)
	}
	args := decodeArgs(t, calls[0].Arguments)
	if !strings.Contains(args["command"].(string), "tmux capture-pane") {
		t.Fatalf("command = %#v", args["command"])
	}
	if numberString(args["timeout"]) != "10" {
		t.Fatalf("timeout = %#v (%T)", args["timeout"], args["timeout"])
	}
	preamble := strings.TrimRight(text[:calls[0].Start], " \t\r\n")
	if !strings.HasSuffix(preamble, "doing.") || strings.Contains(preamble, "call:default_api:") {
		t.Fatalf("preamble = %q", preamble)
	}

	if _, ok := recoverCalls(text, declaredBash(), false); ok {
		t.Fatal("a normal stop must not recover a call glued to a preamble")
	}
}

func TestRecoverCallsKeepsCommaInsideUnquotedValue(t *testing.T) {
	calls, ok := recoverCalls("call:default_api:bash{command:echo hello, world,timeout:5}", declaredBash(), false)
	if !ok || len(calls) != 1 {
		t.Fatalf("recover = %v, %v", calls, ok)
	}
	args := decodeArgs(t, calls[0].Arguments)
	if args["command"] != "echo hello, world" {
		t.Fatalf("command = %#v", args["command"])
	}
	if numberString(args["timeout"]) != "5" {
		t.Fatalf("timeout = %#v (%T)", args["timeout"], args["timeout"])
	}
}

func TestRecoverCallsParsesQuotedAndJSONValues(t *testing.T) {
	quoted, ok := recoverCalls(`call:default_api:bash{command:"echo a, b",timeout:1}`, declaredBash(), false)
	if !ok {
		t.Fatal("quoted call did not parse")
	}
	if decodeArgs(t, quoted[0].Arguments)["command"] != "echo a, b" {
		t.Fatalf("quoted command = %#v", decodeArgs(t, quoted[0].Arguments)["command"])
	}

	shell, ok := recoverCalls(`Malformed function call: call:default_api:shell{command:["pwd"],workdir:"/tmp"}`, declaredBash(), false)
	if !ok || shell[0].Name != "shell" {
		t.Fatalf("finishMessage form = %#v, %v", shell, ok)
	}
	args := decodeArgs(t, shell[0].Arguments)
	command, ok := args["command"].([]any)
	if !ok || len(command) != 1 || command[0] != "pwd" {
		t.Fatalf("command = %#v", args["command"])
	}
	if args["workdir"] != "/tmp" {
		t.Fatalf("workdir = %#v", args["workdir"])
	}
}

func TestRecoverCallsRejectsUnsafeText(t *testing.T) {
	cases := []string{
		"I will call: the function later",
		"call:default_api:bash{command:pwd} and then explain",
		"see call:default_api:bash{command:pwd} then call:default_api:bash{command:ls}",
		"call:default_api:rm{command:rm -rf /}",
		"call:default_api:bash{command:pwd,command:ls}",
		"call:default_api:bash{command:ls",
	}
	for _, text := range cases {
		if _, ok := recoverCalls(text, declaredBash(), true); ok {
			t.Fatalf("recovered unsafe text %q", text)
		}
	}
	if _, ok := recoverCalls("call:default_api:bash{command:pwd}", map[string]string{}, false); ok {
		t.Fatal("recovered a call when the client declared no tools")
	}
}

func TestMalformedStreamPromotesDeclaredSuffixAndHoldsRawCall(t *testing.T) {
	ctx := context.Background()
	var param any
	preamble := "Let's check the pane."
	call := "call:default_api:bash{command:tmux capture-pane -p -t airun-cursor:0,timeout:10}"
	first := []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"` + preamble + call + `"}]}}]}}`)
	chunk := ConvertAntigravityResponseToOpenAI(ctx, "gemini-3.8-flash", bashRequest(), nil, first, &param)
	if got := joinedContent(chunk); got != preamble {
		t.Fatalf("streamed content = %q, want the preamble only", got)
	}
	if gjson.GetBytes(chunk[0], "choices.0.delta.tool_calls").Exists() && gjson.GetBytes(chunk[0], "choices.0.delta.tool_calls").IsArray() {
		t.Fatalf("tool call emitted before the terminal chunk: %s", chunk[0])
	}

	final := []byte(`{"response":{"candidates":[{"finishReason":"MALFORMED_FUNCTION_CALL","finishMessage":"Malformed function call: ` + call + `"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}}`)
	done := ConvertAntigravityResponseToOpenAI(ctx, "gemini-3.8-flash", bashRequest(), nil, final, &param)
	if len(done) != 1 {
		t.Fatalf("terminal chunks = %d", len(done))
	}
	if got := gjson.GetBytes(done[0], "choices.0.finish_reason").String(); got != "tool_calls" {
		t.Fatalf("finish_reason = %q", got)
	}
	if got := gjson.GetBytes(done[0], "choices.0.native_finish_reason").String(); got != "malformed_function_call" {
		t.Fatalf("native_finish_reason = %q", got)
	}
	if got := gjson.GetBytes(done[0], "choices.0.delta.tool_calls.0.function.name").String(); got != "bash" {
		t.Fatalf("tool name = %q, chunk %s", got, done[0])
	}
	args := decodeArgs(t, gjson.GetBytes(done[0], "choices.0.delta.tool_calls.0.function.arguments").String())
	if !strings.Contains(args["command"].(string), "tmux capture-pane") {
		t.Fatalf("arguments = %#v", args)
	}
	if strings.Contains(joinedContent(done), "call:default_api:") {
		t.Fatalf("terminal chunk leaked the raw call: %s", done[0])
	}
}

func TestMalformedStreamKeepsFailureVisibleWhenCallDoesNotParse(t *testing.T) {
	var param any
	chunk := []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"call:default_api:bash{command:ls"}]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}}`)
	out := ConvertAntigravityResponseToOpenAI(context.Background(), "model", bashRequest(), nil, chunk, &param)
	if got := gjson.GetBytes(out[0], "choices.0.finish_reason").String(); got != "malformed_function_call" {
		t.Fatalf("finish_reason = %q, chunk %s", got, out[0])
	}
	if !strings.Contains(joinedContent(out), "call:default_api:bash{command:ls") {
		t.Fatalf("unparsed call text was dropped: %s", out[0])
	}
	if gjson.GetBytes(out[0], "choices.0.delta.tool_calls").IsArray() {
		t.Fatalf("unparsed call became a tool call: %s", out[0])
	}
}

func TestStopWithPreambleStaysText(t *testing.T) {
	var param any
	text := "doing.call:default_api:bash{command:pwd,timeout:1}"
	chunk := []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"` + text + `"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}}`)
	out := ConvertAntigravityResponseToOpenAI(context.Background(), "model", bashRequest(), nil, chunk, &param)
	if got := gjson.GetBytes(out[0], "choices.0.finish_reason").String(); got != "stop" {
		t.Fatalf("finish_reason = %q", got)
	}
	if joinedContent(out) != text {
		t.Fatalf("content = %q", joinedContent(out))
	}
}

func TestStopOfBareCallBecomesToolCall(t *testing.T) {
	var param any
	chunk := []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"call:default_api:bash{command:pwd}"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}}`)
	out := ConvertAntigravityResponseToOpenAI(context.Background(), "model", bashRequest(), nil, chunk, &param)
	if got := gjson.GetBytes(out[0], "choices.0.finish_reason").String(); got != "tool_calls" {
		t.Fatalf("finish_reason = %q, chunk %s", got, out[0])
	}
	if joinedContent(out) != "" {
		t.Fatalf("bare call leaked into content %q", joinedContent(out))
	}
	if got := gjson.GetBytes(out[0], "choices.0.delta.tool_calls.0.function.name").String(); got != "bash" {
		t.Fatalf("tool name = %q", got)
	}
}

func TestTrailingPrefixIsNotDropped(t *testing.T) {
	var param any
	first := ConvertAntigravityResponseToOpenAI(context.Background(), "model", bashRequest(), nil, []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"Hello c"}]}}]}}`), &param)
	if joinedContent(first) != "Hello" {
		t.Fatalf("held too much or too little: %q", joinedContent(first))
	}
	final := ConvertAntigravityResponseToOpenAI(context.Background(), "model", bashRequest(), nil, []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"at"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}}`), &param)
	if joinedContent(first)+joinedContent(final) != "Hello cat" {
		t.Fatalf("joined = %q + %q", joinedContent(first), joinedContent(final))
	}
	if got := gjson.GetBytes(final[0], "choices.0.finish_reason").String(); got != "stop" {
		t.Fatalf("finish_reason = %q", got)
	}
}

func TestTextBeforeRealFunctionCallIsStillDelivered(t *testing.T) {
	var param any
	first := ConvertAntigravityResponseToOpenAI(context.Background(), "model", bashRequest(), nil, []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"Running."},{"functionCall":{"name":"bash","args":{"command":"pwd"}}}]}}]}}`), &param)
	final := ConvertAntigravityResponseToOpenAI(context.Background(), "model", bashRequest(), nil, []byte(`{"response":{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}}`), &param)
	if joinedContent(first)+joinedContent(final) != "Running." {
		t.Fatalf("content = %q %q", joinedContent(first), joinedContent(final))
	}
	if got := gjson.GetBytes(final[0], "choices.0.finish_reason").String(); got != "tool_calls" {
		t.Fatalf("finish_reason = %q, final %s", got, final[0])
	}
	if got := gjson.GetBytes(first[0], "choices.0.delta.tool_calls.0.function.name").String(); got != "bash" {
		t.Fatalf("tool name = %q, first %s", got, first[0])
	}
}

func TestVisibleTextStillStreamsBeforeFinish(t *testing.T) {
	var param any
	first := ConvertAntigravityResponseToOpenAI(context.Background(), "model", nil, nil, []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"Hello"}]}}]}}`), &param)
	if joinedContent(first) != "Hello" {
		t.Fatalf("first chunk content = %q", joinedContent(first))
	}
	final := ConvertAntigravityResponseToOpenAI(context.Background(), "model", nil, nil, []byte(`{"response":{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}}`), &param)
	if got := gjson.GetBytes(final[0], "choices.0.finish_reason").String(); got != "stop" {
		t.Fatalf("finish_reason = %q", got)
	}
}

func TestMalformedFinishMessagePromotesOnDone(t *testing.T) {
	var param any
	payload := map[string]any{
		"response": map[string]any{
			"candidates": []any{
				map[string]any{
					"finishReason":  "MALFORMED_FUNCTION_CALL",
					"finishMessage": `Malformed function call: call:default_api:shell{command:["pwd"],workdir:"/tmp"}`,
				},
			},
		},
	}
	chunk, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	mid := ConvertAntigravityResponseToOpenAI(context.Background(), "model", bashRequest(), nil, chunk, &param)
	if len(mid) != 1 || gjson.GetBytes(mid[0], "choices.0.finish_reason").String() != "" {
		t.Fatalf("usage-less malformed chunk finalized early: %s", mid)
	}
	done := ConvertAntigravityResponseToOpenAI(context.Background(), "model", bashRequest(), nil, []byte("[DONE]"), &param)
	if got := gjson.GetBytes(done[0], "choices.0.finish_reason").String(); got != "tool_calls" {
		t.Fatalf("finish_reason = %q, chunk %s", got, done[0])
	}
	if got := gjson.GetBytes(done[0], "choices.0.delta.tool_calls.0.function.name").String(); got != "shell" {
		t.Fatalf("tool name = %q, chunk %s", got, done[0])
	}
}

func TestNonStreamMalformedFunctionCall(t *testing.T) {
	raw := []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"call:default_api:bash{command:pwd}"}]},"finishReason":"MALFORMED_FUNCTION_CALL","finishMessage":"Malformed function call: call:default_api:bash{command:pwd}"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2},"modelVersion":"gemini-3.8-flash"}}`)
	var param any
	out := ConvertAntigravityResponseToOpenAINonStream(context.Background(), "gemini-3.8-flash", bashRequest(), nil, raw, &param)
	if got := gjson.GetBytes(out, "choices.0.finish_reason").String(); got != "tool_calls" {
		t.Fatalf("finish_reason = %q, body %s", got, out)
	}
	if gjson.GetBytes(out, "choices.0.message.content").String() != "" {
		t.Fatalf("content = %s", gjson.GetBytes(out, "choices.0.message.content").Raw)
	}
	if got := gjson.GetBytes(out, "choices.0.message.tool_calls.0.function.name").String(); got != "bash" {
		t.Fatalf("tool name = %q, body %s", got, out)
	}

	broken := []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"not a call"}]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}}`)
	out = ConvertAntigravityResponseToOpenAINonStream(context.Background(), "model", bashRequest(), nil, broken, &param)
	if got := gjson.GetBytes(out, "choices.0.finish_reason").String(); got != "malformed_function_call" {
		t.Fatalf("unparsed finish_reason = %q, body %s", got, out)
	}
}

func TestBlockedFinishDoesNotPromoteCompleteCall(t *testing.T) {
	for _, reason := range []string{"SAFETY", "RECITATION", "BLOCKLIST"} {
		t.Run(reason, func(t *testing.T) {
			var param any
			out := ConvertAntigravityResponseToOpenAI(context.Background(), "model", bashRequest(), nil, antigravityChunk(t, "call:default_api:bash{command:pwd}", reason, "", true), &param)
			if got := gjson.GetBytes(out[0], "choices.0.finish_reason").String(); got != strings.ToLower(reason) {
				t.Fatalf("finish_reason = %q, chunk %s", got, out[0])
			}
			if gjson.GetBytes(out[0], "choices.0.delta.tool_calls").IsArray() {
				t.Fatalf("%s promoted a tool call: %s", reason, out[0])
			}
			if joinedContent(out) != "call:default_api:bash{command:pwd}" {
				t.Fatalf("content = %q", joinedContent(out))
			}
		})
	}
}

func TestFinishMessageDoesNotOverrideRejectedText(t *testing.T) {
	message := "Malformed function call: call:default_api:bash{command:pwd}"
	cases := []struct {
		name   string
		text   string
		finish string
	}{
		{name: "trailing prose", text: "call:default_api:bash{command:pwd} done explaining", finish: "MALFORMED_FUNCTION_CALL"},
		{name: "undeclared tool", text: "call:default_api:rm{command:rm -rf /}", finish: "MALFORMED_FUNCTION_CALL"},
		{name: "stop preamble", text: "doing.call:default_api:bash{command:pwd}", finish: "STOP"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var param any
			out := ConvertAntigravityResponseToOpenAI(context.Background(), "model", bashRequest(), nil, antigravityChunk(t, tc.text, tc.finish, message, true), &param)
			if gjson.GetBytes(out[0], "choices.0.delta.tool_calls").IsArray() {
				t.Fatalf("finishMessage promoted a rejected call: %s", out[0])
			}
			if !strings.Contains(joinedContent(out), tc.text) && joinedContent(out) != tc.text {
				t.Fatalf("content = %q", joinedContent(out))
			}
		})
	}
}

func TestSplitSecondCallDoesNotLeakFirst(t *testing.T) {
	second := `call:default_api:shell{command:["ls"]}`
	for _, prefix := range []string{"c", "ca", "cal", "call"} {
		t.Run(prefix, func(t *testing.T) {
			var param any
			first := ConvertAntigravityResponseToOpenAI(context.Background(), "model", bashRequest(), nil, antigravityChunk(t, "call:default_api:bash{command:pwd} "+prefix, "", "", false), &param)
			if strings.Contains(joinedContent(first), "call:default_api:bash") {
				t.Fatalf("first call leaked: %s", first[0])
			}
			rest := second[len(prefix):]
			mid := ConvertAntigravityResponseToOpenAI(context.Background(), "model", bashRequest(), nil, antigravityChunk(t, rest, "", "", false), &param)
			final := ConvertAntigravityResponseToOpenAI(context.Background(), "model", bashRequest(), nil, antigravityChunk(t, "", "MALFORMED_FUNCTION_CALL", "", true), &param)
			body := append(append(append([][]byte{}, first...), mid...), final...)
			if strings.Contains(joinedContent(body), "call:default_api:") {
				t.Fatalf("raw call leaked: %s", joinedContent(body))
			}
			names := []string{
				gjson.GetBytes(final[0], "choices.0.delta.tool_calls.0.function.name").String(),
				gjson.GetBytes(final[0], "choices.0.delta.tool_calls.1.function.name").String(),
			}
			if names[0] != "bash" || names[1] != "shell" {
				t.Fatalf("tools = %#v, chunk %s", names, final[0])
			}
		})
	}
}

func TestDiagnosticPrefixIsHeldUntilFinish(t *testing.T) {
	var param any
	text := "Malformed function call: call:default_api:bash{command:pwd}"
	first := ConvertAntigravityResponseToOpenAI(context.Background(), "model", bashRequest(), nil, antigravityChunk(t, text, "", "", false), &param)
	if joinedContent(first) != "" {
		t.Fatalf("prefix streamed early: %q", joinedContent(first))
	}
	final := ConvertAntigravityResponseToOpenAI(context.Background(), "model", bashRequest(), nil, antigravityChunk(t, "", "STOP", "", true), &param)
	if got := gjson.GetBytes(final[0], "choices.0.finish_reason").String(); got != "tool_calls" {
		t.Fatalf("finish_reason = %q, chunk %s", got, final[0])
	}
	if joinedContent(first)+joinedContent(final) != "" {
		t.Fatalf("content = %q", joinedContent(first)+joinedContent(final))
	}
}

func TestCompleteCallWithMaxTokensStaysText(t *testing.T) {
	var param any
	text := "call:default_api:bash{command:pwd}"
	out := ConvertAntigravityResponseToOpenAI(context.Background(), "model", bashRequest(), nil, antigravityChunk(t, text, "MAX_TOKENS", "", true), &param)
	if got := gjson.GetBytes(out[0], "choices.0.finish_reason").String(); got != "max_tokens" {
		t.Fatalf("finish_reason = %q", got)
	}
	if gjson.GetBytes(out[0], "choices.0.delta.tool_calls").IsArray() {
		t.Fatalf("complete call was promoted: %s", out[0])
	}
	if joinedContent(out) != text {
		t.Fatalf("content = %q", joinedContent(out))
	}
}

func TestNonStreamStopShapes(t *testing.T) {
	cases := []struct {
		name    string
		text    string
		promote bool
	}{
		{name: "bare", text: "call:default_api:bash{command:pwd}", promote: true},
		{name: "preamble", text: "doing.call:default_api:bash{command:pwd}", promote: false},
		{name: "trailing", text: "call:default_api:bash{command:pwd} done explaining", promote: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var param any
			raw := wrapResponse(t, antigravityChunk(t, tc.text, "STOP", "", true))
			out := ConvertAntigravityResponseToOpenAINonStream(context.Background(), "model", bashRequest(), nil, raw, &param)
			gotTool := gjson.GetBytes(out, "choices.0.message.tool_calls").IsArray() && len(gjson.GetBytes(out, "choices.0.message.tool_calls").Array()) > 0
			if gotTool != tc.promote {
				t.Fatalf("promote = %v, body %s", gotTool, out)
			}
			if !tc.promote && !strings.Contains(gjson.GetBytes(out, "choices.0.message.content").String(), tc.text) {
				t.Fatalf("content = %s", gjson.GetBytes(out, "choices.0.message.content").Raw)
			}
		})
	}
}

func TestNonStreamPromotesMalformedTextInEachCandidate(t *testing.T) {
	raw := []byte(`{"response":{"candidates":[{"index":0,"content":{"parts":[{"text":"ordinary answer"}]},"finishReason":"STOP"},{"index":1,"content":{"parts":[{"text":"call:default_api:bash{command:pwd}"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3}}}`)
	var param any
	out := ConvertAntigravityResponseToOpenAINonStream(context.Background(), "model", bashRequest(), nil, raw, &param)
	if got := gjson.GetBytes(out, "choices.0.message.content").String(); got != "ordinary answer" {
		t.Fatalf("choice 0 content = %q, body %s", got, out)
	}
	if got := gjson.GetBytes(out, "choices.0.finish_reason").String(); got != "stop" {
		t.Fatalf("choice 0 finish_reason = %q, body %s", got, out)
	}
	if got := gjson.GetBytes(out, "choices.1.message.tool_calls.0.function.name").String(); got != "bash" {
		t.Fatalf("choice 1 tool = %q, body %s", got, out)
	}
	if got := gjson.GetBytes(out, "choices.1.finish_reason").String(); got != "tool_calls" {
		t.Fatalf("choice 1 finish_reason = %q, body %s", got, out)
	}
	if content := gjson.GetBytes(out, "choices.1.message.content"); content.Type != gjson.Null {
		t.Fatalf("choice 1 content = %s, body %s", content.Raw, out)
	}
}

func TestNonStreamNativeToolCallDoesNotSkipLaterCandidate(t *testing.T) {
	raw := []byte(`{"response":{"candidates":[{"index":0,"content":{"parts":[{"functionCall":{"name":"bash","args":{"command":"echo native"}}}]},"finishReason":"STOP"},{"index":1,"content":{"parts":[{"text":"call:default_api:bash{command:pwd}"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3}}}`)
	var param any
	out := ConvertAntigravityResponseToOpenAINonStream(context.Background(), "model", bashRequest(), nil, raw, &param)
	if got := gjson.GetBytes(out, "choices.0.message.tool_calls.0.function.name").String(); got != "bash" {
		t.Fatalf("choice 0 native tool = %q, body %s", got, out)
	}
	if got := gjson.GetBytes(out, "choices.1.message.tool_calls.0.function.name").String(); got != "bash" {
		t.Fatalf("choice 1 recovered tool = %q, body %s", got, out)
	}
	if got := gjson.GetBytes(out, "choices.1.finish_reason").String(); got != "tool_calls" {
		t.Fatalf("choice 1 finish_reason = %q, body %s", got, out)
	}
	if content := gjson.GetBytes(out, "choices.1.message.content"); content.Type != gjson.Null {
		t.Fatalf("choice 1 content = %s, body %s", content.Raw, out)
	}
}

func TestRecoverCallsRejectsQuotedBackslash(t *testing.T) {
	if _, ok := recoverCalls(`call:default_api:bash{command:"printf '\n'"}`, declaredBash(), true); ok {
		t.Fatal("quoted backslash was promoted")
	}
}

func TestRecoverCallsPreservesNestedJSONInteger(t *testing.T) {
	calls, ok := recoverCalls(`call:default_api:bash{payload:{"id":9007199254740993}}`, declaredBash(), false)
	if !ok || len(calls) != 1 {
		t.Fatalf("recover = %#v, %v", calls, ok)
	}
	if !strings.Contains(calls[0].Arguments, "9007199254740993") {
		t.Fatalf("arguments = %s", calls[0].Arguments)
	}
	args := decodeArgs(t, calls[0].Arguments)
	payload, ok := args["payload"].(map[string]any)
	if !ok {
		t.Fatalf("payload = %#v", args["payload"])
	}
	if numberString(payload["id"]) != "9007199254740993" {
		t.Fatalf("id = %#v (%T)", payload["id"], payload["id"])
	}
}

func TestRecoverCallsPreservesTopLevelNumberLiteral(t *testing.T) {
	for _, literal := range []string{"0.1234567890123456789", "9223372036854775808"} {
		t.Run(literal, func(t *testing.T) {
			calls, ok := recoverCalls("call:default_api:bash{value:"+literal+"}", declaredBash(), false)
			if !ok || len(calls) != 1 {
				t.Fatalf("recover = %#v, %v", calls, ok)
			}
			if !strings.Contains(calls[0].Arguments, literal) {
				t.Fatalf("arguments = %s", calls[0].Arguments)
			}
			if numberString(decodeArgs(t, calls[0].Arguments)["value"]) != literal {
				t.Fatalf("value = %#v", decodeArgs(t, calls[0].Arguments)["value"])
			}
		})
	}
}

func TestRecoverCallsRejectsCommaBareKey(t *testing.T) {
	if _, ok := recoverCalls(`call:default_api:bash{command:echo a,b:c}`, declaredBash(), true); ok {
		t.Fatal("comma-separated bare key was promoted")
	}
}

func TestEmptyFinishReasonDoesNotPromote(t *testing.T) {
	text := "call:default_api:bash{command:pwd}"
	var param any
	first := ConvertAntigravityResponseToOpenAI(context.Background(), "model", bashRequest(), nil, antigravityChunk(t, text, "", "", false), &param)
	done := ConvertAntigravityResponseToOpenAI(context.Background(), "model", bashRequest(), nil, []byte("[DONE]"), &param)
	if len(done) != 1 {
		t.Fatalf("done chunks = %d", len(done))
	}
	if got := gjson.GetBytes(done[0], "choices.0.finish_reason").String(); got != "stop" {
		t.Fatalf("finish_reason = %q, chunk %s", got, done[0])
	}
	if gjson.GetBytes(done[0], "choices.0.delta.tool_calls").IsArray() {
		t.Fatalf("empty finish promoted a tool call: %s", done[0])
	}
	if joinedContent(first)+joinedContent(done) != text {
		t.Fatalf("content = %q", joinedContent(first)+joinedContent(done))
	}

	var nonStream any
	out := ConvertAntigravityResponseToOpenAINonStream(context.Background(), "model", bashRequest(), nil, antigravityChunk(t, text, "", "", true), &nonStream)
	if gjson.GetBytes(out, "choices.0.message.tool_calls").IsArray() {
		t.Fatalf("non-stream promoted: %s", out)
	}
	if gjson.GetBytes(out, "choices.0.message.content").String() != text {
		t.Fatalf("non-stream content = %s", gjson.GetBytes(out, "choices.0.message.content").Raw)
	}
	if gjson.GetBytes(out, "choices.0.finish_reason").Type != gjson.Null {
		t.Fatalf("non-stream finish_reason = %s", gjson.GetBytes(out, "choices.0.finish_reason").Raw)
	}
}

func TestSplitDiagnosticPrefixIsHeld(t *testing.T) {
	var param any
	first := ConvertAntigravityResponseToOpenAI(context.Background(), "model", bashRequest(), nil, antigravityChunk(t, "Malformed function", "", "", false), &param)
	second := ConvertAntigravityResponseToOpenAI(context.Background(), "model", bashRequest(), nil, antigravityChunk(t, " call: call:default_api:bash{command:pwd}", "", "", false), &param)
	final := ConvertAntigravityResponseToOpenAI(context.Background(), "model", bashRequest(), nil, antigravityChunk(t, "", "STOP", "", true), &param)
	content := joinedContent(first) + joinedContent(second) + joinedContent(final)
	if strings.Contains(content, "Malformed function") {
		t.Fatalf("diagnostic prefix leaked: %q", content)
	}
	if got := gjson.GetBytes(final[0], "choices.0.finish_reason").String(); got != "tool_calls" {
		t.Fatalf("finish_reason = %q, chunk %s", got, final[0])
	}
	if got := gjson.GetBytes(final[0], "choices.0.delta.tool_calls.0.function.name").String(); got != "bash" {
		t.Fatalf("tool name = %q, chunk %s", got, final[0])
	}
}

func TestCompleteDiagnosticPrefixIsHeld(t *testing.T) {
	for _, second := range []string{
		"call:default_api:bash{command:pwd}",
		" call:default_api:bash{command:pwd}",
	} {
		t.Run(second, func(t *testing.T) {
			var param any
			first := ConvertAntigravityResponseToOpenAI(context.Background(), "model", bashRequest(), nil, antigravityChunk(t, "Malformed function call:", "", "", false), &param)
			mid := ConvertAntigravityResponseToOpenAI(context.Background(), "model", bashRequest(), nil, antigravityChunk(t, second, "", "", false), &param)
			final := ConvertAntigravityResponseToOpenAI(context.Background(), "model", bashRequest(), nil, antigravityChunk(t, "", "STOP", "", true), &param)
			content := joinedContent(first) + joinedContent(mid) + joinedContent(final)
			if strings.Contains(content, "Malformed function") {
				t.Fatalf("diagnostic prefix leaked: %q", content)
			}
			if got := gjson.GetBytes(final[0], "choices.0.finish_reason").String(); got != "tool_calls" {
				t.Fatalf("finish_reason = %q, chunk %s", got, final[0])
			}
			if got := gjson.GetBytes(final[0], "choices.0.delta.tool_calls.0.function.name").String(); got != "bash" {
				t.Fatalf("tool name = %q, chunk %s", got, final[0])
			}
		})
	}
}

func TestPreambleArrivesBeforeFinish(t *testing.T) {
	text := "Before\nMalformed function call: call:default_api:bash{command:pwd}"
	var param any
	first := ConvertAntigravityResponseToOpenAI(context.Background(), "model", bashRequest(), nil, antigravityChunk(t, text, "", "", false), &param)
	if joinedContent(first) != "Before" {
		t.Fatalf("early content = %q", joinedContent(first))
	}
	final := ConvertAntigravityResponseToOpenAI(context.Background(), "model", bashRequest(), nil, antigravityChunk(t, "", "MALFORMED_FUNCTION_CALL", "", true), &param)
	if joinedContent(first)+joinedContent(final) != "Before" {
		t.Fatalf("content = %q", joinedContent(first)+joinedContent(final))
	}
	if got := gjson.GetBytes(final[0], "choices.0.delta.tool_calls.0.function.name").String(); got != "bash" {
		t.Fatalf("tool name = %q, chunk %s", got, final[0])
	}
}

func TestDiagnosticPrefixStaysOutOfPreamble(t *testing.T) {
	text := "Before\nMalformed function call: call:default_api:bash{command:pwd}"
	var param any
	out := ConvertAntigravityResponseToOpenAI(context.Background(), "model", bashRequest(), nil, antigravityChunk(t, text, "MALFORMED_FUNCTION_CALL", "", true), &param)
	if joinedContent(out) != "Before" {
		t.Fatalf("stream content = %q", joinedContent(out))
	}
	if got := gjson.GetBytes(out[0], "choices.0.delta.tool_calls.0.function.name").String(); got != "bash" {
		t.Fatalf("stream tool = %q, chunk %s", got, out[0])
	}

	var nonStream any
	body := ConvertAntigravityResponseToOpenAINonStream(context.Background(), "model", bashRequest(), nil, antigravityChunk(t, text, "MALFORMED_FUNCTION_CALL", "", true), &nonStream)
	if gjson.GetBytes(body, "choices.0.message.content").String() != "Before" {
		t.Fatalf("non-stream content = %s", gjson.GetBytes(body, "choices.0.message.content").Raw)
	}
	if got := gjson.GetBytes(body, "choices.0.message.tool_calls.0.function.name").String(); got != "bash" {
		t.Fatalf("non-stream tool = %q, body %s", got, body)
	}
}

func TestMaxTokensDoesNotPromotePartialCall(t *testing.T) {
	var param any
	chunk := []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"call:default_api:bash{command:ls"}]},"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}}`)
	out := ConvertAntigravityResponseToOpenAI(context.Background(), "model", bashRequest(), nil, chunk, &param)
	if got := gjson.GetBytes(out[0], "choices.0.finish_reason").String(); got != "max_tokens" {
		t.Fatalf("finish_reason = %q", got)
	}
	if gjson.GetBytes(out[0], "choices.0.delta.tool_calls").IsArray() {
		t.Fatalf("truncated call was promoted: %s", out[0])
	}
}

func TestRecoveryRespectsToolChoice(t *testing.T) {
	for _, tc := range []struct {
		name    string
		choice  string
		promote bool
	}{
		{name: "default", promote: true},
		{name: "auto", choice: `"auto"`, promote: true},
		{name: "required", choice: `"required"`, promote: true},
		{name: "none", choice: `"none"`},
		{name: "normalized none", choice: `" NONE "`},
		{name: "object none", choice: `{"type":"none"}`},
		{name: "forced same", choice: `{"type":"function","function":{"name":"bash"}}`, promote: true},
		{name: "forced other", choice: `{"type":"function","function":{"name":"shell"}}`},
		{name: "forced undeclared", choice: `{"type":"function","function":{"name":"missing"}}`},
		{name: "forced missing name", choice: `{"type":"function"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := bashRequest()
			if tc.choice != "" {
				request = []byte(strings.TrimSuffix(string(request), "}") + `,"tool_choice":` + tc.choice + `}`)
			}
			call := "call:default_api:bash{command:pwd}"
			for _, source := range []struct{ name, text, finish, message string }{
				{"stop text", call, "STOP", ""},
				{"malformed text", call, "MALFORMED_FUNCTION_CALL", ""},
				{"finish message", "", "MALFORMED_FUNCTION_CALL", malformedFunctionCallPrefix + " " + call},
			} {
				t.Run(source.name, func(t *testing.T) {
					wantContent, wantFinish := source.text, strings.ToLower(source.finish)
					if tc.promote {
						wantContent, wantFinish = "", "tool_calls"
					}
					check := func(out []byte, messagePath, content string) {
						t.Helper()
						calls := gjson.GetBytes(out, messagePath+".tool_calls").Array()
						if (len(calls) > 0) != tc.promote || content != wantContent {
							t.Fatalf("unexpected recovery or content: %s (content %q)", out, content)
						}
						if tc.promote && (len(calls) != 1 || calls[0].Get("function.name").String() != "bash" || calls[0].Get("function.arguments").String() != `{"command":"pwd"}`) {
							t.Fatalf("unexpected tool call: %s", out)
						}
						if got := gjson.GetBytes(out, "choices.0.finish_reason").String(); got != wantFinish {
							t.Fatalf("finish_reason = %q, want %q: %s", got, wantFinish, out)
						}
					}
					var nonStream any
					body := ConvertAntigravityResponseToOpenAINonStream(context.Background(), "model", request, nil, antigravityChunk(t, source.text, source.finish, source.message, true), &nonStream)
					check(body, "choices.0.message", gjson.GetBytes(body, "choices.0.message.content").String())
					for _, withUsage := range []bool{true, false} {
						var param any
						chunks := ConvertAntigravityResponseToOpenAI(context.Background(), "model", request, nil, antigravityChunk(t, source.text, source.finish, source.message, withUsage), &param)
						chunks = append(chunks, ConvertAntigravityResponseToOpenAI(context.Background(), "model", request, nil, []byte("[DONE]"), &param)...)
						check(chunks[len(chunks)-1], "choices.0.delta", joinedContent(chunks))
					}
				})
			}
		})
	}
}

func TestRecoveryToolChoiceDoesNotFilterNativeCalls(t *testing.T) {
	request := []byte(`{"tools":[{"type":"function","function":{"name":"bash"}}],"tool_choice":"none"}`)
	raw := []byte(`{"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"bash","args":{"command":"pwd"}}}]},"finishReason":"STOP"}],"usageMetadata":{"totalTokenCount":2}}}`)
	var stream, nonStream any
	chunks := ConvertAntigravityResponseToOpenAI(context.Background(), "model", request, nil, raw, &stream)
	body := ConvertAntigravityResponseToOpenAINonStream(context.Background(), "model", request, nil, raw, &nonStream)
	if gjson.GetBytes(chunks[0], "choices.0.delta.tool_calls.0.function.name").String() != "bash" || gjson.GetBytes(body, "choices.0.message.tool_calls.0.function.name").String() != "bash" {
		t.Fatalf("native call was filtered: stream %s, non-stream %s", chunks, body)
	}
}

func TestRecoveryContentIndependentOfChunkBoundaries(t *testing.T) {
	call := "call:default_api:bash{command:pwd}"
	for _, tc := range []struct {
		name, text, finish, content string
		promote                     bool
	}{
		{"word separator", "I will check." + call, "MALFORMED_FUNCTION_CALL", "I will check.", true},
		{"diagnostic after preamble", "Before\nMalformed function call: " + call, "MALFORMED_FUNCTION_CALL", "Before", true},
		{"internal whitespace", "Before\n\n  check.\t\nMalformed function call: " + call, "MALFORMED_FUNCTION_CALL", "Before\n\n  check.", true},
		{"bare call", call, "STOP", "", true},
		{"diagnostic only", malformedFunctionCallPrefix + " " + call, "STOP", "", true},
		{"stop preamble", "Before\nMalformed function call: " + call, "STOP", "Before\nMalformed function call: " + call, false},
		{"blocked", "Before\nMalformed function call: " + call, "SAFETY", "Before\nMalformed function call: " + call, false},
		{"ordinary whitespace", "Hello \nworld\t\n", "STOP", "Hello \nworld\t\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantFinish := strings.ToLower(tc.finish)
			if tc.promote {
				wantFinish = "tool_calls"
			}
			var nonStream any
			body := ConvertAntigravityResponseToOpenAINonStream(context.Background(), "model", bashRequest(), nil, antigravityChunk(t, tc.text, tc.finish, "", true), &nonStream)
			if gjson.GetBytes(body, "choices.0.message.content").String() != tc.content || gjson.GetBytes(body, "choices.0.finish_reason").String() != wantFinish {
				t.Fatalf("non-stream = %s", body)
			}
			checkStream := func(t *testing.T, pieces []string, delayedFinish bool) {
				t.Helper()
				var param any
				var chunks [][]byte
				for i, piece := range pieces {
					finish := ""
					terminal := !delayedFinish && i == len(pieces)-1
					if terminal {
						finish = tc.finish
					}
					out := ConvertAntigravityResponseToOpenAI(context.Background(), "model", bashRequest(), nil, antigravityChunk(t, piece, finish, "", terminal), &param)
					if !terminal && gjson.GetBytes(out[0], "choices.0.delta.tool_calls").IsArray() {
						t.Fatalf("premature recovery: %s", out)
					}
					chunks = append(chunks, out...)
				}
				if delayedFinish {
					chunks = append(chunks, ConvertAntigravityResponseToOpenAI(context.Background(), "model", bashRequest(), nil, antigravityChunk(t, "", tc.finish, "", true), &param)...)
				}
				last := chunks[len(chunks)-1]
				if got := joinedContent(chunks); got != tc.content {
					t.Fatalf("content = %q, want %q", got, tc.content)
				}
				if got := gjson.GetBytes(last, "choices.0.finish_reason").String(); got != wantFinish {
					t.Fatalf("finish_reason = %q, want %q", got, wantFinish)
				}
				calls := gjson.GetBytes(last, "choices.0.delta.tool_calls").Array()
				if (len(calls) > 0) != tc.promote {
					t.Fatalf("unexpected tool calls: %s", last)
				}
				if tc.promote && (len(calls) != 1 || calls[0].Get("function.name").String() != "bash" || calls[0].Get("function.arguments").String() != `{"command":"pwd"}`) {
					t.Fatalf("unexpected recovered call: %s", last)
				}
			}
			for split := 0; split <= len(tc.text); split++ {
				for _, delayed := range []bool{false, true} {
					t.Run(fmt.Sprintf("split_%d_delayed_%t", split, delayed), func(t *testing.T) {
						checkStream(t, []string{tc.text[:split], tc.text[split:]}, delayed)
					})
				}
			}
			t.Run("character chunks", func(t *testing.T) {
				checkStream(t, strings.Split(tc.text, ""), true)
			})
		})
	}
}

func antigravityChunk(t *testing.T, text, finish, finishMessage string, withUsage bool) []byte {
	t.Helper()
	candidate := map[string]any{}
	if text != "" {
		candidate["content"] = map[string]any{
			"parts": []any{map[string]any{"text": text}},
		}
	}
	if finish != "" {
		candidate["finishReason"] = finish
	}
	if finishMessage != "" {
		candidate["finishMessage"] = finishMessage
	}
	response := map[string]any{"candidates": []any{candidate}}
	if withUsage {
		response["usageMetadata"] = map[string]any{
			"promptTokenCount": 1, "candidatesTokenCount": 1, "totalTokenCount": 2,
		}
	}
	raw, err := json.Marshal(map[string]any{"response": response})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func wrapResponse(t *testing.T, chunk []byte) []byte {
	t.Helper()
	response := gjson.GetBytes(chunk, "response")
	if !response.Exists() {
		t.Fatalf("chunk has no response: %s", chunk)
	}
	return chunk
}

func joinedContent(chunks [][]byte) string {
	var b strings.Builder
	for _, msg := range chunks {
		if content := gjson.GetBytes(msg, "choices.0.delta.content"); content.Type == gjson.String {
			b.WriteString(content.String())
		}
	}
	return b.String()
}

func numberString(value any) string {
	switch n := value.(type) {
	case json.Number:
		return n.String()
	default:
		return ""
	}
}

func decodeArgs(t *testing.T, raw string) map[string]any {
	t.Helper()
	var args map[string]any
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&args); err != nil {
		t.Fatalf("arguments %q: %v", raw, err)
	}
	return args
}
