package api

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

// antigravityCloudCodePrefix is the base path native Antigravity clients use as their
// --cloud_code_endpoint. The client cannot send a custom auth header (it always sends
// its own Google token), so the CLIProxyAPI key travels as the first path segment.
const antigravityCloudCodePrefix = "/antigravity-cloudcode/"

const antigravityCloudCodeMethodPrefix = "v1internal:"

// splitAntigravityCloudCodePath extracts the API key and the remaining path from
// /antigravity-cloudcode/<key>/<rest>.
func splitAntigravityCloudCodePath(path string) (key, rest string, ok bool) {
	trimmed, found := strings.CutPrefix(path, antigravityCloudCodePrefix)
	if !found {
		return "", "", false
	}
	key, rest, found = strings.Cut(trimmed, "/")
	if !found || key == "" {
		return "", "", false
	}
	return key, "/" + rest, true
}

// antigravityCloudCodeKeyMiddleware moves the path-embedded API key into the
// Authorization header (replacing the client's Google token, which must never reach
// upstream) and masks it in URL.Path. It must run before any logging middleware.
// Gin has already matched the route, so path params still hold the original values.
func antigravityCloudCodeKeyMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if key, rest, ok := splitAntigravityCloudCodePath(c.Request.URL.Path); ok {
			c.Request.Header.Set("Authorization", "Bearer "+key)
			c.Request.URL.Path = antigravityCloudCodePrefix + "-" + rest
			c.Request.URL.RawPath = ""
		}
		c.Next()
	}
}

// antigravityCloudCodeEnabledMiddleware hides the passthrough unless it is enabled.
func (s *Server) antigravityCloudCodeEnabledMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		cfg := s.getConfig()
		if cfg == nil || !cfg.Antigravity.CloudCodePassthrough {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		c.Next()
	}
}

// writeAntigravityCloudCodeError writes a Google API style error, which is what
// Antigravity clients parse.
func writeAntigravityCloudCodeError(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{"error": gin.H{
		"code":    status,
		"message": message,
		"status":  strings.ToUpper(strings.ReplaceAll(http.StatusText(status), " ", "_")),
	}})
}

// rewriteAntigravityCloudCodeProject points the request at the selected credential's
// project. Requests without a top-level "project" are returned unchanged.
func rewriteAntigravityCloudCodeProject(body []byte, projectID string) []byte {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" || len(bytes.TrimSpace(body)) == 0 {
		return body
	}
	doc := ojson.NewDoc(body)
	current := doc.Get("project")
	if !current.Exists() || current.String() == projectID {
		return body
	}
	if errSet := doc.Set("project", projectID); errSet != nil {
		log.Warnf("antigravity cloudcode: rewrite project failed: %v", errSet)
		return body
	}
	return doc.Bytes()
}

// handleAntigravityCloudCode forwards one Cloud Code v1internal call verbatim through
// a pooled Antigravity credential and streams the upstream response back.
func (s *Server) handleAntigravityCloudCode(c *gin.Context) {
	method := strings.TrimPrefix(c.Param("method"), "/")
	if !strings.HasPrefix(method, antigravityCloudCodeMethodPrefix) || strings.Contains(method, "/") {
		writeAntigravityCloudCodeError(c, http.StatusNotFound, "unsupported Cloud Code method")
		return
	}
	ctx := c.Request.Context()
	body, errRead := io.ReadAll(c.Request.Body)
	if errRead != nil {
		writeAntigravityCloudCodeError(c, clienterror.HTTPStatusFromErrorOr(errRead, http.StatusBadRequest), "failed to read request body")
		return
	}

	manager := s.handlers.AuthManager
	selected, errSelect := manager.SelectAuth(ctx, "antigravity", "", coreexecutor.Options{})
	if errSelect != nil {
		writeAntigravityCloudCodeError(c, clienterror.HTTPStatusFromErrorOr(errSelect, http.StatusServiceUnavailable), "no Antigravity credential available: "+errSelect.Error())
		return
	}
	logging.SetGinCPATraceID(c, selected.EnsureIndex())

	projectID, _ := selected.Metadata["project_id"].(string)
	body = rewriteAntigravityCloudCodeProject(body, projectID)

	upstreamURL := strings.TrimRight(executor.AntigravityRequestBaseURL(selected), "/") + "/" + method
	if rawQuery := c.Request.URL.RawQuery; rawQuery != "" {
		upstreamURL += "?" + rawQuery
	}
	req, errRequest := http.NewRequestWithContext(ctx, c.Request.Method, upstreamURL, bytes.NewReader(body))
	if errRequest != nil {
		writeAntigravityCloudCodeError(c, http.StatusBadRequest, "invalid request")
		return
	}
	for _, name := range []string{"Content-Type", "User-Agent"} {
		if value := strings.TrimSpace(c.GetHeader(name)); value != "" {
			req.Header.Set(name, value)
		}
	}

	cfg := s.getConfig()
	authType, authValue := selected.AccountInfo()
	helps.RecordAPIRequest(ctx, cfg, helps.UpstreamRequestLog{
		URL:       upstreamURL,
		Method:    req.Method,
		Headers:   req.Header.Clone(),
		Body:      body,
		Provider:  "antigravity",
		AuthID:    selected.ID,
		AuthLabel: selected.Label,
		AuthType:  authType,
		AuthValue: authValue,
	})
	resp, errDo := manager.HttpRequest(ctx, selected, req)
	if errDo != nil {
		helps.RecordAPIResponseError(ctx, cfg, errDo)
		writeAntigravityCloudCodeError(c, clienterror.HTTPStatusFromErrorOr(errDo, http.StatusBadGateway), errDo.Error())
		return
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("antigravity cloudcode: close response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, cfg, resp.StatusCode, resp.Header.Clone())
	if resp.StatusCode >= http.StatusBadRequest {
		log.WithFields(log.Fields{"method": method, "status": resp.StatusCode, "auth": selected.ID}).Warn("antigravity cloudcode: upstream request failed")
	}

	if contentType := resp.Header.Get("Content-Type"); contentType != "" {
		c.Header("Content-Type", contentType)
	}
	c.Status(resp.StatusCode)
	streamAntigravityCloudCodeBody(c, resp.Body, func(chunk []byte) { helps.AppendAPIResponseChunk(ctx, cfg, chunk) })
}

// streamAntigravityCloudCodeBody copies the upstream body and flushes after every read
// so SSE events reach the client as soon as they arrive.
func streamAntigravityCloudCodeBody(c *gin.Context, body io.Reader, record func([]byte)) {
	buf := make([]byte, 32<<10)
	for {
		n, errRead := body.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			record(chunk)
			if _, errWrite := c.Writer.Write(chunk); errWrite != nil {
				return
			}
			c.Writer.Flush()
		}
		if errRead != nil {
			if !errors.Is(errRead, io.EOF) {
				log.Warnf("antigravity cloudcode: read upstream body: %v", errRead)
			}
			return
		}
	}
}

func (s *Server) registerAntigravityCloudCodeRoutes() {
	group := s.engine.Group(antigravityCloudCodePrefix + ":key")
	group.Use(s.antigravityCloudCodeEnabledMiddleware(), AuthMiddleware(s.accessManager))
	group.POST("/*method", s.handleAntigravityCloudCode)
}
