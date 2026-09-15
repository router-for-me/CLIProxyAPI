package auth

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/zcode"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// zcodeLoginTimeout bounds how long we wait for the user to authorize.
const zcodeLoginTimeout = 5 * time.Minute

// zCodeVersion is the ZCode desktop client version whose user-agent and identity
// headers we mirror for upstream fidelity. Bump with the ZCode release cadence.
const zCodeVersion = "3.12.0"

// zcodeRunner abstracts the login flow so tests can inject a fake instead of
// hitting the network. nil means production login (runZCodeLogin).
type zcodeRunner interface {
	Run(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error)
}

// ZCodeAuthenticator implements the Z.ai server-mediated OAuth flow for ZCode.
type ZCodeAuthenticator struct {
	run zcodeRunner // nil for production; injectable in tests
}

// NewZCodeAuthenticator constructs a new ZCode authenticator.
func NewZCodeAuthenticator() Authenticator { return &ZCodeAuthenticator{} }

// Provider returns the provider key for zcode.
func (ZCodeAuthenticator) Provider() string { return "zcode" }

// RefreshLead returns nil: ZCode credentials are static and never refresh.
func (ZCodeAuthenticator) RefreshLead() *time.Duration { return nil }

// Login initiates the ZCode server-mediated OAuth flow and saves the credential.
func (a ZCodeAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if a.run != nil {
		return a.run.Run(ctx, cfg, opts)
	}
	return runZCodeLogin(ctx, cfg, opts)
}

// runZCodeLogin builds the OAuth flow, resolver and token storage, then returns
// a *coreauth.Auth with the Metadata and Attributes needed by executors.
func runZCodeLogin(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if opts == nil {
		opts = &LoginOptions{}
	}
	login := &zcode.ZaiCliLogin{}
	flow, err := login.Start(ctx)
	if err != nil {
		return nil, fmt.Errorf("zcode: %w", err)
	}

	fmt.Printf("\nTo authenticate, please visit:\n%s\n\n", flow.AuthorizeURL)
	if !opts.NoBrowser && browser.IsAvailable() {
		if errOpen := browser.OpenURL(flow.AuthorizeURL); errOpen != nil {
			fmt.Printf("Browser open failed (%v); open the URL manually.\n", errOpen)
		}
	}
	fmt.Println("Waiting for authorization...")

	tokens, err := login.Complete(ctx, flow, zcodeLoginTimeout)
	if err != nil {
		return nil, fmt.Errorf("zcode: %w", err)
	}

	cred, err := (&zcode.Resolver{}).ResolveZaiCredential(ctx, tokens.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("zcode: %w", err)
	}
	cred.JWT = tokens.JWT
	cred.UserID = tokens.UserID

	return BuildZCodeAuth(cred, cred.JWT, cred.UserID), nil
}

// BuildZCodeAuth builds the *coreauth.Auth credential record for ZCode from the
// resolved static credential and CLI tokens. It is shared by the CLI login
// runner (runZCodeLogin) and the management web/TUI login handler so both
// produce identical credential shapes: Attributes (api_key, base_url, header:*)
// consumed by the ZCode executor and Metadata (type, api_key, secret, jwt,
// user_id, device_mid) persisted to the auth file.
func BuildZCodeAuth(cred *zcode.Credential, jwt, userID string) *coreauth.Auth {
	deviceMid := uuid.NewString()
	fileName := fmt.Sprintf("zcode-%d.json", time.Now().UnixMilli())

	// Identity headers carried on every upstream request (native ZCode client
	// fidelity). Stored as header:* attributes consumed by ApplyCustomHeadersFromAttrs.
	attrs := map[string]string{
		"api_key":                    cred.FullKey(),
		"base_url":                   "https://api.z.ai/api/anthropic",
		"header:User-Agent":          "ZCode/" + zCodeVersion,
		"header:X-Title":             "ZCode",
		"header:HTTP-Referer":        "https://zcode.z.ai",
		"header:X-ZCode-Agent":       "glm",
		"header:X-ZCode-App-Version": zCodeVersion,
		"header:X-Client-Language":   "zh-CN",
		"header:X-Client-Timezone":   "Asia/Shanghai",
		"header:X-Platform":          runtimeGOOS() + "-" + runtimeGOARCH(),
		"header:X-Os-Category":       osCategory(),
		// X-ZCode-Device-Mid follows the ZCode client LLM identity convention:
		// the spec (docs/superpowers/specs/2026-09-15-zcode-provider.md) documents
		// control-plane X-Device-Mid, which the LLM path omits; this header is sent
		// under a different name X-ZCode-Device-Mid and is the intended native
		// identity set for the LLM path.
		"header:X-ZCode-Device-Mid": deviceMid,
	}
	metadata := map[string]any{
		"type":       "zcode",
		"api_key":    cred.APIKey,
		"secret":     cred.Secret,
		"jwt":        jwt,
		"user_id":    userID,
		"device_mid": deviceMid,
		"timestamp":  time.Now().UnixMilli(),
	}

	return &coreauth.Auth{
		ID:         fileName,
		Provider:   "zcode",
		FileName:   fileName,
		Label:      "ZCode User",
		Storage:    &zcode.TokenStorage{APIKey: cred.APIKey, Secret: cred.Secret, JWT: jwt, UserID: userID, DeviceMid: deviceMid, Provider: "zcode"},
		Metadata:   metadata,
		Attributes: attrs,
	}
}

func runtimeGOOS() string {
	return runtime.GOOS
}

func runtimeGOARCH() string {
	return runtime.GOARCH
}

// osCategory maps the runtime OS to a stable category used by ZCode headers.
func osCategory() string {
	switch runtime.GOOS {
	case "darwin":
		return "macos"
	case "windows":
		return "windows"
	default:
		return "linux"
	}
}
