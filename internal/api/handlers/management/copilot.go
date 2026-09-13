package management

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/copilot"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	log "github.com/sirupsen/logrus"
)

func (h *Handler) RequestCopilotToken(c *gin.Context) {
	client := copilot.NewClient(h.cfg, "")
	code, err := client.StartDeviceFlow(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	state := "github-copilot-" + uuid.NewString()
	RegisterOAuthSession(state, copilot.Provider)
	ctx := PopulateAuthContext(context.Background(), c)
	go func() {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		go watchOAuthSessionCancel(ctx, cancel, state, copilot.Provider)
		githubToken, errWait := client.WaitForAuthorization(ctx, code)
		if errWait != nil {
			if IsOAuthSessionPending(state, copilot.Provider) {
				SetOAuthSessionError(state, errWait.Error())
			}
			return
		}
		metadata, errLogin := client.Login(ctx, githubToken)
		if errLogin != nil {
			if IsOAuthSessionPending(state, copilot.Provider) {
				SetOAuthSessionError(state, errLogin.Error())
			}
			return
		}
		if errGuard := guardOAuthSessionPendingForSave(state, copilot.Provider); errGuard != nil {
			return
		}
		if _, errSave := h.saveTokenRecord(ctx, sdkAuth.NewCopilotAuthRecord(metadata)); errSave != nil {
			log.WithError(errSave).Error("github-copilot: could not save credentials")
			SetOAuthSessionError(state, "Could not save GitHub Copilot credentials")
			return
		}
		CompleteOAuthSession(state)
	}()
	c.JSON(http.StatusOK, gin.H{"status": "ok", "url": code.VerificationURI, "state": state, "flow": "device", "user_code": code.UserCode, "expires_in": code.ExpiresIn})
}

// GetCopilotQuota keeps the GitHub token on the server and uses the account proxy.
func (h *Handler) GetCopilotQuota(c *gin.Context) {
	index := strings.TrimSpace(c.Query("auth_index"))
	if index == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth_index is required"})
		return
	}
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	auth := h.authByIndex(index)
	if auth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth not found"})
		return
	}
	if auth.Provider != copilot.Provider {
		c.JSON(http.StatusBadRequest, gin.H{"error": "credential is not a GitHub Copilot account"})
		return
	}
	token, _ := auth.Metadata["access_token"].(string)
	if token == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "GitHub token is missing"})
		return
	}
	quota, err := copilot.NewClient(h.cfg, auth.ProxyURL).Quota(c.Request.Context(), token)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.Data(http.StatusOK, "application/json", quota)
}
