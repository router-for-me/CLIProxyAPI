// Package usercontrol manages downstream users, API keys, quotas, and OAuth invitations.
package usercontrol

import (
	"context"
	"errors"
	"time"
)

const (
	UserStatusActive   = "active"
	UserStatusDisabled = "disabled"
	KeyStatusActive    = "active"
	KeyStatusRevoked   = "revoked"

	managedKeyPrefix = "cpa_"
	principalPrefix  = "managed:"
)

var (
	ErrNotFound          = errors.New("managed resource not found")
	ErrInvalidCredential = errors.New("invalid managed API key")
	ErrDisabled          = errors.New("managed API key is disabled")
	ErrExpired           = errors.New("managed API key is expired")
	ErrQuotaExceeded     = errors.New("managed user quota exceeded")
	ErrRateLimited       = errors.New("managed user rate limit exceeded")
	ErrInviteUnavailable = errors.New("OAuth invitation is unavailable")
)

// Limits are inherited by every API key owned by a user.
type Limits struct {
	RequestsPerMinute  int      `json:"requests_per_minute"`
	ConcurrentRequests int      `json:"concurrent_requests"`
	MonthlyTokens      int64    `json:"monthly_tokens"`
	AllowedModels      []string `json:"allowed_models"`
}

type User struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Email     string     `json:"email,omitempty"`
	Status    string     `json:"status"`
	Limits    Limits     `json:"limits"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

type CreateUserInput struct {
	Name      string     `json:"name"`
	Email     string     `json:"email"`
	Status    string     `json:"status"`
	Limits    Limits     `json:"limits"`
	ExpiresAt *time.Time `json:"expires_at"`
}

type UpdateUserInput = CreateUserInput

type APIKey struct {
	ID         string     `json:"id"`
	UserID     string     `json:"user_id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Status     string     `json:"status"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

type CreatedAPIKey struct {
	APIKey
	// Secret is returned once. Only its SHA-256 digest is persisted.
	Secret string `json:"secret"`
}

type CreateAPIKeyInput struct {
	Name      string     `json:"name"`
	ExpiresAt *time.Time `json:"expires_at"`
}

type StoredAPIKey struct {
	APIKey
	SecretHash []byte
	User       User
	UsedTokens int64
}

type UsageSummary struct {
	UserID       string    `json:"user_id"`
	PeriodStart  time.Time `json:"period_start"`
	Requests     int64     `json:"requests"`
	Failed       int64     `json:"failed"`
	InputTokens  int64     `json:"input_tokens"`
	OutputTokens int64     `json:"output_tokens"`
	TotalTokens  int64     `json:"total_tokens"`
}

type UsageDelta struct {
	KeyID        string
	Failed       bool
	InputTokens  int64
	OutputTokens int64
	TotalTokens  int64
}

type OAuthInvite struct {
	ID           string     `json:"id"`
	Label        string     `json:"label"`
	Providers    []string   `json:"providers"`
	MaxUses      int        `json:"max_uses"`
	UsedUses     int        `json:"used_uses"`
	ReservedUses int        `json:"reserved_uses"`
	Active       bool       `json:"active"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

type CreatedOAuthInvite struct {
	OAuthInvite
	// Token is deliberately omitted from every later list response.
	Token string `json:"token"`
	URL   string `json:"url,omitempty"`
}

type CreateOAuthInviteInput struct {
	Label     string     `json:"label"`
	Providers []string   `json:"providers"`
	MaxUses   int        `json:"max_uses"`
	ExpiresAt *time.Time `json:"expires_at"`
}

type OAuthContribution struct {
	InviteID    string    `json:"invite_id"`
	Provider    string    `json:"provider"`
	AuthID      string    `json:"auth_id"`
	Email       string    `json:"email,omitempty"`
	CompletedAt time.Time `json:"completed_at"`
}

// Repository keeps the service independent from the concrete PostgreSQL implementation.
type Repository interface {
	CreateUser(context.Context, User) (User, error)
	ListUsers(context.Context) ([]User, error)
	GetUser(context.Context, string) (User, error)
	UpdateUser(context.Context, User) (User, error)
	DeleteUser(context.Context, string) error
	CreateAPIKey(context.Context, APIKey, []byte) (APIKey, error)
	ListAPIKeys(context.Context, string) ([]APIKey, error)
	GetStoredAPIKey(context.Context, string) (StoredAPIKey, error)
	RevokeAPIKey(context.Context, string) error
	RecordUsage(context.Context, UsageDelta) error
	GetUsage(context.Context, string, time.Time) (UsageSummary, error)
	CreateOAuthInvite(context.Context, OAuthInvite, []byte) (OAuthInvite, error)
	ListOAuthInvites(context.Context) ([]OAuthInvite, error)
	RevokeOAuthInvite(context.Context, string) error
	GetOAuthInviteByTokenHash(context.Context, []byte) (OAuthInvite, error)
	ReserveOAuthInvite(context.Context, []byte, string, string) (OAuthInvite, error)
	OAuthInviteOwnsSession(context.Context, []byte, string) (bool, error)
	CompleteOAuthInvite(context.Context, string, string, string) error
}
