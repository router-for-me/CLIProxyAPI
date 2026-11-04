package management

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	kiroauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kiro"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

func (h *Handler) RequestKiroToken(c *gin.Context) {
	ctx := PopulateAuthContext(context.Background(), c)

	sessionProxyURL := oauthSessionProxyURLFromContext(c)
	cfg := h.oauthConfigWithProxy(sessionProxyURL)
	state := fmt.Sprintf("kiro-%d", time.Now().UnixNano())

	ssoClient := kiroauth.NewSSOOIDCClient(cfg)

	regResp, errRegister := ssoClient.RegisterClient(ctx)
	if errRegister != nil {
		log.Errorf("Failed to register Kiro OIDC client: %v", errRegister)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to register client"})
		return
	}

	authResp, errAuth := ssoClient.StartDeviceAuthorization(ctx, regResp.ClientID, regResp.ClientSecret)
	if errAuth != nil {
		log.Errorf("Failed to start Kiro device authorization: %v", errAuth)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start device authorization"})
		return
	}

	RegisterOAuthSessionWithProxy(state, "kiro", sessionProxyURL)

	go func() {
		pollCtx, cancelPoll := context.WithCancel(ctx)
		defer cancelPoll()
		go watchOAuthSessionCancel(pollCtx, cancelPoll, state, "kiro")

		interval := 5 * time.Second
		if authResp.Interval > 0 {
			interval = time.Duration(authResp.Interval) * time.Second
		}
		deadline := time.Now().Add(time.Duration(authResp.ExpiresIn) * time.Second)

		for time.Now().Before(deadline) {
			select {
			case <-pollCtx.Done():
				return
			case <-time.After(interval):
				if !IsOAuthSessionPending(state, "kiro") {
					return
				}

				tokenResp, errToken := ssoClient.CreateToken(pollCtx, regResp.ClientID, regResp.ClientSecret, authResp.DeviceCode)
				if errToken != nil {
					errStr := errToken.Error()
					if strings.Contains(errStr, "authorization_pending") {
						continue
					}
					if strings.Contains(errStr, "slow_down") {
						interval += 5 * time.Second
						continue
					}
					log.Errorf("Kiro token creation failed: %v", errToken)
					SetOAuthSessionError(state, "Token creation failed")
					return
				}

				if !IsOAuthSessionPending(state, "kiro") {
					return
				}

				expiresIn := tokenResp.ExpiresIn
				if expiresIn <= 0 {
					expiresIn = 3600
				}
				expiresAt := time.Now().Add(time.Duration(expiresIn) * time.Second)
				email := ssoClient.FetchUserEmail(pollCtx, tokenResp.AccessToken)
				if email == "" {
					email = kiroauth.ExtractEmailFromJWT(tokenResp.AccessToken)
				}
				profileArn := ssoClient.FetchProfileArn(pollCtx, tokenResp.AccessToken, regResp.ClientID, tokenResp.RefreshToken)

				idPart := kiroauth.SanitizeEmailForFilename(email)
				if idPart == "" {
					idPart = fmt.Sprintf("%d", time.Now().UnixNano()%100000)
				}

				now := time.Now()
				fileName := fmt.Sprintf("kiro-aws-%s.json", idPart)
				label := "kiro-aws"
				if email != "" {
					label = fmt.Sprintf("Kiro (%s)", email)
				}

				metadata := map[string]any{
					"type":          "kiro",
					"access_token":  tokenResp.AccessToken,
					"refresh_token": tokenResp.RefreshToken,
					"profile_arn":   profileArn,
					"expires_at":    expiresAt.Format(time.RFC3339),
					"auth_method":   "builder-id",
					"provider":      "AWS",
					"client_id":     regResp.ClientID,
					"client_secret": regResp.ClientSecret,
					"email":         email,
					"last_refresh":  now.Format(time.RFC3339),
				}

				storage := &kiroauth.KiroTokenStorage{
					Type:         "kiro",
					AccessToken:  tokenResp.AccessToken,
					RefreshToken: tokenResp.RefreshToken,
					ProfileArn:   profileArn,
					ExpiresAt:    expiresAt.Format(time.RFC3339),
					AuthMethod:   "builder-id",
					Provider:     "AWS",
					LastRefresh:  now.Format(time.RFC3339),
					ClientID:     regResp.ClientID,
					ClientSecret: regResp.ClientSecret,
					Region:       "us-east-1",
					Email:        email,
				}

				record := &coreauth.Auth{
					ID:       fileName,
					Provider: "kiro",
					FileName: fileName,
					Label:    label,
					Status:   coreauth.StatusActive,
					Storage:  storage,
					Metadata: metadata,
					Attributes: map[string]string{
						"profile_arn": profileArn,
						"source":      "aws-builder-id",
						"email":       email,
					},
				}

				applySessionProxyToAuthRecord(record, sessionProxyURL)
				if errGuard := claimOAuthSessionCommit(state, "kiro"); errGuard != nil {
					return
				}
				savedPath, errSave := h.saveTokenRecord(pollCtx, record)
				if errSave != nil {
					log.Errorf("Failed to save Kiro authentication tokens: %v", errSave)
					FailOAuthSessionCommit(state, "Failed to save authentication tokens")
					return
				}

				CompleteOAuthSession(state)
				fmt.Printf("Kiro authentication successful! Token saved to %s\n", savedPath)
				return
			}
		}
		if IsOAuthSessionPending(state, "kiro") {
			SetOAuthSessionError(state, "OAuth flow timed out")
		}
	}()

	authURL := authResp.VerificationURIComplete
	if authURL == "" {
		authURL = authResp.VerificationURI
	}

	response := gin.H{"status": "ok", "url": authURL, "state": state, "flow": "device"}
	if userCode := strings.TrimSpace(authResp.UserCode); userCode != "" {
		response["user_code"] = userCode
	}
	if authResp.ExpiresIn > 0 {
		response["expires_in"] = authResp.ExpiresIn
	}
	c.JSON(http.StatusOK, response)
}
