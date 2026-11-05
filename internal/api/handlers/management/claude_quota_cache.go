package management

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func (handler *Handler) GetCachedClaudeQuota(request *gin.Context) {
	authIndex := strings.TrimSpace(request.Query("auth_index"))
	if authIndex == "" {
		request.JSON(http.StatusBadRequest, gin.H{"error": "auth_index is required"})
		return
	}
	credential := handler.authByIndex(authIndex)
	if credential == nil || credential.Disabled || !strings.EqualFold(credential.Provider, "claude") {
		request.JSON(http.StatusNotFound, gin.H{"error": "Claude credential not found"})
		return
	}
	handler.mu.Lock()
	path := ""
	if handler.cfg != nil {
		path = handler.cfg.Routing.ClaudeQuotaSnapshot
	}
	handler.mu.Unlock()
	usage, observed, err := coreauth.ReadClaudeQuotaUsage(path, credential.ID, credential.Index, coreauth.AccessTokenSHA256(credential), time.Now())
	request.Header("Cache-Control", "no-store")
	if err != nil {
		request.JSON(http.StatusServiceUnavailable, gin.H{"error": "Claude cached usage is unavailable"})
		return
	}
	request.JSON(http.StatusOK, gin.H{"observed_at": observed.UTC().Format(time.RFC3339Nano), "usage": usage})
}

func (handler *Handler) GetClaudeQuotaCredentials(request *gin.Context) {
	files := make([]gin.H, 0)
	if handler.authManager != nil {
		for _, credential := range handler.authManager.List() {
			if credential == nil || credential.Disabled || !strings.EqualFold(credential.Provider, "claude") || credential.AuthKind() == coreauth.AuthKindAPIKey {
				continue
			}
			credential.EnsureIndex()
			fingerprint := coreauth.AccessTokenSHA256(credential)
			if fingerprint == "" {
				continue
			}
			files = append(files, gin.H{"id": credential.ID, "auth_index": credential.Index, "type": "claude", "credential_fingerprint": fingerprint})
		}
	}
	request.Header("Cache-Control", "no-store")
	request.JSON(http.StatusOK, gin.H{"files": files})
}
