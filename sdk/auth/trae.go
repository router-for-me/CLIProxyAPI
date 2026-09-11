package auth

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	traeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/trae"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

var unsafeTraeFileNameChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// TraeAuthenticator imports an existing local Trae CLI login without copying its access token.
type TraeAuthenticator struct{}

// NewTraeAuthenticator constructs a Trae CLI credential importer.
func NewTraeAuthenticator() Authenticator { return &TraeAuthenticator{} }

// Provider returns the Trae provider key.
func (TraeAuthenticator) Provider() string { return traeauth.Provider }

// RefreshLead refreshes the referenced Trae CLI state shortly before it expires.
func (TraeAuthenticator) RefreshLead() *time.Duration {
	lead := 5 * time.Minute
	return &lead
}

// Login imports paths and model metadata from the already authenticated local Trae CLI.
func (TraeAuthenticator) Login(ctx context.Context, cfg *config.Config, _ *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	installation, errResolve := traeauth.ResolveInstallation()
	if errResolve != nil {
		return nil, errResolve
	}
	credentials, errLoad := traeauth.LoadCredentials(installation.AuthPath)
	if errLoad != nil {
		return nil, errLoad
	}
	if ctx == nil {
		ctx = context.Background()
	}
	modelCtx, cancelModels := context.WithTimeout(ctx, 30*time.Second)
	models, clientVersion, errModels := traeauth.FetchModels(modelCtx, installation.CLIPath)
	cancelModels()
	if errModels != nil {
		models, clientVersion, errModels = traeauth.LoadCachedModels(installation.ModelsPath)
		if errModels != nil {
			return nil, fmt.Errorf("trae: load model catalog: %w", errModels)
		}
	}

	baseMetadata := map[string]any{"region": credentials.Region}
	metadata := map[string]any{
		"type":                 traeauth.Provider,
		"auth_kind":            "oauth",
		"trae_auth_path":       installation.AuthPath,
		"trae_models_path":     installation.ModelsPath,
		"trae_cli_path":        installation.CLIPath,
		"trae_home":            installation.Home,
		"user_id":              credentials.UserID,
		"region":               credentials.Region,
		"expires_at":           credentials.ExpiresAt,
		"expired":              credentials.ExpiresAt,
		"last_refresh":         credentials.LastRefresh,
		"login_method":         credentials.LoginMethod,
		"credential_kind":      credentials.CredentialKind,
		"client_version":       clientVersion,
		"base_urls":            traeauth.BaseURLs(baseMetadata),
		"models":               models,
		"credential_reference": true,
	}
	label := credentials.UserID
	if label == "" {
		label = "Trae CLI"
	}
	fileLabel := strings.Trim(unsafeTraeFileNameChars.ReplaceAllString(label, "_"), "._-")
	if fileLabel == "" {
		fileLabel = "local"
	}
	fileName := "trae-" + fileLabel + ".json"
	return &coreauth.Auth{
		ID:       fileName,
		Provider: traeauth.Provider,
		FileName: fileName,
		Label:    label,
		Metadata: metadata,
	}, nil
}
