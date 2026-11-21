package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	x "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	tr "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestCopilotPlannedRouteCannotOutliveCatalog(t *testing.T) {
	e := NewCopilotExecutor(nil)
	a := copilotTestAuth(t, "account", "claude-opus-5", copilot.Chat)
	req := x.Request{Model: "claude-opus-5", Payload: []byte(`{"messages":[]}`)}
	opts := e.PlanRequestRoute(a, req, x.Options{SourceFormat: tr.FormatOpenAI})
	plan := opts.Metadata[copilotRouteKey].(copilotAttemptRoute)
	plan.expiresAt = time.Unix(1, 0)
	opts.Metadata[copilotRouteKey] = plan
	_, err := e.prepare(context.Background(), a, req, opts, false, false)
	var status statusErr
	if !errors.As(err, &status) || status.StatusCode() != 404 {
		t.Fatalf("expired route: %v", err)
	}
}

func TestCopilotIncrementalResponseRequiresStatefulRoute(t *testing.T) {
	e := NewCopilotExecutor(nil)
	opts := x.Options{SourceFormat: tr.FormatOpenAIResponse}
	for _, endpoint := range []string{copilot.Chat, copilot.Messages, copilot.Responses} {
		a := copilotTestAuth(t, "account", "claude-opus-5", endpoint)
		for _, input := range []string{`"another question"`, `[{"type":"function_call_output","call_id":"a","output":"result"}]`} {
			req := x.Request{Model: "claude-opus-5", Payload: []byte(`{"previous_response_id":"resp1","input":` + input + `}`)}
			for _, mode := range [][2]bool{{false, false}, {true, false}, {false, true}} {
				p, err := e.prepare(context.Background(), a, req, opts, mode[0], mode[1])
				var status statusErr
				if endpoint == copilot.Responses {
					if err != nil || ojson.GetBytesNoCopy(p.body, "previous_response_id").String() != "resp1" {
						t.Fatalf("native: %v", err)
					}
				} else if !errors.As(err, &status) || status.StatusCode() != 400 || !strings.Contains(err.Error(), "full conversation history") {
					t.Fatalf("%s: expected actionable 400, got %v", endpoint, err)
				}
			}
		}
	}
}

func TestCopilotResponsesHistoryForChat(t *testing.T) {
	const model = "gpt-5.3-codex"
	const output = `[{"type":"reasoning","id":"rs1","summary":[],"encrypted_content":"TEST-PRIVATE"},{"type":"message","id":"msg1","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"Checking."}]},{"type":"function_call","id":"fc1","call_id":"call_a","name":"sample","arguments":"{}"}]`
	const answer = `{"id":"resp1","model":"gpt-5.3-codex","status":"completed","output":` + output + `}`
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint("stream=", stream), func(t *testing.T) {
			e := NewCopilotExecutor(nil)
			a := copilotTestAuth(t, "account", model, copilot.Responses)
			opts := x.Options{SourceFormat: tr.FormatOpenAI, Metadata: map[string]any{x.CanonicalSessionIDMetadataKey: "session", x.CallerScopeMetadataKey: "caller"}}
			doc := ojson.NewDocString(`{"messages":[{"role":"user","content":"lookup"}]}`)
			upstream := answer
			if stream {
				var events strings.Builder
				for i, item := range ojson.Parse(output).Array() {
					fmt.Fprintf(&events, "data: {\"type\":\"response.output_item.done\",\"output_index\":%d,\"item\":%s}\n\n", i, item.Raw)
				}
				events.WriteString("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
				upstream = events.String()
			}
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(upstream)), Request: r}, nil
			}))
			req := x.Request{Model: model, Payload: doc.Bytes()}
			if stream {
				res, err := e.ExecuteStream(ctx, a, req, opts)
				if err != nil {
					t.Fatal(err)
				}
				for chunk := range res.Chunks {
					if chunk.Err != nil || bytes.Contains(chunk.Payload, []byte("TEST-PRIVATE")) {
						t.Fatal("invalid Chat SSE")
					}
				}
				_ = doc.SetRaw("messages.-1", `{"role":"assistant","content":"Checking.","tool_calls":[{"type":"function","id":"call_a","function":{"name":"sample","arguments":"{}"}}]}`)
			} else {
				res, err := e.Execute(ctx, a, req, opts)
				if err != nil {
					t.Fatal(err)
				}
				if bytes.Contains(res.Payload, []byte("TEST-PRIVATE")) {
					t.Fatal("encrypted reasoning was exposed to Chat")
				}
				_ = doc.SetRaw("messages.-1", ojson.GetBytesNoCopy(res.Payload, "choices.0.message").Raw)
			}
			_ = doc.SetRaw("messages.-1", `{"role":"tool","tool_call_id":"call_a","content":"result"}`)
			prepare := func(cfg *config.Config, options x.Options) ojson.Result {
				t.Helper()
				n := helps.PayloadConfigEvaluationCount()
				p, err := e.WithConfig(cfg).prepare(ctx, a, x.Request{Model: model, Payload: doc.Bytes()}, options, stream, false)
				if err != nil {
					t.Fatal(err)
				}
				if helps.PayloadConfigEvaluationCount() != n+1 {
					t.Fatal("payload evaluated more than once")
				}
				return ojson.ParseBytes(p.body)
			}
			wire := prepare(nil, opts)
			items := wire.Get("input").Array()
			if len(items) != 5 || items[1].Raw != ojson.Parse(output).Get("0").Raw || items[2].Get("phase").String() != "commentary" || items[3].Get("call_id").String() != "call_a" || items[4].Get("output").String() != "result" {
				t.Fatal("missing or reordered Responses items")
			}
			// The last semantic stage can filter/replace replayed items, or send a
			// different model without inheriting the first one's private state.
			match := []config.PayloadModelRule{{Name: model}}
			cfg := &config.Config{}
			cfg.Payload.Filter = []config.PayloadFilterRule{{Models: match, Params: []string{"input.1.encrypted_content"}}}
			cfg.Payload.Override = []config.PayloadRule{{Models: match, Params: map[string]any{"input.2.phase": "final_answer"}}}
			wire = prepare(cfg, opts)
			if wire.Get("input.1.encrypted_content").Exists() || wire.Get("input.2.phase").String() != "final_answer" {
				t.Fatal("lost final payload edits")
			}
			cfg.Payload.Filter = nil
			cfg.Payload.Override[0].Params = map[string]any{"model": "mai-code-2"}
			wire = prepare(cfg, opts)
			if wire.Get("model").String() != "mai-code-2" || strings.Contains(wire.Raw, "TEST-PRIVATE") {
				t.Fatal("replay followed a model override")
			}
			cfg.Payload.Override[0].Models = []config.PayloadModelRule{{Name: "unrelated"}}
			if !strings.Contains(prepare(cfg, opts).Raw, "TEST-PRIVATE") {
				t.Fatal("an unrelated model rule disabled replay")
			}
			cfg.Payload.Replace = []config.PayloadReplaceRule{{Models: match, Target: config.PayloadReplaceTarget{Paths: []string{"model"}}, Replacements: []config.PayloadReplacement{{model: "mai-code-2"}}}}
			wire = prepare(cfg, opts)
			if wire.Get("model").String() != "mai-code-2" || strings.Contains(wire.Raw, "TEST-PRIVATE") {
				t.Fatal("replay followed a model replacement")
			}
			other := opts
			other.Metadata = map[string]any{x.CanonicalSessionIDMetadataKey: "session", x.CallerScopeMetadataKey: "other"}
			if strings.Contains(prepare(nil, other).Raw, "TEST-PRIVATE") {
				t.Fatal("cross-caller replay")
			}
			_ = doc.Set("messages.1.tool_calls.0.function.arguments", `{"changed":true}`)
			if strings.Contains(prepare(nil, opts).Raw, "TEST-PRIVATE") {
				t.Fatal("different call inherited replay")
			}
		})
	}
}

func TestCopilotResponsesPromptAndStrictTools(t *testing.T) {
	for _, model := range []string{"gpt-5.3-codex", "gpt-6-sol"} {
		for _, src := range []tr.Format{tr.FormatClaude, tr.FormatOpenAI} {
			e := NewCopilotExecutor(nil)
			a := copilotTestAuth(t, "account", model, copilot.Responses)
			doc := ojson.NewDocString(`{"messages":[{"role":"user","content":"hello"}]}`)
			if src == tr.FormatClaude {
				_ = doc.Set("system", "policy")
				_ = doc.SetRaw("tools", `[{"name":"a","strict":true,"input_schema":{"type":"object","properties":{},"additionalProperties":false}},{"name":"b","input_schema":{"type":"object"}}]`)
			} else {
				_ = doc.SetRaw("messages", `[{"role":"system","content":"policy"},{"role":"developer","content":"explicit developer"},{"role":"user","content":"hello"},{"role":"system","content":"later"}]`)
			}
			prep := func() ojson.Result {
				p, err := e.prepare(context.Background(), a, x.Request{Model: model, Payload: doc.Bytes()}, x.Options{SourceFormat: src}, false, false)
				if err != nil {
					t.Fatal(err)
				}
				return ojson.ParseBytes(p.body)
			}
			w := prep()
			i := 0
			if model == "gpt-6-sol" {
				i++
				if w.Get("input.0.role").String() != "system" || w.Get("instructions").Exists() {
					t.Fatal("wrong system role")
				}
			} else if w.Get("instructions").String() != "policy" {
				t.Fatal("missing instructions")
			}
			if src == tr.FormatOpenAI {
				if w.Get(fmt.Sprintf("input.%d.role", i)).String() != "developer" || !strings.Contains(w.Raw, "later") {
					t.Fatal("moved an explicit developer or mid-conversation prompt")
				}
			} else if w.Get("tools.0.strict").Type != ojson.True || w.Get("tools.1.strict").Type != ojson.False || w.Get("tool_choice").Exists() {
				t.Fatal("incorrect strict or tool_choice defaults")
			}
			_ = doc.Set("instructions", "explicit instructions")
			if prep().Get("instructions").String() != "explicit instructions" {
				t.Fatal("overwrote explicit instructions")
			}
			if src == tr.FormatClaude {
				_ = doc.SetRaw("tool_choice", `{"type":"tool","name":"a"}`)
				if prep().Get("tool_choice.name").String() != "a" {
					t.Fatal("lost explicit tool choice")
				}
			}
		}
	}
}
