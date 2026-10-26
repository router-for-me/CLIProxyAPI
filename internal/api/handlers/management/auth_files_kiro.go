package management

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	log "github.com/sirupsen/logrus"
)

// kiroAuthURL is overridden only by tests to point at a mock desktop auth service.
var kiroAuthURL string

func (h *Handler) RequestKiroToken(c *gin.Context) {
	ctx := PopulateAuthContext(context.Background(), c)
	client := &kiro.Client{HTTP: helps.NewProxyAwareHTTPClient(ctx, h.cfg, nil, 0), AuthURL: kiroAuthURL}
	if provider, ok := kiro.SocialProvider(c.Query("method")); ok {
		h.requestKiroSocialToken(ctx, c, client, provider)
		return
	}
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

// requestKiroSocialToken runs the Google/GitHub PKCE login. The browser lands on
// a registered localhost port; remote browsers submit that URL through the
// shared OAuth callback endpoint, which writes the same callback file.
func (h *Handler) requestKiroSocialToken(ctx context.Context, c *gin.Context, client *kiro.Client, provider string) {
	state := "kiro-" + uuid.NewString()
	RegisterOAuthSession(state, "kiro")
	login, err := client.StartSocialLogin(provider, state, func(callback kiro.SocialCallback) {
		if callback.State != state {
			return
		}
		if _, errWrite := WriteOAuthCallbackFileForPendingSession(h.cfg.AuthDir, "kiro", state, callback.Code, callback.Error); errWrite != nil {
			log.WithError(errWrite).Debug("kiro: social callback was not accepted")
		}
	})
	if err != nil {
		CancelOAuthSession(state)
		log.WithError(err).Warn("kiro: social login failed to start")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start Kiro callback server"})
		return
	}
	go func() {
		defer login.Close()
		code, errWait := waitKiroSocialCallback(h.cfg.AuthDir, state)
		if errWait != nil {
			if IsOAuthSessionPending(state, "kiro") {
				SetOAuthSessionError(state, errWait.Error())
			}
			return
		}
		token, errExchange := client.ExchangeSocialCode(ctx, login, code)
		if errExchange != nil {
			log.WithError(errExchange).Warn("kiro: social token exchange failed")
			if IsOAuthSessionPending(state, "kiro") {
				SetOAuthSessionError(state, oauthSessionErrorWithCause("Kiro authentication failed", errExchange))
			}
			return
		}
		if errGuard := guardOAuthSessionPendingForSave(state, "kiro"); errGuard != nil {
			return
		}
		record := sdkAuth.NewKiroAuthRecord(kiro.SocialTokenMetadata(token, login))
		if _, errSave := h.saveTokenRecord(ctx, record); errSave != nil {
			log.WithError(errSave).Error("kiro: saving credential failed")
			SetOAuthSessionError(state, "Failed to save Kiro credential")
			return
		}
		CompleteOAuthSession(state)
	}()
	c.JSON(http.StatusOK, gin.H{"status": "ok", "url": login.AuthURL, "state": state, "flow": "social", "expires_in": int(kiro.SocialLoginTimeout.Seconds())})
}

func waitKiroSocialCallback(authDir, state string) (string, error) {
	waitFile := filepath.Join(authDir, fmt.Sprintf(".oauth-kiro-%s.oauth", state))
	deadline := time.Now().Add(kiro.SocialLoginTimeout)
	for {
		if !IsOAuthSessionPending(state, "kiro") {
			return "", fmt.Errorf("Kiro login is no longer pending")
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("Timeout waiting for Kiro authorization")
		}
		if data, errRead := os.ReadFile(waitFile); errRead == nil {
			if errRemove := os.Remove(waitFile); errRemove != nil {
				log.WithError(errRemove).Debug("kiro: remove callback file failed")
			}
			var payload oauthCallbackFilePayload
			if errDecode := json.Unmarshal(data, &payload); errDecode != nil {
				return "", fmt.Errorf("Invalid Kiro authorization callback")
			}
			if payload.Error != "" {
				return "", fmt.Errorf("Kiro authorization failed: %s", payload.Error)
			}
			if payload.State != state {
				return "", fmt.Errorf("Kiro authorization failed: state mismatch")
			}
			if strings.TrimSpace(payload.Code) == "" {
				return "", fmt.Errorf("Kiro authorization failed: code not found")
			}
			return payload.Code, nil
		}
		time.Sleep(500 * time.Millisecond)
	}
}
