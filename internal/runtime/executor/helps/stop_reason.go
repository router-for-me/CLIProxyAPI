package helps

import (
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// claudeStopReasons maps Anthropic Messages stop_reason values.
var claudeStopReasons = map[string]usage.StopReason{
	"end_turn":                      usage.StopReasonEndTurn,
	"stop_sequence":                 usage.StopReasonEndTurn,
	"tool_use":                      usage.StopReasonToolUse,
	"max_tokens":                    usage.StopReasonMaxTokens,
	"model_context_window_exceeded": usage.StopReasonMaxTokens,
	"refusal":                       usage.StopReasonRefusal,
	"pause_turn":                    usage.StopReasonPause,
}

// responsesIncompleteReasons maps OpenAI Responses incomplete_details.reason values.
var responsesIncompleteReasons = map[string]usage.StopReason{
	"max_output_tokens": usage.StopReasonMaxTokens,
	"content_filter":    usage.StopReasonRefusal,
}

// claudeStopReason reads a Messages response, or a message_delta event, which
// carries the reason under delta.
func claudeStopReason(root ojson.Result) usage.StopReason {
	wire := root.Get("delta.stop_reason").String()
	if wire == "" {
		wire = root.Get("stop_reason").String()
	}
	return claudeStopReasons[wire]
}

// responsesStopReason reads a terminal Responses event. Responses has no stop
// reason for tool calls, so a completed response stopped for a tool when its
// output asks the client to run one.
func responsesStopReason(root ojson.Result, detail usage.Detail) usage.StopReason {
	switch root.Get("response.status").String() {
	case "completed":
		if tools := detail.ToolUsageDetails; tools != nil && tools.ToolCallsRequested != nil && *tools.ToolCallsRequested > 0 {
			return usage.StopReasonToolUse
		}
		return usage.StopReasonEndTurn
	case "incomplete":
		return responsesIncompleteReasons[root.Get("response.incomplete_details.reason").String()]
	default:
		return ""
	}
}
