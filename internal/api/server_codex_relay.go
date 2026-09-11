package api

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// relayCodexUpstream runs performRequest with the selected credential and
// relays the upstream response verbatim. unavailableErr, when non-nil, marks a
// credential misconfiguration that ends the selection as missing_base_url
// instead of a transport failure.
func (s *Server) relayCodexUpstream(c *gin.Context, ctx context.Context, selection *auth.HomeDispatchSelection, selected *auth.Auth, selectionModel, label string, performRequest func(*auth.Auth) (*http.Response, error), unavailableErr error) {
	if errCtx := ctx.Err(); errCtx != nil {
		if selection != nil {
			selection.End("attempt_canceled")
		}
		c.JSON(clienterror.HTTPStatusFromErrorOr(errCtx, http.StatusRequestTimeout), gin.H{"error": errCtx.Error()})
		return
	}
	resp, err := performRequest(selected)
	if err != nil {
		if unavailableErr != nil && errors.Is(err, unavailableErr) {
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
			log.Errorf("%s: close response body error: %v", label, errClose)
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
		c.JSON(clienterror.HTTPStatusFromErrorOr(err, http.StatusBadGateway), gin.H{"error": "Failed to read Codex upstream response"})
		return
	}
	helps.AppendAPIResponseChunk(ctx, s.cfg, upstreamBody)
	if selection != nil && resp.StatusCode == http.StatusUnauthorized {
		s.handlers.AuthManager.ReportHomeUnauthorized(ctx, selected, "codex", selectionModel, upstreamBody)
		log.WithField("status", resp.StatusCode).Warnf("%s upstream request failed: %s", label, logging.SafeDiagnosticForLog(string(upstreamBody)))
	}
	if contentType := resp.Header.Get("Content-Type"); contentType != "" {
		c.Header("Content-Type", contentType)
	}
	c.Status(resp.StatusCode)
	_, _ = c.Writer.Write(upstreamBody)
}
