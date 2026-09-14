package management

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/devin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

type devinOAuthService interface {
	BuildAuthorizationURL(redirectURI, codeChallenge, state string) string
	ExchangeCodeForToken(ctx context.Context, code, codeVerifier string) (string, error)
	CreateAuthRecord(ctx context.Context, token string) (*coreauth.Auth, error)
}

var newDevinOAuthService = func(cfg *config.Config) devinOAuthService {
	client := util.SetProxy(&cfg.SDKConfig, &http.Client{Timeout: 30 * time.Second})
	return devin.NewDevinAuthService(client)
}

// devinCallbackURL builds the required loopback callback URL for Devin OAuth.
// Devin's authorization page strictly validates that redirect_uri matches
// http://127.0.0.1:<port>/callback (http protocol, 127.0.0.1 host, and /callback path).
func (h *Handler) devinCallbackURL() (string, error) {
	if h == nil || h.cfg == nil || h.cfg.Port <= 0 {
		return "", fmt.Errorf("server port is not configured")
	}
	return fmt.Sprintf("http://127.0.0.1:%d/callback", h.cfg.Port), nil
}

// RequestDevinToken starts the same callback/status flow used by the other WebUI providers.
func (h *Handler) RequestDevinToken(c *gin.Context) {
	redirectURI, errRedirect := h.devinCallbackURL()
	if errRedirect != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "callback server unavailable"})
		return
	}
	pkceCodes, errPKCE := devin.GeneratePKCECodes()
	if errPKCE != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate PKCE codes"})
		return
	}
	state, errState := misc.GenerateRandomState()
	if errState != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate state parameter"})
		return
	}

	authSvc := newDevinOAuthService(h.cfg)
	authURL := authSvc.BuildAuthorizationURL(redirectURI, pkceCodes.CodeChallenge, state)
	RegisterOAuthSession(state, "devin")
	// Do not inherit the HTTP request cancellation: login continues after returning the URL.
	ctx := PopulateAuthContext(context.Background(), c)
	authDir := h.cfg.AuthDir
	go h.completeDevinOAuth(ctx, authDir, state, pkceCodes.CodeVerifier, authSvc)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "url": authURL, "state": state})
}

func (h *Handler) completeDevinOAuth(ctx context.Context, authDir, state, codeVerifier string, authSvc devinOAuthService) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	go watchOAuthSessionCancel(ctx, cancel, state, "devin")

	waitFile := filepath.Join(authDir, fmt.Sprintf(".oauth-devin-%s.oauth", state))
	defer func() {
		if errRemove := os.Remove(waitFile); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
			log.Warn("failed to remove Devin OAuth callback file")
		}
	}()
	result, errWait := waitDevinOAuthCallback(ctx, waitFile, state)
	if errWait != nil {
		if IsOAuthSessionPending(state, "devin") {
			SetOAuthSessionError(state, errWait.Error())
		}
		return
	}
	if !IsOAuthSessionPending(state, "devin") {
		return
	}
	if result.State != state {
		SetOAuthSessionError(state, "State code error")
		return
	}
	if result.Error != "" {
		SetOAuthSessionError(state, "Devin authorization denied")
		return
	}
	if strings.TrimSpace(result.Code) == "" {
		SetOAuthSessionError(state, "Missing authorization code")
		return
	}

	token, errExchange := authSvc.ExchangeCodeForToken(ctx, result.Code, codeVerifier)
	if errExchange != nil || strings.TrimSpace(token) == "" {
		// Upstream errors can contain tokens or authorization codes; never expose them.
		if IsOAuthSessionPending(state, "devin") {
			SetOAuthSessionError(state, "Failed to exchange authorization code for tokens")
		}
		return
	}
	if !IsOAuthSessionPending(state, "devin") {
		return
	}
	record, errRecord := authSvc.CreateAuthRecord(ctx, token)
	if errRecord != nil {
		if IsOAuthSessionPending(state, "devin") {
			SetOAuthSessionError(state, "Failed to create Devin authentication record")
		}
		return
	}
	if errGuard := guardOAuthSessionPendingForSave(state, "devin"); errGuard != nil {
		return
	}
	if _, errSave := h.saveTokenRecord(ctx, record); errSave != nil {
		SetOAuthSessionError(state, "Failed to save authentication tokens")
		return
	}
	CompleteOAuthSession(state)
	log.Info("Devin authentication successful")
}

func waitDevinOAuthCallback(ctx context.Context, path, state string) (*oauthCallbackFilePayload, error) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if !IsOAuthSessionPending(state, "devin") {
			return nil, errOAuthSessionNotPending
		}
		if ctx.Err() != nil {
			return nil, errors.New("Timeout waiting for OAuth callback")
		}
		data, errRead := os.ReadFile(path)
		if errRead == nil {
			var result oauthCallbackFilePayload
			if errDecode := json.Unmarshal(data, &result); errDecode != nil {
				return nil, errors.New("Invalid OAuth callback")
			}
			return &result, nil
		}
		if !errors.Is(errRead, os.ErrNotExist) {
			return nil, errors.New("Failed to read OAuth callback")
		}
		select {
		case <-ctx.Done():
			return nil, errors.New("Timeout waiting for OAuth callback")
		case <-ticker.C:
		}
	}
}
