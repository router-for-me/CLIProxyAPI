package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	execution "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	translator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// launchPrincipal is established by AuthMiddleware, never a caller header.
// Hashing keeps an inline API key out of the binding store and all responses.
func launchPrincipal(c *gin.Context) string {
	principal, provider := c.GetString("userApiKey"), c.GetString("accessProvider")
	if principal == "" || provider == "" {
		return ""
	}
	b, _ := json.Marshal([]string{provider, principal})
	digest := sha256.Sum256(b)
	return hex.EncodeToString(digest[:])
}

func registerDelegatedLaunch(v1 *gin.RouterGroup, pool *auth.LaunchPool) {
	base := v1.Group("/delegated-launch")
	base.Use(func(c *gin.Context) {
		if launchPrincipal(c) == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": auth.LaunchRefusal{Code: "authentication_required", QuotaScope: "unknown", QuotaDomains: []string{}}})
		}
	})
	base.POST("/prepare", func(c *gin.Context) {
		var request auth.PrepareLaunchRequest
		decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&request) != nil || decoder.Decode(&struct{}{}) != io.EOF {
			c.JSON(http.StatusBadRequest, gin.H{"error": auth.LaunchRefusal{Code: "invalid_prepare", QuotaScope: "unknown", QuotaDomains: []string{}}})
			return
		}
		c.JSON(http.StatusOK, pool.Prepare(c.Request.Context(), launchPrincipal(c), request))
	})
	base.POST("/cancel", func(c *gin.Context) {
		var request auth.CancelLaunchRequest
		decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 1024))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&request) != nil || decoder.Decode(&struct{}{}) != io.EOF || request.ClientRequestID == "" || len(request.ClientRequestID) > 256 {
			c.Status(400)
			return
		}
		pool.CancelPrepare(launchPrincipal(c), request.ClientRequestID)
		c.JSON(200, gin.H{"status": "released"})
	})
	bound := base.Group("/bindings/:bindingId/sessions/:sessionId")
	bound.POST("/release", func(c *gin.Context) {
		pool.Release(launchPrincipal(c), c.Param("bindingId"), c.Param("sessionId"))
		c.JSON(200, gin.H{"status": "released"})
	})
	bound.POST("/touch", func(c *gin.Context) {
		if !pool.Touch(launchPrincipal(c), c.Param("bindingId"), c.Param("sessionId")) {
			c.Status(409)
			return
		}
		c.JSON(200, gin.H{"status": "active"})
	})
	bound.POST("/resume", func(c *gin.Context) {
		var request auth.ResumeLaunchRequest
		decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 1024))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&request) != nil || decoder.Decode(&struct{}{}) != io.EOF || request.ExpectedRefusalVersion == nil || *request.ExpectedRefusalVersion == 0 {
			c.Status(400)
			return
		}
		if !pool.Resume(launchPrincipal(c), c.Param("bindingId"), c.Param("sessionId"), *request.ExpectedRefusalVersion) {
			c.Status(409)
			return
		}
		c.JSON(200, gin.H{"status": "acknowledged"})
	})
	bound.GET("/state", func(c *gin.Context) {
		c.JSON(200, pool.Inspect(launchPrincipal(c), c.Param("bindingId"), c.Param("sessionId")))
	})
	// An existing launcher can set base_url to prepare.basePath. No native
	// custom-header capability is assumed. WebSocket is explicitly unsupported.
	bound.GET("/responses", func(c *gin.Context) {
		c.JSON(http.StatusConflict, gin.H{"error": auth.LaunchRefusal{Code: "transport_unsupported", QuotaScope: "unknown", QuotaDomains: []string{}, BindingID: c.Param("bindingId")}})
	})
	bound.POST("/responses", func(c *gin.Context) { dispatchBoundResponse(c, pool) })
	bound.POST("/responses/compact", func(c *gin.Context) { dispatchBoundResponse(c, pool) })
}

func writeBoundError(c *gin.Context, err error) {
	var refusal *auth.LaunchRefusal
	if errors.As(err, &refusal) {
		c.JSON(refusal.StatusCode(), gin.H{"error": refusal})
		return
	}
	status := http.StatusBadGateway
	var upstream interface{ StatusCode() int }
	if errors.As(err, &upstream) && upstream.StatusCode() >= 400 && upstream.StatusCode() <= 599 {
		status = upstream.StatusCode()
	}
	c.JSON(status, gin.H{"error": gin.H{"code": "upstream_error"}})
}

func dispatchBoundResponse(c *gin.Context, pool *auth.LaunchPool) {
	if strings.EqualFold(c.GetHeader("Upgrade"), "websocket") {
		writeBoundError(c, &auth.LaunchRefusal{Code: "transport_unsupported", QuotaScope: "unknown", QuotaDomains: []string{}})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 2<<20))
	var request struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err != nil || json.Unmarshal(body, &request) != nil {
		writeBoundError(c, &auth.LaunchRefusal{Code: "invalid_request", QuotaScope: "unknown", QuotaDomains: []string{}})
		return
	}
	alt := ""
	if strings.HasSuffix(c.Request.URL.Path, "/responses/compact") {
		alt = "responses/compact"
		request.Stream = false
	}
	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()
	response, stream, err := pool.ExecuteBound(ctx, launchPrincipal(c), c.Param("bindingId"), c.Param("sessionId"), execution.Request{Model: request.Model, Payload: body, Format: translator.FormatOpenAIResponse}, execution.Options{Alt: alt, Stream: request.Stream, SourceFormat: translator.FormatOpenAIResponse, OriginalRequest: body})
	if err != nil {
		writeBoundError(c, err)
		return
	}
	if !request.Stream {
		c.Data(http.StatusOK, "application/json", response.Payload)
		return
	}
	if stream == nil {
		writeBoundError(c, errors.New("empty stream"))
		return
	}
	wrote := false
	for {
		select {
		case <-ctx.Done():
			return
		case chunk, ok := <-stream.Chunks:
			if !ok {
				return
			}
			if chunk.Err != nil {
				if !wrote {
					writeBoundError(c, chunk.Err)
					return
				}
				var refusal *auth.LaunchRefusal
				if !errors.As(chunk.Err, &refusal) {
					refusal = &auth.LaunchRefusal{Code: "upstream_error", QuotaScope: "unknown", QuotaDomains: []string{}}
				}
				code := "server_error"
				switch refusal.Code {
				case "usage_limit", "model_quota", "quota_unverified":
					code = "insufficient_quota"
				case "throttled":
					code = "rate_limit_exceeded"
				case "model_capacity":
					code = "server_is_overloaded"
				}
				payload, _ := json.Marshal(gin.H{"type": "response.failed", "response": gin.H{"status": "failed", "error": gin.H{"code": code, "message": refusal.Code}}})
				_, _ = c.Writer.Write(append(append([]byte("event: response.failed\ndata: "), payload...), []byte("\n\n")...))
				c.Writer.Flush()
				return
			}
			if len(chunk.Payload) == 0 {
				continue
			}
			if !wrote {
				c.Header("Content-Type", "text/event-stream")
				c.Header("Cache-Control", "no-cache")
				wrote = true
			}
			if err := openai.WriteResponsesSSEChunk(c.Writer, chunk.Payload); err != nil {
				return
			}
			c.Writer.Flush()
		}
	}
}
