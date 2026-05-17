package auth

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

var labelRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

var ErrRefreshNotSupported = errors.New("cliproxy auth: refresh not supported")

// LoginOptions captures generic knobs shared across authenticators.
// Provider-specific logic can inspect Metadata for extra parameters.
type LoginOptions struct {
	NoBrowser    bool
	ProjectID    string
	CallbackPort int
	Metadata     map[string]string
	Prompt       func(prompt string) (string, error)

	// Label, when set, is appended to the auth filename so multiple accounts
	// for the same provider can coexist in one auth-dir without overwriting
	// each other. For Claude the file becomes claude-<email>-<label>.json.
	// Must contain only alphanumeric characters, hyphens and underscores.
	Label string
}

// ValidateLabel returns an error if label contains characters that are not
// safe for use in a filename (anything outside [a-zA-Z0-9_-]).
func ValidateLabel(label string) error {
	if label == "" {
		return nil
	}
	if !labelRe.MatchString(label) {
		return fmt.Errorf("label %q contains invalid characters: only letters, digits, hyphens and underscores are allowed", label)
	}
	return nil
}

// Authenticator manages login and optional refresh flows for a provider.
type Authenticator interface {
	Provider() string
	Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error)
	RefreshLead() *time.Duration
}
