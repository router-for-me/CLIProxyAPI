package helps

// Devin (Cognition / Windsurf) streams a StopReason varint in field #5 of each
// GetChatMessage response frame. The values follow the
// exa.codeium_common_pb.StopReason enum used by the Windsurf extension:
//
//	 0 UNSPECIFIED     -> natural end (no signal)
//	 1 INCOMPLETE      -> request cut short, model wanted more
//	 2 STOP_PATTERN    -> model emitted its stop sequence (normal end)
//	 3 MAX_TOKENS      -> output token budget exhausted
//	 4-9 internal      -> treated as normal end
//	10 FUNCTION_CALL   -> model wants a tool invoked
//	11 CONTENT_FILTER  -> blocked by safety filter
//	12 NON_INSERTION   -> normal end
//	13 ERROR           -> errors arrive via the Connect trailer, not this field
const (
	DevinStopReasonUnspecified   uint64 = 0
	DevinStopReasonIncomplete    uint64 = 1
	DevinStopReasonStopPattern   uint64 = 2
	DevinStopReasonMaxTokens     uint64 = 3
	DevinStopReasonFunctionCall  uint64 = 10
	DevinStopReasonContentFilter uint64 = 11
)

// Normalized stop reasons carried on interactions payloads (interaction.stop_reason).
// They intentionally mirror OpenAI chat-completions finish_reason vocabulary so
// downstream translators can map them without a second lookup table.
const (
	InteractionsStopReasonStop          = "stop"
	InteractionsStopReasonLength        = "length"
	InteractionsStopReasonToolCalls     = "tool_calls"
	InteractionsStopReasonContentFilter = "content_filter"
)

// Interaction status values used on interactions payloads (interaction.status).
const (
	InteractionsStatusCompleted  = "completed"
	InteractionsStatusIncomplete = "incomplete"
)

// DevinStopReasonToInteractions maps a raw Devin StopReason enum value to the
// interactions-format status and normalized stop_reason.
func DevinStopReasonToInteractions(v uint64) (status string, stopReason string) {
	switch v {
	case DevinStopReasonIncomplete, DevinStopReasonMaxTokens:
		return InteractionsStatusIncomplete, InteractionsStopReasonLength
	case DevinStopReasonFunctionCall:
		return InteractionsStatusCompleted, InteractionsStopReasonToolCalls
	case DevinStopReasonContentFilter:
		return InteractionsStatusCompleted, InteractionsStopReasonContentFilter
	default:
		return InteractionsStatusCompleted, InteractionsStopReasonStop
	}
}
