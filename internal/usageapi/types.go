// Package usageapi normalizes upstream quota limits into the usage report
// shape used by agent clients. Reports contain limit windows only: no
// credential identifiers, tokens, or request history are exposed.
package usageapi

import "encoding/json"

// Response is the wire response for the user-facing usage endpoint.
// It intentionally matches the aggregate usage schema used by agent clients:
// exactly generatedAt plus reports, with no additional top-level fields.
type Response struct {
	GeneratedAt int64    `json:"generatedAt"`
	Reports     []Report `json:"reports"`
}

// Report describes the usage limits of one credential.
type Report struct {
	Provider  string         `json:"provider"`
	FetchedAt int64          `json:"fetchedAt"`
	Limits    []Limit        `json:"limits"`
	Notes     []string       `json:"notes,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// Limit is one quota window or bucket.
type Limit struct {
	ID     string  `json:"id"`
	Label  string  `json:"label"`
	Scope  Scope   `json:"scope"`
	Window *Window `json:"window,omitempty"`
	Amount Amount  `json:"amount"`
	Status string  `json:"status,omitempty"`
}

// Scope identifies what a limit applies to. Account, project, and organization
// identifiers are deliberately never populated.
type Scope struct {
	Provider string `json:"provider"`
	ModelID  string `json:"modelId,omitempty"`
	Tier     string `json:"tier,omitempty"`
	WindowID string `json:"windowId,omitempty"`
}

// Window describes a limit reset window.
type Window struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	DurationMs *int64 `json:"durationMs,omitempty"`
	ResetsAt   int64  `json:"resetsAt,omitempty"`
}

// Amount carries quantitative usage data. Percent amounts use the 0..100
// scale for used, limit, and remaining, consistent with agent-client usage
// renderers.
type Amount struct {
	Used              *float64 `json:"used,omitempty"`
	Limit             *float64 `json:"limit,omitempty"`
	Remaining         *float64 `json:"remaining,omitempty"`
	UsedFraction      *float64 `json:"usedFraction,omitempty"`
	RemainingFraction *float64 `json:"remainingFraction,omitempty"`
	Unit              string   `json:"unit"`
}

// MarshalJSON keeps metadata values JSON-safe without changing the report
// shape. It exists so callers never need to post-process reports.
func (r Report) MarshalJSON() ([]byte, error) {
	type plain Report
	if len(r.Metadata) == 0 {
		return json.Marshal(plain(r))
	}
	safe := make(map[string]any, len(r.Metadata))
	for key, value := range r.Metadata {
		switch value.(type) {
		case string, float64, int, int64, bool:
			safe[key] = value
		}
	}
	copyReport := plain(r)
	copyReport.Metadata = safe
	return json.Marshal(copyReport)
}
