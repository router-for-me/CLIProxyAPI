package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	x "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	tr "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestCopilotStreamingVisibleReasoning(t *testing.T) {
	const model = "gemini-4-neon"
	const stream = "data: " + `{"id":"cmpl-1","model":"gemini-4-neon","choices":[{"index":0,"delta":{"role":"assistant","reasoning_text":"visible trace","reasoning_opaque":"OPAQUE-ONLY"}}]}` + "\n\n" +
		"data: " + `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"a","type":"function","function":{"name":"sample","arguments":"{}"}}]}}]}` + "\n\n" +
		"data: " + `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"extra_content":{"google":{"thought_signature":"SIGNATURE-ONLY"}}}]},"finish_reason":"tool_calls"}]}` + "\n\ndata: [DONE]\n\n"
	for _, f := range []tr.Format{tr.FormatClaude, tr.FormatOpenAIResponse} {
		t.Run(f.String(), func(t *testing.T) {
			e := NewCopilotExecutor(&config.Config{})
			auth := copilotTestAuth(t, "account", model, copilot.Chat)
			opts := x.Options{SourceFormat: f, Metadata: map[string]any{x.CanonicalSessionIDMetadataKey: "session", x.CallerScopeMetadataKey: "caller"}}
			doc := ojson.NewDocString(`{"messages":[{"role":"user","content":"sample"}]}`)
			if f == tr.FormatOpenAIResponse {
				doc = ojson.NewDocString(`{"input":"sample"}`)
			}
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: make(http.Header), Request: r, Body: io.NopCloser(strings.NewReader(stream))}, nil
			}))
			res, err := e.ExecuteStream(ctx, auth, x.Request{Model: model, Payload: doc.Bytes()}, opts)
			if err != nil {
				t.Fatal(err)
			}
			var downstream bytes.Buffer
			for chunk := range res.Chunks {
				if chunk.Err != nil {
					t.Fatal(chunk.Err)
				}
				downstream.Write(chunk.Payload)
			}
			out := downstream.String()
			if !strings.Contains(out, "visible trace") || strings.Contains(out, "-ONLY") {
				t.Fatalf("incorrect translated stream: %s", out)
			}
			if f == tr.FormatClaude {
				_ = doc.SetRaw("messages.-1", `{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"sample","input":{}}]}`)
				_ = doc.SetRaw("messages.-1", `{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"done"}]}`)
			} else {
				_ = doc.SetRaw("input", `[{"role":"user","content":"sample"},{"type":"function_call","call_id":"a","name":"sample","arguments":"{}"},{"type":"function_call_output","call_id":"a","output":"done"}]`)
			}
			p, err := e.prepare(ctx, auth, x.Request{Model: model, Payload: doc.Bytes()}, opts, false, false)
			if err != nil {
				t.Fatal(err)
			}
			m := ojson.GetBytesNoCopy(p.body, "messages.1")
			if m.Get("reasoning_opaque").String() != "OPAQUE-ONLY" || m.Get("tool_calls.0.extra_content.google.thought_signature").String() != "SIGNATURE-ONLY" {
				t.Fatal("late stream metadata was not restored")
			}
		})
	}
}

func TestCopilotChatCrossProtocolReplay(t *testing.T) {
	const model = "gemini-4-neon"
	const first = `{"id":"chat-first","model":"gemini-4-neon","choices":[{"index":0,"message":{"role":"assistant","content":null,"reasoning_text":"visible trace","reasoning_opaque":"OPAQUE-ONLY","tool_calls":[{"id":"a","type":"function","function":{"name":"get_a","arguments":"{\"y\":2,\"x\":1}"},"extra_content":{"google":{"thought_signature":"SIGNATURE-ONLY"}}},{"id":"b","type":"function","function":{"name":"get_b","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`
	const final = `{"id":"chat-final","model":"gemini-4-neon","choices":[{"index":0,"message":{"role":"assistant","content":"green"},"finish_reason":"stop"}]}`
	for _, from := range []tr.Format{tr.FormatClaude, tr.FormatOpenAIResponse} {
		t.Run(from.String(), func(t *testing.T) {
			e := NewCopilotExecutor(&config.Config{})
			auth := copilotTestAuth(t, "account", model, copilot.Chat)
			opts := x.Options{SourceFormat: from, Metadata: map[string]any{x.CanonicalSessionIDMetadataKey: "session", x.CallerScopeMetadataKey: "caller"}}
			doc := ojson.NewDocString(`{"messages":[{"role":"user","content":"sample"}]}`)
			if from == tr.FormatOpenAIResponse {
				doc = ojson.NewDocString(`{"input":[{"role":"user","content":"sample"}]}`)
			}
			var initial http.Header
			var count int
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripFunc(func(r *http.Request) (*http.Response, error) {
				count++
				answer := final
				if count == 1 {
					initial = r.Header.Clone()
					answer = first
				} else {
					raw, _ := io.ReadAll(r.Body)
					root := ojson.ParseBytes(raw)
					msgs := root.Get("messages").Array()
					if len(msgs) != 7 {
						t.Fatalf("unexpected wire message count: %d (%s)", len(msgs), raw)
					}
					assistant := msgs[1]
					if from == tr.FormatClaude && assistant.Get("tool_calls.0.function.arguments").String() != `{"y":2,"x":1}` {
						t.Fatal("lost the issued argument spelling")
					}
					if assistant.Get("reasoning_text").String() != "visible trace" || assistant.Get("reasoning_content").Exists() || assistant.Get("reasoning_opaque").String() != "OPAQUE-ONLY" || assistant.Get("tool_calls.0.extra_content.google.thought_signature").String() != "SIGNATURE-ONLY" || assistant.Get("tool_calls.1.extra_content").Exists() {
						t.Fatalf("incorrect replay: %s", assistant.Raw)
					}
					if msgs[2].Get("role").String() != "tool" || msgs[2].Get("content").String() != "description" || msgs[3].Get("role").String() != "tool" || msgs[3].Get("content").Type != ojson.String || msgs[3].Get("content").String() != "" {
						t.Fatalf("incorrect tool results: %s %s", msgs[2].Raw, msgs[3].Raw)
					}
					for i, img := range []string{"AQ==", "Ag==", "Aw=="} {
						m := msgs[4+i]
						caption := "Here is an image:"
						if i == 0 {
							caption = "first swatch"
						}
						if m.Get("role").String() != "user" || m.Get("content.#").Int() != 2 || m.Get("content.1.description").Exists() || m.Get("content.0.text").String() != caption || !strings.HasSuffix(m.Get("content.1.image_url.url").String(), img) {
							t.Fatalf("incorrect image relay: %s", m.Raw)
						}
					}
					for _, k := range []string{"X-Interaction-Id", "X-Agent-Task-Id", "X-Client-Session-Id"} {
						if r.Header.Get(k) != initial.Get(k) {
							t.Errorf("changed %s", k)
						}
					}
					if r.Header.Get("X-Initiator") != "agent" || r.Header.Get("Copilot-Vision-Request") != "true" {
						t.Fatal("missing tool-vision identity")
					}
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(answer)), Request: r}, nil
			}))
			resp, err := e.Execute(ctx, auth, x.Request{Model: model, Payload: doc.Bytes()}, opts)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(resp.Payload, []byte("visible trace")) || bytes.Contains(resp.Payload, []byte("OPAQUE-ONLY")) || bytes.Contains(resp.Payload, []byte("SIGNATURE-ONLY")) {
				t.Fatalf("incorrect translated response: %s", resp.Payload)
			}
			out := ojson.ParseBytes(resp.Payload)
			if from == tr.FormatClaude {
				_ = doc.Set("messages.-1", map[string]any{"role": "assistant", "content": out.Get("content").Value()})
				_ = doc.SetRaw("messages.-1", `{"role":"user","content":[{"type":"tool_result","tool_use_id":"b","content":{"type":"image","source":{"type":"base64","media_type":"image/png","data":"Aw=="}}},{"type":"tool_result","tool_use_id":"a","content":[{"type":"text","text":"description"},{"type":"image","description":"first swatch","source":{"type":"base64","media_type":"image/png","data":"AQ=="}},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"Ag=="}}]}]}`)
			} else {
				for _, item := range out.Get("output").Array() {
					_ = doc.SetRaw("input.-1", item.Raw)
				}
				_ = doc.SetRaw("input.-1", `{"type":"function_call_output","call_id":"a","output":[{"type":"input_text","text":"description"},{"type":"input_image","description":"first swatch","image_url":"data:image/png;base64,AQ=="},{"type":"input_image","image_url":"data:image/png;base64,Ag=="}]}`)
				_ = doc.SetRaw("input.-1", `{"type":"function_call_output","call_id":"b","output":{"type":"input_image","image_url":"data:image/png;base64,Aw=="}}`)
			}
			e = e.WithConfig(&config.Config{})
			req := x.Request{Model: model, Payload: doc.Bytes()}
			if _, err = e.Execute(ctx, auth, req, opts); err != nil {
				t.Fatal(err)
			}
			// User rules on generated messages and restored metadata must remain final.
			cfg := &config.Config{}
			match := []config.PayloadModelRule{{Name: model}}
			cfg.Payload.Filter = []config.PayloadFilterRule{{Models: match, Params: []string{"messages.1.reasoning_opaque", "messages.1.tool_calls.0.extra_content"}}}
			cfg.Payload.Override = []config.PayloadRule{{Models: match, Params: map[string]any{"messages.4.content.0.text": "overridden"}}}
			p, err := e.WithConfig(cfg).prepare(ctx, auth, req, opts, true, false)
			if err != nil {
				t.Fatal(err)
			}
			w := ojson.ParseBytes(p.body)
			if w.Get("messages.1.reasoning_opaque").Exists() || w.Get("messages.1.tool_calls.0.extra_content").Exists() || w.Get("messages.4.content.0.text").String() != "overridden" {
				t.Fatal("built-in processing overwrote user rules")
			}
			cfg.Payload.Filter = nil
			cfg.Payload.Override[0].Params = map[string]any{"model": "kimi-k3", "messages.1.reasoning_content": "configured"}
			p, err = e.WithConfig(cfg).prepare(ctx, auth, req, opts, false, false)
			if err != nil {
				t.Fatal(err)
			}
			w = ojson.ParseBytes(p.body)
			if w.Get("model").String() != "kimi-k3" || bytes.Contains(p.body, []byte("-ONLY")) || w.Get("messages.1.reasoning_content").String() != "configured" {
				t.Fatal("model override carried private replay or lost an explicit user field")
			}
		})
	}
}
