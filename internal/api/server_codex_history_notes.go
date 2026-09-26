package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

const (
	codexHistoryNotesOfficialBase = "https://chatgpt.com/backend-api/codex/"
)

var allowedCodexHistoryNotesPaths = map[string]struct{}{
	"alpha/history/v2/list_windows":       {},
	"alpha/history/v2/list_items":         {},
	"alpha/history/v2/read_item":          {},
	"alpha/history/v2/search_contents":    {},
	"alpha/notes/v2/list_files_by_prefix": {},
	"alpha/notes/v2/read_file":            {},
	"alpha/notes/v2/search_contents":      {},
	"alpha/notes/v2/append_to_file":       {},
	"alpha/notes/v2/write_file":           {},
}

func parseCodexHistoryNotesPath(path string) (string, bool) {
	path = strings.TrimSpace(strings.SplitN(path, "?", 2)[0])
	switch {
	case strings.HasPrefix(path, "/backend-api/codex/"):
		path = strings.TrimPrefix(path, "/backend-api/codex/")
	case strings.HasPrefix(path, "/v1/"):
		path = strings.TrimPrefix(path, "/v1/")
	default:
		return "", false
	}
	if _, ok := allowedCodexHistoryNotesPaths[path]; !ok {
		return "", false
	}
	return path, true
}

func codexHistoryNotesSessionID(body []byte, headerSessionID string) string {
	var payload struct {
		Context struct {
			SessionID string `json:"session_id"`
		} `json:"context"`
	}
	if err := json.Unmarshal(body, &payload); err == nil {
		if sessionID := strings.TrimSpace(payload.Context.SessionID); sessionID != "" {
			return sessionID
		}
	}
	return strings.TrimSpace(headerSessionID)
}

// codexAlphaHistoryNotes forwards Codex history/notes backend operations.
// The payload is already in Codex format and must not pass through a protocol translator.
func (s *Server) codexAlphaHistoryNotes(c *gin.Context) {
	relativePath, ok := parseCodexHistoryNotesPath(c.Request.URL.Path)
	if !ok {
		c.Status(http.StatusNotFound)
		return
	}
	if s == nil || s.handlers == nil || s.handlers.AuthManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Codex auth manager unavailable"})
		return
	}

	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 16<<20))
	if err != nil {
		c.JSON(clienterror.HTTPStatusFromErrorOr(err, http.StatusBadRequest), gin.H{"error": "Failed to read history/notes request"})
		return
	}

	var routing struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &routing)
	sessionID := codexHistoryNotesSessionID(body, c.GetHeader("Session_id"))

	selectionHeaders := c.Request.Header.Clone()
	if sessionID != "" {
		selectionHeaders.Set("X-Session-ID", sessionID)
	}
	ctx := context.WithValue(c.Request.Context(), "gin", c)
	ctx = handlers.EnrichContextWithSessionHierarchy(ctx, selectionHeaders, body, nil)
	selectionModel, errRoute := s.codexAlphaSearchSelectionModel(ctx, c, body, strings.TrimSpace(routing.Model))
	if errRoute != nil {
		log.WithError(errRoute).Warn("codex history/notes: model router returned an unsupported target")
		c.JSON(clienterror.HTTPStatusFromErrorOr(errRoute, http.StatusServiceUnavailable), gin.H{"error": errRoute.Error()})
		return
	}
	// Reuse the Alpha Search credential policy: official Codex OAuth, plus
	// API keys that opted into Codex backend extras via alpha-search: true.
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
	if selection != nil && selection.CanonicalSessionID != "" {
		meta := logging.GetClientRequestMetadata(ctx)
		meta.SessionID = selection.CanonicalSessionID
		if selection.ParentSessionID != "" {
			meta.ParentSessionID = selection.ParentSessionID
		} else {
			meta.ParentSessionID = ""
		}
		if meta.SessionID == meta.ParentSessionID {
			meta.ParentSessionID = ""
		}
		ctx = logging.WithClientRequestMetadata(ctx, meta)
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

	baseHeaders := make(http.Header)
	baseHeaders.Set("Content-Type", "application/json")
	baseHeaders.Set("Accept", "application/json")
	baseHeaders.Set("Originator", "codex_cli_rs")
	for _, name := range []string{
		"Version",
		"User-Agent",
		"Session_id",
		"X-Client-Request-Id",
		"x-openai-encrypted-tool-arguments",
		"x-openai-tool-output-truncation-policy",
	} {
		if value := strings.TrimSpace(c.GetHeader(name)); value != "" {
			baseHeaders.Set(name, value)
		}
	}
	if sessionID != "" {
		baseHeaders.Set("Session_id", sessionID)
		baseHeaders.Set("X-Session-ID", sessionID)
	}

	errMissingBaseURL := errors.New("Codex history/notes API key base URL unavailable")
	routeModel := strings.TrimSpace(selectionModel)
	if routeModel == "" {
		routeModel = strings.TrimSpace(routing.Model)
	}
	performRequest := func(current *auth.Auth) (*http.Response, error) {
		headers := baseHeaders.Clone()
		if accountID, ok := current.Metadata["account_id"].(string); ok && strings.TrimSpace(accountID) != "" {
			headers.Set("Chatgpt-Account-Id", accountID)
		}
		upstreamURL := codexHistoryNotesOfficialBase + relativePath
		requestBody := body
		if current.AuthKind() == auth.AuthKindAPIKey {
			baseURL := ""
			if current.Attributes != nil {
				baseURL = strings.TrimSpace(current.Attributes["base_url"])
			}
			if baseURL == "" {
				return nil, errMissingBaseURL
			}
			upstreamURL = strings.TrimRight(baseURL, "/") + "/" + relativePath
			if upstreamModel := s.handlers.AuthManager.ResolveExecutionModel(current, routeModel); upstreamModel != "" {
				requestBody = rewriteCodexAlphaSearchModel(requestBody, upstreamModel)
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
	closeResponseBody := func() error {
		errClose := resp.Body.Close()
		if errClose != nil {
			log.Errorf("codex history/notes: close response body error: %v", errClose)
		}
		return errClose
	}
	if selection != nil {
		if errBind := selection.Bind(closeResponseBody); errBind != nil {
			if resp.StatusCode == http.StatusUnauthorized {
				s.handlers.AuthManager.ReportHomeUnauthorized(ctx, selected, "codex", selectionModel)
			}
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
		helps.AppendAPIResponseChunk(ctx, s.cfg, upstreamBody)
		if selection != nil && resp.StatusCode == http.StatusUnauthorized {
			s.handlers.AuthManager.ReportHomeUnauthorized(ctx, selected, "codex", selectionModel, upstreamBody)
		}
		helps.RecordAPIResponseError(ctx, s.cfg, err)
		c.JSON(clienterror.HTTPStatusFromErrorOr(err, http.StatusBadGateway), gin.H{"error": "Failed to read Codex history/notes response"})
		return
	}
	helps.AppendAPIResponseChunk(ctx, s.cfg, upstreamBody)
	if selection != nil && resp.StatusCode == http.StatusUnauthorized {
		s.handlers.AuthManager.ReportHomeUnauthorized(ctx, selected, "codex", selectionModel, upstreamBody)
		log.WithField("status", resp.StatusCode).Warnf("codex history/notes upstream request failed: %s", logging.SafeDiagnosticForLog(string(upstreamBody)))
	}
	if contentType := resp.Header.Get("Content-Type"); contentType != "" {
		c.Header("Content-Type", contentType)
	}
	c.Status(resp.StatusCode)
	_, _ = c.Writer.Write(upstreamBody)
}
