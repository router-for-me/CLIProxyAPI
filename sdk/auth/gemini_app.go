package auth

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/geminiapp"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// GeminiAppAuthenticator imports a browser session without requiring API credentials.
type GeminiAppAuthenticator struct{ client *http.Client }

func NewGeminiAppAuthenticator() *GeminiAppAuthenticator    { return &GeminiAppAuthenticator{} }
func (*GeminiAppAuthenticator) Provider() string            { return geminiapp.Provider }
func (*GeminiAppAuthenticator) RefreshLead() *time.Duration { return nil }

func (a *GeminiAppAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil || opts == nil || opts.Metadata["cookie-file"] == "" {
		return nil, fmt.Errorf("gemini-app: configuration and cookie-file are required")
	}
	account := strings.TrimSpace(opts.Metadata["account"])
	if account == "" {
		return nil, fmt.Errorf("gemini-app: a stable account name is required; reuse --gemini-app-account when re-importing")
	}
	data, err := os.ReadFile(opts.Metadata["cookie-file"])
	if err != nil {
		return nil, fmt.Errorf("gemini-app: read cookie file: %w", err)
	}
	cookies, err := geminiapp.ParseCookies(data)
	if err != nil {
		return nil, err
	}
	base := a.client
	if base == nil {
		base = util.SetProxy(&cfg.SDKConfig, &http.Client{Timeout: 30 * time.Second})
	}
	if ctx == nil {
		ctx = context.Background()
	}
	client := geminiapp.NewClient(base, cookies)
	if _, err = geminiapp.FetchSession(ctx, client); err != nil {
		return nil, err
	}
	cookies = geminiapp.SessionCookies(client)
	if cookies[geminiapp.CookiePSID] == "" {
		return nil, fmt.Errorf("gemini-app: session cookie was removed during validation; re-import cookies")
	}
	// A caller-owned account name survives cookie rotation and safely replaces
	// the same account's expired credential through the existing token store.
	digest := sha256.Sum256([]byte(account))
	name := fmt.Sprintf("gemini-app-%x.json", digest[:16])
	storedCookies := make(map[string]any, len(cookies))
	for key, value := range cookies {
		storedCookies[key] = value
	}
	metadata := map[string]any{
		"type":      geminiapp.Provider,
		"auth_kind": coreauth.AuthKindOAuth,
		"label":     "Gemini App",
		"account":   account,
		"cookies":   storedCookies,
	}
	now := time.Now()
	return &coreauth.Auth{ID: name, FileName: name, Provider: geminiapp.Provider, Label: "Gemini App", Status: coreauth.StatusActive, Metadata: metadata, CreatedAt: now, UpdatedAt: now}, nil
}
