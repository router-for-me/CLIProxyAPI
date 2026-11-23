package helps

import (
	"net/http"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/copilot"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestCopilotTurnInitiator(t *testing.T) {
	for _, tc := range []struct {
		body   string
		format translator.Format
		want   string
	}{
		{`{"contents":[{"role":"user","parts":[{"functionResponse":{"name":"lookup","response":{}}}]}]}`, translator.FormatGemini, "agent"},
		{`{"contents":[{"role":"user","parts":[{"functionResponse":{}},{"text":"New user input"}]}]}`, translator.FormatGemini, "user"},
		{`{"input":[{"type":"function_result","result":{}}]}`, translator.FormatInteractions, "agent"},
	} {
		if got := ParseCopilotTurnInput([]byte(tc.body), tc.format).Initiator; got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.format, got, tc.want)
		}
	}
}

func TestCopilotStreamedToolTurns(t *testing.T) {
	for _, tc := range []struct {
		format              translator.Format
		event, continuation string
	}{
		{translator.FormatOpenAI, `{"choices":[{"delta":{"tool_calls":[{"id":"call-1"}]}}]}`, `{"messages":[{"role":"tool","tool_call_id":"call-1","content":"ok"}]}`},
		{translator.FormatClaude, `{"type":"content_block_start","content_block":{"type":"tool_use","id":"call-1"}}`, `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-1"}]}]}`},
		{translator.FormatOpenAIResponse, `{"type":"response.output_item.added","item":{"type":"function_call","call_id":"call-1"}}`, `{"input":[{"type":"function_call_output","call_id":"call-1"}]}`},
		{translator.FormatOpenAIResponse, `{"type":"response.completed","response":{"id":"resp-1"}}`, `{"previous_response_id":"resp-1","input":[{"type":"function_call_output","call_id":"unknown"}]}`},
	} {
		t.Run(tc.format.String(), func(t *testing.T) {
			links := NewCopilotTurnLinks()
			opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.CanonicalSessionIDMetadataKey: "session", cliproxyexecutor.CallerScopeMetadataKey: "caller"}}
			first := links.Resolve("account", CopilotTurnInput{Initiator: "user"}, opts, tc.format, ojson.Result{})
			links.Observe(first, tc.format, ojson.Parse(tc.event))
			replay := ojson.Parse(tc.continuation)
			// Starting another user turn must neither reuse nor invalidate the first one.
			next := links.Resolve("account", CopilotTurnInput{Initiator: "user"}, opts, tc.format, replay)
			if first.session != next.session || first.interaction == next.interaction || first.task == next.task {
				t.Fatal("user turns must share only the session ID")
			}
			if got := links.Resolve("account", CopilotTurnInput{Initiator: "agent"}, opts, tc.format, replay); got != first {
				t.Fatal("continuation did not resolve its own originating turn")
			}
			if got := links.Resolve("another-account", CopilotTurnInput{Initiator: "agent"}, opts, tc.format, replay); got.session == first.session || got.interaction == first.interaction {
				t.Fatal("account scopes collided")
			}
			links.Observe(next, tc.format, ojson.Parse(tc.event))
			if got := links.Resolve("account", CopilotTurnInput{Initiator: "agent"}, opts, tc.format, replay); got == first || got == next {
				t.Fatal("an ambiguous upstream ID was linked to the wrong turn")
			}
			opts.Metadata[cliproxyexecutor.CallerScopeMetadataKey] = "another-caller"
			if got := links.Resolve("account", CopilotTurnInput{Initiator: "agent"}, opts, tc.format, replay); got.session == first.session || got.interaction == first.interaction {
				t.Fatal("caller scopes collided")
			}
		})
	}
}

func TestCopilotClaudeImageRelayRequiresMatchingToolResult(t *testing.T) {
	const result = `{"type":"tool_result","tool_use_id":"call-a","content":[{"type":"image"}]}`
	const tool = `{"role":"tool","tool_call_id":"call-a","content":"ok"}`
	var generated CopilotTurnInput
	media := ojson.NewDocString(`{"messages":[{"role":"tool","tool_call_id":"call-a","content":[{"type":"image_url"}]}]}`)
	generated.RelayChatToolMedia(media)
	relay := media.Get("messages.1").Raw
	links := NewCopilotTurnLinks()
	opts := cliproxyexecutor.Options{}
	first := links.Resolve("account", CopilotTurnInput{Initiator: "user"}, opts, translator.FormatOpenAI, ojson.Result{})
	links.Observe(first, translator.FormatOpenAI, ojson.Parse(`{"choices":[{"message":{"tool_calls":[{"id":"call-a"},{"id":"call-b"}]}}]}`))
	other := links.Resolve("account", CopilotTurnInput{Initiator: "user"}, opts, translator.FormatOpenAI, ojson.Result{})
	links.Observe(other, translator.FormatOpenAI, ojson.Parse(`{"choices":[{"message":{"tool_calls":[{"id":"call-c"}]}}]}`))
	const resultB = `{"type":"tool_result","tool_use_id":"call-b","content":"ok"}`
	const toolB = `{"role":"tool","tool_call_id":"call-b"}`
	for _, tc := range []struct {
		name, content, messages string
		reuse                   bool
	}{
		{"matching result", result, tool + "," + relay, true},
		{"reordered exact batch", resultB + "," + result, tool + "," + toolB + "," + relay, true},
		{"incomplete batch", resultB + "," + result, tool + "," + relay, false},
		{"extra wire result", result, toolB + "," + tool + "," + relay, false},
		{"duplicate wire ID", resultB + "," + result, tool + "," + tool + "," + relay, false},
		{"duplicate source ID", result + "," + result, tool + "," + tool + "," + relay, false},
		{"different parents", `{"type":"tool_result","tool_use_id":"call-c"},` + result, `{"role":"tool","tool_call_id":"call-c"},` + tool + "," + relay, false},
		{"unknown parent", `{"type":"tool_result","tool_use_id":"unknown"},` + result, `{"role":"tool","tool_call_id":"unknown"},` + tool + "," + relay, false},
		{"unverified trailing reminder", result, tool + "," + relay + `,{"role":"user","content":[{"type":"text","text":"<system-reminder>\nunrelated\n</system-reminder>"}]}`, false},
		{"wire result changed", result, `{"role":"tool","tool_call_id":"call-b"},` + relay, false},
		{"wire result removed", result, relay, false},
		{"old matching result not adjacent", result, tool + `,{"role":"assistant","content":"done"},` + relay, false},
		{"tool result with fresh user image", result + `,{"type":"image"}`, tool + "," + relay, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := ParseCopilotTurnInput([]byte(`{"messages":[{"role":"user","content":[`+tc.content+`]}]}`), translator.FormatClaude)
			// Record the relay from an actual tool result, not from the candidate wire history.
			input.chatRelays = generated.chatRelays
			got := links.Resolve("account", input, opts, translator.FormatOpenAI, ojson.Parse(`{"messages":[`+tc.messages+`]}`))
			if (got == first) != tc.reuse || got == other {
				t.Errorf("turn reuse = %t, want %t", got == first, tc.reuse)
			}
		})
	}
}

func TestCopilotRecycledToolIDsUseUserHistory(t *testing.T) {
	links := NewCopilotTurnLinks()
	opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.CanonicalSessionIDMetadataKey: "session"}}
	format := translator.FormatOpenAI
	call := `{"choices":[{"message":{"tool_calls":[{"id":"recycled"}]}}]}`
	first := `{"role":"user","content":"again"}`
	result := `{"role":"tool","tool_call_id":"recycled","content":"ok"}`
	assistant := `{"role":"assistant","tool_calls":[{"id":"recycled"}]}`
	second := first + "," + assistant + "," + result + "," + first
	resolve := func(history string) CopilotTurn {
		body := `{"messages":[` + history + `]}`
		return links.Resolve("account", ParseCopilotTurnInput([]byte(body), format), opts, format, ojson.Parse(body))
	}
	a := resolve(first)
	links.Observe(a, format, ojson.Parse(call))
	b := resolve(second)
	links.Observe(b, format, ojson.Parse(call))
	if a.interaction == b.interaction {
		t.Fatal("user turns were merged")
	}
	for _, tc := range []struct {
		history string
		want    CopilotTurn
	}{{second, b}, {first, a}} {
		if got := resolve(tc.history + "," + assistant + "," + result); got != tc.want {
			t.Fatal("reused call ID was not linked to its own user turn")
		}
	}
	if got := resolve(result); got == a || got == b {
		t.Fatal("an ambiguous history-free continuation was attributed to another turn")
	}
}

func TestCopilotCLIHeadersUseAccountScopedSessionsAndFeatureBetas(t *testing.T) {
	opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.CanonicalSessionIDMetadataKey: "incoming-session"}}
	route := copilot.Route{Format: translator.FormatClaude, AnthropicBetas: []string{"thinking-binding-controls-2026-08-01"}}
	links := NewCopilotTurnLinks()
	headers := make(http.Header)
	SetCopilotInferenceHeaders(headers, route, ojson.Parse(`{"thinking":{"type":"adaptive","block_binding":{"prefix_mismatch_behavior":"drop_block"}}}`), "agent", links.Resolve("account-a", CopilotTurnInput{Initiator: "agent"}, opts, route.Format, ojson.Result{}))
	if headers.Get("Anthropic-Beta") != "thinking-binding-controls-2026-08-01" || headers.Get("X-Interaction-Type") != "conversation-agent" || headers.Get("Openai-Intent") != "" {
		t.Fatal("incorrect CLI Messages headers")
	}
	same := make(http.Header)
	SetCopilotInferenceHeaders(same, route, ojson.Parse(`{"thinking":{"type":"adaptive"}}`), "user", links.Resolve("account-a", CopilotTurnInput{Initiator: "user"}, opts, route.Format, ojson.Result{}))
	other := make(http.Header)
	SetCopilotInferenceHeaders(other, route, ojson.Parse(`{"thinking":{"type":"enabled"}}`), "user", links.Resolve("account-b", CopilotTurnInput{Initiator: "user"}, opts, route.Format, ojson.Result{}))
	if same.Get("Anthropic-Beta") != "" || other.Get("Anthropic-Beta") != "" || same.Get("X-Client-Session-Id") != headers.Get("X-Client-Session-Id") || other.Get("X-Client-Session-Id") == same.Get("X-Client-Session-Id") || same.Get("X-Client-Session-Id") == "incoming-session" {
		t.Fatal("unexpected message beta or session identity")
	}
}
