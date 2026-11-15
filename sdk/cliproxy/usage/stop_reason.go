package usage

// StopReason is why an upstream response ended, in one vocabulary for every
// protocol. The zero value means the reason is unknown.
type StopReason string

const (
	// StopReasonEndTurn means the model finished its reply.
	StopReasonEndTurn StopReason = "end_turn"
	// StopReasonToolUse means the model stopped to ask the client to run a tool.
	StopReasonToolUse StopReason = "tool_use"
	// StopReasonMaxTokens means the reply hit the output or context limit.
	StopReasonMaxTokens StopReason = "max_tokens"
	// StopReasonRefusal means the model or a content filter declined to answer.
	StopReasonRefusal StopReason = "refusal"
	// StopReasonPause means the upstream paused a long server-side turn for the
	// client to continue.
	StopReasonPause StopReason = "pause"
)
