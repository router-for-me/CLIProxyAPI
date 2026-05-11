package codearts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	IAMHost      = "https://iam.cn-north-4.myhuaweicloud.com"
	APIHost      = "https://ide.cn-north-4.myhuaweicloud.com"
	RedirectHost = "https://devcloud.cn-north-4.huaweicloud.com/codeartside"
	ChatURL      = "https://snap-access.cn-north-4.myhuaweicloud.com/v1/chat/chat"
	GptsURL      = "https://snap-access.cn-north-4.myhuaweicloud.com/v1/agent-center/agents"

	DefaultAgentID = "a8bcb36232554267a5142361cc25a393"

	tokenRefreshMargin = 4 * time.Hour
)

// CodeArtsAuth manages the CodeArts authentication lifecycle.
type CodeArtsAuth struct {
	httpClient *http.Client
}

// NewCodeArtsAuth creates a new CodeArtsAuth instance.
func NewCodeArtsAuth(httpClient *http.Client) *CodeArtsAuth {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &CodeArtsAuth{httpClient: httpClient}
}

// AuthorizationURL returns the URL the user should visit to log in.
// Matches Python: build_login_url(ticket_id, port, theme=1, locale="zh-cn", version=3, uri_scheme="codearts")
func (a *CodeArtsAuth) AuthorizationURL(ticketID string, port int) string {
	params := url.Values{}
	params.Set("ticket_id", ticketID)
	params.Set("theme", "1")
	params.Set("locale", "zh-cn")
	params.Set("version", "3")
	params.Set("uri_scheme", "codearts")
	params.Set("port", fmt.Sprintf("%d", port))
	params.Set("is_redirect", "true")
	return fmt.Sprintf("%s/redirect1?%s", RedirectHost, params.Encode())
}

// PollForLoginResult polls the ticket endpoint until the user completes login.
// Matches Python: poll_login_ticket(ticket_id, identifier, timeout=120)
// Returns the full auth result JSON map.
func (a *CodeArtsAuth) PollForLoginResult(ctx context.Context, ticketID, identifier string) (map[string]interface{}, error) {
	pollURL := fmt.Sprintf("%s/v2/login/ticket", APIHost)

	for i := 0; i < 60; i++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}

		payload, _ := json.Marshal(map[string]string{
			"ticket_id":  ticketID,
			"identifier": identifier,
		})

		req, err := http.NewRequestWithContext(ctx, "POST", pollURL, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := a.httpClient.Do(req)
		if err != nil {
			log.Debugf("codearts: poll attempt %d failed: %v", i+1, err)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		var result map[string]interface{}
		if err := json.Unmarshal(body, &result); err != nil {
			continue
		}

		// Python checks: if data.get("status") == "success": return data.get("result")
		if status, _ := result["status"].(string); status == "success" {
			if authResult, ok := result["result"].(map[string]interface{}); ok {
				log.Info("codearts: login successful")
				return authResult, nil
			}
		}

		log.Debugf("codearts: poll attempt %d, status=%v", i+1, result["status"])
	}
	return nil, fmt.Errorf("codearts: login timed out after 120s")
}

// ExchangeForSecurityToken exchanges X-Auth-Token for AK/SK/SecurityToken.
// Matches Python: get_credential_by_token(x_auth_token)
func (a *CodeArtsAuth) ExchangeForSecurityToken(ctx context.Context, xAuthToken string) (*CodeArtsTokenData, error) {
	exchangeURL := fmt.Sprintf("%s/v3.0/OS-CREDENTIAL/securitytokens", IAMHost)

	payload := map[string]interface{}{
		"auth": map[string]interface{}{
			"identity": map[string]interface{}{
				"methods": []string{"token"},
				"token": map[string]interface{}{
					"duration_seconds": 86400,
				},
			},
		},
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, "POST", exchangeURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json;charset=utf8")
	req.Header.Set("X-Auth-Token", xAuthToken)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("codearts: security token exchange failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 201 {
		return nil, fmt.Errorf("codearts: security token exchange returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Credential struct {
			Access        string `json:"access"`
			Secret        string `json:"secret"`
			SecurityToken string `json:"securitytoken"`
			ExpiresAt     string `json:"expires_at"`
		} `json:"credential"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("codearts: failed to parse security token response: %w", err)
	}

	expiresAt, _ := time.Parse(time.RFC3339, result.Credential.ExpiresAt)

	return &CodeArtsTokenData{
		AK:            result.Credential.Access,
		SK:            result.Credential.Secret,
		SecurityToken: result.Credential.SecurityToken,
		ExpiresAt:     expiresAt,
		XAuthToken:    xAuthToken,
	}, nil
}

// ProcessLoginResult extracts credentials from login result.
// Matches Python logic: check for credential in result, or exchange x_auth_token.
func (a *CodeArtsAuth) ProcessLoginResult(ctx context.Context, authResult map[string]interface{}) (*CodeArtsTokenData, error) {
	userID, _ := authResult["user_id"].(string)
	userName, _ := authResult["user_name"].(string)
	domainID, _ := authResult["domain_id"].(string)

	// Check if credential is directly in the result
	var tokenData *CodeArtsTokenData

	if credMap, ok := authResult["credential"].(map[string]interface{}); ok {
		// Credential directly in login result
		ak, _ := credMap["access"].(string)
		sk, _ := credMap["secret"].(string)
		secToken, _ := credMap["securitytoken"].(string)
		expiresAtStr, _ := credMap["expires_at"].(string)
		expiresAt, _ := time.Parse(time.RFC3339, expiresAtStr)

		tokenData = &CodeArtsTokenData{
			AK:            ak,
			SK:            sk,
			SecurityToken: secToken,
			ExpiresAt:     expiresAt,
		}
	} else {
		// Need to exchange x_auth_token for credential
		xAuthToken, _ := authResult["x_auth_token"].(string)
		if xAuthToken == "" {
			xAuthToken, _ = authResult["token"].(string)
		}
		if xAuthToken == "" {
			return nil, fmt.Errorf("codearts: no credential or x_auth_token in login result")
		}

		log.Info("codearts: exchanging X-Auth-Token for AK/SK credentials")
		var err error
		tokenData, err = a.ExchangeForSecurityToken(ctx, xAuthToken)
		if err != nil {
			return nil, err
		}
		tokenData.XAuthToken = xAuthToken
	}

	tokenData.UserID = userID
	tokenData.UserName = userName
	tokenData.DomainID = domainID

	return tokenData, nil
}

// NeedsRefresh returns true if the token should be refreshed.
func NeedsRefresh(token *CodeArtsTokenData) bool {
	if token == nil {
		return true
	}
	return token.IsExpired(tokenRefreshMargin)
}

// RefreshToken refreshes the security token using POST /v2/login/refresh.
// Matches Python: refresh_token(credential)
func (a *CodeArtsAuth) RefreshToken(ctx context.Context, token *CodeArtsTokenData) (*CodeArtsTokenData, error) {
	if token == nil || (token.AK == "" || token.SK == "") {
		return nil, fmt.Errorf("codearts: cannot refresh without AK/SK")
	}

	refreshURL := fmt.Sprintf("%s/v2/login/refresh", APIHost)
	body := []byte(`{"duration_seconds":86400}`)

	req, err := http.NewRequestWithContext(ctx, "POST", refreshURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Security-Token", token.SecurityToken)
	req.Header.Set("Access-Key", token.AK)

	// Sign with SDK-HMAC-SHA256
	SignRequest(req, body, token.AK, token.SK, token.SecurityToken)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("codearts: refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		log.Warnf("codearts: refresh returned %d, attempting re-exchange", resp.StatusCode)
		if token.XAuthToken != "" {
			return a.ExchangeForSecurityToken(ctx, token.XAuthToken)
		}
		return nil, fmt.Errorf("codearts: refresh failed with status %d", resp.StatusCode)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("codearts: failed to parse refresh response: %w", err)
	}

	// Extract credential from response
	credMap, ok := result["credential"].(map[string]interface{})
	if !ok {
		if r, ok2 := result["result"].(map[string]interface{}); ok2 {
			credMap, _ = r["credential"].(map[string]interface{})
		}
	}
	if credMap == nil {
		credMap = result
	}

	ak, _ := credMap["access"].(string)
	sk, _ := credMap["secret"].(string)
	secToken, _ := credMap["securitytoken"].(string)
	expiresAtStr, _ := credMap["expires_at"].(string)
	expiresAt, _ := time.Parse(time.RFC3339, expiresAtStr)

	if ak == "" || sk == "" {
		return nil, fmt.Errorf("codearts: refresh response missing credentials")
	}

	return &CodeArtsTokenData{
		AK:            ak,
		SK:            sk,
		SecurityToken: secToken,
		ExpiresAt:     expiresAt,
		XAuthToken:    token.XAuthToken,
		UserID:        token.UserID,
		UserName:      token.UserName,
		DomainID:      token.DomainID,
		Email:         token.Email,
	}, nil
}
