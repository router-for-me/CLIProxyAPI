// Package events implements a bounded in-memory ring buffer for structured
// events emitted by the runtime (routing decisions, breaker trips, cooldown
// waits, etc.). The buffer is count-bounded (not byte-bounded), non-blocking
// on the producer side, and emits a dropped counter when contention forces a
// skip so callers can surface the loss via /v0/management/events/stats.
//
// Consumers are the /v0/management/events GET endpoint, the
// /v0/management/events/stream SSE endpoint, and the dashboard live-events tab.
package events

import (
	"encoding/json"
	"time"
)

// Event is a structured event recorded by the system. The Payload field
// is opaque JSON; specific event types (routing.decision, breaker.tripped,
// etc.) define their own payload shape.
type Event struct {
	Type      string          `json:"type"`
	Ts        time.Time       `json:"ts"`
	RequestID string          `json:"request_id,omitempty"`
	Model     string          `json:"model,omitempty"`
	AuthID    string          `json:"auth_id,omitempty"`
	Channel   string          `json:"channel,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}
