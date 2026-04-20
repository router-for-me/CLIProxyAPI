package executor

// ResponsesMode represents the runtime capability of an upstream provider
// regarding the OpenAI Responses API (/v1/responses).
type ResponsesMode int

const (
	// ResponsesModeUnknown means the upstream has not been probed yet.
	// The first request will attempt native /responses and fall back on capability errors.
	ResponsesModeUnknown ResponsesMode = iota

	// ResponsesModeNative means the upstream supports /responses natively.
	// Requests are forwarded directly without translation.
	ResponsesModeNative

	// ResponsesModeChatFallback means the upstream only supports /chat/completions.
	// Responses requests are translated and state is maintained locally.
	ResponsesModeChatFallback
)

func (m ResponsesMode) String() string {
	switch m {
	case ResponsesModeNative:
		return "native"
	case ResponsesModeChatFallback:
		return "chat_fallback"
	default:
		return "unknown"
	}
}
