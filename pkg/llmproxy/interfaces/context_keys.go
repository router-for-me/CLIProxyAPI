package interfaces

// ContextKey is a custom type for context keys to avoid collisions.
type ContextKey string

const (
	ContextKeyGin        ContextKey = "gin"
	ContextKeyHandler    ContextKey = "handler"
	ContextKeyRequestID  ContextKey = "request_id"
	ContextKeyRoundRobin ContextKey = "cliproxy.roundtripper"
	ContextKeyAlt        ContextKey = "alt"
)
