package management

import (
	"net/http"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// ObserveClaudeQuotaV8 makes a small, credential-pinned inference request and
// returns the quota headers Claude includes with the response.
func (h *Handler) ObserveClaudeQuotaV8(c *gin.Context) {
	if h.authManager == nil || h.authManager.HomeEnabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "local auth manager is not available"})
		return
	}
	var request struct {
		AuthIndex string `json:"auth_index"`
	}
	if errBind := c.ShouldBindJSON(&request); errBind != nil || strings.TrimSpace(request.AuthIndex) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth_index is required"})
		return
	}
	credential, found := h.lookupAuthFile("", request.AuthIndex)
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "credential not found"})
		return
	}
	if credential.Provider != "claude" || credential.AuthKind() != coreauth.AuthKindOAuth {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Claude OAuth credential required"})
		return
	}
	if credential.Disabled || credential.Status == coreauth.StatusDisabled || credential.RequiresSignIn {
		c.JSON(http.StatusConflict, gin.H{"error": "credential is disabled or requires sign-in"})
		return
	}
	model := claudeQuotaProbeModel(credential)
	if model == "" {
		c.JSON(http.StatusConflict, gin.H{"error": "no Haiku model is registered for this credential"})
		return
	}

	doc := ojson.NewDocString(`{"model":"","max_tokens":1,"messages":[{"role":"user","content":"OK"}]}`)
	_ = doc.Set("model", model)
	payload := doc.Bytes()
	startedAt := time.Now()
	response, errProbe := h.authManager.Execute(c.Request.Context(), []string{"claude"}, cliproxyexecutor.Request{
		Model:   model,
		Payload: payload,
	}, cliproxyexecutor.Options{
		Headers:         http.Header{"X-Session-Id": {uuid.NewString()}},
		OriginalRequest: payload,
		SourceFormat:    translator.FormatClaude,
		Metadata:        map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: credential.ID},
	})
	if errProbe != nil {
		status := clienterror.HTTPStatusFromErrorOr(errProbe, http.StatusBadGateway)
		if status == http.StatusTooManyRequests {
			// The request can be rejected while still delivering useful quota headers.
			// Scheduler-generated cooldown errors can carry synthetic headers, so use
			// only a new upstream observation recorded for this credential.
			updated, ok := h.authManager.GetByID(credential.ID)
			if ok && updated.RegistrationEpoch == credential.RegistrationEpoch && updated.Quota.ObservedAt.After(startedAt) {
				c.JSON(http.StatusOK, gin.H{"quota": quotaObservationPayload(updated.Quota)})
				return
			}
		}
		c.JSON(status, gin.H{"error": "Claude quota probe failed"})
		return
	}
	var quota coreauth.QuotaState
	if !quota.ObserveResponseHeadersForProvider("claude", response.Headers, time.Now()) {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Claude did not return quota headers"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"quota": quotaObservationPayload(quota)})
}

func claudeQuotaProbeModel(credential *coreauth.Auth) string {
	models := registry.GetGlobalRegistry().GetModelsForClient(credential.ID)
	for _, name := range []string{"claude-haiku-5-5", "claude-haiku-4-5-20251001"} {
		if credential.Prefix != "" {
			name = credential.Prefix + "/" + name
		}
		for _, model := range models {
			if model != nil && model.ID == name {
				return name
			}
		}
	}
	return ""
}
