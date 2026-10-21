package auth

import "time"

const LaunchProtocolVersion = 2

// PrepareLaunchRequest is version 2 of the pool-owned admission protocol.
type PrepareLaunchRequest struct {
	ClientRequestID     string   `json:"clientRequestId"`
	SessionID           string   `json:"sessionId"`
	Provider            string   `json:"provider"`
	Model               string   `json:"model"`
	BlockedQuotaDomains []string `json:"blockedQuotaDomains"`
}

type LaunchWindow struct {
	ID          string    `json:"id"`
	UsedPercent float64   `json:"usedPercent"`
	ResetsAt    time.Time `json:"resetsAt"`
}

// LaunchResult never contains credential material. A binding is not a reservation.
type LaunchResult struct {
	ProtocolVersion      int            `json:"protocolVersion"`
	Status               string         `json:"status"` // ready, waiting, unverified
	Code                 string         `json:"code,omitempty"`
	QuotaScope           string         `json:"quotaScope,omitempty"`
	QuotaDomains         []string       `json:"quotaDomains"`
	BindingID            string         `json:"bindingId,omitempty"`
	BasePath             string         `json:"basePath,omitempty"`
	Provider             string         `json:"provider,omitempty"`
	Model                string         `json:"model,omitempty"`
	SessionID            string         `json:"sessionId,omitempty"`
	CheckedAt            time.Time      `json:"checkedAt"`
	ExpiresAt            time.Time      `json:"expiresAt"`
	Windows              []LaunchWindow `json:"windows,omitempty"`
	OrdinaryUsageAllowed bool           `json:"ordinaryUsageAllowed"`
}

// LaunchRefusal is machine-readable; consumers must not classify error prose.
type LaunchRefusal struct {
	// Authenticated failed-attempt identity; never evidence of depleted quota.
	AttemptedAccountDomain string   `json:"attemptedAccountDomain,omitempty"`
	Code                   string   `json:"code"`
	QuotaScope             string   `json:"quotaScope"`
	QuotaDomains           []string `json:"quotaDomains"`
	BindingID              string   `json:"bindingId,omitempty"`
}

func (e *LaunchRefusal) Error() string { return e.Code }
func (e *LaunchRefusal) StatusCode() int {
	switch e.Code {
	case "usage_limit", "model_quota", "quota_unverified", "throttled", "model_capacity":
		return 429
	}
	return 409
}

// LaunchBindingState is authenticated control-plane metadata, never error prose.
type LaunchBindingState struct {
	ProtocolVersion  int            `json:"protocolVersion"`
	State            string         `json:"state"`
	Active           bool           `json:"active"`
	ExecutorAttempts int            `json:"executorAttempts"`
	RefusalVersion   uint64         `json:"refusalVersion"`
	Refusal          *LaunchRefusal `json:"refusal,omitempty"`
}

// ResumeLaunchRequest acknowledges only the inspected, currently latched refusal.
type ResumeLaunchRequest struct {
	ExpectedRefusalVersion *uint64 `json:"expectedRefusalVersion"`
}

// CancelLaunchRequest cancels preparation in the authenticated request-ID scope.
type CancelLaunchRequest struct {
	ClientRequestID string `json:"clientRequestId"`
}
