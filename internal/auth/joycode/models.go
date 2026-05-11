package joycode

import "time"

type JoyCodeTokenData struct {
	PTKey       string    `json:"ptKey"`
	UserID      string    `json:"userId,omitempty"`
	Tenant      string    `json:"tenant,omitempty"`
	OrgFullName string    `json:"orgFullName,omitempty"`
	LoginType   string    `json:"loginType,omitempty"`
	ExpiresAt   time.Time `json:"expires_at,omitempty"`
}
