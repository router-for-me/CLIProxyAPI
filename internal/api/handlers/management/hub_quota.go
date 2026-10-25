package management

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/multiuser"
	log "github.com/sirupsen/logrus"
)

// hubQuotaRoute allowlists the exact upstream endpoints a hub credential may be
// queried against through HubQuotaAPICall. The token is attached server-side,
// so without this list a member could point the proxy at an arbitrary host and
// exfiltrate the credential owner's access token.
type hubQuotaRoute struct {
	host     string
	prefixes []string
}

// hubQuotaRoutes maps the credential provider to its known quota/usage
// endpoints (mirrors the frontend quota adapters in
// frontend/src/utils/quota/constants.ts).
var hubQuotaRoutes = map[string][]hubQuotaRoute{
	"antigravity": {
		{host: "daily-cloudcode-pa.googleapis.com", prefixes: []string{"/v1internal:retrieveUserQuotaSummary", "/v1internal:loadCodeAssist"}},
		{host: "daily-cloudcode-pa.sandbox.googleapis.com", prefixes: []string{"/v1internal:retrieveUserQuotaSummary", "/v1internal:loadCodeAssist"}},
		{host: "cloudcode-pa.googleapis.com", prefixes: []string{"/v1internal:retrieveUserQuotaSummary", "/v1internal:loadCodeAssist"}},
	},
	"claude": {
		{host: "api.anthropic.com", prefixes: []string{"/api/oauth/usage", "/api/oauth/profile"}},
	},
	"codex": {
		{host: "chatgpt.com", prefixes: []string{"/backend-api/wham/", "/backend-api/subscriptions"}},
	},
	"zcode": {
		{host: "bigmodel.cn", prefixes: []string{"/api/monitor/usage/quota/limit"}},
		{host: "zcode.z.ai", prefixes: []string{"/api/v1/coding-plan/reset/status", "/api/v1/coding-plan/reset/use"}},
	},
	"kiro": {
		{host: "management.us-east-1.kiro.dev", prefixes: []string{"/getUsageLimits"}},
		{host: "management.eu-central-1.kiro.dev", prefixes: []string{"/getUsageLimits"}},
	},
	"kimi": {
		{host: "api.kimi.com", prefixes: []string{"/coding/v1/usages"}},
		{host: "api.kimi.ai", prefixes: []string{"/coding/v1/usages"}},
	},
	"xai": {
		{host: "cli-chat-proxy.grok.com", prefixes: []string{"/v1/billing", "/v1/user", "/v1/settings"}},
		{host: "api.x.ai", prefixes: []string{"/v1/me", "/v1/chat/completions"}},
	},
	"meta": {
		{host: "api.meta.ai", prefixes: []string{"/muse-code/key"}},
	},
	"devin": {
		{host: "server.codeium.com", prefixes: []string{"/exa.seat_management_pb.SeatManagementService/"}},
	},
}

func hubQuotaRouteAllowed(provider, host, path string) bool {
	routes, known := hubQuotaRoutes[quotaProviderKey(provider)]
	if !known {
		return false
	}
	host = strings.ToLower(strings.TrimSpace(host))
	if quotaProviderKey(provider) == "kiro" {
		if path != "/getUsageLimits" {
			return false
		}
		for _, route := range routes {
			if route.host == host {
				return true
			}
		}
		return false
	}
	for _, route := range routes {
		if route.host != host {
			continue
		}
		for _, prefix := range route.prefixes {
			if path == prefix || !strings.EqualFold(strings.TrimSpace(provider), "zcode") && strings.HasPrefix(path, prefix) {
				return true
			}
		}
	}
	return false
}

type hubQuotaAPICallRequest struct {
	Method string            `json:"method"`
	URL    string            `json:"url"`
	Header map[string]string `json:"header"`
	Data   string            `json:"data"`
}

// HubQuotaAPICall lets any hub member query the upstream quota/usage endpoint
// of a channel-mounted credential. It mirrors the management APICall request
// and response shape, but resolves the credential from the route, restricts the
// target to the provider's known quota endpoints, and never exposes the token.
// The HTTP client timeout follows the same credential-acquisition exception as
// the management APICall.
//
// Endpoint: POST /v8/hub/credentials/:id/api-call
func (h *Handler) HubQuotaAPICall(c *gin.Context) {
	a, ok := h.hubCredential(c, false)
	if !ok {
		return
	}
	if strings.TrimSpace(a.ChannelID()) == "" {
		hubError(c, multiuser.ErrNotFound)
		return
	}
	c.Set("hubAction", "credential.quota")

	var body hubQuotaAPICallRequest
	if errBind := c.ShouldBindJSON(&body); errBind != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	method := strings.ToUpper(strings.TrimSpace(body.Method))
	if method != http.MethodGet && method != http.MethodPost {
		c.JSON(http.StatusBadRequest, gin.H{"error": "only GET and POST quota queries are allowed"})
		return
	}

	urlStr := strings.TrimSpace(body.URL)
	parsedURL, errParse := url.Parse(urlStr)
	if errParse != nil || parsedURL.Scheme != "https" || parsedURL.Host == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid url"})
		return
	}
	if !hubQuotaRouteAllowed(a.Provider, parsedURL.Hostname(), parsedURL.EscapedPath()) {
		c.JSON(http.StatusForbidden, gin.H{"error": "url is not an allowed quota endpoint for this provider"})
		return
	}

	var errPrepare error
	a, errPrepare = h.prepareKiroAuth(c.Request.Context(), a, "")
	if errPrepare != nil {
		log.WithError(errPrepare).Debug("hub quota: Kiro credential preparation failed")
		c.JSON(http.StatusBadRequest, gin.H{"error": "Kiro credential preparation failed"})
		return
	}
	reqHeaders := body.Header
	if reqHeaders == nil {
		reqHeaders = map[string]string{}
	}
	if errQuota := prepareCredentialQuotaRequest(a, parsedURL, reqHeaders); errQuota != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errQuota.Error()})
		return
	}
	urlStr = parsedURL.String()

	var token string
	var tokenResolved bool
	var tokenErr error
	resolveToken := func() error {
		if !tokenResolved {
			token, tokenErr = h.resolveTokenForAuth(c.Request.Context(), a, "")
			tokenResolved = true
		}
		if token == "" {
			if tokenErr != nil {
				return errors.New("auth token refresh failed")
			}
			return errors.New("auth token not found")
		}
		return nil
	}

	for key, value := range reqHeaders {
		if !strings.Contains(value, "$TOKEN$") {
			continue
		}
		if errToken := resolveToken(); errToken != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": errToken.Error()})
			return
		}
		reqHeaders[key] = strings.ReplaceAll(value, "$TOKEN$", token)
	}
	if strings.Contains(body.Data, "$TOKEN$") {
		if errToken := resolveToken(); errToken != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": errToken.Error()})
			return
		}
		replacement := token
		if json.Valid([]byte(body.Data)) && strings.ContainsAny(token, "\"\\\r\n\t") {
			if b, errMarshal := json.Marshal(token); errMarshal == nil && len(b) >= 2 {
				replacement = string(b[1 : len(b)-1])
			}
		}
		body.Data = strings.ReplaceAll(body.Data, "$TOKEN$", replacement)
	}

	var requestBody io.Reader
	if body.Data != "" {
		requestBody = strings.NewReader(body.Data)
	}
	req, errNew := http.NewRequestWithContext(c.Request.Context(), method, urlStr, requestBody)
	if errNew != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to build request"})
		return
	}
	var hostOverride string
	for key, value := range reqHeaders {
		if strings.EqualFold(key, "host") {
			hostOverride = strings.TrimSpace(value)
			continue
		}
		req.Header.Set(key, value)
	}
	if hostOverride != "" {
		req.Host = hostOverride
	}
	if errAuth := applyZCodeResetAuthorization(a, req); errAuth != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errAuth.Error()})
		return
	}

	httpClient := &http.Client{Timeout: defaultAPICallTimeout}
	httpClient.Transport = h.apiCallTransport(a, "")
	resp, errDo := httpClient.Do(req)
	if errDo != nil {
		log.WithError(errDo).Debug("hub quota api-call request failed")
		c.JSON(http.StatusBadGateway, gin.H{"error": "request failed"})
		return
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
	}()

	respBody, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to read response"})
		return
	}
	c.JSON(http.StatusOK, apiCallResponse{
		StatusCode: resp.StatusCode,
		Header:     resp.Header,
		Body:       string(respBody),
	})
}
