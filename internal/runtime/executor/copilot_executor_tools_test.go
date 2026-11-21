package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	tr "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestCopilotClaudeToolImageViaKimiChat(t *testing.T) {
	const model = "kimi-k3"
	const img = `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}`
	const screenCall = `{"id":"call_screen","type":"function","function":{"name":"screenshot","arguments":"{}"}}`
	const textCall = `{"id":"call_text","type":"function","function":{"name":"screenshot","arguments":"{}"}}`
	const screenResult = `{"type":"tool_result","tool_use_id":"call_screen","content":[` + img + `]}`
	const textResult = `{"type":"tool_result","tool_use_id":"call_text","content":"text"}`
	for _, tc := range []struct {
		name               string
		parallel, reminder bool
	}{
		{"single", false, false},
		{"parallel results in reverse order", true, false},
		{"deferred system reminder", false, true},
		{"reordered results and reminder", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := NewCopilotExecutor(&config.Config{})
			auth := copilotTestAuth(t, "account", model, copilot.Chat)
			opts := cliproxyexecutor.Options{SourceFormat: tr.FormatClaude, Metadata: map[string]any{cliproxyexecutor.CanonicalSessionIDMetadataKey: "session"}}
			doc := ojson.NewDocString(`{"messages":[{"role":"user","content":"Take a screenshot"}],"tools":[{"name":"screenshot","input_schema":{"type":"object"}}]}`)
			var first http.Header
			for i, initiator := range []string{"user", "agent", "user"} {
				ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripFunc(func(r *http.Request) (*http.Response, error) {
					response := `{"id":"chat-2","model":"kimi-k3","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`
					if got := r.Header.Get("X-Initiator"); got != initiator {
						t.Errorf("request %d: initiator = %q, want %q", i, got, initiator)
					}
					if i == 0 {
						first = r.Header.Clone()
						calls := screenCall
						if tc.parallel {
							calls += "," + textCall
						}
						response = `{"id":"chat-1","model":"kimi-k3","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[` + calls + `]},"finish_reason":"tool_calls"}]}`
					} else {
						if r.Header.Get("Copilot-Vision-Request") != "true" || r.Header.Get("X-Client-Session-Id") != first.Get("X-Client-Session-Id") {
							t.Errorf("request %d: missing vision header or changed session", i)
						}
						for _, key := range []string{"X-Interaction-Id", "X-Agent-Task-Id"} {
							got := r.Header.Get(key)
							if got == "" || (got == first.Get(key)) != (i == 1) {
								t.Errorf("request %d: unexpected %s reuse", i, key)
							}
						}
						if i == 1 {
							body, err := io.ReadAll(r.Body)
							if err != nil {
								t.Fatal(err)
							}
							msgs := ojson.GetBytesNoCopy(body, "messages").Array()
							if tc.reminder {
								if len(msgs) == 0 || msgs[len(msgs)-1].Get("content").String() != "<system-reminder>\nbackground update\n</system-reminder>" {
									t.Fatal("missing deferred reminder after image relay")
								}
								msgs = msgs[:len(msgs)-1]
							}
							lastID := "screenshot:0"
							if msgs[1].Get("tool_calls.0.id").String() != lastID || msgs[1].Get("content").Raw != "null" {
								t.Fatal("incorrect Kimi assistant call envelope")
							}
							if tc.parallel {
								if len(msgs) < 3 || msgs[len(msgs)-3].Get("tool_call_id").String() != lastID || msgs[1].Get("tool_calls.1.id").String() != "screenshot:1" {
									t.Fatal("tool results not aligned to call order")
								}
								lastID = "screenshot:1"
							}
							if len(msgs) < 2 {
								t.Fatal("missing relayed tool result")
							}
							tool, relay := msgs[len(msgs)-2], msgs[len(msgs)-1]
							if tool.Get("role").String() != "tool" || tool.Get("name").Exists() || tool.Get("tool_call_id").String() != lastID || relay.Get("role").String() != "user" || relay.Get("content.1.type").String() != "image_url" {
								t.Fatal("expected tool result followed by a user image relay")
							}
						}
					}
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response)), Request: r}, nil
				}))
				resp, err := e.Execute(ctx, auth, cliproxyexecutor.Request{Model: model, Payload: doc.Bytes()}, opts)
				if err != nil {
					t.Fatal(err)
				}
				if i < 2 {
					_ = doc.Set("messages.-1", map[string]any{"role": "assistant", "content": ojson.GetBytesNoCopy(resp.Payload, "content").Value()})
					if i == 0 {
						if tc.reminder {
							_ = doc.SetRaw("messages.-1", `{"role":"system","content":"background update"}`)
						}
						results := screenResult
						if tc.parallel {
							results = textResult + "," + screenResult
						}
						_ = doc.SetRaw("messages.-1", `{"role":"user","content":[`+results+`]}`)
					} else {
						_ = doc.SetRaw("messages.-1", `{"role":"user","content":[`+img+`]}`)
					}
				}
			}
		})
	}
}

func TestCopilotToolRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name, endpoint                                           string
		from                                                     tr.Format
		prompt, first, final, idPath, replay, setCall, setOutput string
		upstreamItems, callKey, outputKey                        string
		vision                                                   bool
	}{
		{"chat via messages", copilot.Messages, tr.FormatOpenAI,
			`{"messages":[{"role":"user","content":"calculate"}]}`,
			`{"id":"msg_1","type":"message","model":"test-model","role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"calculate","input":{"x":1}}],"stop_reason":"tool_use"}`,
			`{"id":"msg_2","type":"message","model":"test-model","role":"assistant","content":[{"type":"text","text":"success"}],"stop_reason":"end_turn"}`,
			"choices.0.message.tool_calls.0.id", `{"messages":[{"role":"user","content":"calculate"},{"role":"assistant","tool_calls":[{"type":"function","function":{"name":"calculate","arguments":"{\"x\":1}"}}]},{"role":"tool","content":"result"}]}`,
			"messages.1.tool_calls.0.id", "messages.2.tool_call_id", "messages", "content.0.id", "content.0.tool_use_id", false},
		{"messages via responses with image result", copilot.Responses, tr.FormatClaude,
			`{"max_tokens":128,"messages":[{"role":"user","content":"calculate"}]}`,
			`{"id":"resp_1","status":"completed","model":"test-model","output":[{"id":"fc_1","type":"function_call","call_id":"call_1","name":"calculate","arguments":"{\"x\":1}"}]}`,
			`{"id":"resp_2","status":"completed","model":"test-model","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"success"}]}]}`,
			"content.0.id", `{"max_tokens":128,"messages":[{"role":"user","content":"calculate"},{"role":"assistant","content":[{"type":"tool_use","name":"calculate","input":{"x":1}}]},{"role":"user","content":[{"type":"tool_result","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]}]}]}`,
			"messages.1.content.0.id", "messages.2.content.0.tool_use_id", "input", "call_id", "call_id", true},
		{"responses via chat", copilot.Chat, tr.FormatOpenAIResponse,
			`{"input":"calculate"}`,
			`{"id":"chat_1","model":"test-model","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"calculate","arguments":"{\"x\":1}"}}]},"finish_reason":"tool_calls"}]}`,
			`{"id":"chat_2","model":"test-model","choices":[{"index":0,"message":{"role":"assistant","content":"success"},"finish_reason":"stop"}]}`,
			"output.0.call_id", `{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"calculate"}]},{"type":"function_call","name":"calculate","arguments":"{\"x\":1}"},{"type":"function_call_output","output":"result"}]}`,
			"input.1.call_id", "input.2.call_id", "messages", "tool_calls.0.id", "tool_call_id", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const model = "test-model"
			executor := NewCopilotExecutor(&config.Config{})
			auth := copilotTestAuth(t, "one", model, tc.endpoint)
			opts := cliproxyexecutor.Options{SourceFormat: tc.from}
			var firstHeaders http.Header
			attempt := 0
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripFunc(func(r *http.Request) (*http.Response, error) {
				attempt++
				response := tc.first
				if attempt == 1 {
					firstHeaders = r.Header.Clone()
				}
				if attempt == 2 {
					for _, key := range []string{"X-Client-Session-Id", "X-Interaction-Id", "X-Agent-Task-Id"} {
						if r.Header.Get(key) == "" || r.Header.Get(key) != firstHeaders.Get(key) {
							t.Errorf("%s changed across tool continuation", key)
						}
					}
					response = tc.final
					if r.Header.Get("X-Initiator") != "agent" || (r.Header.Get("Copilot-Vision-Request") == "true") != tc.vision {
						t.Fatalf("unexpected continuation headers: %v", r.Header)
					}
					body, _ := io.ReadAll(r.Body)
					items := ojson.GetBytesNoCopy(body, tc.upstreamItems).Array()
					if len(items) < 3 {
						t.Fatalf("missing tool replay: %s", body)
					}
					call, result := items[len(items)-2].Get(tc.callKey).String(), items[len(items)-1].Get(tc.outputKey).String()
					if call == "" || call != result {
						t.Fatalf("unpaired tool replay: %s", body)
					}
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response)), Request: r}, nil
			}))
			req := cliproxyexecutor.Request{Model: model, Payload: []byte(tc.prompt)}
			first, err := executor.Execute(ctx, auth, req, opts)
			if err != nil {
				t.Fatal(err)
			}
			callID := ojson.GetBytesNoCopy(first.Payload, tc.idPath).String()
			if callID == "" {
				t.Fatalf("missing tool call from translation: %s", first.Payload)
			}
			// Auth reconciliation can rebind the executor between these two requests.
			executor = executor.WithConfig(&config.Config{})
			doc := ojson.NewDocString(tc.replay)
			_ = doc.Set(tc.setCall, callID)
			_ = doc.Set(tc.setOutput, callID)
			req.Payload = doc.Bytes()
			final, err := executor.Execute(ctx, auth, req, opts)
			if err != nil || !strings.Contains(string(final.Payload), "success") {
				t.Fatalf("failed continuation: %s, %v", final.Payload, err)
			}
		})
	}
}
