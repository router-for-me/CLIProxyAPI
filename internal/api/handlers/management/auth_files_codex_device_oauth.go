package management

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	log "github.com/sirupsen/logrus"
)

func (h *Handler) requestCodexDeviceToken(c *gin.Context) {
	state, errState := misc.GenerateRandomState()
	if errState != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate OAuth state"})
		return
	}
	sessionProxyURL := oauthSessionProxyURLFromContext(c)
	cfg := h.oauthConfigWithProxy(sessionProxyURL)
	client := &http.Client{Timeout: 30 * time.Second}
	if cfg != nil {
		util.SetProxy(&cfg.SDKConfig, client)
	}
	device, errStart := sdkAuth.StartCodexDeviceLogin(c.Request.Context(), client)
	if errStart != nil {
		log.Errorf("Failed to start Codex device flow: %s", redactSessionProxyInString(errStart.Error(), sessionProxyURL))
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to start device authorization flow"})
		return
	}

	device.ExpiresIn = min(device.ExpiresIn, int(oauthSessionTTL/time.Second))
	RegisterOAuthSessionWithProxy(state, "codex", sessionProxyURL)
	response := gin.H{
		"status": "ok", "state": state, "flow": "device",
		"url": device.VerificationURL, "user_code": device.UserCode, "expires_in": device.ExpiresIn,
	}
	bindOAuthSessionTarget(c, state, response)
	ctx := PopulateAuthContext(context.Background(), c)
	go func() {
		pollCtx, cancelPoll := context.WithCancel(ctx)
		defer cancelPoll()
		go watchOAuthSessionCancel(pollCtx, cancelPoll, state, "codex")

		record, errLogin := sdkAuth.CompleteCodexDeviceLogin(pollCtx, cfg, client, device)
		if errLogin != nil {
			if IsOAuthSessionPending(state, "codex") {
				log.Errorf("Codex device authentication failed: %s", redactSessionProxyInString(errLogin.Error(), sessionProxyURL))
				SetOAuthSessionError(state, "Authentication failed")
			}
			return
		}
		h.commitAndCompleteBuiltinOAuthSession(ctx, state, "codex", sessionProxyURL, record, "Failed to save authentication tokens", true)
	}()

	c.JSON(http.StatusOK, response)
}
