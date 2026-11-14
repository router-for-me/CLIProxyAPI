package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	kiroauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type KiroAuthenticator struct{}

func NewKiroAuthenticator() *KiroAuthenticator {
	return &KiroAuthenticator{}
}

func (a *KiroAuthenticator) Provider() string {
	return "kiro"
}

func (a *KiroAuthenticator) RefreshLead() *time.Duration {
	d := 20 * time.Minute
	return &d
}

func extractKiroIdentifier(accountName, profileArn, clientID string) string {
	if accountName != "" {
		return kiroauth.SanitizeEmailForFilename(accountName)
	}
	if profileArn != "" {
		parts := strings.Split(profileArn, "/")
		if len(parts) >= 2 {
			return kiroauth.SanitizeEmailForFilename(parts[len(parts)-1])
		}
	}
	if clientID != "" {
		return kiroauth.SanitizeEmailForFilename(clientID)
	}
	return fmt.Sprintf("%d", time.Now().UnixNano()%100000)
}

func (a *KiroAuthenticator) createAuthRecord(tokenData *kiroauth.KiroTokenData, source string) (*coreauth.Auth, error) {
	seq := time.Now().UnixNano() % 100000
	var label, idPart string
	if tokenData.AuthMethod == "idc" {
		label = "kiro-idc"
		if tokenData.Email != "" {
			idPart = kiroauth.SanitizeEmailForFilename(tokenData.Email)
		} else if tokenData.StartURL != "" {
			identifier := kiroauth.ExtractIDCIdentifier(tokenData.StartURL)
			if identifier != "" {
				idPart = fmt.Sprintf("%s-%05d", identifier, seq)
			} else {
				idPart = fmt.Sprintf("%05d", seq)
			}
		} else {
			idPart = fmt.Sprintf("%05d", seq)
		}
	} else {
		label = fmt.Sprintf("kiro-%s", source)
		idPart = extractKiroIdentifier(tokenData.Email, tokenData.ProfileArn, tokenData.ClientID)
	}

	fileName := fmt.Sprintf("%s-%s.json", label, idPart)
	metadata := map[string]any{
		"type":          "kiro",
		"access_token":  tokenData.AccessToken,
		"refresh_token": tokenData.RefreshToken,
		"profile_arn":   tokenData.ProfileArn,
		"expires_at":    tokenData.ExpiresAt,
		"auth_method":   tokenData.AuthMethod,
		"provider":      tokenData.Provider,
		"client_id":     tokenData.ClientID,
		"client_secret": tokenData.ClientSecret,
		"email":         tokenData.Email,
	}

	if tokenData.StartURL != "" {
		metadata["start_url"] = tokenData.StartURL
	}
	if tokenData.Region != "" {
		metadata["region"] = tokenData.Region
	}

	attributes := map[string]string{
		"profile_arn": tokenData.ProfileArn,
		"source":      source,
		"email":       tokenData.Email,
	}

	if tokenData.AuthMethod == "idc" {
		attributes["source"] = "aws-idc"
		if tokenData.StartURL != "" {
			attributes["start_url"] = tokenData.StartURL
		}
		if tokenData.Region != "" {
			attributes["region"] = tokenData.Region
		}
	}

	storage := &kiroauth.KiroTokenStorage{
		Type:         "kiro",
		AccessToken:  tokenData.AccessToken,
		RefreshToken: tokenData.RefreshToken,
		ProfileArn:   tokenData.ProfileArn,
		ExpiresAt:    tokenData.ExpiresAt,
		AuthMethod:   tokenData.AuthMethod,
		Provider:     tokenData.Provider,
		LastRefresh:  time.Now().Format(time.RFC3339),
		ClientID:     tokenData.ClientID,
		ClientSecret: tokenData.ClientSecret,
		Region:       tokenData.Region,
		StartURL:     tokenData.StartURL,
		Email:        tokenData.Email,
	}

	record := &coreauth.Auth{
		ID:         fileName,
		Provider:   "kiro",
		FileName:   fileName,
		Label:      label,
		Status:     coreauth.StatusActive,
		Storage:    storage,
		Metadata:   metadata,
		Attributes: attributes,
	}

	return record, nil
}

func (a *KiroAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("kiro auth: configuration is required")
	}

	method := ""
	if opts != nil && opts.Metadata != nil {
		method = opts.Metadata["method"]
	}

	switch method {
	case "import":
		return a.ImportFromKiroIDE(ctx, cfg)
	case "builder-id-authcode":
		return a.LoginWithAuthCode(ctx, cfg, opts)
	case "builder-id":
		ssoClient := kiroauth.NewSSOOIDCClient(cfg)
		tokenData, err := ssoClient.LoginWithBuilderID(ctx)
		if err != nil {
			return nil, err
		}
		return a.createAuthRecord(tokenData, "aws")
	case "idc", "idc-authcode":
		startURL := ""
		region := ""
		useDevice := false
		if opts != nil && opts.Metadata != nil {
			startURL = opts.Metadata["start-url"]
			region = opts.Metadata["region"]
			useDevice = opts.Metadata["flow"] == "device"
		}
		return a.LoginWithIDC(ctx, cfg, startURL, region, useDevice)
	default:
		var idcOpts *kiroauth.IDCLoginOptions
		if opts != nil && opts.Metadata != nil && opts.Metadata["start-url"] != "" {
			idcOpts = &kiroauth.IDCLoginOptions{
				StartURL:      opts.Metadata["start-url"],
				Region:        opts.Metadata["region"],
				UseDeviceCode: opts.Metadata["flow"] == "device",
			}
		}
		ssoClient := kiroauth.NewSSOOIDCClient(cfg)
		tokenData, err := ssoClient.LoginWithMethodSelection(ctx, idcOpts)
		if err != nil {
			return nil, fmt.Errorf("login failed: %w", err)
		}
		return a.createAuthRecord(tokenData, "aws")
	}
}

func (a *KiroAuthenticator) LoginWithAuthCode(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("kiro auth: configuration is required")
	}
	ssoClient := kiroauth.NewSSOOIDCClient(cfg)
	tokenData, err := ssoClient.LoginWithBuilderIDAuthCode(ctx)
	if err != nil {
		return nil, fmt.Errorf("login failed: %w", err)
	}
	return a.createAuthRecord(tokenData, "aws")
}

func (a *KiroAuthenticator) LoginWithIDC(ctx context.Context, cfg *config.Config, startURL, region string, useDeviceCode bool) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("kiro auth: configuration is required")
	}
	ssoClient := kiroauth.NewSSOOIDCClient(cfg)
	var tokenData *kiroauth.KiroTokenData
	var err error
	if useDeviceCode {
		tokenData, err = ssoClient.LoginWithIDC(ctx, startURL, region)
	} else {
		tokenData, err = ssoClient.LoginWithIDCAuthCode(ctx, startURL, region)
	}
	if err != nil {
		return nil, fmt.Errorf("idc login failed: %w", err)
	}
	return a.createAuthRecord(tokenData, "idc")
}

func (a *KiroAuthenticator) ImportFromKiroIDE(ctx context.Context, cfg *config.Config) (*coreauth.Auth, error) {
	tokenData, err := kiroauth.LoadKiroIDEToken()
	if err != nil {
		return nil, fmt.Errorf("failed to load Kiro IDE token: %w", err)
	}
	if tokenData.Email == "" {
		tokenData.Email = kiroauth.ExtractEmailFromJWT(tokenData.AccessToken)
	}
	provider := kiroauth.SanitizeEmailForFilename(strings.ToLower(strings.TrimSpace(tokenData.Provider)))
	if provider == "" {
		provider = "imported"
	}
	return a.createAuthRecord(tokenData, provider)
}

func (a *KiroAuthenticator) Refresh(ctx context.Context, cfg *config.Config, auth *coreauth.Auth) (*coreauth.Auth, error) {
	if auth == nil || auth.Metadata == nil {
		return nil, fmt.Errorf("invalid auth record")
	}

	refreshToken, ok := auth.Metadata["refresh_token"].(string)
	if !ok || refreshToken == "" {
		return nil, fmt.Errorf("refresh token not found")
	}

	clientID, _ := auth.Metadata["client_id"].(string)
	clientSecret, _ := auth.Metadata["client_secret"].(string)
	clientIDHash, _ := auth.Metadata["client_id_hash"].(string)
	authMethod, _ := auth.Metadata["auth_method"].(string)
	startURL, _ := auth.Metadata["start_url"].(string)
	region, _ := auth.Metadata["region"].(string)

	if (clientID == "" || clientSecret == "") && clientIDHash != "" {
		if loadedClientID, loadedClientSecret, err := loadDeviceRegistrationCredentials(clientIDHash); err == nil {
			clientID = loadedClientID
			clientSecret = loadedClientSecret
		}
	}

	if clientID == "" || clientSecret == "" {
		return nil, fmt.Errorf("client_id or client_secret missing for Kiro refresh")
	}

	ssoClient := kiroauth.NewSSOOIDCClient(cfg)
	var tokenData *kiroauth.KiroTokenData
	var err error
	if authMethod == "idc" && region != "" {
		tokenData, err = ssoClient.RefreshTokenWithRegion(ctx, clientID, clientSecret, refreshToken, region, startURL)
	} else {
		tokenData, err = ssoClient.RefreshToken(ctx, clientID, clientSecret, refreshToken)
	}
	if err != nil {
		return nil, fmt.Errorf("token refresh failed: %w", err)
	}

	updated := auth.Clone()
	now := time.Now()
	updated.Metadata["access_token"] = tokenData.AccessToken
	updated.Metadata["refresh_token"] = tokenData.RefreshToken
	updated.Metadata["expires_at"] = tokenData.ExpiresAt
	updated.Metadata["last_refresh"] = now.Format(time.RFC3339)
	if clientID != "" && updated.Metadata["client_id"] == nil {
		updated.Metadata["client_id"] = clientID
	}
	if clientSecret != "" && updated.Metadata["client_secret"] == nil {
		updated.Metadata["client_secret"] = clientSecret
	}
	return updated, nil
}

func loadDeviceRegistrationCredentials(clientIDHash string) (clientID, clientSecret string, err error) {
	if clientIDHash == "" {
		return "", "", fmt.Errorf("clientIdHash is empty")
	}
	if strings.Contains(clientIDHash, "/") || strings.Contains(clientIDHash, "\\") || strings.Contains(clientIDHash, "..") {
		return "", "", fmt.Errorf("invalid clientIdHash: contains path separator")
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("failed to get home directory: %w", err)
	}

	deviceRegPath := filepath.Join(homeDir, ".aws", "sso", "cache", clientIDHash+".json")
	data, err := os.ReadFile(deviceRegPath)
	if err != nil {
		return "", "", fmt.Errorf("failed to read device registration file: %w", err)
	}

	var deviceReg struct {
		ClientID     string `json:"clientId"`
		ClientSecret string `json:"clientSecret"`
	}

	if err := json.Unmarshal(data, &deviceReg); err != nil {
		return "", "", fmt.Errorf("failed to parse device registration: %w", err)
	}

	if deviceReg.ClientID == "" || deviceReg.ClientSecret == "" {
		return "", "", fmt.Errorf("device registration missing clientId or clientSecret")
	}

	return deviceReg.ClientID, deviceReg.ClientSecret, nil
}
