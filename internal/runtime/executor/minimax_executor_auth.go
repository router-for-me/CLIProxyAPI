package executor

import (
	"context"
	"fmt"
	"strings"
	"time"

	minimaxauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/minimax"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// readAuthMetadataString reads a string value from auth metadata.
func readAuthMetadataString(auth *cliproxyauth.Auth, key string) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	value, _ := auth.Metadata[key].(string)
	return value
}

// minimaxTokenRefresher performs the OAuth refresh exchange. The production
// implementation is the MiniMax device flow client; tests substitute a stub.
type minimaxTokenRefresher interface {
	RefreshToken(ctx context.Context, refreshToken string) (*minimaxauth.TokenData, error)
}

// Refresh renews the MiniMax OAuth access token and writes the result back to
// the credential so it survives a reload.
func (e *MinimaxExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, statusErr{code: 500, msg: "minimax executor: auth is nil"}
	}
	refreshToken := strings.TrimSpace(readAuthMetadataString(auth, "refresh_token"))
	if refreshToken == "" {
		return auth, nil
	}
	region := minimaxauth.ResolveRegionFromAuth(auth)
	client := minimaxauth.NewDeviceFlowClient(e.cfg, region)
	if err := applyMinimaxRefresh(ctx, client, auth); err != nil {
		log.Warnf("minimax executor: token refresh failed: %v", err)
		return nil, err
	}
	return auth, nil
}

// applyMinimaxRefresh performs the refresh exchange and writes the new tokens
// back onto the credential's metadata, attributes, and typed storage.
func applyMinimaxRefresh(ctx context.Context, client minimaxTokenRefresher, auth *cliproxyauth.Auth) error {
	if auth == nil {
		return statusErr{code: 500, msg: "minimax executor: auth is nil"}
	}
	refreshToken := strings.TrimSpace(readAuthMetadataString(auth, "refresh_token"))
	if refreshToken == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	tokenData, err := client.RefreshToken(ctx, refreshToken)
	if err != nil {
		return err
	}
	if tokenData == nil {
		return fmt.Errorf("minimax executor: refresh returned no token data")
	}

	region := minimaxauth.ResolveRegionFromAuth(auth)
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["type"] = "minimax"
	auth.Metadata["auth_kind"] = cliproxyauth.AuthKindOAuth
	auth.Metadata["access_token"] = tokenData.AccessToken
	if tokenData.RefreshToken != "" {
		auth.Metadata["refresh_token"] = tokenData.RefreshToken
	}
	if tokenData.Scope != "" {
		auth.Metadata["scope"] = tokenData.Scope
	}
	if !tokenData.ExpiresAt.IsZero() {
		auth.Metadata["expired"] = tokenData.ExpiresAt.UTC().Format(time.RFC3339)
	}
	auth.Metadata["last_refresh"] = time.Now().UTC().Format(time.RFC3339)
	if tokenData.ResourceURL != "" {
		auth.Metadata["resource_url"] = tokenData.ResourceURL
		auth.Metadata["base_url"] = tokenData.ResourceURL
	}

	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	auth.Attributes["region"] = region
	if base := minimaxauth.ResolveBaseURL(auth); base != "" {
		auth.Attributes["base_url"] = base
	}

	if storage, ok := auth.Storage.(*minimaxauth.MinimaxTokenStorage); ok && storage != nil {
		cloned := *storage
		cloned.AccessToken = tokenData.AccessToken
		if tokenData.RefreshToken != "" {
			cloned.RefreshToken = tokenData.RefreshToken
		}
		if tokenData.Scope != "" {
			cloned.Scope = tokenData.Scope
		}
		if !tokenData.ExpiresAt.IsZero() {
			cloned.Expired = tokenData.ExpiresAt.UTC().Format(time.RFC3339)
		}
		cloned.LastRefresh = time.Now().UTC().Format(time.RFC3339)
		if tokenData.ResourceURL != "" {
			cloned.ResourceURL = tokenData.ResourceURL
			cloned.BaseURL = tokenData.ResourceURL
		}
		cloned.Metadata = nil
		auth.Storage = &cloned
	}
	return nil
}
