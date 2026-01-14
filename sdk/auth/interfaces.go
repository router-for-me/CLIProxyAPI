package auth

import (
	"context"
	"errors"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
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
}

// Authenticator manages login and optional refresh flows for a provider.
type Authenticator interface {
	Provider() string
	Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error)
	RefreshLead() *time.Duration
}
