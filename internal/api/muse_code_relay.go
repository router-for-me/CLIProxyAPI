package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	metaauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/meta"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

const (
	museCodeProvider = "meta"
	// museCodeRelayMaxBody caps the request and response bodies the relay carries.
	museCodeRelayMaxBody = 16 << 20
)

// museCodeRelayPaths are the Muse Code client endpoints, relative to
// /muse-code, that the relay forwards. The client asks for them at the root of
// the origin its base URL names, and refuses to start without the model
// catalog. Endpoints that mint or revoke credentials, and telemetry, are left
// out on purpose.
var museCodeRelayPaths = []string{"models", "config", "search", "browser_open"}

// museCodeRelayQueryCredentials are the query parameters client access accepts
// as a proxy API key. They are never passed on to Meta.
var museCodeRelayQueryCredentials = []string{"key", "auth_token"}

// registerMuseCodeRoutes serves the Muse Code client endpoints so the Muse CLI
// can use the proxy as its endpoint.
func (s *Server) registerMuseCodeRoutes() {
	museCode := s.engine.Group("/muse-code")
	museCode.Use(AuthMiddleware(s.accessManager))
	for _, path := range museCodeRelayPaths {
		museCode.GET("/"+path, s.museCodeRelay)
		museCode.POST("/"+path, s.museCodeRelay)
	}
}

// museCodeRelay forwards one Muse Code client request to Meta with a Meta
// credential and returns the answer unchanged. These payloads are already in
// Meta's format and must not pass through a protocol translator.
func (s *Server) museCodeRelay(c *gin.Context) {
	if s == nil || s.handlers == nil || s.handlers.AuthManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Meta auth manager unavailable"})
		return
	}
	// Home dispatches a credential for a model, and these requests carry none,
	// so they are refused plainly rather than with the legacy selection error.
	if s.handlers.AuthManager.HomeEnabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Muse Code endpoints are unavailable while Home is enabled"})
		return
	}

	var body []byte
	if c.Request.Method != http.MethodGet && c.Request.Body != nil {
		requestBody, errRead := io.ReadAll(io.LimitReader(c.Request.Body, museCodeRelayMaxBody+1))
		if errRead != nil {
			c.JSON(clienterror.HTTPStatusFromErrorOr(errRead, http.StatusBadRequest), gin.H{"error": "Failed to read Muse Code request"})
			return
		}
		if len(requestBody) > museCodeRelayMaxBody {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "Muse Code request is too large"})
			return
		}
		body = requestBody
	}

	ctx := context.WithValue(c.Request.Context(), "gin", c)
	selectionModel := museCodeSelectionModel()
	if selectionModel == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Meta auth unavailable"})
		return
	}
	selected, errSelect := s.handlers.AuthManager.SelectAuth(ctx, museCodeProvider, selectionModel, coreexecutor.Options{Headers: c.Request.Header.Clone()})
	if errSelect != nil {
		for _, value := range auth.SafeResponseHeaders(errSelect).Values("Retry-After") {
			c.Writer.Header().Add("Retry-After", value)
		}
		c.JSON(clienterror.HTTPStatusFromErrorOr(errSelect, http.StatusServiceUnavailable), gin.H{"error": errSelect.Error()})
		return
	}
	if selected == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Meta auth unavailable"})
		return
	}
	// A Meta login that holds only a DCA token gets its API key minted and saved
	// here, before the origin is derived, because the mint may name the origin.
	if executor, okExecutor := s.handlers.AuthManager.Executor(museCodeProvider); okExecutor {
		if preparer, okPreparer := executor.(auth.RequestAuthPreparer); okPreparer {
			prepared, errPrepare := s.handlers.AuthManager.PrepareRequestAuth(ctx, preparer, selected)
			if errPrepare != nil {
				c.JSON(clienterror.HTTPStatusFromErrorOr(errPrepare, http.StatusBadGateway), gin.H{"error": errPrepare.Error()})
				return
			}
			if prepared != nil {
				selected = prepared
			}
		}
	}

	upstreamURL, errURL := museCodeUpstreamURL(selected, c.Request.URL.Path, museCodeRelayQuery(c.Request.URL.RawQuery))
	if errURL != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": errURL.Error()})
		return
	}
	req, errRequest := s.handlers.AuthManager.NewHttpRequest(ctx, selected, c.Request.Method, upstreamURL, body, museCodeRelayHeaders(c.Request.Header))
	if errRequest != nil {
		c.JSON(clienterror.HTTPStatusFromErrorOr(errRequest, http.StatusBadGateway), gin.H{"error": errRequest.Error()})
		return
	}
	resp, errDo := s.handlers.AuthManager.HttpRequest(ctx, selected, req)
	if errDo != nil {
		c.JSON(clienterror.HTTPStatusFromErrorOr(errDo, http.StatusBadGateway), gin.H{"error": errDo.Error()})
		return
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("muse code relay: close response body error: %v", errClose)
		}
	}()
	upstreamBody, errRead := io.ReadAll(io.LimitReader(resp.Body, museCodeRelayMaxBody+1))
	if errRead != nil {
		c.JSON(clienterror.HTTPStatusFromErrorOr(errRead, http.StatusBadGateway), gin.H{"error": "Failed to read Muse Code response"})
		return
	}
	if len(upstreamBody) > museCodeRelayMaxBody {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Muse Code response is too large"})
		return
	}
	for _, name := range []string{"Content-Type", "Cache-Control", "Etag", "Last-Modified"} {
		if value := resp.Header.Get(name); value != "" {
			c.Header(name, value)
		}
	}
	c.Status(resp.StatusCode)
	_, _ = c.Writer.Write(upstreamBody)
}

// museCodeSelectionModel returns a model some Meta credential serves. Selecting
// with it leaves out the credentials that serve none, such as a config key
// turned off with a "*" model exclusion, which a selection without a model
// would still pick.
func museCodeSelectionModel() string {
	for _, model := range registry.GetGlobalRegistry().GetAvailableModelsByProvider(museCodeProvider) {
		if model != nil && strings.TrimSpace(model.ID) != "" {
			return model.ID
		}
	}
	return ""
}

// museCodeRelayQuery drops the proxy credentials from a relayed query string.
func museCodeRelayQuery(rawQuery string) string {
	if rawQuery == "" {
		return ""
	}
	values, _ := url.ParseQuery(rawQuery)
	for _, name := range museCodeRelayQueryCredentials {
		values.Del(name)
	}
	return values.Encode()
}

// museCodeUpstreamURL maps a relayed request onto the origin the credential
// talks to. Muse Code endpoints live at the root of that origin, beside the
// versioned API the credential's base URL names.
func museCodeUpstreamURL(selected *auth.Auth, path, rawQuery string) (string, error) {
	baseURL := ""
	if selected != nil {
		if selected.Attributes != nil {
			baseURL = strings.TrimSpace(selected.Attributes["base_url"])
		}
		for _, key := range []string{"base_url", "api_base_url"} {
			if baseURL != "" || selected.Metadata == nil {
				break
			}
			if value, ok := selected.Metadata[key].(string); ok {
				baseURL = strings.TrimSpace(value)
			}
		}
	}
	if baseURL == "" {
		baseURL = metaauth.DefaultAPIBaseURL
	}
	parsed, errParse := url.Parse(baseURL)
	if errParse != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("Meta credential has an invalid base URL")
	}
	upstream := url.URL{Scheme: parsed.Scheme, Host: parsed.Host, Path: path, RawQuery: rawQuery}
	return upstream.String(), nil
}

// museCodeRelayHeaders keeps the request headers of the Muse Code client that
// Meta's endpoints read. The client authenticates to the proxy with a proxy
// API key, so its Authorization header is never forwarded; the Meta credential
// is injected when the request is prepared.
func museCodeRelayHeaders(in http.Header) http.Header {
	out := make(http.Header)
	for name, values := range in {
		canonical := http.CanonicalHeaderKey(name)
		switch {
		case canonical == "Accept", canonical == "Content-Type", canonical == "Traceparent", canonical == "If-None-Match":
		case strings.HasPrefix(canonical, "X-Tbh-"), strings.HasPrefix(canonical, "X-Meta-Ai-"):
		default:
			continue
		}
		out[canonical] = append([]string(nil), values...)
	}
	return out
}
