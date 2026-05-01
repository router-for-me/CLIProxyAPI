package executor

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	neturl "net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	claudeauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

const (
	codexConversationDefaultAcceptLanguage = "en-US,en;q=0.9"
	codexConversationDefaultLanguage       = "en-US"
	codexConversationBrowserUserAgent      = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.7103.113 Safari/537.36"
	codexConversationSecCHUA               = `"Chromium";v="136", "Google Chrome";v="136", "Not.A/Brand";v="99"`
	codexConversationSecCHUAFullVersion    = `"Chromium";v="136.0.7103.113", "Google Chrome";v="136.0.7103.113", "Not.A/Brand";v="99.0.0.0"`
	codexConversationSentinelScriptURL     = "https://sentinel.openai.com/sentinel/20260124ceb8/sdk.js"
	codexConversationSentinelErrorPrefix   = "wQ8Lk5FbGpA2NcR9dShT6gYjU7VxZ4D"
	codexConversationSentinelMaxAttempts   = 500000
	codexConversationCSRFPath              = "/api/auth/csrf"
	codexConversationSessionPath           = "/api/auth/session"
	codexConversationValidatePath          = "/backend-api/me"
)

type codexConversationChatRequirementsResponse struct {
	Token       string `json:"token"`
	ProofOfWork struct {
		Required   bool   `json:"required"`
		Seed       string `json:"seed"`
		Difficulty string `json:"difficulty"`
	} `json:"proofofwork"`
}

type codexConversationSentinelGenerator struct {
	deviceID         string
	userAgent        string
	requirementsSeed string
	sid              string
	random           *rand.Rand
}

func ensureCodexConversationSession(ctx context.Context, client *http.Client, auth *cliproxyauth.Auth, req *http.Request, bearerToken string) error {
	if req == nil {
		return nil
	}

	deviceID := strings.TrimSpace(req.Header.Get("Oai-Device-Id"))
	if deviceID == "" {
		deviceID = codexConversationDeviceID(auth)
	}
	if deviceID != "" {
		req.Header.Set("Oai-Device-Id", deviceID)
	}
	misc.EnsureHeader(req.Header, nil, "Accept-Language", codexConversationDefaultAcceptLanguage)
	misc.EnsureHeader(req.Header, nil, "Oai-Language", codexConversationDefaultLanguage)
	misc.EnsureHeader(req.Header, nil, "Sec-Fetch-Dest", "empty")
	misc.EnsureHeader(req.Header, nil, "Sec-Fetch-Mode", "cors")
	misc.EnsureHeader(req.Header, nil, "Sec-Fetch-Site", "same-origin")
	misc.EnsureHeader(req.Header, nil, "Sec-Ch-Ua", codexConversationSecCHUA)
	misc.EnsureHeader(req.Header, nil, "Sec-Ch-Ua-Mobile", "?0")
	misc.EnsureHeader(req.Header, nil, "Sec-Ch-Ua-Platform", `"Windows"`)
	misc.EnsureHeader(req.Header, nil, "Sec-Ch-Ua-Arch", `"x86"`)
	misc.EnsureHeader(req.Header, nil, "Sec-Ch-Ua-Bitness", `"64"`)
	misc.EnsureHeader(req.Header, nil, "Sec-Ch-Ua-Full-Version", `"136.0.7103.113"`)
	misc.EnsureHeader(req.Header, nil, "Sec-Ch-Ua-Full-Version-List", codexConversationSecCHUAFullVersion)
	misc.EnsureHeader(req.Header, nil, "Sec-Ch-Ua-Platform-Version", `"15.0.0"`)

	builtCookieHeader := buildCodexConversationCookieHeader(auth, deviceID, "")
	cookieHeader := mergeCodexConversationCookies(req.Header.Get("Cookie"), builtCookieHeader)
	if cookieHeader != "" {
		req.Header.Set("Cookie", cookieHeader)
	}

	if strings.TrimSpace(req.Header.Get("Openai-Sentinel-Chat-Requirements-Token")) != "" &&
		strings.TrimSpace(req.Header.Get("Openai-Sentinel-Proof-Token")) != "" {
		return nil
	}

	if client == nil || strings.TrimSpace(bearerToken) == "" || deviceID == "" {
		return nil
	}

	requirementsToken, proofToken, oaiSC, err := codexConversationFetchChatRequirements(
		ctx,
		client,
		req.URL,
		bearerToken,
		deviceID,
		req.Header.Get("User-Agent"),
		cookieHeader,
	)
	if err != nil {
		return err
	}

	if requirementsToken != "" {
		req.Header.Set("Openai-Sentinel-Chat-Requirements-Token", requirementsToken)
	}
	if proofToken != "" {
		req.Header.Set("Openai-Sentinel-Proof-Token", proofToken)
	}
	if oaiSC != "" {
		updatedCookieHeader := mergeCodexConversationCookies(cookieHeader, "oai-sc="+oaiSC)
		if updatedCookieHeader != "" {
			req.Header.Set("Cookie", updatedCookieHeader)
		}
		codexConversationPersistCookieHeader(auth, "oai-sc="+oaiSC)
		codexConversationPersistOaiSC(auth, oaiSC)
	}

	return nil
}

func newCodexConversationHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, rawURL string) *http.Client {
	parsed, err := neturl.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed == nil {
		return newProxyAwareHTTPClient(ctx, cfg, auth, 0)
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return newProxyAwareHTTPClient(ctx, cfg, auth, 0)
	}

	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if host == "" {
		return newProxyAwareHTTPClient(ctx, cfg, auth, 0)
	}
	if !strings.Contains(host, "chatgpt.com") && !strings.Contains(host, "openai.com") {
		return newProxyAwareHTTPClient(ctx, cfg, auth, 0)
	}

	sdkCfg := &config.SDKConfig{}
	if cfg != nil {
		*sdkCfg = cfg.SDKConfig
	}
	if auth != nil && strings.TrimSpace(auth.ProxyURL) != "" {
		sdkCfg.ProxyURL = strings.TrimSpace(auth.ProxyURL)
	}

	return claudeauth.NewAnthropicHttpClient(sdkCfg)
}

func (e *CodexExecutor) resolveCodexConversationBearerToken(ctx context.Context, auth *cliproxyauth.Auth, targetURL string) (string, error) {
	if auth == nil {
		return "", statusErr{code: http.StatusUnauthorized, msg: "codex conversation bridge: missing auth"}
	}

	client := newCodexConversationHTTPClient(ctx, e.cfg, auth, targetURL)
	prepareCodexConversationAuthContext(ctx, client, auth, targetURL)

	var lastErr error
	accessToken := codexConversationAccessToken(auth)
	if accessToken != "" {
		valid, err := codexConversationValidateAccessToken(ctx, client, targetURL, accessToken)
		if err == nil && valid {
			return accessToken, nil
		}
		lastErr = err
	}

	if sessionToken := codexConversationSessionToken(auth); sessionToken != "" {
		refreshedToken, err := codexConversationRefreshAccessTokenBySession(ctx, client, auth, targetURL, sessionToken)
		if err == nil && refreshedToken != "" {
			if auth.Metadata == nil {
				auth.Metadata = make(map[string]any)
			}
			auth.Metadata["access_token"] = refreshedToken
			return refreshedToken, nil
		}
		lastErr = err
	}

	if codexConversationRefreshToken(auth) != "" {
		updated, err := e.Refresh(ctx, auth)
		if err == nil && updated != nil {
			if updated.Metadata == nil {
				updated.Metadata = make(map[string]any)
			}
			auth.Metadata = updated.Metadata
			if updated.Attributes != nil {
				if auth.Attributes == nil {
					auth.Attributes = make(map[string]string)
				}
				for k, v := range updated.Attributes {
					auth.Attributes[k] = v
				}
			}
			if refreshedToken := metaStringValue(auth.Metadata, "access_token"); refreshedToken != "" && refreshedToken != accessToken {
				return refreshedToken, nil
			}
		}
		lastErr = err
	}

	if lastErr != nil {
		return "", lastErr
	}
	return "", statusErr{code: http.StatusUnauthorized, msg: "codex conversation bridge: no usable access token"}
}

func codexConversationValidateAccessToken(ctx context.Context, client *http.Client, targetURL, accessToken string) (bool, error) {
	if client == nil || strings.TrimSpace(accessToken) == "" {
		return false, nil
	}
	endpoint := codexConversationRequestOriginFromRawURL(targetURL) + codexConversationValidatePath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", codexConversationBrowserUserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	body, readErr := ioReadAllAndClose(resp)
	if readErr != nil {
		return false, readErr
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return false, nil
	default:
		return false, statusErr{code: resp.StatusCode, msg: summarizeErrorBody(resp.Header.Get("Content-Type"), body)}
	}
}

func codexConversationRefreshAccessTokenBySession(ctx context.Context, client *http.Client, auth *cliproxyauth.Auth, targetURL, sessionToken string) (string, error) {
	if client == nil || strings.TrimSpace(sessionToken) == "" {
		return "", nil
	}
	endpoint := codexConversationRequestOriginFromRawURL(targetURL) + codexConversationSessionPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", codexConversationBrowserUserAgent)
	req.Header.Set("Cookie", buildCodexConversationCookieHeader(auth, codexConversationDeviceID(auth), ""))

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	body, readErr := ioReadAllAndClose(resp)
	if readErr != nil {
		return "", readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", newCodexStatusErr(resp.StatusCode, body)
	}

	var parsed struct {
		AccessToken string `json:"accessToken"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", err
	}
	if strings.TrimSpace(parsed.AccessToken) == "" {
		return "", fmt.Errorf("codex conversation bridge: session refresh response missing accessToken")
	}
	return strings.TrimSpace(parsed.AccessToken), nil
}

func prepareCodexConversationAuthContext(ctx context.Context, client *http.Client, auth *cliproxyauth.Auth, targetURL string) {
	if client == nil {
		return
	}

	endpoint := codexConversationRequestOriginFromRawURL(targetURL) + codexConversationCSRFPath
	deviceID := codexConversationDeviceID(auth)
	cookieHeader := buildCodexConversationCookieHeader(auth, deviceID, "")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Referer", codexConversationRequestOriginFromRawURL(targetURL)+"/")
	req.Header.Set("User-Agent", codexConversationBrowserUserAgent)
	req.Header.Set("Accept-Language", codexConversationDefaultAcceptLanguage)
	req.Header.Set("Oai-Language", codexConversationDefaultLanguage)
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Sec-Ch-Ua", codexConversationSecCHUA)
	req.Header.Set("Sec-Ch-Ua-Mobile", "?0")
	req.Header.Set("Sec-Ch-Ua-Platform", `"Windows"`)
	req.Header.Set("Sec-Ch-Ua-Arch", `"x86"`)
	req.Header.Set("Sec-Ch-Ua-Bitness", `"64"`)
	req.Header.Set("Sec-Ch-Ua-Full-Version", `"136.0.7103.113"`)
	req.Header.Set("Sec-Ch-Ua-Full-Version-List", codexConversationSecCHUAFullVersion)
	req.Header.Set("Sec-Ch-Ua-Platform-Version", `"15.0.0"`)
	if strings.TrimSpace(cookieHeader) != "" {
		req.Header.Set("Cookie", cookieHeader)
	}

	resp, err := client.Do(req)
	if err != nil {
		return
	}
	body, readErr := ioReadAllAndClose(resp)
	if readErr != nil {
		return
	}
	codexConversationPersistCookieHeader(auth, collectCodexConversationSetCookies(resp))
	if isCodexChallengePage(body) {
		return
	}
}

func codexConversationFetchChatRequirements(ctx context.Context, client *http.Client, targetURL *neturl.URL, bearerToken, deviceID, userAgent, cookieHeader string) (requirementsToken, proofToken, oaiSC string, err error) {
	if client == nil {
		return "", "", "", nil
	}

	origin := codexConversationRequestOrigin(targetURL)
	endpoints := []string{
		origin + "/backend-api/sentinel/chat-requirements",
		origin + "/backend-api/sentinel/chat-requirements/prepare",
	}

	generator := newCodexConversationSentinelGenerator(deviceID, userAgent)
	reqBody, marshalErr := json.Marshal(map[string]any{
		"p": generator.generateRequirementsToken(),
	})
	if marshalErr != nil {
		return "", "", "", marshalErr
	}

	var lastErr error
	for i := range endpoints {
		endpoint := endpoints[i]
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
		if reqErr != nil {
			lastErr = reqErr
			continue
		}
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(bearerToken))
		req.Header.Set("Accept", "*/*")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", codexConversationBrowserizeUserAgent(userAgent))
		req.Header.Set("Oai-Device-Id", deviceID)
		req.Header.Set("Oai-Language", codexConversationDefaultLanguage)
		req.Header.Set("Origin", origin)
		req.Header.Set("Referer", origin+"/")
		req.Header.Set("Sec-Fetch-Dest", "empty")
		req.Header.Set("Sec-Fetch-Mode", "cors")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.Header.Set("Sec-Ch-Ua", codexConversationSecCHUA)
		req.Header.Set("Sec-Ch-Ua-Mobile", "?0")
		req.Header.Set("Sec-Ch-Ua-Platform", `"Windows"`)
		req.Header.Set("Sec-Ch-Ua-Arch", `"x86"`)
		req.Header.Set("Sec-Ch-Ua-Bitness", `"64"`)
		req.Header.Set("Sec-Ch-Ua-Full-Version", `"136.0.7103.113"`)
		req.Header.Set("Sec-Ch-Ua-Full-Version-List", codexConversationSecCHUAFullVersion)
		req.Header.Set("Sec-Ch-Ua-Platform-Version", `"15.0.0"`)
		if strings.TrimSpace(cookieHeader) != "" {
			req.Header.Set("Cookie", cookieHeader)
		}

		resp, doErr := client.Do(req)
		if doErr != nil {
			lastErr = doErr
			continue
		}

		body, readErr := ioReadAllAndClose(resp)
		if readErr != nil {
			lastErr = readErr
			continue
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			lastErr = newCodexStatusErr(resp.StatusCode, body)
			continue
		}

		var parsed codexConversationChatRequirementsResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			lastErr = err
			continue
		}
		token := strings.TrimSpace(parsed.Token)
		if token == "" {
			lastErr = fmt.Errorf("codex conversation bridge: chat-requirements response missing token")
			continue
		}

		proof := generator.generateRequirementsToken()
		if parsed.ProofOfWork.Required && strings.TrimSpace(parsed.ProofOfWork.Seed) != "" {
			proof = generator.generateToken(parsed.ProofOfWork.Seed, parsed.ProofOfWork.Difficulty)
		}

		return token, proof, extractCookieValue(resp, "oai-sc"), nil
	}

	if lastErr != nil {
		return "", "", "", lastErr
	}
	return "", "", "", fmt.Errorf("codex conversation bridge: failed to fetch chat-requirements")
}

func ioReadAllAndClose(resp *http.Response) ([]byte, error) {
	if resp == nil || resp.Body == nil {
		return nil, nil
	}
	defer func() { _ = resp.Body.Close() }()
	return io.ReadAll(resp.Body)
}

func extractCookieValue(resp *http.Response, name string) string {
	if resp == nil {
		return ""
	}
	for _, cookie := range resp.Cookies() {
		if strings.EqualFold(cookie.Name, name) && strings.TrimSpace(cookie.Value) != "" {
			return strings.TrimSpace(cookie.Value)
		}
	}
	for _, setCookie := range resp.Header.Values("Set-Cookie") {
		if value := extractCookieValueFromHeader(setCookie, name); value != "" {
			return value
		}
	}
	return ""
}

func extractCookieValueFromHeader(setCookie, name string) string {
	for _, segment := range strings.Split(setCookie, ";") {
		trimmed := strings.TrimSpace(segment)
		if trimmed == "" {
			continue
		}
		key, value, ok := strings.Cut(trimmed, "=")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(key), name) {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func buildCodexConversationCookieHeader(auth *cliproxyauth.Auth, deviceID, oaiSC string) string {
	var parts []string
	if auth != nil {
		if raw := metaStringValue(auth.Metadata, "cookie"); raw != "" {
			parts = append(parts, raw)
		}
		if raw := metaStringValue(auth.Metadata, "cookies"); raw != "" {
			parts = append(parts, raw)
		}
		if sessionToken := metaStringValue(auth.Metadata, "session_token"); sessionToken != "" {
			parts = append(parts, "__Secure-next-auth.session-token="+sessionToken)
		}
		if storedOaiSC := metaStringValue(auth.Metadata, "oai_sc"); storedOaiSC != "" {
			parts = append(parts, "oai-sc="+storedOaiSC)
		}
		if auth.Attributes != nil {
			if raw := strings.TrimSpace(auth.Attributes["cookie"]); raw != "" {
				parts = append(parts, raw)
			}
			if raw := strings.TrimSpace(auth.Attributes["cookies"]); raw != "" {
				parts = append(parts, raw)
			}
			if sessionToken := strings.TrimSpace(auth.Attributes["session_token"]); sessionToken != "" {
				parts = append(parts, "__Secure-next-auth.session-token="+sessionToken)
			}
			if storedOaiSC := strings.TrimSpace(auth.Attributes["oai_sc"]); storedOaiSC != "" {
				parts = append(parts, "oai-sc="+storedOaiSC)
			}
		}
	}
	if deviceID != "" {
		parts = append(parts, "oai-did="+deviceID)
	}
	if oaiSC != "" {
		parts = append(parts, "oai-sc="+oaiSC)
	}
	return mergeCodexConversationCookies(parts...)
}

func codexConversationCookieValue(auth *cliproxyauth.Auth, name string) string {
	name = strings.TrimSpace(name)
	if auth == nil || name == "" {
		return ""
	}

	if value := codexConversationCookieValueFromStrings(name, metaStringValue(auth.Metadata, "cookie"), metaStringValue(auth.Metadata, "cookies")); value != "" {
		return value
	}
	if auth.Attributes != nil {
		if value := codexConversationCookieValueFromStrings(name, strings.TrimSpace(auth.Attributes["cookie"]), strings.TrimSpace(auth.Attributes["cookies"])); value != "" {
			return value
		}
	}
	return ""
}

func codexConversationCookieValueFromStrings(name string, rawCookies ...string) string {
	for i := range rawCookies {
		for _, segment := range strings.Split(rawCookies[i], ";") {
			key, value, ok := strings.Cut(strings.TrimSpace(segment), "=")
			if !ok {
				continue
			}
			if strings.EqualFold(strings.TrimSpace(key), name) && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
	}
	return ""
}

func collectCodexConversationSetCookies(resp *http.Response) string {
	if resp == nil {
		return ""
	}

	var parts []string
	for _, cookie := range resp.Cookies() {
		if strings.TrimSpace(cookie.Name) == "" || strings.TrimSpace(cookie.Value) == "" {
			continue
		}
		parts = append(parts, cookie.Name+"="+cookie.Value)
	}
	for _, setCookie := range resp.Header.Values("Set-Cookie") {
		for _, segment := range strings.Split(setCookie, ";") {
			pair := strings.TrimSpace(segment)
			if pair == "" {
				continue
			}
			key, value, ok := strings.Cut(pair, "=")
			if !ok {
				continue
			}
			name := strings.TrimSpace(key)
			if name == "" || strings.Contains(strings.ToLower(name), "path") || strings.Contains(strings.ToLower(name), "domain") || strings.Contains(strings.ToLower(name), "expires") || strings.Contains(strings.ToLower(name), "max-age") || strings.Contains(strings.ToLower(name), "samesite") || strings.Contains(strings.ToLower(name), "httponly") || strings.Contains(strings.ToLower(name), "secure") {
				continue
			}
			parts = append(parts, name+"="+strings.TrimSpace(value))
			break
		}
	}
	return mergeCodexConversationCookies(parts...)
}

func codexConversationPersistCookieHeader(auth *cliproxyauth.Auth, cookieHeader string) {
	if auth == nil || strings.TrimSpace(cookieHeader) == "" {
		return
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["cookie"] = mergeCodexConversationCookies(metaStringValue(auth.Metadata, "cookie"), metaStringValue(auth.Metadata, "cookies"), cookieHeader)
}

func codexConversationPersistOaiSC(auth *cliproxyauth.Auth, oaiSC string) {
	if auth == nil || strings.TrimSpace(oaiSC) == "" {
		return
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["oai_sc"] = strings.TrimSpace(oaiSC)
}

func mergeCodexConversationCookies(parts ...string) string {
	order := make([]string, 0, 8)
	values := make(map[string]string)
	for i := range parts {
		raw := strings.TrimSpace(parts[i])
		if raw == "" {
			continue
		}
		for _, segment := range strings.Split(raw, ";") {
			pair := strings.TrimSpace(segment)
			if pair == "" {
				continue
			}
			key, value, ok := strings.Cut(pair, "=")
			if !ok {
				continue
			}
			name := strings.TrimSpace(key)
			if name == "" {
				continue
			}
			if _, exists := values[name]; !exists {
				order = append(order, name)
			}
			values[name] = strings.TrimSpace(value)
		}
	}
	if len(order) == 0 {
		return ""
	}
	merged := make([]string, 0, len(order))
	for i := range order {
		name := order[i]
		merged = append(merged, name+"="+values[name])
	}
	return strings.Join(merged, "; ")
}

func codexConversationDeviceID(auth *cliproxyauth.Auth) string {
	if auth != nil {
		if value := metaStringValue(auth.Metadata, "oai_device_id"); value != "" {
			return value
		}
		if value := metaStringValue(auth.Metadata, "device_id"); value != "" {
			return value
		}
		if auth.Attributes != nil {
			if value := strings.TrimSpace(auth.Attributes["oai_device_id"]); value != "" {
				return value
			}
			if value := strings.TrimSpace(auth.Attributes["device_id"]); value != "" {
				return value
			}
		}
		if value := codexConversationCookieValue(auth, "oai-did"); value != "" {
			return value
		}
	}
	return uuid.NewString()
}

func codexConversationAccessToken(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if value := metaStringValue(auth.Metadata, "access_token"); value != "" {
		return value
	}
	if auth.Attributes != nil {
		if value := strings.TrimSpace(auth.Attributes["access_token"]); value != "" {
			return value
		}
	}
	return ""
}

func codexConversationSessionToken(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if value := metaStringValue(auth.Metadata, "session_token"); value != "" {
		return value
	}
	if auth.Attributes != nil {
		if value := strings.TrimSpace(auth.Attributes["session_token"]); value != "" {
			return value
		}
	}
	if value := codexConversationCookieValue(auth, "__Secure-next-auth.session-token"); value != "" {
		return value
	}
	return ""
}

func codexConversationRefreshToken(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if value := metaStringValue(auth.Metadata, "refresh_token"); value != "" {
		return value
	}
	if auth.Attributes != nil {
		if value := strings.TrimSpace(auth.Attributes["refresh_token"]); value != "" {
			return value
		}
	}
	return ""
}

func codexConversationRequestOrigin(targetURL *neturl.URL) string {
	if targetURL != nil && strings.TrimSpace(targetURL.Scheme) != "" && strings.TrimSpace(targetURL.Host) != "" {
		return targetURL.Scheme + "://" + targetURL.Host
	}
	return "https://chatgpt.com"
}

func codexConversationRequestOriginFromRawURL(rawURL string) string {
	parsed, err := neturl.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "https://chatgpt.com"
	}
	return codexConversationRequestOrigin(parsed)
}

func codexConversationBrowserizeUserAgent(userAgent string) string {
	trimmed := strings.TrimSpace(userAgent)
	if trimmed == "" {
		return codexConversationBrowserUserAgent
	}
	lower := strings.ToLower(trimmed)
	if strings.Contains(lower, "mozilla/") || strings.Contains(lower, "chrome/") {
		return trimmed
	}
	return codexConversationBrowserUserAgent
}

func newCodexConversationSentinelGenerator(deviceID, userAgent string) *codexConversationSentinelGenerator {
	seed := time.Now().UnixNano()
	return &codexConversationSentinelGenerator{
		deviceID:         strings.TrimSpace(deviceID),
		userAgent:        codexConversationBrowserizeUserAgent(userAgent),
		requirementsSeed: fmt.Sprintf("%.16f", rand.New(rand.NewSource(seed)).Float64()),
		sid:              uuid.NewString(),
		random:           rand.New(rand.NewSource(seed + 1)),
	}
}

func (g *codexConversationSentinelGenerator) generateToken(seed, difficulty string) string {
	if g == nil {
		return ""
	}
	normalizedSeed := strings.TrimSpace(seed)
	if normalizedSeed == "" {
		normalizedSeed = g.requirementsSeed
	}
	normalizedDifficulty := strings.TrimSpace(difficulty)
	if normalizedDifficulty == "" {
		normalizedDifficulty = "0"
	}

	start := time.Now()
	config := g.getConfig()
	for nonce := 0; nonce < codexConversationSentinelMaxAttempts; nonce++ {
		if result := g.runCheck(start, normalizedSeed, normalizedDifficulty, config, nonce); result != "" {
			return "gAAAAAB" + result
		}
	}
	return "gAAAAAB" + codexConversationSentinelErrorPrefix + g.base64Encode("None")
}

func (g *codexConversationSentinelGenerator) generateRequirementsToken() string {
	if g == nil {
		return ""
	}
	config := g.getConfig()
	config[3] = 1
	config[9] = int(math.Round(g.randomRange(5, 50)))
	return "gAAAAAC" + g.base64Encode(config)
}

func (g *codexConversationSentinelGenerator) runCheck(start time.Time, seed, difficulty string, config []any, nonce int) string {
	config[3] = nonce
	config[9] = int(math.Round(float64(time.Since(start).Milliseconds())))
	data := g.base64Encode(config)
	hashHex := codexConversationSentinelHash(seed + data)
	prefixLen := len(difficulty)
	if prefixLen <= len(hashHex) && hashHex[:prefixLen] <= difficulty {
		return data + "~S"
	}
	return ""
}

func (g *codexConversationSentinelGenerator) getConfig() []any {
	if g == nil {
		return nil
	}
	nowUTC := time.Now().UTC()
	perfNow := g.randomRange(1000, 50000)
	timeOrigin := float64(time.Now().UnixMilli()) - perfNow
	navProps := []string{
		"vendorSub", "productSub", "vendor", "maxTouchPoints",
		"scheduling", "userActivation", "doNotTrack", "geolocation",
		"connection", "plugins", "mimeTypes", "pdfViewerEnabled",
		"webkitTemporaryStorage", "webkitPersistentStorage",
		"hardwareConcurrency", "cookieEnabled", "credentials",
		"mediaDevices", "permissions", "locks", "ink",
	}
	docKeys := []string{"location", "implementation", "URL", "documentURI", "compatMode"}
	winKeys := []string{"Object", "Function", "Array", "Number", "parseFloat", "undefined"}
	hardwareConcurrency := []int{4, 8, 12, 16}

	return []any{
		"1920x1080",
		nowUTC.Format("Mon Jan 02 2006 15:04:05 GMT+0000 (Coordinated Universal Time)"),
		4294705152,
		g.random.Float64(),
		g.userAgent,
		codexConversationSentinelScriptURL,
		nil,
		nil,
		"en-US",
		"en-US,en",
		g.random.Float64(),
		navProps[g.random.Intn(len(navProps))] + "\u2212undefined",
		docKeys[g.random.Intn(len(docKeys))],
		winKeys[g.random.Intn(len(winKeys))],
		perfNow,
		g.sid,
		"",
		hardwareConcurrency[g.random.Intn(len(hardwareConcurrency))],
		timeOrigin,
	}
}

func (g *codexConversationSentinelGenerator) randomRange(min, max float64) float64 {
	return min + g.random.Float64()*(max-min)
}

func (g *codexConversationSentinelGenerator) base64Encode(data any) string {
	raw, _ := json.Marshal(data)
	return base64.StdEncoding.EncodeToString(raw)
}

func codexConversationSentinelHash(text string) string {
	var h uint32 = 2166136261
	for _, r := range text {
		h ^= uint32(r)
		h *= 16777619
	}
	h ^= h >> 16
	h *= 2246822507
	h ^= h >> 13
	h *= 3266489909
	h ^= h >> 16
	return fmt.Sprintf("%08x", h)
}
