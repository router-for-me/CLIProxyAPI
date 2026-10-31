package auth

import (
	"context"
	"errors"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

var ErrRefreshNotSupported = errors.New("cliproxy auth: refresh not supported")

// LoginOptions captures generic knobs shared across authenticators.
// Provider-specific logic can inspect Metadata for extra parameters.
type LoginOptions struct {
	NoBrowser    bool
	ProjectID    string
	CallbackPort int
	Metadata     map[string]string
	Prompt       func(prompt string) (string, error)
	// OnDeviceCode runs after a device code is issued and before token polling starts.
	OnDeviceCode func(DeviceCodeInfo)
}

// DeviceCodeInfo contains the user-facing values needed to complete a device login.
// It deliberately omits the private device authorization identifier used for polling.
type DeviceCodeInfo struct {
	UserCode        string
	VerificationURL string
	ExpiresIn       int // seconds
}

// Authenticator manages login and optional refresh flows for a provider.
type Authenticator interface {
	Provider() string
	Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error)
	RefreshLead() *time.Duration
}
