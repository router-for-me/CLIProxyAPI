package responses

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestMoveLeadingCDToWorkdir(t *testing.T) {
	cases := []struct {
		name, tool, in   string
		wantCmd, wantDir string
		unchanged        bool
	}{
		{name: "basic", tool: "exec_command", in: `{"cmd":"cd /home/x/repo && rg -n foo src","yield_time_ms":1000}`, wantCmd: "rg -n foo src", wantDir: "/home/x/repo"},
		{name: "overrides workdir", tool: "exec_command", in: `{"cmd":"cd /a && ls","workdir":"/b"}`, wantCmd: "ls", wantDir: "/a"},
		{name: "quoted", tool: "shell_command", in: `{"command":"cd '/a b' && ls"}`, wantCmd: "ls", wantDir: "/a b"},
		{name: "repeated", tool: "exec_command", in: `{"cmd":"cd /a && cd /a/b && ls | head"}`, wantCmd: "ls | head", wantDir: "/a/b"},
		{name: "keeps html chars", tool: "exec_command", in: `{"cmd":"cd /a && echo '<x>' && true"}`, wantCmd: "echo '<x>' && true", wantDir: "/a"},
		{name: "relative", tool: "exec_command", in: `{"cmd":"cd sub && ls"}`, unchanged: true},
		{name: "tilde", tool: "exec_command", in: `{"cmd":"cd ~/x && ls"}`, unchanged: true},
		{name: "semicolon", tool: "exec_command", in: `{"cmd":"cd /a; ls"}`, unchanged: true},
		{name: "cd only", tool: "exec_command", in: `{"cmd":"cd /a && "}`, unchanged: true},
		{name: "variable", tool: "exec_command", in: `{"cmd":"cd \"$HOME\" && ls"}`, unchanged: true},
		{name: "other tool", tool: "write_stdin", in: `{"cmd":"cd /a && ls"}`, unchanged: true},
		{name: "invalid json", tool: "exec_command", in: `{"cmd":"cd /a && ls"`, unchanged: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := moveLeadingCDToWorkdir(tc.tool, tc.in)
			if tc.unchanged {
				if got != tc.in {
					t.Fatalf("expected unchanged, got %s", got)
				}
				return
			}
			var fields map[string]any
			if err := json.Unmarshal([]byte(got), &fields); err != nil {
				t.Fatalf("invalid json %s: %v", got, err)
			}
			cmdField := shellCommandField[tc.tool]
			if fields[cmdField] != tc.wantCmd || fields["workdir"] != tc.wantDir {
				t.Fatalf("got %s", got)
			}
			if tc.name == "basic" && fields["yield_time_ms"] != float64(1000) {
				t.Fatalf("lost field: %s", got)
			}
		})
	}
}

func cdWorkdirToolUseChunks() []string {
	return []string{
		`data: {"type":"message_start","message":{"id":"msg_cd","usage":{"input_tokens":1,"output_tokens":0}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_cd","name":"exec_command","input":{}}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"cmd\":\"cd /repo && "}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"rg -n foo\"}"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_stop"}`,
	}
}

func assertCDMovedToWorkdir(t *testing.T, where, args string) {
	t.Helper()
	root := ojson.Parse(args)
	if got := root.Get("cmd").String(); got != "rg -n foo" {
		t.Fatalf("%s cmd = %q, want %q (args %s)", where, got, "rg -n foo", args)
	}
	if got := root.Get("workdir").String(); got != "/repo" {
		t.Fatalf("%s workdir = %q, want /repo (args %s)", where, got, args)
	}
}

func TestConvertClaudeResponseToOpenAIResponses_MovesLeadingCDToWorkdir(t *testing.T) {
	var chunks [][]byte
	for _, chunk := range cdWorkdirToolUseChunks() {
		chunks = append(chunks, []byte(chunk))
	}
	var deltas strings.Builder
	var argsDone, itemDone, completed ojson.Result
	for _, output := range translateClaudeResponsesStreamThroughRegistry(chunks) {
		event, data := parseClaudeResponsesSSEEvent(t, output)
		switch event {
		case "response.function_call_arguments.delta":
			deltas.WriteString(data.Get("delta").String())
		case "response.function_call_arguments.done":
			argsDone = data
		case "response.output_item.done":
			itemDone = data
		case "response.completed":
			completed = data
		}
	}
	assertCDMovedToWorkdir(t, "deltas", deltas.String())
	assertCDMovedToWorkdir(t, "arguments.done", argsDone.Get("arguments").String())
	assertCDMovedToWorkdir(t, "output_item.done", itemDone.Get("item.arguments").String())
	assertCDMovedToWorkdir(t, "completed", completed.Get("response.output.0.arguments").String())
	if deltas.String() != argsDone.Get("arguments").String() {
		t.Fatalf("deltas %q disagree with arguments.done %q", deltas.String(), argsDone.Get("arguments").String())
	}
}

func TestConvertClaudeResponseToOpenAIResponsesNonStream_MovesLeadingCDToWorkdir(t *testing.T) {
	raw := []byte(strings.Join(cdWorkdirToolUseChunks(), "\n"))
	out := ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "claude-test", nil, nil, raw, nil)
	assertCDMovedToWorkdir(t, "non-stream", ojson.GetBytesNoCopy(out, "output.0.arguments").String())
}
