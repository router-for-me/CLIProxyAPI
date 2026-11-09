package management

import (
	"context"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	qoderauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/qoder"
	log "github.com/sirupsen/logrus"
)

// RequestQoderToken starts Qoder international device authorization.
func (h *Handler) RequestQoderToken(c *gin.Context) {
	h.requestQoderToken(c, qoderauth.RegionGlobal)
}

// RequestQoderCNToken starts Qoder China device authorization.
func (h *Handler) RequestQoderCNToken(c *gin.Context) {
	h.requestQoderToken(c, qoderauth.RegionCN)
}

func (h *Handler) requestQoderToken(c *gin.Context, region string) {
	ctx := PopulateAuthContext(context.Background(), c)
	provider := qoderauth.ProviderForRegion(region)
	client := qoderauth.NewClient(h.cfg, region)
	flow, err := client.StartDeviceFlow()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start Qoder authorization"})
		return
	}
	state := provider + "-" + uuid.NewString()
	RegisterOAuthSession(state, provider)
	go func() {
		pollCtx, cancelPoll := context.WithCancel(ctx)
		defer cancelPoll()
		go watchOAuthSessionCancel(pollCtx, cancelPoll, state, provider)
		storage, errPoll := client.PollForToken(pollCtx, flow)
		if errPoll != nil {
			if IsOAuthSessionPending(state, provider) {
				SetOAuthSessionError(state, oauthSessionErrorWithCause("Authentication failed", errPoll))
			}
			return
		}
		if !IsOAuthSessionPending(state, provider) {
			return
		}
		record, errRecord := qoderauth.NewAuthRecord(storage)
		if errRecord != nil {
			SetOAuthSessionError(state, oauthSessionErrorWithCause("Authentication failed", errRecord))
			return
		}
		if errGuard := guardOAuthSessionPendingForSave(state, provider); errGuard != nil {
			return
		}
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Errorf("Failed to save Qoder authentication tokens: %v", errSave)
			SetOAuthSessionError(state, "Failed to save authentication tokens")
			return
		}
		fmt.Printf("Qoder authentication saved to %s\n", savedPath)
		CompleteOAuthSession(state)
	}()
	c.JSON(http.StatusOK, gin.H{
		"status": "ok", "url": flow.VerificationURI, "state": state, "flow": "device", "expires_in": 300,
	})
}
