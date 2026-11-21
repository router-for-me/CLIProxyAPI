package common

// InlineChatToolMediaKey asks Chat request translators to keep media on its
// originating tool result. Copilot then emits its own per-image user messages.
type InlineChatToolMediaKey struct{}
