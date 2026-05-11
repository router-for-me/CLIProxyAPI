package codearts

import "time"

// CodeArtsTokenData holds the authentication credentials.
type CodeArtsTokenData struct {
	AK            string    `json:"access"`
	SK            string    `json:"secret"`
	SecurityToken string    `json:"securitytoken"`
	ExpiresAt     time.Time `json:"expires_at"`
	XAuthToken    string    `json:"x_auth_token,omitempty"`
	Email         string    `json:"email,omitempty"`
	UserID        string    `json:"user_id,omitempty"`
	UserName      string    `json:"user_name,omitempty"`
	DomainID      string    `json:"domain_id,omitempty"`
}

// IsExpired returns true if the token is expired or will expire within margin.
func (t *CodeArtsTokenData) IsExpired(margin time.Duration) bool {
	if t.ExpiresAt.IsZero() {
		return true
	}
	return time.Now().Add(margin).After(t.ExpiresAt)
}
