package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	x "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	tr "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestCopilotChatSignedMessagesContinuation(t *testing.T) {
	const model = "claude-haiku-4.5"
	const prompt = `{"messages":[{"role":"system","content":"test"},{"role":"user","content":"calculate"}]}`
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			e := NewCopilotExecutor(nil)
			a := copilotTestAuth(t, "account", model, copilot.Messages)
			catalog := ojson.NewDocString(a.Metadata[copilot.CatalogKey].(string))
			_ = catalog.Set("data.0.capabilities.supports.max_thinking_budget", 16000)
			a.Metadata[copilot.CatalogKey] = catalog.String()
			opts := x.Options{SourceFormat: tr.FormatOpenAI, Metadata: map[string]any{x.CanonicalSessionIDMetadataKey: "session", x.CallerScopeMetadataKey: "caller"}}
			doc := ojson.NewDocString(prompt)
			var body []byte
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: make(http.Header), Request: r, Body: io.NopCloser(bytes.NewReader(body))}, nil
			}))
			prepare := func(cfg *config.Config, opts x.Options, raw []byte) ojson.Result {
				t.Helper()
				n := helps.PayloadConfigEvaluationCount()
				p, err := e.WithConfig(cfg).prepare(ctx, a, x.Request{Model: model, Payload: raw}, opts, stream, false)
				if err != nil {
					t.Fatal(err)
				}
				if helps.PayloadConfigEvaluationCount() != n+1 {
					t.Fatal("expected one payload evaluation")
				}
				return ojson.ParseBytes(p.body)
			}
			for i := 1; i <= 2; i++ {
				id := fmt.Sprint("call_", i)
				signature := fmt.Sprint("PRIVATE-SIG-", i)
				answer := ojson.NewDocString(`{"type":"message","role":"assistant","content":[{"type":"thinking","thinking":"visible"},{"type":"redacted_thinking","data":"PRIVATE-DATA"},{"type":"tool_use","name":"sample","caller":{"type":"direct"}}],"stop_reason":"tool_use"}`)
				_ = answer.Set("id", fmt.Sprint("msg_", i))
				_ = answer.Set("model", model)
				_ = answer.Set("content.0.signature", signature)
				_ = answer.Set("content.2.id", id)
				_ = answer.Set("content.2.input.ticket", i-1)
				body = answer.Bytes()
				req := x.Request{Model: model, Payload: doc.Bytes()}
				if stream {
					body, _ = common.ClaudeMessagesJSONToSSE(body)
					res, err := e.ExecuteStream(ctx, a, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					var visible bytes.Buffer
					for chunk := range res.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
						visible.Write(chunk.Payload)
					}
					if strings.Contains(visible.String(), "PRIVATE-") || !strings.Contains(visible.String(), id) {
						t.Fatal("incorrect Chat stream")
					}
					_ = doc.Set("messages.-1", map[string]any{"role": "assistant", "reasoning_content": "visible", "tool_calls": []any{map[string]any{"id": id, "type": "function", "function": map[string]string{"name": "sample", "arguments": fmt.Sprintf(`{"ticket":%d}`, i-1)}}}})
				} else {
					res, err := e.Execute(ctx, a, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					if bytes.Contains(res.Payload, []byte("PRIVATE-")) {
						t.Fatal("private block exposed to Chat")
					}
					_ = doc.SetRaw("messages.-1", ojson.GetBytesNoCopy(res.Payload, "choices.0.message").Raw)
				}
				_ = doc.Set("messages.-1", map[string]any{"role": "tool", "tool_call_id": id, "content": fmt.Sprint(i)})
				wire := prepare(nil, opts, doc.Bytes())
				for j := 1; j <= i; j++ {
					p := fmt.Sprintf("messages.%d.content", 2*j-1)
					blocks := wire.Get(p)
					if blocks.Get("#.type").Raw != `["thinking","redacted_thinking","tool_use"]` || blocks.Get("0.signature").String() != fmt.Sprint("PRIVATE-SIG-", j) || blocks.Get("1.data").String() != "PRIVATE-DATA" || blocks.Get("2.caller").Exists() || blocks.Get("2.input.ticket").Int() != int64(j-1) {
						t.Fatalf("lost signed block/order on round %d, history turn %d (types %s)", i, j, blocks.Get("#.type").Raw)
					}
				}
			}
			for path, value := range map[string]string{"messages.0.content": "new system", "messages.1.content": "new history", "messages.2.reasoning_content": "edited", "messages.2.tool_calls.0.function.arguments": `{"ticket":5}`, "messages.3.tool_call_id": "call/1"} {
				changed := ojson.NewDoc(doc.Bytes())
				_ = changed.Set(path, value)
				if strings.Contains(prepare(nil, opts, changed.Bytes()).Raw, "PRIVATE-") {
					t.Errorf("replayed across %s", path)
				}
			}
			cfg := &config.Config{}
			cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: model}}, Params: map[string]any{"model": "other"}}}
			if wire := prepare(cfg, opts, doc.Bytes()); wire.Get("model").String() != "other" || strings.Contains(wire.Raw, "PRIVATE-") {
				t.Fatal("replay followed retarget")
			}
			cfg.Payload.Override[0].Params = map[string]any{"messages.0.content.0.text": "configured"}
			if wire := prepare(cfg, opts, doc.Bytes()); wire.Get("messages.0.content.0.text").String() != "configured" || strings.Contains(wire.Raw, "PRIVATE-") {
				t.Fatal("replay followed rewritten transcript")
			}
			for _, meta := range []map[string]any{nil, {x.CanonicalSessionIDMetadataKey: "new", x.CallerScopeMetadataKey: "caller"}, {x.CanonicalSessionIDMetadataKey: "session", x.CallerScopeMetadataKey: "new"}} {
				other := opts
				other.Metadata = meta
				if strings.Contains(prepare(nil, other, doc.Bytes()).Raw, "PRIVATE-") {
					t.Fatal("replay crossed scope")
				}
			}
			a.ID = "other"
			if strings.Contains(prepare(nil, opts, doc.Bytes()).Raw, "PRIVATE-") {
				t.Fatal("replay crossed account")
			}
			a.ID = "account"
			// Indistinguishable unsigned/signed siblings invalidate the old signed entry.
			body = []byte(`{"type":"message","role":"assistant","id":"branch","content":[{"type":"tool_use","name":"sample","id":"call_1","input":{"ticket":0}}],"stop_reason":"tool_use"}`)
			if _, err := e.Execute(ctx, a, x.Request{Model: model, Payload: []byte(prompt)}, opts); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(prepare(nil, opts, doc.Bytes()).Raw, "PRIVATE-") {
				t.Fatal("ambiguous branch inherited signature")
			}
		})
	}
}

func TestCopilotContinuationEnvelopeIsolation(t *testing.T) {
	const claude = `{"messages":[{"role":"assistant","content":[{"type":"tool_use","name":"probe","id":"original","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"original","content":"ok"}]}]}`
	shared := ojson.ParseBytes(tr.Default().TranslateRequest(tr.FormatClaude, tr.FormatOpenAI, "kimi-k3", []byte(claude), false))
	if shared.Get("messages.0.content").Type != ojson.String || shared.Get("messages.1.name").String() != "probe" || shared.Get("messages.1.tool_call_id").String() != "original" {
		t.Fatal("shared Claude to Chat translator changed")
	}
	e := NewCopilotExecutor(nil)
	for _, model := range []string{"gpt-6-luna", "mai-code-2-flash"} {
		a := copilotTestAuth(t, "account", model, copilot.Responses)
		p, err := e.prepare(context.Background(), a, x.Request{Model: model, Payload: []byte(`{"messages":[{"role":"assistant","tool_calls":[{"type":"function","id":"fc_orig","function":{"name":"sample","arguments":"{}"}}]},{"role":"tool","tool_call_id":"fc_orig","content":"ok"}]}`)}, x.Options{SourceFormat: tr.FormatOpenAI}, false, false)
		if err != nil {
			t.Fatal(err)
		}
		items := ojson.GetBytesNoCopy(p.body, "input")
		if items.Get("0.id").String() != "fc_fc_orig" || items.Get("0.status").String() != "completed" || items.Get("0.call_id").String() != "fc_orig" || items.Get("1.call_id").String() != "fc_orig" {
			t.Fatal("missing CLI-generated function item metadata")
		}
		native := `{"input":[{"type":"function_call","id":"caller_id","status":"in_progress","call_id":"fc_orig","name":"sample","arguments":"{}"}]}`
		p, err = e.prepare(context.Background(), a, x.Request{Model: model, Payload: []byte(native)}, x.Options{SourceFormat: tr.FormatOpenAIResponse}, false, false)
		if err != nil || ojson.GetBytesNoCopy(p.body, "input.0").Raw != ojson.Get(native, "input.0").Raw {
			t.Fatal("native Responses item changed")
		}
	}
}
