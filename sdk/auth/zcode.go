package auth

import (
	"context"
	"fmt"
	"net/http"
	"runtime"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/zcode"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

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

// zcodeProviderFromOptions reads the ZCode OAuth provider variant ("zai"
// global, default; "bigmodel" China) from LoginOptions.Metadata.
// zcodeProviderFromOptions reads the ZCode OAuth provider variant ("zai"
// global, default; "bigmodel" China) from LoginOptions.Metadata. Unknown values
// are rejected so a typo cannot silently perform a Z.ai login.
func zcodeProviderFromOptions(opts *LoginOptions) (string, error) {
	if opts == nil || opts.Metadata == nil {
		return zcode.ProviderZai, nil
	}
	return zcode.NormalizeProvider(opts.Metadata["provider"])
}

// runZCodeLogin builds the OAuth flow, resolver and token storage, then returns
// a *coreauth.Auth with the Metadata and Attributes needed by executors.
func runZCodeLogin(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if opts == nil {
		opts = &LoginOptions{}
	}
	provider, err := zcodeProviderFromOptions(opts)
	if err != nil {
		return nil, err
	}

	// The ZCode server owns the OAuth callback for both providers
	// (zcode.z.ai/api/v1/oauth/cli/callback/<provider>), so cross-device login
	// needs no localhost redirect: the same cli/init + poll flow serves both.
	credClient := zcodeHTTPClient(cfg)
	login := &zcode.CliLogin{Provider: provider, HTTP: credClient}
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

	tokens, err := login.Complete(ctx, flow, zcode.LoginTimeout)
	if err != nil {
		return nil, fmt.Errorf("zcode: %w", err)
	}

	cred, err := (&zcode.Resolver{HTTP: credClient}).ResolveCredential(ctx, tokens.AccessToken, provider)
	if err != nil {
		return nil, fmt.Errorf("zcode: %w", err)
	}
	cred.JWT = tokens.JWT
	cred.UserID = tokens.UserID

	return BuildZCodeAuth(cred, cred.JWT, cred.UserID, provider), nil
}

// zcodeHTTPClient builds the HTTP client used for ZCode credential acquisition
// (OAuth init/poll and the biz credential resolution). It honors the configured
// proxy-url, matching the kimi precedent, so a deployment behind a proxy can log
// in as well as serve traffic. The timeout is allowed here: this is credential
// acquisition, never the established model connection.
func zcodeHTTPClient(cfg *config.Config) *http.Client {
	client := &http.Client{Timeout: 30 * time.Second}
	if cfg == nil {
		return client
	}
	sdkCfg := cfg.SDKConfig
	return util.SetProxy(&sdkCfg, client)
}

// ZCode coding-plan Anthropic endpoints per provider variant.
const (
	zcodeZaiAnthropicBase      = "https://api.z.ai/api/anthropic"
	zcodeBigmodelAnthropicBase = "https://open.bigmodel.cn/api/anthropic"
)

// anthropicBaseForProvider returns the coding-plan Anthropic endpoint for a
// provider variant ("bigmodel" China, anything else Z.ai global).
func anthropicBaseForProvider(provider string) string {
	if provider == zcode.ProviderBigmodel {
		return zcodeBigmodelAnthropicBase
	}
	return zcodeZaiAnthropicBase
}

// BuildZCodeAuth builds the *coreauth.Auth credential record for ZCode from the
// resolved static credential and CLI tokens. It is shared by the CLI login
// runner (runZCodeLogin) and the management web/TUI login handler so both
// produce identical credential shapes: Attributes (api_key, base_url, header:*)
// consumed by the ZCode executor and Metadata (type, api_key, secret, jwt,
// user_id, device_mid) persisted to the auth file. provider selects the
// coding-plan endpoint (Z.ai global vs Bigmodel China).
func BuildZCodeAuth(cred *zcode.Credential, jwt, userID, provider string) *coreauth.Auth {
	deviceMid := uuid.NewString()
	fileName := fmt.Sprintf("zcode-%d.json", time.Now().UnixMilli())
	baseURL := anthropicBaseForProvider(provider)

	// Identity headers carried on every upstream request (native ZCode client
	// fidelity). Stored as header:* attributes consumed by ApplyCustomHeadersFromAttrs
	// and mirrored into metadata/storage (headers) so they survive a restart and are
	// reconstructed back into header:* attrs by ApplyCustomHeadersFromMetadata.
	identity := identityHeaders(deviceMid)
	attrs := map[string]string{
		"api_key":  cred.FullKey(),
		"base_url": baseURL,
	}
	for name, value := range identity {
		attrs["header:"+name] = value
	}
	metadata := map[string]any{
		"type":       "zcode",
		"provider":   provider,
		"api_key":    cred.APIKey,
		"secret":     cred.Secret,
		"jwt":        jwt,
		"user_id":    userID,
		"device_mid": deviceMid,
		"base_url":   baseURL,
		"headers":    copyStringMap(identity),
		"timestamp":  time.Now().UnixMilli(),
	}

	return &coreauth.Auth{
		ID:         fileName,
		Provider:   "zcode",
		FileName:   fileName,
		Label:      "ZCode User",
		Storage:    &zcode.TokenStorage{APIKey: cred.APIKey, Secret: cred.Secret, JWT: jwt, UserID: userID, DeviceMid: deviceMid, Provider: "zcode", BaseURL: baseURL, Headers: copyStringMap(identity)},
		Metadata:   metadata,
		Attributes: attrs,
	}
}

// copyStringMap returns an independent copy so the metadata and token-storage
// views of the identity headers do not share one mutable map.
func copyStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func identityHeaders(deviceMid string) map[string]string {
	return map[string]string{
		// X-ZCode-Device-Mid follows the ZCode client LLM identity convention:
		// the spec (docs/superpowers/specs/2026-09-15-zcode-provider.md) documents
		// control-plane X-Device-Mid, which the LLM path omits; this header is sent
		// under a different name X-ZCode-Device-Mid and is the intended native
		// identity set for the LLM path.
		"User-Agent":          "ZCode/" + zCodeVersion,
		"X-Title":             "ZCode",
		"HTTP-Referer":        "https://zcode.z.ai",
		"X-ZCode-Agent":       "glm",
		"X-ZCode-App-Version": zCodeVersion,
		"X-Client-Language":   "zh-CN",
		"X-Client-Timezone":   "Asia/Shanghai",
		"X-Platform":          runtimeGOOS() + "-" + runtimeGOARCH(),
		"X-Os-Category":       osCategory(),
		"X-ZCode-Device-Mid":  deviceMid,
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
