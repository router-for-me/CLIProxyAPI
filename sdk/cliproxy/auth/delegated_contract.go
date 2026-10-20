package auth

import "time"

// PrepareLaunchRequest is version 1 of the pool-owned admission protocol.
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
	Code         string   `json:"code"`
	QuotaScope   string   `json:"quotaScope"`
	QuotaDomains []string `json:"quotaDomains"`
	BindingID    string   `json:"bindingId,omitempty"`
}

func (e *LaunchRefusal) Error() string   { return e.Code }
func (e *LaunchRefusal) StatusCode() int { return 409 }
