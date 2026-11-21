package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	x "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	tr "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestCopilotClaudeResponsesPrivateHistory(t *testing.T) {
	const model = "grok-4.6"
	const prompt = `{"system":"test","messages":[{"role":"user","content":"lookup"}]}`
	const call = `{"type":"function_call","id":"fc_original","status":"completed","call_id":"call_A","name":"lookup","arguments":"{\"x\":1}"}`
	const reason = `{"type":"reasoning","id":"rs_original","summary":[{"type":"summary_text","text":"public summary"}],"content":[{"type":"reasoning_text","text":"PRIVATE-raw"}],"encrypted_content":"PRIVATE-opaque="}`
	for _, stream := range []bool{false, true} {
		for _, private := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%t/private=%t", stream, private), func(t *testing.T) {
				e := NewCopilotExecutor(nil)
				a := copilotTestAuth(t, "account", model, copilot.Responses)
				opts := x.Options{SourceFormat: tr.FormatClaude, Metadata: map[string]any{x.CanonicalSessionIDMetadataKey: "session", x.CallerScopeMetadataKey: "caller"}}
				output := "[" + call + "]"
				if private {
					output = "[" + reason + "," + call + "]"
				}
				flat := `{"id":"resp1","status":"completed","model":"grok-4.6","output":` + output + `}`
				up := flat
				if stream {
					var b strings.Builder
					ev := func(v string) { fmt.Fprintf(&b, "data: %s\n\n", v) }
					ev(`{"type":"response.created","response":{"id":"resp1","model":"grok-4.6"}}`)
					for i, it := range ojson.Parse(output).Array() {
						fmt.Fprintf(&b, "data: {\"type\":\"response.output_item.added\",\"output_index\":%d,\"item\":%s}\n\n", i, it.Raw)
						if it.Get("type").String() == "reasoning" {
							ev(`{"type":"response.reasoning_summary_part.added"}`)
							ev(`{"type":"response.reasoning_summary_text.delta","delta":"public summary"}`)
							ev(`{"type":"response.reasoning_text.delta","delta":"PRIVATE-raw"}`)
						} else {
							ev(`{"type":"response.function_call_arguments.delta","delta":"{\"x\":1}"}`)
						}
						fmt.Fprintf(&b, "data: {\"type\":\"response.output_item.done\",\"output_index\":%d,\"item\":%s}\n\n", i, it.Raw)
					}
					ev(`{"type":"response.completed","response":{"id":"resp1","status":"completed","output":[]}}`)
					up = b.String()
				}
				ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripFunc(func(r *http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Header: make(http.Header), Request: r, Body: io.NopCloser(strings.NewReader(up))}, nil
				}))
				req := x.Request{Model: model, Payload: []byte(prompt)}
				var content string
				if stream {
					res, err := e.ExecuteStream(ctx, a, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					acc := newKimiThinkingReplayStreamAccumulator()
					for ch := range res.Chunks {
						if ch.Err != nil {
							t.Fatal(ch.Err)
						}
						if bytes.Contains(ch.Payload, []byte("PRIVATE-")) {
							t.Fatal("upstream private reasoning leaked to Messages SSE")
						}
						acc.observe(ch.Payload)
					}
					b, ok := acc.content()
					if !ok {
						t.Fatal("incomplete client stream")
					}
					content = string(b)
				} else {
					res, err := e.Execute(ctx, a, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					if bytes.Contains(res.Payload, []byte("PRIVATE-")) {
						t.Fatal("upstream private reasoning leaked to Messages")
					}
					content = ojson.GetBytesNoCopy(res.Payload, "content").Raw
				}
				if strings.Contains(content, "public summary") != private {
					t.Fatal("visible summary changed")
				}
				d := ojson.NewDocString(prompt)
				_ = d.SetRaw("messages.-1", `{"role":"assistant","content":`+content+`}`)
				_ = d.SetRaw("messages.-1", `{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_A","content":"ticket"}]}`)
				wire := func(cfg *config.Config, doc *ojson.Doc, o x.Options) ojson.Result {
					t.Helper()
					n := helps.PayloadConfigEvaluationCount()
					p, err := e.WithConfig(cfg).prepare(ctx, a, x.Request{Model: model, Payload: doc.Bytes()}, o, stream, false)
					if err != nil {
						t.Fatal(err)
					}
					if helps.PayloadConfigEvaluationCount() != n+1 {
						t.Fatal("payload evaluated more than once")
					}
					return ojson.ParseBytes(p.body)
				}
				items := wire(nil, d, opts).Get("input").Array()
				expected := ojson.Parse(output).Array()
				if len(items) != len(expected)+2 {
					t.Fatalf("got %d input items, expected %d", len(items), len(expected)+2)
				}
				for i, it := range expected {
					if !reflect.DeepEqual(it.Value(), items[1+i].Value()) {
						t.Fatalf("did not restore complete %s", it.Get("type").String())
					}
				}
				out := items[len(items)-1]
				if out.Get("call_id").String() != "call_A" || out.Get("output").String() != "ticket" {
					t.Fatal("lost tool result pairing")
				}
				miss := func(j ojson.Result) {
					t.Helper()
					if strings.Contains(j.Raw, "fc_original") || strings.Contains(j.Raw, "PRIVATE-") {
						t.Fatal("unmatched history inherited the original output")
					}
				}
				idx := len(ojson.Parse(content).Array()) - 1
				for path, v := range map[string]any{"system": "other", "messages.0.content": "changed", fmt.Sprintf("messages.1.content.%d.input.x", idx): 2, "messages.2.content.0.tool_use_id": "unrelated"} {
					other := ojson.NewDoc(d.Bytes())
					_ = other.Set(path, v)
					miss(wire(nil, other, opts))
				}
				if private {
					other := ojson.NewDoc(d.Bytes())
					_ = other.Set("messages.1.content.0.thinking", "changed")
					miss(wire(nil, other, opts))
				}
				for _, meta := range []map[string]any{nil, {x.CanonicalSessionIDMetadataKey: "other", x.CallerScopeMetadataKey: "caller"}, {x.CanonicalSessionIDMetadataKey: "session", x.CallerScopeMetadataKey: "other"}} {
					o := opts
					o.Metadata = meta
					miss(wire(nil, d, o))
				}
				a.ID = "other"
				miss(wire(nil, d, opts))
				a.ID = "account"
				cfg := &config.Config{}
				cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: model}}, Params: map[string]any{"model": "retargeted"}}}
				j := wire(cfg, d, opts)
				miss(j)
				if j.Get("model").String() != "retargeted" {
					t.Fatal("payload model override lost")
				}
				cfg.Payload.Override[0].Params = map[string]any{"input.0.content.0.text": "configured"}
				j = wire(cfg, d, opts)
				miss(j)
				if j.Get("input.0.content.0.text").String() != "configured" {
					t.Fatal("payload history override lost")
				}
				// The shared translator projects both IDs to the same Claude tool_use ID.
				// Even when only one matches the translated wire, neither sibling is safe.
				up = strings.NewReplacer("fc_original", "fc_sibling", "call_A", "call.A").Replace(flat)
				if _, err := e.Execute(ctx, a, req, opts); err != nil {
					t.Fatal(err)
				}
				j = wire(nil, d, opts)
				miss(j)
				if strings.Contains(j.Raw, "fc_sibling") {
					t.Fatal("selected ambiguous sibling")
				}
			})
		}
	}
}

func TestCopilotClaudeResponsesReplayAcrossToolBatches(t *testing.T) {
	const model = "grok-4.6"
	const first = `[{"type":"reasoning","encrypted_content":"PRIVATE-first"},{"type":"function_call","id":"fc_a","call_id":"a","name":"lookup","arguments":"{}","status":"completed"},{"type":"function_call","id":"fc_b","call_id":"b","name":"lookup","arguments":"{}","status":"completed"}]`
	const second = `[{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"Checking."}]},{"type":"reasoning","encrypted_content":"PRIVATE-second","content":[{"type":"reasoning_text","text":"PRIVATE-unpublished"}]},{"type":"function_call","id":"fc_c","call_id":"c","name":"lookup","arguments":"{}","status":"completed"}]`
	e := NewCopilotExecutor(nil)
	a := copilotTestAuth(t, "account", model, copilot.Responses)
	opts := x.Options{SourceFormat: tr.FormatClaude, Metadata: map[string]any{x.CanonicalSessionIDMetadataKey: "session", x.CallerScopeMetadataKey: "caller"}}
	d := ojson.NewDocString(`{"messages":[{"role":"user","content":"test"}]}`)
	var output string
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Request: r, Body: io.NopCloser(strings.NewReader(`{"id":"r","model":"grok-4.6","status":"completed","output":` + output + "}"))}, nil
	}))
	prepare := func(doc *ojson.Doc) string {
		t.Helper()
		p, err := e.prepare(ctx, a, x.Request{Model: model, Payload: doc.Bytes()}, opts, false, false)
		if err != nil {
			t.Fatal(err)
		}
		return string(p.body)
	}
	for i, batch := range []string{first, second} {
		output = batch
		res, err := e.Execute(ctx, a, x.Request{Model: model, Payload: d.Bytes()}, opts)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(res.Payload, []byte("PRIVATE-")) {
			t.Fatal("private item exposed to Messages")
		}
		_ = d.SetRaw("messages.-1", `{"role":"assistant","content":`+ojson.GetBytesNoCopy(res.Payload, "content").Raw+"}")
		results := `[{"type":"tool_result","tool_use_id":"b","content":"two"},{"type":"tool_result","tool_use_id":"a","content":"one"}]`
		if i == 1 {
			results = `[{"type":"tool_result","tool_use_id":"c","content":"done"}]`
		}
		_ = d.SetRaw("messages.-1", `{"role":"user","content":`+results+"}")
	}
	w := ojson.Get(prepare(d), "input").Array()
	if len(w) != 10 {
		t.Fatalf("expected ten final input items, got %d", len(w))
	}
	for i, batch := range []string{first, second} {
		start := []int{1, 6}[i]
		for j, it := range ojson.Parse(batch).Array() {
			if !reflect.DeepEqual(w[start+j].Value(), it.Value()) {
				t.Fatalf("lost history batch %d item %d", i, j)
			}
		}
	}
	other := ojson.NewDoc(d.Bytes())
	_ = other.Set("messages.2.content.0.content", "different tool result")
	if strings.Contains(prepare(other), "PRIVATE-second") {
		t.Fatal("replayed second turn with changed parent results")
	}
	_ = other.Set("messages.2.content.0.tool_use_id", "a")
	if strings.Contains(prepare(other), "PRIVATE-") {
		t.Fatal("replayed with duplicate result IDs")
	}
	// Redaction belongs to the response format, never to cache availability.
	opts.Metadata = nil
	res, err := e.Execute(ctx, a, x.Request{Model: model, Payload: d.Bytes()}, opts)
	if err != nil || bytes.Contains(res.Payload, []byte("PRIVATE-")) {
		t.Fatal("redaction requires a cache hit")
	}
	opts.SourceFormat = tr.FormatOpenAIResponse
	res, err = e.Execute(ctx, a, x.Request{Model: model, Payload: []byte(`{"input":"test"}`)}, opts)
	if err != nil || ojson.GetBytesNoCopy(res.Payload, "output.1.encrypted_content").String() != "PRIVATE-second" {
		t.Fatal("native Responses output was redacted")
	}
}
