package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	managementHandlers "github.com/router-for-me/CLIProxyAPI/v7/internal/api/handlers/management"
	claudemodels "github.com/router-for-me/CLIProxyAPI/v7/internal/client/claude/models"
	codexlive "github.com/router-for-me/CLIProxyAPI/v7/internal/client/codex/live"
	codexmodels "github.com/router-for-me/CLIProxyAPI/v7/internal/client/codex/models"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/client/grokbuild"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers/gemini"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers/openai"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
)

const oauthCallbackSuccessHTML = `<html><head><meta charset="utf-8"><title>Authentication successful</title><script>setTimeout(function(){window.close();},5000);</script></head><body><h1>Authentication successful!</h1><p>You can close this window.</p><p>This window will close automatically in 5 seconds.</p></body></html>`

const codexAlphaSearchSourceFormat = "codex-alpha-search"

// setupRoutes configures the API routes for the server.
// It defines the endpoints and associates them with their respective handlers.
func (s *Server) setupRoutes() {
	healthzHandler := func(c *gin.Context) {
		if c.Request.Method == http.MethodHead {
			c.Status(http.StatusOK)
			return
		}

		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	}
	s.engine.GET("/healthz", healthzHandler)
	s.engine.HEAD("/healthz", healthzHandler)

	s.engine.GET("/management.html", s.serveManagementControlPanel)
	openaiHandlers := openai.NewOpenAIAPIHandler(s.handlers)
	geminiHandlers := gemini.NewGeminiAPIHandler(s.handlers)
	claudeCodeHandlers := claude.NewClaudeCodeAPIHandler(s.handlers)
	openaiResponsesHandlers := openai.NewOpenAIResponsesAPIHandler(s.handlers)
	s.codexLiveHandler = codexlive.NewHandler(s.handlers.AuthManager, s.cfg)

	// OpenAI compatible API routes
	v1 := s.engine.Group("/v1")
	v1.Use(AuthMiddleware(s.accessManager))
	{
		v1.GET("/models", s.unifiedModelsHandler(openaiHandlers, claudeCodeHandlers))
		v1.POST("/chat/completions", openaiHandlers.ChatCompletions)
		v1.POST("/completions", openaiHandlers.Completions)
		v1.POST("/images/generations", openaiHandlers.ImagesGenerations)
		v1.POST("/images/edits", openaiHandlers.ImagesEdits)
		v1.POST("/videos", openaiHandlers.XAIVideosGenerations)
		v1.POST("/videos/generations", openaiHandlers.XAIVideosGenerations)
		v1.POST("/videos/edits", openaiHandlers.XAIVideosEdits)
		v1.POST("/videos/extensions", openaiHandlers.XAIVideosExtensions)
		v1.GET("/videos/:request_id", openaiHandlers.XAIVideosRetrieve)
		v1.POST("/messages", claudeCodeHandlers.ClaudeMessages)
		v1.POST("/messages/count_tokens", claudeCodeHandlers.ClaudeCountTokens)
		v1.GET("/responses", openaiResponsesHandlers.ResponsesWebsocket)
		v1.POST("/responses", openaiResponsesHandlers.Responses)
		v1.POST("/responses/compact", openaiResponsesHandlers.Compact)
		v1.POST("/alpha/search", s.codexAlphaSearch)
		v1.POST("/live", s.codexLiveHandler.Handle)
		v1.GET("/live/:call_id", s.codexLiveHandler.HandleSideband)
	}

	realtimeAuth := realtimeAuthMiddleware(s.accessManager, s.codexLiveHandler)
	standardAuth := realtimeStandardAuthMiddleware(s.accessManager)
	s.engine.GET("/v1/realtime", realtimeAuth, s.codexLiveHandler.HandleRealtimeWebsocket)
	s.engine.POST("/v1/realtime", realtimeAuth, s.codexLiveHandler.Handle)
	s.engine.POST("/v1/realtime/calls", realtimeAuth, s.codexLiveHandler.Handle)
	s.engine.GET("/v1/realtime/calls/:call_id", realtimeAuth, s.codexLiveHandler.HandleSideband)
	s.engine.POST("/v1/realtime/client_secrets", standardAuth, s.codexLiveHandler.CreateClientSecret)
	s.engine.POST("/v1/realtime/sessions", standardAuth, s.codexLiveHandler.CreateLegacySession)
	s.engine.POST("/v1/realtime/transcription_sessions", standardAuth, s.codexLiveHandler.HandleTranscriptionSession)
	s.engine.GET("/v1/realtime/translations", realtimeAuth, s.codexLiveHandler.HandleTranslation)
	s.engine.POST("/v1/realtime/translations", realtimeAuth, s.codexLiveHandler.HandleTranslation)
	s.engine.POST("/v1/realtime/translations/client_secrets", standardAuth, s.codexLiveHandler.HandleTranslation)
	s.engine.POST("/v1/realtime/calls/:call_id/hangup", standardAuth, s.codexLiveHandler.HandleHangup)
	s.engine.POST("/v1/realtime/calls/:call_id/accept", standardAuth, s.codexLiveHandler.HandleSIPControl)
	s.engine.POST("/v1/realtime/calls/:call_id/reject", standardAuth, s.codexLiveHandler.HandleSIPControl)
	s.engine.POST("/v1/realtime/calls/:call_id/refer", standardAuth, s.codexLiveHandler.HandleSIPControl)

	openaiV1 := s.engine.Group("/openai/v1")
	openaiV1.Use(AuthMiddleware(s.accessManager))
	{
		openaiV1.POST("/videos", openaiHandlers.VideosCreate)
		openaiV1.GET("/videos/:video_id/content", openaiHandlers.VideosContent)
		openaiV1.GET("/videos/:video_id", openaiHandlers.VideosRetrieve)
	}

	// Codex CLI direct route aliases (chatgpt_base_url compatible)
	codexDirect := s.engine.Group("/backend-api/codex")
	codexDirect.Use(AuthMiddleware(s.accessManager))
	{
		codexDirect.GET("/responses", openaiResponsesHandlers.ResponsesWebsocket)
		codexDirect.POST("/responses", openaiResponsesHandlers.Responses)
		codexDirect.POST("/responses/compact", openaiResponsesHandlers.Compact)
		codexDirect.POST("/alpha/search", s.codexAlphaSearch)
	}

	// Gemini compatible API routes
	v1beta := s.engine.Group("/v1beta")
	v1beta.Use(AuthMiddleware(s.accessManager))
	{
		v1beta.GET("/models", s.geminiModelsHandler(geminiHandlers))
		v1beta.POST("/interactions", geminiHandlers.Interactions)
		v1beta.POST("/models/*action", geminiHandlers.GeminiHandler)
		v1beta.GET("/models/*action", s.geminiGetHandler(geminiHandlers))
	}

	// Root endpoint
	s.engine.GET("/", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"message": "CLI Proxy API Server",
			"endpoints": []string{
				"POST /v1/chat/completions",
				"POST /v1/completions",
				"GET /v1/models",
			},
		})
	})

	// OAuth callback endpoints (reuse main server port)
	// These endpoints receive provider redirects and persist
	// the short-lived code/state for the waiting goroutine.
	s.engine.GET("/anthropic/callback", func(c *gin.Context) {
		code := c.Query("code")
		state := c.Query("state")
		errStr := c.Query("error")
		if errStr == "" {
			errStr = c.Query("error_description")
		}
		if state != "" {
			_, _ = managementHandlers.WriteOAuthCallbackFileForPendingSession(s.cfg.AuthDir, "anthropic", state, code, errStr)
		}
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusOK, oauthCallbackSuccessHTML)
	})

	s.engine.GET("/codex/callback", func(c *gin.Context) {
		code := c.Query("code")
		state := c.Query("state")
		errStr := c.Query("error")
		if errStr == "" {
			errStr = c.Query("error_description")
		}
		if state != "" {
			_, _ = managementHandlers.WriteOAuthCallbackFileForPendingSession(s.cfg.AuthDir, "codex", state, code, errStr)
		}
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusOK, oauthCallbackSuccessHTML)
	})

	s.engine.GET("/antigravity/callback", func(c *gin.Context) {
		code := c.Query("code")
		state := c.Query("state")
		errStr := c.Query("error")
		if errStr == "" {
			errStr = c.Query("error_description")
		}
		if state != "" {
			_, _ = managementHandlers.WriteOAuthCallbackFileForPendingSession(s.cfg.AuthDir, "antigravity", state, code, errStr)
		}
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusOK, oauthCallbackSuccessHTML)
	})

	// Management routes are registered lazily by registerManagementRoutes when a secret is configured.
}

func (s *Server) codexAlphaSearchModelRouterHost() handlers.PluginModelRouterHost {
	if s == nil {
		return nil
	}
	if s.pluginHost != nil {
		return s.pluginHost
	}
	if s.handlers != nil && s.handlers.ModelRouterHost != nil {
		return s.handlers.ModelRouterHost
	}
	return nil
}

func (s *Server) codexAlphaSearchSelectionModel(ctx context.Context, c *gin.Context, body []byte, model string) (string, error) {
	host := s.codexAlphaSearchModelRouterHost()
	if host == nil {
		return model, nil
	}

	var headers http.Header
	queryValues := make(map[string][]string)
	requestPath := ""
	if c != nil && c.Request != nil {
		headers = c.Request.Header.Clone()
		if c.Request.URL != nil {
			queryValues = c.Request.URL.Query()
			requestPath = c.Request.URL.Path
		}
	}
	metadata := map[string]any{
		coreexecutor.RequestedModelMetadataKey: model,
	}
	if requestPath != "" {
		metadata[coreexecutor.RequestPathMetadataKey] = requestPath
	}
	resp, handled := host.RouteModel(ctx, pluginapi.ModelRouteRequest{
		SourceFormat:   codexAlphaSearchSourceFormat,
		RequestedModel: model,
		Headers:        headers,
		Query:          queryValues,
		Body:           body,
		Metadata:       metadata,
	})
	if !handled || !resp.Handled {
		return model, nil
	}
	if resp.TargetKind != pluginapi.ModelRouteTargetProvider || !strings.EqualFold(strings.TrimSpace(resp.Target), "codex") {
		return "", fmt.Errorf("unsupported Codex Alpha Search model route target %q (%q)", resp.TargetKind, resp.Target)
	}
	if targetModel := strings.TrimSpace(resp.TargetModel); targetModel != "" {
		return targetModel, nil
	}
	return model, nil
}

func sanitizeCodexAlphaSearchBody(body []byte) []byte {
	var payload map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(body, &payload); errUnmarshal != nil || payload == nil {
		return body
	}

	removed := false
	for _, field := range []string{"prompt_cache_key", "prompt_cache_retention"} {
		if _, exists := payload[field]; exists {
			delete(payload, field)
			removed = true
		}
	}
	if !removed {
		return body
	}

	sanitizedBody, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return body
	}
	return sanitizedBody
}

// rewriteCodexAlphaSearchModel replaces the top-level model field with the
// credential-resolved upstream model before the request is forwarded.
func rewriteCodexAlphaSearchModel(body []byte, upstreamModel string) []byte {
	upstreamModel = strings.TrimSpace(upstreamModel)
	if upstreamModel == "" {
		return body
	}

	var payload map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(body, &payload); errUnmarshal != nil || payload == nil {
		return body
	}
	if _, exists := payload["model"]; !exists {
		return body
	}

	modelJSON, errMarshalModel := json.Marshal(upstreamModel)
	if errMarshalModel != nil {
		return body
	}
	if string(payload["model"]) == string(modelJSON) {
		return body
	}

	payload["model"] = modelJSON
	rewrittenBody, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return body
	}
	return rewrittenBody
}

func homeSelectionAttemptContext(ctx context.Context, selection *auth.HomeDispatchSelection) (context.Context, func(), error) {
	if selection == nil {
		return nil, func() {}, errors.New("Home dispatch selection is nil")
	}
	return selection.AttemptContext(ctx)
}

// codexAlphaSearch forwards the standalone search endpoint used by current
// Codex clients. Unlike /responses, this payload is already in Codex search
// format and must not pass through a protocol translator.
func (s *Server) codexAlphaSearch(c *gin.Context) {
	if s == nil || s.handlers == nil || s.handlers.AuthManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Codex auth manager unavailable"})
		return
	}

	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 16<<20))
	if err != nil {
		c.JSON(clienterror.HTTPStatusFromErrorOr(err, http.StatusBadRequest), gin.H{"error": "Failed to read search request"})
		return
	}

	var routing struct {
		ID    string `json:"id"`
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &routing)
	upstreamRequestBody := sanitizeCodexAlphaSearchBody(body)

	selectionHeaders := c.Request.Header.Clone()
	if sessionID := strings.TrimSpace(routing.ID); sessionID != "" {
		selectionHeaders.Set("X-Session-ID", sessionID)
	}
	ctx := context.WithValue(c.Request.Context(), "gin", c)
	selectionModel, errRoute := s.codexAlphaSearchSelectionModel(ctx, c, body, strings.TrimSpace(routing.Model))
	if errRoute != nil {
		log.WithError(errRoute).Warn("codex alpha search: model router returned an unsupported target")
		c.JSON(clienterror.HTTPStatusFromErrorOr(errRoute, http.StatusServiceUnavailable), gin.H{"error": errRoute.Error()})
		return
	}
	selectionOpts := coreexecutor.Options{Headers: selectionHeaders, OriginalRequest: body}
	var selection *auth.HomeDispatchSelection
	var selected *auth.Auth
	if s.handlers.AuthManager.HomeEnabled() {
		selection, err = s.handlers.AuthManager.SelectHomeAuthWithCredentialPolicy(ctx, "codex", selectionModel, auth.CredentialPolicyCodexAlphaSearchV1, selectionOpts)
		if selection != nil {
			selected = selection.CloneAuth()
		}
	} else {
		selected, err = s.handlers.AuthManager.SelectAuthWithCredentialPolicy(ctx, "codex", selectionModel, auth.CredentialPolicyCodexAlphaSearchV1, selectionOpts)
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
	var releaseAttempt func()
	if selection != nil {
		attemptCtx, release, errBind := homeSelectionAttemptContext(ctx, selection)
		if errBind != nil {
			selection.End("attempt_bind_failed")
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": errBind.Error()})
			return
		}
		ctx = attemptCtx
		releaseAttempt = release
		defer releaseAttempt()
	}
	logging.SetGinCPATraceID(c, selected.EnsureIndex())

	baseHeaders := make(http.Header)
	baseHeaders.Set("Content-Type", "application/json")
	baseHeaders.Set("Accept", "application/json")
	baseHeaders.Set("Originator", "codex_cli_rs")
	for _, name := range []string{"Version", "User-Agent", "Session_id", "X-Client-Request-Id"} {
		if value := strings.TrimSpace(c.GetHeader(name)); value != "" {
			baseHeaders.Set(name, value)
		}
	}

	errMissingBaseURL := errors.New("Codex Alpha Search API key base URL unavailable")
	routeModel := strings.TrimSpace(selectionModel)
	if routeModel == "" {
		routeModel = strings.TrimSpace(routing.Model)
	}
	performRequest := func(current *auth.Auth) (*http.Response, error) {
		headers := baseHeaders.Clone()
		if accountID, ok := current.Metadata["account_id"].(string); ok && strings.TrimSpace(accountID) != "" {
			headers.Set("Chatgpt-Account-Id", accountID)
		}
		upstreamURL := "https://chatgpt.com/backend-api/codex/alpha/search"
		requestBody := upstreamRequestBody
		// API-key Alpha Search reuses normal credential-aware model resolution so
		// CPA routing prefixes and model aliases are not forwarded upstream.
		if current.AuthKind() == auth.AuthKindAPIKey {
			baseURL := ""
			if current.Attributes != nil {
				baseURL = strings.TrimSpace(current.Attributes["base_url"])
			}
			if baseURL == "" {
				return nil, errMissingBaseURL
			}
			upstreamURL = strings.TrimRight(baseURL, "/") + "/alpha/search"
			if upstreamModel := s.handlers.AuthManager.ResolveExecutionModel(current, routeModel); upstreamModel != "" {
				requestBody = rewriteCodexAlphaSearchModel(upstreamRequestBody, upstreamModel)
			}
		}
		req, errRequest := s.handlers.AuthManager.NewHttpRequest(ctx, current, http.MethodPost, upstreamURL, requestBody, headers)
		if errRequest != nil {
			return nil, errRequest
		}
		authType, authValue := current.AccountInfo()
		helps.RecordAPIRequest(ctx, s.cfg, helps.UpstreamRequestLog{
			URL:       upstreamURL,
			Method:    http.MethodPost,
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

	if errCtx := ctx.Err(); errCtx != nil {
		if selection != nil {
			selection.End("attempt_canceled")
		}
		c.JSON(clienterror.HTTPStatusFromErrorOr(errCtx, http.StatusRequestTimeout), gin.H{"error": errCtx.Error()})
		return
	}
	resp, err := performRequest(selected)
	if err != nil {
		if errors.Is(err, errMissingBaseURL) {
			if selection != nil {
				selection.End("missing_base_url")
			}
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
			return
		}
		if selection != nil {
			selection.End("request_failed")
		}
		helps.RecordAPIResponseError(ctx, s.cfg, err)
		c.JSON(clienterror.HTTPStatusFromErrorOr(err, http.StatusBadGateway), gin.H{"error": err.Error()})
		return
	}
	if selection != nil && resp.StatusCode == http.StatusUnauthorized {
		s.handlers.AuthManager.ReportHomeUnauthorized(ctx, selected, "codex", selectionModel)
		helps.RecordAPIResponseMetadata(ctx, s.cfg, resp.StatusCode, resp.Header.Clone())
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("codex alpha search: close unauthorized response body error: %v", errClose)
		}
		refreshed, didRefresh, errRefresh := s.handlers.AuthManager.RefreshHomeSelectionAfterUnauthorized(ctx, selection, selected)
		if errRefresh != nil {
			selection.End("refresh_failed")
			c.JSON(clienterror.HTTPStatusFromErrorOr(errRefresh, http.StatusServiceUnavailable), gin.H{"error": errRefresh.Error()})
			return
		}
		if !didRefresh || refreshed == nil {
			selection.End("refresh_unavailable")
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Codex credential unauthorized"})
			return
		}
		selected = refreshed
		logging.SetGinCPATraceID(c, selected.EnsureIndex())
		resp, err = performRequest(selected)
		if err != nil {
			if errors.Is(err, errMissingBaseURL) {
				selection.End("missing_base_url")
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
				return
			}
			selection.End("retry_failed")
			helps.RecordAPIResponseError(ctx, s.cfg, err)
			c.JSON(clienterror.HTTPStatusFromErrorOr(err, http.StatusBadGateway), gin.H{"error": err.Error()})
			return
		}
		if resp.StatusCode == http.StatusUnauthorized {
			s.handlers.AuthManager.ReportHomeUnauthorized(ctx, selected, "codex", selectionModel)
		}
	}
	closeResponseBody := func() error {
		errClose := resp.Body.Close()
		if errClose != nil {
			log.Errorf("codex alpha search: close response body error: %v", errClose)
		}
		return errClose
	}
	if selection != nil {
		if errBind := selection.Bind(closeResponseBody); errBind != nil {
			selection.End("response_bind_failed")
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": errBind.Error()})
			return
		}
		defer selection.End("response_closed")
	} else {
		defer func() { _ = closeResponseBody() }()
	}
	helps.RecordAPIResponseMetadata(ctx, s.cfg, resp.StatusCode, resp.Header.Clone())
	upstreamBody, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		helps.RecordAPIResponseError(ctx, s.cfg, err)
		c.JSON(clienterror.HTTPStatusFromErrorOr(err, http.StatusBadGateway), gin.H{"error": "Failed to read Codex search response"})
		return
	}
	helps.AppendAPIResponseChunk(ctx, s.cfg, upstreamBody)
	if contentType := resp.Header.Get("Content-Type"); contentType != "" {
		c.Header("Content-Type", contentType)
	}
	c.Status(resp.StatusCode)
	_, _ = c.Writer.Write(upstreamBody)
}

// AttachWebsocketRoute registers a websocket upgrade handler on the primary Gin engine.
// The handler is served as-is without additional middleware beyond the standard stack already configured.
func (s *Server) AttachWebsocketRoute(path string, handler http.Handler) {
	if s == nil || s.engine == nil || handler == nil {
		return
	}
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		trimmed = "/v1/ws"
	}
	if !strings.HasPrefix(trimmed, "/") {
		trimmed = "/" + trimmed
	}
	s.wsRouteMu.Lock()
	if _, exists := s.wsRoutes[trimmed]; exists {
		s.wsRouteMu.Unlock()
		return
	}
	s.wsRoutes[trimmed] = struct{}{}
	s.wsRouteMu.Unlock()

	authMiddleware := AuthMiddleware(s.accessManager)
	conditionalAuth := func(c *gin.Context) {
		if !s.wsAuthEnabled.Load() {
			c.Next()
			return
		}
		authMiddleware(c)
	}
	finalHandler := func(c *gin.Context) {
		handler.ServeHTTP(c.Writer, c.Request)
		c.Abort()
	}

	s.engine.GET(trimmed, conditionalAuth, finalHandler)
}

// isAnthropicModelsRequest reports whether a /v1/models request should be served in
// Anthropic format. Anthropic API clients send the Anthropic-Version header; Claude
// Code additionally uses a claude-cli User-Agent.
func isAnthropicModelsRequest(c *gin.Context) bool {
	if c.GetHeader("Anthropic-Version") != "" {
		return true
	}
	return strings.HasPrefix(c.GetHeader("User-Agent"), "claude-cli")
}

// unifiedModelsHandler creates a unified handler for the /v1/models endpoint
// that routes to different handlers based on the request.
// Anthropic API requests (Anthropic-Version header, or a claude-cli User-Agent)
// route to the Claude handler, otherwise they route to the OpenAI handler.
func (s *Server) unifiedModelsHandler(openaiHandler *openai.OpenAIAPIHandler, claudeHandler *claude.ClaudeCodeAPIHandler) gin.HandlerFunc {
	return func(c *gin.Context) {
		if grokbuild.IsGrokShellUserAgent(c.GetHeader("User-Agent")) {
			s.handleGrokModels(c)
			return
		}

		if _, ok := c.Request.URL.Query()["client_version"]; ok {
			if s != nil && s.cfg != nil && s.cfg.Home.Enabled {
				s.handleHomeCodexClientModels(c)
				return
			}
			openaiHandler.OpenAIModels(c)
			return
		}

		if s != nil && s.cfg != nil && s.cfg.Home.Enabled {
			s.handleHomeModels(c)
			return
		}

		// Route to Claude handler for Anthropic API requests.
		if isAnthropicModelsRequest(c) {
			claudeHandler.ClaudeModels(c)
		} else {
			openaiHandler.OpenAIModels(c)
		}
	}
}

func grokModelsFromHomeEntries(entries []homeModelEntry) []grokbuild.ModelInfo {
	models := make([]grokbuild.ModelInfo, 0, len(entries))
	for _, entry := range entries {
		models = append(models, grokbuild.ModelInfo{
			ID:            entry.id,
			DisplayName:   entry.displayName,
			ContextLength: entry.contextLength,
		})
	}
	return models
}

func grokModelsFromRegistryInfos(infos []*registry.ModelInfo) []grokbuild.ModelInfo {
	models := make([]grokbuild.ModelInfo, 0, len(infos))
	for _, info := range infos {
		if info == nil {
			continue
		}
		model := grokbuild.ModelInfo{
			ID:            info.ID,
			DisplayName:   info.DisplayName,
			ContextLength: info.ContextLength,
		}
		if info.Thinking != nil {
			model.ReasoningLevels = append([]string(nil), info.Thinking.Levels...)
		}
		models = append(models, model)
	}
	return models
}

func (s *Server) handleGrokModels(c *gin.Context) {
	var models []grokbuild.ModelInfo
	if s != nil && s.cfg != nil && s.cfg.Home.Enabled {
		entries, ok := s.loadHomeModelEntries(c)
		if !ok {
			return
		}
		models = grokModelsFromHomeEntries(entries)
	} else {
		models = grokModelsFromRegistryInfos(registry.GetGlobalRegistry().GetAvailableModelInfos())
	}
	c.JSON(http.StatusOK, grokbuild.BuildResponse(models))
}

// handleHomeCodexClientModels builds the Codex client catalog from Home model IDs.
// Template metadata still comes from the local/remote codex_client_models catalog.
func (s *Server) handleHomeCodexClientModels(c *gin.Context) {
	entries, ok := s.loadHomeModelEntries(c)
	if !ok {
		return
	}

	models := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		model := map[string]any{
			"id":     entry.id,
			"object": "model",
		}
		if entry.created > 0 {
			model["created"] = entry.created
		}
		if entry.ownedBy != "" {
			model["owned_by"] = entry.ownedBy
		}
		if entry.displayName != "" {
			model["display_name"] = entry.displayName
			model["description"] = entry.displayName
		}
		models = append(models, model)
	}

	c.JSON(http.StatusOK, codexmodels.BuildResponse(models, nil, s.cfg.Codex.OptimizeMultiAgentV2))
}

func (s *Server) geminiModelsHandler(geminiHandler *gemini.GeminiAPIHandler) gin.HandlerFunc {
	return func(c *gin.Context) {
		if s != nil && s.cfg != nil && s.cfg.Home.Enabled {
			s.handleHomeGeminiModels(c)
			return
		}

		geminiHandler.GeminiModels(c)
	}
}

func (s *Server) geminiGetHandler(geminiHandler *gemini.GeminiAPIHandler) gin.HandlerFunc {
	return func(c *gin.Context) {
		if s != nil && s.cfg != nil && s.cfg.Home.Enabled {
			s.handleHomeGeminiModel(c)
			return
		}

		geminiHandler.GeminiGetHandler(c)
	}
}

type homeModelEntry struct {
	id                  string
	created             int64
	ownedBy             string
	displayName         string
	contextLength       int
	maxCompletionTokens int
}

func (s *Server) handleHomeModels(c *gin.Context) {
	entries, ok := s.loadHomeModelEntries(c)
	if !ok {
		return
	}

	isClaude := isAnthropicModelsRequest(c)

	if isClaude {
		disableCloaking := s.cfg != nil && s.cfg.ClaudeCode.DisableCloakingModelList
		c.JSON(http.StatusOK, claudemodels.BuildResponse(formatHomeClaudeModels(entries), disableCloaking))
		return
	}

	filtered := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		model := map[string]any{
			"id":     entry.id,
			"object": "model",
		}
		if entry.created > 0 {
			model["created"] = entry.created
		}
		if entry.ownedBy != "" {
			model["owned_by"] = entry.ownedBy
		}
		filtered = append(filtered, model)
	}
	c.JSON(http.StatusOK, gin.H{
		"object": "list",
		"data":   filtered,
	})
}

func formatHomeClaudeModels(entries []homeModelEntry) []map[string]any {
	out := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		out = append(out, formatHomeClaudeModel(entry))
	}
	return out
}

func formatHomeClaudeModel(entry homeModelEntry) map[string]any {
	displayName := entry.displayName
	if displayName == "" {
		displayName = entry.id
	}
	maxInput := entry.contextLength
	if maxInput <= 0 {
		maxInput = registry.DefaultClaudeMaxInputTokens
	}
	maxOutput := entry.maxCompletionTokens
	if maxOutput <= 0 {
		maxOutput = registry.DefaultClaudeMaxOutputTokens
	}
	model := map[string]any{
		"id":               entry.id,
		"object":           "model",
		"owned_by":         entry.ownedBy,
		"type":             "model",
		"display_name":     displayName,
		"max_input_tokens": maxInput,
		"max_tokens":       maxOutput,
	}
	if entry.created > 0 {
		model["created_at"] = time.Unix(entry.created, 0).UTC().Format(time.RFC3339)
	}
	return model
}

func (s *Server) handleHomeGeminiModels(c *gin.Context) {
	entries, ok := s.loadHomeModelEntries(c)
	if !ok {
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"models": formatHomeGeminiModels(entries),
	})
}

func (s *Server) handleHomeGeminiModel(c *gin.Context) {
	entries, ok := s.loadHomeModelEntries(c)
	if !ok {
		return
	}

	action := strings.TrimPrefix(c.Param("action"), "/")
	action = strings.TrimSpace(action)
	for _, entry := range entries {
		if homeGeminiModelMatches(entry, action) {
			c.JSON(http.StatusOK, formatHomeGeminiModel(entry))
			return
		}
	}

	c.JSON(http.StatusNotFound, handlers.ErrorResponse{
		Error: handlers.ErrorDetail{
			Message: "Not Found",
			Type:    "not_found",
		},
	})
}

func (s *Server) loadHomeModelEntries(c *gin.Context) ([]homeModelEntry, bool) {
	if s == nil || c == nil || c.Request == nil {
		return nil, false
	}
	client := home.Current()
	if client == nil {
		c.JSON(http.StatusServiceUnavailable, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "home control center unavailable",
				Type:    "server_error",
			},
		})
		return nil, false
	}

	raw, errGet := client.GetModels(c.Request.Context(), c.Request.Header, c.Request.URL.Query())
	if errGet != nil {
		c.JSON(http.StatusBadGateway, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: errGet.Error(),
				Type:    "server_error",
			},
		})
		return nil, false
	}

	if statusCode, ok := homeModelsAuthStatus(raw); ok {
		c.JSON(statusCode, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: homeModelsErrorMessage(raw),
				Type:    "authentication_error",
			},
		})
		return nil, false
	}

	entries, errDecode := decodeHomeModels(raw)
	if errDecode != nil {
		c.JSON(http.StatusBadGateway, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: errDecode.Error(),
				Type:    "server_error",
			},
		})
		return nil, false
	}

	return entries, true
}

func formatHomeGeminiModels(entries []homeModelEntry) []map[string]any {
	out := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		out = append(out, formatHomeGeminiModel(entry))
	}
	return out
}

func formatHomeGeminiModel(entry homeModelEntry) map[string]any {
	name := entry.id
	if !strings.HasPrefix(name, "models/") {
		name = "models/" + name
	}
	displayName := entry.displayName
	if displayName == "" {
		displayName = entry.id
	}
	return map[string]any{
		"name":                       name,
		"displayName":                displayName,
		"description":                displayName,
		"supportedGenerationMethods": []string{"generateContent"},
	}
}

func homeGeminiModelMatches(entry homeModelEntry, action string) bool {
	id := strings.TrimSpace(entry.id)
	if id == "" || action == "" {
		return false
	}
	normalizedAction := strings.TrimPrefix(action, "models/")
	normalizedID := strings.TrimPrefix(id, "models/")
	return action == id || action == "models/"+id || normalizedAction == normalizedID
}

// homeModelsAuthStatus inspects a home models response for an authentication/error envelope.
// It returns the HTTP status code to surface (401 for credential issues, 502 otherwise)
// and true when the payload is an error response rather than model data.
func homeModelsAuthStatus(raw []byte) (int, bool) {
	errType := homeModelsErrorType(raw)
	if errType == "" {
		return 0, false
	}
	if errType == "no_credentials" || errType == "invalid_credential" {
		return http.StatusUnauthorized, true
	}
	return http.StatusBadGateway, true
}

func homeModelsErrorType(raw []byte) string {
	top, ok := unmarshalHomeModelsTopLevel(raw)
	if !ok {
		return ""
	}
	rawErr, exists := top["error"]
	if !exists {
		return ""
	}
	var errObj struct {
		Type string `json:"type"`
	}
	if errUnmarshal := json.Unmarshal(rawErr, &errObj); errUnmarshal != nil {
		return ""
	}
	return strings.TrimSpace(errObj.Type)
}

func homeModelsErrorMessage(raw []byte) string {
	top, ok := unmarshalHomeModelsTopLevel(raw)
	if !ok {
		return "home models request failed"
	}
	rawErr, exists := top["error"]
	if !exists {
		return "home models request failed"
	}
	var errObj struct {
		Message string `json:"message"`
	}
	if errUnmarshal := json.Unmarshal(rawErr, &errObj); errUnmarshal != nil {
		return "home models request failed"
	}
	if msg := strings.TrimSpace(errObj.Message); msg != "" {
		return msg
	}
	return "home models request failed"
}

func unmarshalHomeModelsTopLevel(raw []byte) (map[string]json.RawMessage, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var top map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(raw, &top); errUnmarshal != nil {
		return nil, false
	}
	return top, true
}

func decodeHomeModels(raw []byte) ([]homeModelEntry, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("home models payload is empty")
	}

	var bySection map[string][]map[string]any
	if err := json.Unmarshal(raw, &bySection); err != nil {
		return nil, fmt.Errorf("parse home models payload: %w", err)
	}
	if len(bySection) == 0 {
		return nil, fmt.Errorf("home models payload has no sections")
	}

	seen := make(map[string]struct{})
	out := make([]homeModelEntry, 0, 256)
	for _, models := range bySection {
		for _, model := range models {
			id, _ := model["id"].(string)
			id = strings.TrimSpace(id)
			if id == "" {
				name, _ := model["name"].(string)
				name = strings.TrimSpace(name)
				id = strings.TrimPrefix(name, "models/")
			}
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}

			ownedBy, _ := model["owned_by"].(string)
			ownedBy = strings.TrimSpace(ownedBy)
			displayName, _ := model["display_name"].(string)
			displayName = strings.TrimSpace(displayName)
			if displayName == "" {
				displayName, _ = model["displayName"].(string)
				displayName = strings.TrimSpace(displayName)
			}

			out = append(out, homeModelEntry{
				id:                  id,
				created:             homeModelInt64Value(model, "created"),
				ownedBy:             ownedBy,
				displayName:         displayName,
				contextLength:       int(homeModelInt64Value(model, "context_length", "contextLength", "inputTokenLimit", "max_input_tokens")),
				maxCompletionTokens: int(homeModelInt64Value(model, "max_completion_tokens", "maxCompletionTokens", "outputTokenLimit", "max_tokens")),
			})
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	if len(out) == 0 {
		return nil, fmt.Errorf("home models payload contains no models")
	}
	return out, nil
}

func homeModelInt64Value(model map[string]any, keys ...string) int64 {
	for _, key := range keys {
		switch value := model[key].(type) {
		case float64:
			return int64(value)
		case int64:
			return value
		case int:
			return int64(value)
		case json.Number:
			if n, errInt := value.Int64(); errInt == nil {
				return n
			}
		case string:
			if n, errParse := strconv.ParseInt(strings.TrimSpace(value), 10, 64); errParse == nil {
				return n
			}
		}
	}
	return 0
}
