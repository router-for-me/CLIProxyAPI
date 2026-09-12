package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

const codexBackendUpstreamBase = "https://chatgpt.com/backend-api"

// codexBackendPooledPaths lists the chatgpt.com backend paths that describe the
// credential serving the turns rather than the caller's account: usage windows
// and usage-limit reset credits. Everything else stays account-scoped.
var codexBackendPooledPaths = []string{"/wham/usage", "/wham/rate-limit-reset-credits"}

func codexBackendUsesPooledCredential(path string) bool {
	for _, prefix := range codexBackendPooledPaths {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

// codexBackendPassthrough serves chatgpt.com backend endpoints so a Codex client
// can point chatgpt_base_url at the proxy. Usage reads are answered with a pooled
// Codex OAuth credential, so the client's usage view reflects the pool serving
// its turns rather than the signed-in account. Account-scoped endpoints (plugins,
// settings, tasks, profile) are forwarded with the caller's own identity, so the
// client keeps the environment of the account it is signed into.
func (s *Server) codexBackendPassthrough(c *gin.Context) {
	if s == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Codex backend unavailable"})
		return
	}
	path := "/" + strings.Trim(c.Param("segment"), "/") + c.Param("path")
	req := codexBackendStripProxyKey(c)
	if codexBackendUsesPooledCredential(path) {
		s.codexBackendPooledRequest(c, req, path)
		return
	}
	s.codexBackendIdentityProxy().ServeHTTP(c.Writer, req)
}

// codexBackendStripProxyKey removes the configured proxy key that AuthMiddleware
// matched, wherever the caller carried it, so it never reaches chatgpt.com or the
// request log. With no configured keys nothing matched and the request is
// forwarded untouched.
func codexBackendStripProxyKey(c *gin.Context) *http.Request {
	key := c.GetString("userApiKey")
	if key == "" {
		return c.Request
	}
	req := c.Request.Clone(c.Request.Context())
	for _, name := range []string{"Authorization", "X-Api-Key", "X-Goog-Api-Key"} {
		value := req.Header.Get(name)
		if len(value) > 7 && strings.EqualFold(value[:7], "Bearer ") {
			value = strings.TrimSpace(value[7:])
		}
		if value == key {
			req.Header.Del(name)
		}
	}
	query := req.URL.Query()
	stripped := false
	for _, name := range []string{"key", "auth_token"} {
		if query.Get(name) == key {
			query.Del(name)
			stripped = true
		}
	}
	if stripped {
		req.URL.RawQuery = query.Encode()
	}
	return req
}

// codexBackendIdentityProxy forwards the request verbatim, including the
// caller's Authorization and ChatGPT account headers, to chatgpt.com. It is
// built per request so a hot-reloaded proxy-url takes effect immediately, the
// same way the Codex executor builds its client per request.
func (s *Server) codexBackendIdentityProxy() *httputil.ReverseProxy {
	// The incoming path already carries /backend-api, so rewrite only the origin.
	upstream, _ := url.Parse(strings.TrimSuffix(codexBackendUpstreamBase, "/backend-api"))
	transport := s.codexBackendTransport
	if transport == nil {
		// chatgpt.com rejects Go's default TLS fingerprint on account endpoints
		// (451 no_biscuit_no_service), so reuse the Chrome-profile client the
		// Codex executor already uses; it also honors the configured proxy-url.
		transport = helps.NewUtlsHTTPClient(context.Background(), s.cfg, nil, 0).Transport
	}
	return &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(upstream)
			pr.Out.Host = upstream.Host
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, errProxy error) {
			log.WithError(errProxy).Warnf("codex backend passthrough: upstream request failed for %s", r.URL.Path)
			w.WriteHeader(http.StatusBadGateway)
		},
	}
}

// codexBackendPooledRequest answers a usage endpoint with a pooled Codex OAuth
// credential selected the same way the alpha-search route selects one.
func (s *Server) codexBackendPooledRequest(c *gin.Context, req *http.Request, path string) {
	if s.handlers == nil || s.handlers.AuthManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Codex auth manager unavailable"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, 16<<20))
	if err != nil {
		c.JSON(clienterror.HTTPStatusFromErrorOr(err, http.StatusBadRequest), gin.H{"error": "Failed to read Codex backend request"})
		return
	}

	ctx := context.WithValue(c.Request.Context(), "gin", c)
	selectionOpts := coreexecutor.Options{Headers: req.Header.Clone(), OriginalRequest: body}
	var selection *auth.HomeDispatchSelection
	var selected *auth.Auth
	// Home dispatch rejects an empty model and a usage read carries none, so
	// route it as the Codex default template; local selection keeps the empty
	// model like the Codex Live handler.
	selectionModel := ""
	if s.handlers.AuthManager.HomeEnabled() {
		selectionModel = registry.CodexClientDefaultModel
		selection, err = s.handlers.AuthManager.SelectHomeAuthWithCredentialPolicy(ctx, "codex", selectionModel, auth.CredentialPolicyCodexBackendV1, selectionOpts)
		if selection != nil {
			selected = selection.CloneAuth()
		}
	} else {
		selected, err = s.handlers.AuthManager.SelectAuthWithCredentialPolicy(ctx, "codex", selectionModel, auth.CredentialPolicyCodexBackendV1, selectionOpts)
	}
	if err != nil {
		status := clienterror.HTTPStatusFromErrorOr(err, http.StatusServiceUnavailable)
		for _, value := range auth.SafeResponseHeaders(err).Values("Retry-After") {
			c.Writer.Header().Add("Retry-After", value)
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	if selected == nil {
		if selection != nil {
			selection.End("missing_auth")
		}
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Codex auth unavailable"})
		return
	}
	if selection != nil {
		attemptCtx, release, errBind := homeSelectionAttemptContext(ctx, selection)
		if errBind != nil {
			selection.End("attempt_bind_failed")
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": errBind.Error()})
			return
		}
		ctx = attemptCtx
		defer release()
	}
	logging.SetGinCPATraceID(c, selected.EnsureIndex())

	upstreamURL := codexBackendUpstreamBase + path
	if rawQuery := req.URL.RawQuery; rawQuery != "" {
		upstreamURL += "?" + rawQuery
	}
	method := req.Method
	baseHeaders := make(http.Header)
	baseHeaders.Set("Accept", "application/json")
	baseHeaders.Set("Originator", "codex_cli_rs")
	for _, name := range []string{"Content-Type", "Version", "User-Agent", "Session_id", "X-Client-Request-Id"} {
		if value := strings.TrimSpace(req.Header.Get(name)); value != "" {
			baseHeaders.Set(name, value)
		}
	}
	var requestBody []byte
	if len(body) > 0 {
		requestBody = body
	}
	performRequest := func(current *auth.Auth) (*http.Response, error) {
		headers := baseHeaders.Clone()
		if accountID, ok := current.Metadata["account_id"].(string); ok && strings.TrimSpace(accountID) != "" {
			headers.Set("Chatgpt-Account-Id", accountID)
		}
		req, errRequest := s.handlers.AuthManager.NewHttpRequest(ctx, current, method, upstreamURL, requestBody, headers)
		if errRequest != nil {
			return nil, errRequest
		}
		authType, authValue := current.AccountInfo()
		helps.RecordAPIRequest(ctx, s.cfg, helps.UpstreamRequestLog{
			URL:       upstreamURL,
			Method:    method,
			Headers:   req.Header.Clone(),
			Body:      requestBody,
			Provider:  "codex",
			AuthID:    current.ID,
			AuthLabel: current.Label,
			AuthType:  authType,
			AuthValue: authValue,
		})
		return s.handlers.AuthManager.HttpRequest(ctx, current, req)
	}
	s.relayCodexUpstream(c, ctx, selection, selected, selectionModel, "codex backend passthrough", performRequest, nil)
}
