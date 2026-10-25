package management

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	log "github.com/sirupsen/logrus"
)

func (h *Handler) RequestKiroToken(c *gin.Context) {
	ctx := PopulateAuthContext(context.Background(), c)
	client := &kiro.Client{HTTP: helps.NewProxyAwareHTTPClient(ctx, h.cfg, nil, 0)}
	device, err := client.StartDeviceFlow(c.Request.Context(), c.Query("region"), c.Query("start_url"))
	if err != nil {
		log.WithError(err).Warn("kiro: device authorization failed")
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to start Kiro device authorization"})
		return
	}
	state := "kiro-" + uuid.NewString()
	RegisterOAuthSession(state, "kiro")
	go func() {
		pollCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		go watchOAuthSessionCancel(pollCtx, cancel, state, "kiro")
		token, errWait := client.WaitForAuthorization(pollCtx, device)
		if errWait != nil {
			if IsOAuthSessionPending(state, "kiro") {
				SetOAuthSessionError(state, oauthSessionErrorWithCause("Kiro authentication failed", errWait))
			}
			return
		}
		if errGuard := guardOAuthSessionPendingForSave(state, "kiro"); errGuard != nil {
			return
		}
		record := sdkAuth.NewKiroAuthRecord(kiro.TokenMetadata(token, device))
		if _, errSave := h.saveTokenRecord(ctx, record); errSave != nil {
			log.WithError(errSave).Error("kiro: saving credential failed")
			SetOAuthSessionError(state, "Failed to save Kiro credential")
			return
		}
		CompleteOAuthSession(state)
	}()
	url := device.VerificationURIComplete
	if url == "" {
		url = device.VerificationURI
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "url": url, "state": state, "flow": "device", "user_code": device.UserCode, "expires_in": device.ExpiresIn})
}
