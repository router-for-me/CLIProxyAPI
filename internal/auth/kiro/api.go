package kiro

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	log "github.com/sirupsen/logrus"
)

const (
	kiroRuntimeUserAgent = "aws-sdk-js/1.0.18 KiroAPIProxy"
	kiroRuntimeAmzAgent  = "aws-sdk-js/1.0.18 KiroAPIProxy"
)

var kiroCredentialFileUnsafe = regexp.MustCompile(`[^A-Za-z0-9._@-]+`)

// CredentialFileName returns a provider-prefixed filename for a Kiro credential.
func CredentialFileName(email, userID string) string {
	if cleaned := sanitizeKiroFilePart(email); cleaned != "" {
		return fmt.Sprintf("kiro-%s.json", cleaned)
	}
	if cleaned := sanitizeKiroFilePart(userID); cleaned != "" {
		return fmt.Sprintf("kiro-%s.json", cleaned)
	}
	return fmt.Sprintf("kiro-%d.json", time.Now().Unix())
}

func sanitizeKiroFilePart(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.ToLower(value)
	value = kiroCredentialFileUnsafe.ReplaceAllString(value, "_")
	value = strings.Trim(value, "._-")
	return value
}

func (ka *KiroAuth) doRequest(ctx context.Context, region, method, path string, body io.Reader, accessToken, profileArn string) (*http.Response, error) {
	safeRegion := AssertValidAwsRegion(region)
	endpoint := fmt.Sprintf("https://q.%s.amazonaws.com%s", safeRegion, path)
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", kiroRuntimeUserAgent)
	req.Header.Set("x-amz-user-agent", kiroRuntimeAmzAgent)
	req.Header.Set("x-amzn-kiro-agent-mode", "vibe")
	if strings.TrimSpace(accessToken) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	}
	if strings.TrimSpace(profileArn) != "" {
		req.Header.Set("x-amzn-kiro-profile-arn", strings.TrimSpace(profileArn))
	}
	resp, err := ka.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// GetUserInfo fetches the current user identity details from Kiro usage limits.
func (ka *KiroAuth) GetUserInfo(ctx context.Context, accessToken, profileArn, region string) (email, userID string, err error) {
	resp, err := ka.doRequest(ctx, region, http.MethodGet, "/getUsageLimits?origin=AI_EDITOR&resourceType=AGENTIC_REQUEST&isEmailRequired=true"+profileArnQuery(profileArn), nil, accessToken, profileArn)
	if err != nil {
		return "", "", err
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Warnf("kiro auth: close GetUserInfo response body: %v", errClose)
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	var payload struct {
		UserInfo struct {
			Email  string `json:"email"`
			UserID string `json:"userId"`
		} `json:"userInfo"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", "", err
	}
	return strings.TrimSpace(payload.UserInfo.Email), strings.TrimSpace(payload.UserInfo.UserID), nil
}

// GetUsageLimits fetches the raw usage limit payload for quota UI normalization.
func (ka *KiroAuth) GetUsageLimits(ctx context.Context, accessToken, profileArn, region string) ([]byte, error) {
	resp, err := ka.doRequest(ctx, region, http.MethodGet, "/getUsageLimits?origin=AI_EDITOR&resourceType=AGENTIC_REQUEST&isEmailRequired=true"+profileArnQuery(profileArn), nil, accessToken, profileArn)
	if err != nil {
		return nil, err
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Warnf("kiro auth: close GetUsageLimits response body: %v", errClose)
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
	return body, nil
}

// ResolveProfileArn returns the profile ARN for a credential, querying AWS or refreshing if needed.
func (ka *KiroAuth) ResolveProfileArn(ctx context.Context, creds *KiroCredentials) (string, error) {
	if creds == nil {
		return "", fmt.Errorf("credentials are nil")
	}
	if profileArn := strings.TrimSpace(creds.ProfileArn); profileArn != "" {
		return profileArn, nil
	}
	if creds.AccessToken != "" {
		if profileArn, err := ka.ListAvailableProfiles(creds.AccessToken, creds.Region); err == nil && strings.TrimSpace(profileArn) != "" {
			creds.ProfileArn = strings.TrimSpace(profileArn)
			return creds.ProfileArn, nil
		}
	}
	if refreshed, err := ka.RefreshToken(ctx, creds); err == nil && refreshed != nil {
		if profileArn := strings.TrimSpace(refreshed.ProfileArn); profileArn != "" {
			creds.ProfileArn = profileArn
			if refreshed.AccessToken != "" {
				creds.AccessToken = refreshed.AccessToken
			}
			if refreshed.RefreshToken != "" {
				creds.RefreshToken = refreshed.RefreshToken
			}
			if refreshed.ExpiresAt > 0 {
				creds.ExpiresAt = refreshed.ExpiresAt
			}
			creds.LastRefreshed = refreshed.LastRefreshed
			return profileArn, nil
		}
		if refreshed.AccessToken != "" {
			if profileArn, err := ka.ListAvailableProfiles(refreshed.AccessToken, creds.Region); err == nil && strings.TrimSpace(profileArn) != "" {
				creds.ProfileArn = strings.TrimSpace(profileArn)
				return creds.ProfileArn, nil
			}
		}
	}
	// For IDC accounts, do not return the builder-id fallback if AWS didn't return a profile.
	authMethod := strings.ToLower(strings.TrimSpace(creds.AuthMethod))
	if authMethod == "idc" || authMethod == "iam-sso" || authMethod == "api_key" {
		return "", nil
	}
	// Fallback to default profile ARN for the auth method if AWS did not return one.
	fallback := DefaultProfileArnForMethod(creds.AuthMethod)
	creds.ProfileArn = fallback
	return fallback, nil
}

func profileArnQuery(profileArn string) string {
	profileArn = strings.TrimSpace(profileArn)
	if profileArn == "" {
		return ""
	}
	return "&profileArn=" + url.QueryEscape(profileArn)
}

// NormalizeKiroMetadata extracts best-effort identity fields for auth records.
func NormalizeKiroMetadata(creds *KiroCredentials, email, userID string) map[string]any {
	meta := map[string]any{
		"type":           "kiro",
		"access_token":   creds.AccessToken,
		"refresh_token":  creds.RefreshToken,
		"profile_arn":    creds.ProfileArn,
		"auth_method":    creds.AuthMethod,
		"client_id":      creds.ClientID,
		"client_secret":  creds.ClientSecret,
		"region":         creds.Region,
		"expires_at":     creds.ExpiresAt,
		"last_refreshed": creds.LastRefreshed,
	}
	if email != "" {
		meta["email"] = email
	}
	if userID != "" {
		meta["user_id"] = userID
	}
	return meta
}

// PersistKiroCredentials saves a Kiro credential to disk using the standard token store layout.
func PersistKiroCredentials(cfg *config.Config, creds *KiroCredentials, email, userID string) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("config is required")
	}
	if creds == nil {
		return "", fmt.Errorf("credentials are required")
	}
	fileName := CredentialFileName(email, userID)
	path := filepath.Join(cfg.AuthDir, fileName)
	return path, SaveTokenToFile(creds, path)
}
