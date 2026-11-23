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

func TestCopilotNativeChatAssistantHistory(t *testing.T) {
	const model = "gemini-4-neon"
	const configured = `[{"type":"text","text":"configured"}]`
	auth := copilotTestAuth(t, "account", model, copilot.Chat)
	req := x.Request{Model: model, Payload: []byte(`{"messages":[
		{"role":"user","content":"Give me a code."},
		{"role":"assistant","content":[{"type":"text","text":"Code: "},{"type":"text","text":"0731"}]},
		{"role":"user","content":"Repeat the code."}
	]}`)}
	opts := x.Options{SourceFormat: tr.FormatOpenAI}
	for _, tc := range []struct {
		name     string
		stream   bool
		override []config.PayloadRule
		wantRaw  string
	}{
		{name: "buffered", wantRaw: `"Code: 0731"`},
		{name: "streaming", stream: true, wantRaw: `"Code: 0731"`},
		{
			name: "payload override",
			override: []config.PayloadRule{{
				Models: []config.PayloadModelRule{{Name: model}},
				Params: map[string]any{"messages.1.content": configured},
			}},
			wantRaw: configured,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Payload.OverrideRaw = tc.override
			p, err := NewCopilotExecutor(cfg).prepare(t.Context(), auth, req, opts, tc.stream, false)
			if err != nil {
				t.Fatal(err)
			}
			if got := ojson.GetBytesNoCopy(p.body, "messages.1.content"); got.Raw != tc.wantRaw {
				t.Fatalf("upstream assistant content = %s, want %s", got.Raw, tc.wantRaw)
			}
		})
	}
}

func TestCopilotChatAssistantTextFlatteningAndReplay(t *testing.T) {
	const model = "gemini-4-neon"
	const turn1Resp = `{"id":"chat-t1","model":"gemini-4-neon","choices":[{"index":0,"message":{"role":"assistant","content":"Triage report with Plan A and Plan B.","reasoning_text":"trace-1","reasoning_opaque":"OPAQUE-1"},"finish_reason":"stop"}]}`
	const turn2Resp = `{"id":"chat-t2","model":"gemini-4-neon","choices":[{"index":0,"message":{"role":"assistant","content":"Confirming Plan A before closing.","reasoning_text":"trace-2","reasoning_opaque":"OPAQUE-2","tool_calls":[{"id":"call_ask","type":"function","function":{"name":"ask","arguments":"{\"question\":\"Confirm?\"}"},"extra_content":{"google":{"thought_signature":"SIG-2"}}}]},"finish_reason":"tool_calls"}]}`
	const turn3Resp = `{"id":"chat-t3","model":"gemini-4-neon","choices":[{"index":0,"message":{"role":"assistant","content":"Done."},"finish_reason":"stop"}]}`

	for _, from := range []tr.Format{tr.FormatOpenAIResponse, tr.FormatClaude} {
		t.Run(from.String(), func(t *testing.T) {
			e := NewCopilotExecutor(&config.Config{})
			auth := copilotTestAuth(t, "account", model, copilot.Chat)
			opts := x.Options{
				SourceFormat: from,
				Metadata: map[string]any{
					x.CanonicalSessionIDMetadataKey: "session-assistant-text",
					x.CallerScopeMetadataKey:        "caller",
				},
			}
			doc := ojson.NewDocString(`{"messages":[{"role":"user","content":"triage issue 7810"}]}`)
			if from == tr.FormatOpenAIResponse {
				doc = ojson.NewDocString(`{"input":[{"role":"user","content":[{"type":"input_text","text":"triage issue 7810"}]}]}`)
			}

			var bodies [][]byte
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripFunc(func(r *http.Request) (*http.Response, error) {
				raw, errRead := io.ReadAll(r.Body)
				if errRead != nil {
					return nil, errRead
				}
				bodies = append(bodies, raw)
				answer := turn3Resp
				switch len(bodies) {
				case 1:
					answer = turn1Resp
				case 2:
					answer = turn2Resp
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(answer)), Request: r}, nil
			}))

			// Turn 1: text-only assistant reply.
			resp1, errExec1 := e.Execute(ctx, auth, x.Request{Model: model, Payload: doc.Bytes()}, opts)
			if errExec1 != nil {
				t.Fatal(errExec1)
			}
			out1 := ojson.ParseBytes(resp1.Payload)
			if from == tr.FormatClaude {
				_ = doc.Set("messages.-1", map[string]any{"role": "assistant", "content": out1.Get("content").Value()})
				_ = doc.SetRaw("messages.-1", `{"role":"user","content":"Plan A"}`)
			} else {
				for _, item := range out1.Get("output").Array() {
					_ = doc.SetRaw("input.-1", item.Raw)
				}
				_ = doc.SetRaw("input.-1", `{"role":"user","content":[{"type":"input_text","text":"Plan A"}]}`)
			}

			// Turn 2: client replays Turn 1 assistant text and sends user follow-up.
			resp2, errExec2 := e.Execute(ctx, auth, x.Request{Model: model, Payload: doc.Bytes()}, opts)
			if errExec2 != nil {
				t.Fatal(errExec2)
			}
			turn2Req := ojson.ParseBytes(bodies[1])
			msg1 := turn2Req.Get("messages.1")
			if msg1.Get("role").String() != "assistant" || msg1.Get("content").Type != ojson.String || msg1.Get("content").String() != "Triage report with Plan A and Plan B." {
				t.Fatalf("turn 2 assistant content not flattened to string: %s", msg1.Raw)
			}
			if msg1.Get("reasoning_text").String() != "trace-1" || msg1.Get("reasoning_opaque").String() != "OPAQUE-1" || msg1.Get("reasoning_content").Exists() {
				t.Fatalf("turn 2 assistant reasoning replay mismatch: %s", msg1.Raw)
			}

			out2 := ojson.ParseBytes(resp2.Payload)
			if from == tr.FormatClaude {
				_ = doc.Set("messages.-1", map[string]any{"role": "assistant", "content": out2.Get("content").Value()})
				_ = doc.SetRaw("messages.-1", `{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_ask","content":"Confirmed"}]}`)
			} else {
				for _, item := range out2.Get("output").Array() {
					_ = doc.SetRaw("input.-1", item.Raw)
				}
				_ = doc.SetRaw("input.-1", `{"type":"function_call_output","call_id":"call_ask","output":"Confirmed"}`)
			}

			// Turn 3: client replays Turn 2 assistant text + tool call and tool output.
			if _, errExec3 := e.Execute(ctx, auth, x.Request{Model: model, Payload: doc.Bytes()}, opts); errExec3 != nil {
				t.Fatal(errExec3)
			}
			turn3Req := ojson.ParseBytes(bodies[2])
			msg3 := turn3Req.Get("messages.3")
			if msg3.Get("role").String() != "assistant" || msg3.Get("content").Type != ojson.String || msg3.Get("content").String() != "Confirming Plan A before closing." {
				t.Fatalf("turn 3 assistant text+tool_calls content not flattened to string: %s", msg3.Raw)
			}
			if msg3.Get("reasoning_text").String() != "trace-2" || msg3.Get("reasoning_opaque").String() != "OPAQUE-2" || msg3.Get("reasoning_content").Exists() || msg3.Get("tool_calls.0.extra_content.google.thought_signature").String() != "SIG-2" {
				t.Fatalf("turn 3 assistant replay metadata mismatch: %s", msg3.Raw)
			}

			// Payload override must remain the final semantic barrier after assistant content flattening.
			// Override with a single plain-text array to catch any ordering regression that flattens after payload rules.
			cfg := &config.Config{}
			cfg.Payload.Override = []config.PayloadRule{{
				Models: []config.PayloadModelRule{{Name: model}},
				Params: map[string]any{
					"messages.1.content": []map[string]any{{"type": "text", "text": "user-configured-override"}},
				},
			}}
			prepared, errPrep := e.WithConfig(cfg).prepare(ctx, auth, x.Request{Model: model, Payload: doc.Bytes()}, opts, true, false)
			if errPrep != nil {
				t.Fatal(errPrep)
			}
			got := ojson.GetBytesNoCopy(prepared.body, "messages.1.content")
			if !got.IsArray() || got.Get("#").Int() != 1 || got.Get("0.text").String() != "user-configured-override" {
				t.Fatalf("payload override did not stay final after flattening: %s", got.Raw)
			}
		})
	}
}
