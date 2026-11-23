package executor

import (
	"context"
	"os"
	"reflect"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	x "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	tr "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// Business bodies captured from the official CLI runtime, with a fixed clock.
// Date and system instructions are client input, not proxy-injected defaults.
func TestCopilotCLIFirstTurnGolden(t *testing.T) {
	b, err := os.ReadFile("testdata/copilot/cli_1_0_114_first_turn.json")
	if err != nil {
		t.Fatal(err)
	}
	var captures map[string]json.RawMessage
	if err = json.Unmarshal(b, &captures); err != nil {
		t.Fatal(err)
	}
	const system = "Reply with exactly the label requested. No tools or explanation."
	const user = "<current_datetime>2026-11-20T12:00:00Z</current_datetime>\n\nReturn exactly PARITY-27."
	for _, tc := range []struct{ model, endpoint string }{{"gemini-4-hydrogen", copilot.Chat}, {"claude-haiku-5.5", copilot.Messages}, {"mai-code-2-flash", copilot.Responses}} {
		for _, from := range []tr.Format{tr.FormatOpenAI, tr.FormatClaude, tr.FormatOpenAIResponse} {
			t.Run(tc.model+"/"+from.String(), func(t *testing.T) {
				e := NewCopilotExecutor(nil)
				a := copilotTestAuth(t, "parity", tc.model, tc.endpoint)
				opts := x.Options{SourceFormat: from}
				src := ojson.NewDocString("{}")
				_ = src.Set("model", tc.model)
				fn := map[string]any{"name": "probe_lookup", "description": "No-op test tool."}
				schema := map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
				switch from {
				case tr.FormatOpenAI:
					_ = src.Set("messages", []any{map[string]string{"role": "system", "content": system}, map[string]string{"role": "user", "content": user}})
					fn["parameters"] = schema
					_ = src.Set("tools", []any{map[string]any{"type": "function", "function": fn}})
				case tr.FormatClaude:
					_ = src.Set("system", system)
					_ = src.Set("messages", []any{map[string]string{"role": "user", "content": user}})
					fn["input_schema"] = schema
					_ = src.Set("tools", []any{fn})
				case tr.FormatOpenAIResponse:
					_ = src.Set("instructions", system)
					_ = src.Set("input", []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]string{"type": "input_text", "text": user}}}})
					fn["type"] = "function"
					fn["parameters"] = schema
					_ = src.Set("tools", []any{fn})
				}
				expected := ojson.NewDoc(captures[tc.model])
				check := func(stream bool) {
					t.Helper()
					p, err := e.prepare(context.Background(), a, x.Request{Model: tc.model, Payload: src.Bytes()}, opts, stream, false)
					if err != nil {
						t.Fatal(err)
					}
					var got, want any
					if err = json.Unmarshal(p.body, &got); err != nil {
						t.Fatal(err)
					}
					if err = json.Unmarshal(expected.Bytes(), &want); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("wire mismatch\n got: %s\nwant: %s", p.body, expected.String())
					}
				}
				check(true)
				_ = src.Delete("tools")
				if tc.endpoint == copilot.Responses {
					_ = expected.SetRaw("tools", "[]")
					_ = expected.Delete("parallel_tool_calls")
				} else {
					_ = expected.Delete("tools")
					_ = expected.Delete("tool_choice")
				}
				check(true)
				// The CLI keeps the Haiku upstream streaming even for a buffered SDK session.
				if tc.endpoint != copilot.Messages {
					_ = expected.Delete("stream")
					_ = expected.Delete("stream_options")
					check(false)
					_ = src.Set("stream", false)
					_ = expected.Set("stream", false)
					check(false)
				}
			})
		}
	}
}

func TestCopilotCLIDefaultsDoNotOverrideIdentityOrToolRules(t *testing.T) {
	const model = "claude-haiku-5.5"
	a := copilotTestAuth(t, "parity", model, copilot.Messages)
	e := NewCopilotExecutor(nil)
	d := ojson.NewDocString(`{"messages":[{"role":"user","content":"hi"}]}`)
	opts := x.Options{SourceFormat: tr.FormatOpenAI}
	for _, key := range []string{"user", "metadata.user_id"} {
		_ = d.Set(key, "explicit")
		p, err := e.prepare(context.Background(), a, x.Request{Model: model, Payload: d.Bytes()}, opts, true, false)
		if err != nil || ojson.GetBytesNoCopy(p.body, "metadata.user_id").String() != "explicit" {
			t.Fatalf("lost explicit %s: %v", key, err)
		}
		_ = d.Delete(key)
	}
	cfg := &config.Config{}
	cfg.Payload.Default = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: model}}, Params: map[string]any{"metadata.user_id": "configured"}}}
	p, err := e.WithConfig(cfg).prepare(context.Background(), a, x.Request{Model: model, Payload: d.Bytes()}, opts, true, false)
	if err != nil || ojson.GetBytesNoCopy(p.body, "metadata.user_id").String() != "configured" {
		t.Fatalf("synthetic identity masked payload.default: %v", err)
	}
	const mai = "mai-code-2-flash"
	a = copilotTestAuth(t, "parity", mai, copilot.Responses)
	opts.SourceFormat = tr.FormatOpenAIResponse
	req := x.Request{Model: mai, Payload: []byte(`{"input":"hi","tools":[{"type":"function","name":"f","strict":true},{"type":"web_search"}]}`)}
	p, err = e.prepare(context.Background(), a, req, opts, true, false)
	if err != nil || !ojson.GetBytesNoCopy(p.body, "tools.0.strict").Bool() || ojson.GetBytesNoCopy(p.body, "tools.1.strict").Exists() {
		t.Fatal("changed explicit or built-in tool strictness")
	}
	req.Payload = []byte(`{"input":"next","previous_response_id":"resp_1"}`)
	p, err = e.prepare(context.Background(), a, req, opts, true, false)
	if err != nil || ojson.GetBytesNoCopy(p.body, "tools").Exists() {
		t.Fatal("incremental request acquired an explicit empty tool catalog")
	}
}
