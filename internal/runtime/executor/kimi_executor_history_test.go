package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestKimiExecutorResponsesReplaysDiscoveredTools(t *testing.T) {
	const ping = `{"type":"function","name":"ping","description":"current","parameters":{"type":"object","properties":{}}}`
	const oldPing = `{"type":"function","name":"ping","description":"old","parameters":{"type":"object","properties":{}}}`
	const read = `{"type":"function","name":"read","parameters":{"type":"object","properties":{}}}`
	const search = `{"type":"tool_search","execution":"client"}`
	const call = `{"type":"tool_search_call","execution":"client","call_id":"s1","status":"completed","arguments":{"query":"ping","limit":1}}`
	const user = `{"type":"message","role":"user","content":[{"type":"input_text","text":"say tool_search"}]}`
	const history = `{"type":"reasoning","encrypted_content":"opaque","summary":[]},{"type":"function_call","call_id":"f1","name":"ping","namespace":"workspace","arguments":"{}"},{"type":"function_call_output","call_id":"f1","output":"pong"},{"type":"custom_tool_call","call_id":"p1","name":"apply_patch","input":"*** Begin Patch\n*** End Patch"},{"type":"custom_tool_call_output","call_id":"p1","output":"done"}`
	namespace := func(name, tools string) string {
		return `{"type":"namespace","name":"` + name + `","description":"workspace tools","tools":[` + tools + `]}`
	}
	output := func(tools string) string {
		return `{"type":"tool_search_output","execution":"client","call_id":"s1","status":"completed","tools":[` + tools + `]}`
	}
	additional := func(tools string) string {
		return `{"type":"additional_tools","tools":[` + tools + `]}`
	}
	tests := []struct {
		name, input, tools, choice, wantInput, wantTools, wantChoice string
	}{
		{name: "issue reproduction without top-level tools", input: user + "," + call + "," + output(namespace("t", ping)) + "," + user, wantInput: user + "," + user, wantTools: "[" + namespace("t", ping) + "]"},
		{name: "function discovery", input: call + "," + output(ping) + "," + user, tools: "[" + search + "]", wantInput: user, wantTools: "[" + ping + "]"},
		{name: "current function wins", input: output(oldPing) + "," + user, tools: "[" + ping + "]", wantInput: user, wantTools: "[" + ping + "]"},
		{name: "latest discovered definition wins", input: output(oldPing) + "," + output(ping) + "," + user, wantInput: user, wantTools: "[" + ping + "]"},
		{name: "replayed output is deduplicated", input: output(ping) + "," + output(ping) + "," + user, tools: "[]", wantInput: user, wantTools: "[" + ping + "]"},
		{name: "merge namespace members", input: output(namespace("workspace", oldPing+","+read)) + "," + user, tools: "[" + namespace("workspace", ping) + "]", wantInput: user, wantTools: "[" + namespace("workspace", ping+","+read) + "]"},
		{name: "merge successive namespace discoveries", input: output(namespace("workspace", oldPing+","+read)) + "," + output(namespace("workspace", ping)) + "," + user, wantInput: user, wantTools: "[" + namespace("workspace", ping+","+read) + "]"},
		{name: "namespace identity is retained", input: output(namespace("remote", ping)) + "," + user, tools: "[" + namespace("local", ping) + "," + ping + "]", wantInput: user, wantTools: "[" + namespace("local", ping) + "," + ping + "," + namespace("remote", ping) + "]"},
		{name: "empty search results", input: call + "," + output("") + "," + user, wantInput: user},
		{name: "call without output", input: call + "," + user, tools: "[]", wantInput: user, wantTools: "[]"},
		{name: "all search history", input: call + "," + output(ping), wantInput: "", wantTools: "[" + ping + "]"},
		{name: "supported history remains byte-equivalent", input: user + "," + call + "," + output(namespace("workspace", ping)) + "," + history + "," + user, wantInput: user + "," + history + "," + user, wantTools: "[" + namespace("workspace", ping) + "]"},
		{name: "discovered function schema is normalized", input: output(`{"type":"function","name":"read","parameters":{"$defs":{"value":{"type":"string"}},"properties":{"path":{"$ref":"#/$defs/value"}}}}`) + "," + user, wantInput: user, wantTools: `[{"type":"function","name":"read","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}]`},
		{name: "discovered namespace schema is normalized", input: output(namespace("workspace", `{"type":"function","name":"read","parameters":{"$defs":{"value":{"type":"string"}},"properties":{"path":{"$ref":"#/$defs/value"}}}}`)) + "," + user, wantInput: user, wantTools: "[" + namespace("workspace", `{"type":"function","name":"read","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}`) + "]"},
		{name: "discovered namespace search is removed", input: output(namespace("workspace", search+","+ping)) + "," + user, wantInput: user, wantTools: "[" + namespace("workspace", ping) + "]"},
		{name: "required uses discovered function", input: output(ping) + "," + user, tools: "[" + search + "]", choice: `"required"`, wantInput: user, wantTools: "[" + ping + "]", wantChoice: `"required"`},
		{name: "forced search resets after discovery", input: output(ping) + "," + user, tools: "[" + search + "]", choice: search, wantInput: user, wantTools: "[" + ping + "]", wantChoice: `"auto"`},
		{name: "additional search without top-level tools", input: additional(search) + "," + user, choice: `"required"`, wantInput: user, wantChoice: `"auto"`},
		{name: "additional mixed tools preserve history", input: user + "," + additional(search+","+ping) + "," + history, wantInput: user + "," + additional(ping) + "," + history},
		{name: "additional nested search", input: additional(namespace("workspace", search+","+ping)) + "," + user, wantInput: additional(namespace("workspace", ping)) + "," + user},
		{name: "additional emptied namespace", input: additional(namespace("workspace", namespace("discovery", search))) + "," + user, choice: `"required"`, wantInput: user, wantChoice: `"auto"`},
		{name: "additional function keeps required choice", input: additional(ping) + "," + user, tools: "[" + search + "]", choice: `"required"`, wantInput: additional(ping) + "," + user, wantTools: "[]", wantChoice: `"required"`},
		{name: "top-level function keeps required choice", input: additional(search) + "," + user, tools: "[" + ping + "]", choice: `"required"`, wantInput: user, wantTools: "[" + ping + "]", wantChoice: `"required"`},
		{name: "removed additional namespace choice", input: additional(namespace("workspace", namespace("discovery", search)+","+ping)) + "," + user, choice: `{"type":"allowed_tools","tools":[{"type":"namespace","name":"discovery"}]}`, wantInput: additional(namespace("workspace", ping)) + "," + user, wantChoice: `"auto"`},
		{name: "top-level namespace preserves additional choice", input: additional(namespace("workspace", search)) + "," + user, tools: "[" + namespace("workspace", ping) + "]", choice: `{"type":"allowed_tools","tools":[{"type":"namespace","name":"workspace"}]}`, wantInput: user, wantTools: "[" + namespace("workspace", ping) + "]", wantChoice: `{"type":"allowed_tools","tools":[{"type":"namespace","name":"workspace"}]}`},
		{name: "additional namespace preserves top-level choice", input: additional(namespace("workspace", ping)) + "," + user, tools: "[" + namespace("workspace", search) + "]", choice: `{"type":"allowed_tools","tools":[{"type":"namespace","name":"workspace"}]}`, wantInput: additional(namespace("workspace", ping)) + "," + user, wantTools: "[]", wantChoice: `{"type":"allowed_tools","tools":[{"type":"namespace","name":"workspace"}]}`},
		{name: "all additional items removed", input: additional(search) + "," + additional(namespace("discovery", search)), choice: search, wantInput: "", wantChoice: `"auto"`},
		{name: "discovered function with additional search", input: output(ping) + "," + additional(search) + "," + user, choice: `"required"`, wantInput: user, wantTools: "[" + ping + "]", wantChoice: `"required"`},
		{name: "supported additional items unchanged", input: additional(ping) + "," + user, wantInput: additional(ping) + "," + user},
		{name: "original empty additional item unchanged", input: additional("") + "," + user, wantInput: additional("") + "," + user},
		{name: "no search history is untouched", input: user + "," + history, tools: "[" + ping + "]", wantInput: user + "," + history, wantTools: "[" + ping + "]"},
	}
	for _, streaming := range []bool{false, true} {
		mode := "non-streaming/"
		if streaming {
			mode = "streaming/"
		}
		for _, tt := range tests {
			t.Run(mode+tt.name, func(t *testing.T) {
				var upstreamBody []byte
				ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", kimiRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
					var errRead error
					upstreamBody, errRead = io.ReadAll(req.Body)
					if errRead != nil {
						return nil, errRead
					}
					for _, item := range gjson.GetBytes(upstreamBody, "input").Array() {
						if kind := item.Get("type").String(); kind == "tool_search_call" || kind == "tool_search_output" || (kind == "additional_tools" && kimiTestContainsToolSearch(item.Get("tools"))) {
							return &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","message":"Invalid request Error"}}`))}, nil
						}
					}
					response := `{"id":"resp_test","object":"response","status":"completed","output":[]}`
					contentType := "application/json"
					if streaming {
						response = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":" + response + "}\n\n"
						contentType = "text/event-stream"
					}
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(response))}, nil
				}))
				payload := `{"model":"kimi-k3","input":[` + tt.input + `],"metadata":{"keep":"unchanged"}}`
				if tt.tools != "" {
					payload = strings.TrimSuffix(payload, "}") + `,"tools":` + tt.tools + "}"
				}
				if tt.choice != "" {
					payload = strings.TrimSuffix(payload, "}") + `,"tool_choice":` + tt.choice + "}"
				}
				executor := NewKimiExecutor(&config.Config{})
				auth := &cliproxyauth.Auth{Metadata: map[string]any{"access_token": "test-kimi-key"}}
				request := cliproxyexecutor.Request{Model: "kimi-k3", Payload: []byte(payload)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: streaming}
				if streaming {
					result, err := executor.ExecuteStream(ctx, auth, request, opts)
					if err != nil {
						t.Fatal(err)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				} else if _, err := executor.Execute(ctx, auth, request, opts); err != nil {
					t.Fatal(err)
				}
				for path, want := range map[string]string{"input": "[" + tt.wantInput + "]", "tools": tt.wantTools, "tool_choice": tt.wantChoice, "metadata": `{"keep":"unchanged"}`} {
					got := gjson.GetBytes(upstreamBody, path).Raw
					if want == "" {
						if got != "" {
							t.Errorf("%s = %s, want absent", path, got)
						}
						continue
					}
					var gotValue, wantValue any
					if err := json.Unmarshal([]byte(got), &gotValue); err != nil {
						t.Fatalf("decode %s: %v", path, err)
					}
					if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
						t.Fatalf("decode expected %s: %v", path, err)
					}
					if !reflect.DeepEqual(gotValue, wantValue) {
						t.Errorf("%s = %s, want %s", path, got, want)
					}
				}
				if string(request.Payload) != payload {
					t.Error("caller payload was mutated")
				}
				if got := gjson.GetBytes(upstreamBody, "input").Raw; got != "["+tt.wantInput+"]" {
					t.Errorf("remaining history changed: %s", got)
				}
			})
		}
	}
}
