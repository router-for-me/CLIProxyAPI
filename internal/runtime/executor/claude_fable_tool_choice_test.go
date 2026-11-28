package executor

import (
	"reflect"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// Claude Fable 5.1 and 5.2 reject forced tool use like Claude 5.5:
// 400 "tool_choice: type "tool" and "any" are not supported for this model".
func TestClaudeExecutor_FableForcedToolChoiceBoundaries(t *testing.T) {
	const forced = `{"type":"tool","name":"lookup_weather","disable_parallel_tool_use":true}`
	const auto = `{"type":"auto","disable_parallel_tool_use":true}`
	for _, tc := range []struct {
		name, route, choice, want string
	}{
		{name: "fable 5.1", route: "claude-fable-5-1", choice: forced, want: auto},
		{name: "fable 5.2", route: "claude-fable-5-2", choice: forced, want: auto},
		{name: "fable 5.2 any", route: "claude-fable-5-2", choice: `{"type":"any"}`, want: `{"type":"auto"}`},
		{name: "fable 5.2 1m", route: "claude-fable-5-2[1m]", choice: forced, want: auto},
		{name: "fable 5.2 thinking suffix", route: "claude-fable-5-2(high)", choice: forced, want: auto},
		{name: "fable 5.1 variant", route: "claude-fable-5-1-max", choice: forced, want: auto},
		{name: "fable 5.2 date", route: "claude-fable-5-2-20261101", choice: forced, want: auto},
		{name: "fable 5.1 dotted", route: "claude-fable-5.1", choice: forced, want: auto},
		{name: "fable 5.2 dotted", route: "claude-fable-5.2", choice: forced, want: auto},
		{name: "fable 5.2 dotted 1m", route: "claude-fable-5.2[1m]", choice: forced, want: auto},
		{name: "fable 5.2 dotted date", route: "claude-fable-5.2-20261101", choice: forced, want: auto},
		{name: "provider-qualified fable 5.1", route: "anthropic/claude-fable-5-1", choice: forced, want: auto},
		{name: "provider-qualified dotted fable 5.2", route: "anthropic/claude-fable-5.2", choice: forced, want: auto},
		{name: "provider-qualified opus 5.5", route: "anthropic/claude-opus-5-5", choice: forced, want: auto},
		{name: "provider-qualified fable 5 keeps forced", route: "anthropic/claude-fable-5", choice: forced, want: forced},
		{name: "fable 5 keeps forced", route: "claude-fable-5", choice: forced, want: forced},
		{name: "fable 5 1m keeps forced", route: "claude-fable-5[1m]", choice: forced, want: forced},
		{name: "near match 5.10", route: "claude-fable-5-10", choice: forced, want: forced},
		{name: "near match 5.20", route: "claude-fable-5-20", choice: forced, want: forced},
		{name: "fable 5.3 not assumed", route: "claude-fable-5-3", choice: forced, want: forced},
		{name: "dotted near match 5.10", route: "claude-fable-5.10", choice: forced, want: forced},
		{name: "dotted near match 5.20", route: "claude-fable-5.20", choice: forced, want: forced},
		{name: "dotted fable 5.3 not assumed", route: "claude-fable-5.3", choice: forced, want: forced},
		{name: "fable 5.2 auto", route: "claude-fable-5-2", choice: auto, want: auto},
		{name: "fable 5.2 none", route: "claude-fable-5-2", choice: `{"type":"none"}`, want: `{"type":"none"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := captureClaudeIssue7000(t, NewClaudeExecutor(&config.Config{}), &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-fable-tool-choice"}}, cliproxyexecutor.Request{Model: tc.route, Payload: claudeIssue7000Payload(tc.route, tc.choice)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}, "Execute")
			if got, want := ojson.GetBytes(body, "tool_choice").Value(), ojson.Parse(tc.want).Value(); !reflect.DeepEqual(got, want) {
				t.Fatalf("tool_choice = %#v, want %#v; body=%s", got, want, body)
			}
		})
	}
}

// Messages, streaming and count_tokens all send the downgraded choice, so a
// token count matches the request that is actually sent.
func TestClaudeExecutor_FableForcedToolChoiceEveryPath(t *testing.T) {
	const forced = `{"type":"tool","name":"lookup_weather","disable_parallel_tool_use":true}`
	for _, model := range []string{"claude-fable-5-1", "claude-fable-5-2"} {
		for _, method := range []string{"Execute", "ExecuteStream", "CountTokens"} {
			t.Run(model+"/"+method, func(t *testing.T) {
				body, _ := captureClaudeIssue7000(t, NewClaudeExecutor(&config.Config{}), &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-fable-tool-choice"}}, cliproxyexecutor.Request{Model: model, Payload: claudeIssue7000Payload(model, forced)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}, method)
				if got := ojson.GetBytes(body, "tool_choice.type").String(); got != "auto" {
					t.Fatalf("tool_choice.type = %q, want auto; body=%s", got, body)
				}
				if ojson.GetBytes(body, "tool_choice.name").Exists() {
					t.Fatalf("forced tool name survived: %s", body)
				}
				if !ojson.GetBytes(body, "tool_choice.disable_parallel_tool_use").Bool() {
					t.Fatalf("disable_parallel_tool_use dropped: %s", body)
				}
			})
		}
	}
}
