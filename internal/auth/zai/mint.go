package zai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const (
	// zaiBizBaseURL / bigModelBizBaseURL host the business (org/project/api-key)
	// API used to provision a standard coding-plan key from the OAuth login.
	zaiBizBaseURL      = "https://api.z.ai"
	bigModelBizBaseURL = "https://bigmodel.cn"

	// ZAIAPIBaseURL / BigModelAPIBaseURL are the standard Anthropic-compatible
	// coding-plan endpoints that accept the minted API key. Unlike the ZCode-only
	// "zcode-plan" endpoint, these are the documented endpoints and do not gate
	// requests behind a client/captcha check.
	ZAIAPIBaseURL      = "https://api.z.ai/api/anthropic"
	BigModelAPIBaseURL = "https://open.bigmodel.cn/api/anthropic"

	// mintKeyName is the name of the API key the proxy provisions (matches the
	// official client so re-login reuses the same key instead of creating new ones).
	mintKeyName = "zcode-api-key"
	// defaultOrgName / defaultProjectName are the account's default organization and
	// project names; a matching entry is preferred, otherwise the first is used.
	defaultOrgName     = "默认机构"
	defaultProjectName = "默认项目"
)

// MintAPIKey exchanges the OAuth credentials for a standard coding-plan API key
// - the same provisioning the official client performs (the
// inference:mint_agent_key scope) - and returns the key together with the
// Anthropic-compatible inference base URL.
//
// For "zai" the OAuth access token is first exchanged for a business token via
// /api/auth/z/login; for "bigmodel" the OAuth access token authorizes the
// business API directly. The resulting "<apiKey>.<secretKey>" works against the
// standard endpoint without the ZCode-only coding-plan captcha.
func (a *ZAIAuth) MintAPIKey(ctx context.Context, ready *ReadyResult) (apiKey, baseURL string, err error) {
	if ready == nil {
		return "", "", fmt.Errorf("zai: ready result is nil")
	}
	if a.provider == ProviderBigModel {
		token := strings.TrimSpace(ready.ZAIAccessToken)
		if token == "" {
			token = strings.TrimSpace(ready.Token)
		}
		key, errMint := a.mintBizAPIKey(ctx, bigModelBizBaseURL, token, false)
		return key, BigModelAPIBaseURL, errMint
	}
	bizToken, errLogin := a.bizLogin(ctx, ready.ZAIAccessToken)
	if errLogin != nil {
		return "", "", errLogin
	}
	key, errMint := a.mintBizAPIKey(ctx, zaiBizBaseURL, "Bearer "+bizToken, true)
	return key, ZAIAPIBaseURL, errMint
}

// bizLogin exchanges the Z.AI OAuth access token for a business access token.
func (a *ZAIAuth) bizLogin(ctx context.Context, accessToken string) (string, error) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return "", fmt.Errorf("zai: missing access token for business login")
	}
	body, _ := json.Marshal(map[string]string{"token": accessToken})
	data, err := a.bizRequest(ctx, http.MethodPost, zaiBizBaseURL+"/api/auth/z/login", "", body)
	if err != nil {
		return "", fmt.Errorf("zai: business login: %w", err)
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err = json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("zai: parse business login: %w", err)
	}
	if strings.TrimSpace(out.AccessToken) == "" {
		return "", fmt.Errorf("zai: business login response missing access_token")
	}
	return out.AccessToken, nil
}

// mintBizAPIKey resolves the org/project, finds or creates the coding-plan API
// key, and copies its secret. authorization is sent verbatim in the Authorization
// header (raw token for bigmodel, "Bearer <token>" for zai). When requireSecret
// is true the secret key must be present; otherwise the bare apiKey is returned.
func (a *ZAIAuth) mintBizAPIKey(ctx context.Context, host, authorization string, requireSecret bool) (string, error) {
	// 1. Resolve organization + project from the customer info.
	data, err := a.bizRequest(ctx, http.MethodGet, host+"/api/biz/customer/getCustomerInfo", authorization, nil)
	if err != nil {
		return "", fmt.Errorf("zai: get customer info: %w", err)
	}
	var ci struct {
		Organizations []struct {
			OrganizationID   string `json:"organizationId"`
			OrganizationName string `json:"organizationName"`
			Projects         []struct {
				ProjectID   string `json:"projectId"`
				ProjectName string `json:"projectName"`
			} `json:"projects"`
		} `json:"organizations"`
	}
	if err = json.Unmarshal(data, &ci); err != nil {
		return "", fmt.Errorf("zai: parse customer info: %w", err)
	}
	if len(ci.Organizations) == 0 {
		return "", fmt.Errorf("zai: no organization on account (no active coding plan?)")
	}
	// Prefer an organization that actually has projects, favoring the account's
	// default organization among those; fall back to the first organization so the
	// check below surfaces a clear "no project" error when none have any.
	org := ci.Organizations[0]
	for i := range ci.Organizations {
		candidate := ci.Organizations[i]
		if len(candidate.Projects) == 0 {
			continue
		}
		isDefault := strings.Contains(candidate.OrganizationName, defaultOrgName)
		if len(org.Projects) == 0 || isDefault {
			org = candidate
			if isDefault {
				break
			}
		}
	}
	if len(org.Projects) == 0 {
		return "", fmt.Errorf("zai: no project in organization")
	}
	proj := org.Projects[0]
	for i := range org.Projects {
		if strings.Contains(org.Projects[i].ProjectName, defaultProjectName) {
			proj = org.Projects[i]
			break
		}
	}
	if strings.TrimSpace(org.OrganizationID) == "" || strings.TrimSpace(proj.ProjectID) == "" {
		return "", fmt.Errorf("zai: unable to resolve organization/project")
	}

	// 2. Find the existing coding-plan key, or create it.
	keysURL := fmt.Sprintf("%s/api/biz/v1/organization/%s/projects/%s/api_keys", host, org.OrganizationID, proj.ProjectID)
	apiKey := ""
	if listData, errList := a.bizRequest(ctx, http.MethodGet, keysURL, authorization, nil); errList == nil {
		var keys []struct {
			Name   string `json:"name"`
			APIKey string `json:"apiKey"`
		}
		if json.Unmarshal(listData, &keys) == nil {
			for _, k := range keys {
				if k.Name == mintKeyName {
					apiKey = strings.TrimSpace(k.APIKey)
					break
				}
			}
		}
	}
	if apiKey == "" {
		body, _ := json.Marshal(map[string]string{"name": mintKeyName})
		createData, errCreate := a.bizRequest(ctx, http.MethodPost, keysURL, authorization, body)
		if errCreate != nil {
			return "", fmt.Errorf("zai: create api key: %w", errCreate)
		}
		var created struct {
			APIKey string `json:"apiKey"`
		}
		if err = json.Unmarshal(createData, &created); err != nil {
			return "", fmt.Errorf("zai: parse created api key: %w", err)
		}
		apiKey = strings.TrimSpace(created.APIKey)
	}
	if apiKey == "" {
		return "", fmt.Errorf("zai: api key response missing apiKey")
	}

	// 3. Copy the secret key. Final key is "<apiKey>.<secretKey>".
	secretKey := ""
	if copyData, errCopy := a.bizRequest(ctx, http.MethodGet, keysURL+"/copy/"+url.PathEscape(apiKey), authorization, nil); errCopy == nil {
		var cp struct {
			SecretKey string `json:"secretKey"`
		}
		if json.Unmarshal(copyData, &cp) == nil {
			secretKey = strings.TrimSpace(cp.SecretKey)
		}
	}
	if secretKey == "" {
		if requireSecret {
			return "", fmt.Errorf("zai: api key copy response missing secretKey")
		}
		return apiKey, nil
	}
	return apiKey + "." + secretKey, nil
}

// bizRequest performs a business API call and unwraps the {code,msg,data}
// envelope. A code of 0 or 200 indicates success.
func (a *ZAIAuth) bizRequest(ctx context.Context, method, endpoint, authorization string, body []byte) (json.RawMessage, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var env struct {
		Code json.Number     `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err = json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("parse response envelope: %w", err)
	}
	if code := env.Code.String(); code != "" && code != "0" && code != "200" {
		msg := strings.TrimSpace(env.Msg)
		if msg == "" {
			msg = "business error " + code
		}
		return nil, fmt.Errorf("%s", msg)
	}
	return env.Data, nil
}
