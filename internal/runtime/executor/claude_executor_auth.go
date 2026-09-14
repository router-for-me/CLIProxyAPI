package executor

import (
	"context"
	"fmt"
	"strings"
	"time"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	claudeAccountProfileCheckedAtKey = "claude_account_profile_checked_at"
	claudeAccountProfileTimeout      = 10 * time.Second
)

type claudeOAuthProfileFetcher func(context.Context, *cliproxyauth.Auth, string) (*claudeauth.OAuthProfile, error)

func (e *ClaudeExecutor) ShouldPrepareRequestAuth(auth *cliproxyauth.Auth) bool {
	apiKey, _ := claudeCreds(auth)
	if !isClaudeOAuthToken(apiKey) || auth == nil {
		return false
	}
	if !claudeauth.HasCanonicalDeviceIDPool(claudeauth.ReadDeviceIDPool(&auth.Metadata)) {
		return true
	}
	return helps.ClaudeCredentialAccountUUID(auth) == ""
}

func isClaudeSetupToken(auth *cliproxyauth.Auth, apiKey string) bool {
	if !isClaudeOAuthToken(apiKey) || auth == nil {
		return false
	}
	if skip, _ := auth.Metadata["skip_account_profile"].(bool); skip {
		return true
	}
	if isSetup, _ := auth.Metadata["is_setup_token"].(bool); isSetup {
		return true
	}
	if isSetup, _ := auth.Metadata["setup_token"].(bool); isSetup {
		return true
	}
	if kind := strings.ToLower(auth.Attributes["auth_kind"]); kind == "setup_token" || kind == "setup-token" {
		return true
	}
	scopes := strings.ToLower(claudeauth.ReadMetadataString(&auth.Metadata, "scopes"))
	if scopes == "" {
		scopes = strings.ToLower(claudeauth.ReadMetadataString(&auth.Metadata, "scope"))
	}
	if scopes != "" && !strings.Contains(scopes, "user:profile") && !strings.Contains(scopes, "user:office") {
		return true
	}
	return false
}

func isClaudeOAuthScope403(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "status 403") ||
		strings.Contains(msg, "403 forbidden") ||
		strings.Contains(msg, "403") ||
		strings.Contains(msg, "forbidden") ||
		strings.Contains(msg, "permission_error") ||
		strings.Contains(msg, "scope requirement") ||
		strings.Contains(msg, "insufficient_scope") ||
		strings.Contains(msg, "user:profile") ||
		strings.Contains(msg, "user:office")
}

func (e *ClaudeExecutor) PrepareRequestAuth(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil || !e.ShouldPrepareRequestAuth(auth) {
		return auth, nil
	}
	apiKey, _ := claudeCreds(auth)
	claudeauth.EnsureMetadataMap(&auth.Metadata)
	if _, errDeviceIDs := helps.EnsureClaudeCredentialDevicePoolRequired(ctx, auth); errDeviceIDs != nil {
		return nil, errDeviceIDs
	}
	if helps.ClaudeCredentialAccountUUID(auth) != "" {
		return auth, nil
	}

	if isClaudeSetupToken(auth, apiKey) {
		seed := helps.ClaudeCLIAuthIdentitySeed(auth)
		if seed == "" {
			seed = "claude-setup-token|" + apiKey
		}
		claudeauth.StoreMetadataString(&auth.Metadata, "account_uuid", helps.StableClaudeCLIAccountUUID(seed))
		claudeauth.StoreMetadataString(&auth.Metadata, claudeAccountProfileCheckedAtKey, time.Now().UTC().Format(time.RFC3339))
		return auth, nil
	}

	profile, errProfile := e.fetchClaudeOAuthProfile(ctx, auth, apiKey)
	if errProfile != nil {
		if errContext := ctx.Err(); errContext != nil {
			return nil, errContext
		}
		if isClaudeOAuthScope403(errProfile) {
			log.Debugf("Claude OAuth account profile lookup returned 403 for auth %s: %v (falling back to stable credential identity)", auth.ID, errProfile)
			seed := helps.ClaudeCLIAuthIdentitySeed(auth)
			if seed == "" {
				seed = "claude-oauth-fallback|" + apiKey
			}
			claudeauth.StoreMetadataString(&auth.Metadata, "account_uuid", helps.StableClaudeCLIAccountUUID(seed))
			claudeauth.StoreMetadataString(&auth.Metadata, claudeAccountProfileCheckedAtKey, time.Now().UTC().Format(time.RFC3339))
			return auth, nil
		}
		return nil, fmt.Errorf("populate Claude OAuth account profile: %w", errProfile)
	}
	if profile == nil || strings.TrimSpace(profile.Account.UUID) == "" {
		log.Debugf("Claude OAuth account profile lookup returned empty account UUID for auth %s (falling back to stable credential identity)", auth.ID)
		seed := helps.ClaudeCLIAuthIdentitySeed(auth)
		if seed == "" {
			seed = "claude-oauth-fallback|" + apiKey
		}
		claudeauth.StoreMetadataString(&auth.Metadata, "account_uuid", helps.StableClaudeCLIAccountUUID(seed))
		claudeauth.StoreMetadataString(&auth.Metadata, claudeAccountProfileCheckedAtKey, time.Now().UTC().Format(time.RFC3339))
		return auth, nil
	}
	claudeauth.StoreMetadataString(&auth.Metadata, "account_uuid", profile.Account.UUID)
	claudeauth.StoreMetadataString(&auth.Metadata, "email", profile.Account.Email)
	claudeauth.StoreMetadataString(&auth.Metadata, "organization_uuid", profile.Organization.UUID)
	claudeauth.StoreMetadataString(&auth.Metadata, "organization_name", profile.Organization.Name)
	claudeauth.StoreMetadataString(&auth.Metadata, claudeAccountProfileCheckedAtKey, time.Now().UTC().Format(time.RFC3339))
	return auth, nil
}

func (e *ClaudeExecutor) fetchClaudeOAuthProfile(ctx context.Context, auth *cliproxyauth.Auth, apiKey string) (*claudeauth.OAuthProfile, error) {
	if e == nil {
		return nil, fmt.Errorf("fetch Claude OAuth profile: executor is nil")
	}
	if e.oauthProfileFetcher != nil {
		return e.oauthProfileFetcher(ctx, auth, apiKey)
	}
	if auth == nil {
		return nil, fmt.Errorf("fetch Claude OAuth profile: auth is nil")
	}
	profileCtx, cancelProfile := context.WithTimeout(ctx, claudeAccountProfileTimeout)
	defer cancelProfile()
	service := claudeauth.NewClaudeAuthWithProxyURL(e.cfg, auth.ProxyURL)
	return service.FetchOAuthProfile(profileCtx, apiKey)
}

func (e *ClaudeExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("claude executor: refresh called")
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	if auth == nil {
		return nil, fmt.Errorf("claude executor: auth is nil")
	}
	refreshToken := claudeauth.ReadMetadataString(&auth.Metadata, "refresh_token")
	if refreshToken == "" {
		refreshToken = claudeauth.ReadMetadataString(&auth.Metadata, "refreshToken")
	}
	if refreshToken == "" {
		return auth, nil
	}
	svc := claudeauth.NewClaudeAuthWithProxyURL(e.cfg, auth.ProxyURL)
	td, err := svc.RefreshTokensWithRetry(ctx, refreshToken, 3)
	if err != nil {
		return nil, err
	}
	claudeauth.EnsureMetadataMap(&auth.Metadata)
	claudeauth.StoreMetadataValue(&auth.Metadata, "access_token", td.AccessToken)
	claudeauth.StoreMetadataString(&auth.Metadata, "refresh_token", td.RefreshToken)
	// Profile fields are optional when token rotation succeeds but the follow-up
	// profile lookup fails. Never erase the previously resolved credential identity.
	claudeauth.StoreMetadataString(&auth.Metadata, "email", td.Email)
	claudeauth.StoreMetadataString(&auth.Metadata, "account_uuid", td.AccountUUID)
	claudeauth.StoreMetadataString(&auth.Metadata, "organization_uuid", td.OrganizationUUID)
	claudeauth.StoreMetadataString(&auth.Metadata, "organization_name", td.OrganizationName)
	claudeauth.StoreMetadataValue(&auth.Metadata, "expired", td.Expire)
	claudeauth.StoreMetadataValue(&auth.Metadata, "type", "claude")
	claudeauth.StoreMetadataValue(&auth.Metadata, "last_refresh", time.Now().Format(time.RFC3339))
	return auth, nil
}
