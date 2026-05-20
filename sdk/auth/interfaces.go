package auth

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
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

	// ProxyURL is an optional proxy applied ONLY to this OAuth login session.
	// When non-empty:
	//   - the OAuth handshake (token exchange + refresh during login) routes through this proxy
	//   - the resulting Auth record persists ProxyURL so subsequent refresh and inference
	//     for this credential reuse the same proxy independently of cfg.ProxyURL
	// Supports the same value space as cfg.ProxyURL: "" (inherit cfg.ProxyURL),
	// "direct" / "none" (explicit bypass), or a valid http(s)/socks5 URL.
	ProxyURL string
}

// Authenticator manages login and optional refresh flows for a provider.
type Authenticator interface {
	Provider() string
	Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error)
	RefreshLead() *time.Duration
}

// CloneCfgWithProxy returns a *config.Config whose SDKConfig.ProxyURL is replaced by
// the given proxyURL (trimmed). When proxyURL is empty or cfg is nil, cfg itself is
// returned unchanged so callers can pass-through with no allocation.
//
// Used by per-provider Login implementations to inject a per-login proxy without
// mutating the shared application config.
func CloneCfgWithProxy(cfg *config.Config, proxyURL string) *config.Config {
	trimmed := strings.TrimSpace(proxyURL)
	if cfg == nil || trimmed == "" {
		return cfg
	}
	cfgCopy := *cfg
	sdkCopy := cfg.SDKConfig
	sdkCopy.ProxyURL = trimmed
	cfgCopy.SDKConfig = sdkCopy
	return &cfgCopy
}
