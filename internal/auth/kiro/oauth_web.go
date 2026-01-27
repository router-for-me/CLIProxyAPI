// Package kiro provides OAuth Web authentication for Kiro.
package kiro

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
)

const (
	defaultSessionExpiry = 10 * time.Minute
	pollIntervalSeconds  = 5
)

type authSessionStatus string

const (
	statusPending authSessionStatus = "pending"
	statusSuccess authSessionStatus = "success"
	statusFailed  authSessionStatus = "failed"
)

type webAuthSession struct {
	stateID          string
	deviceCode       string
	userCode         string
	authURL          string
	verificationURI  string
	expiresIn        int
	interval         int
	status           authSessionStatus
	startedAt        time.Time
	completedAt      time.Time
	expiresAt        time.Time
	error            string
	tokenData        *KiroTokenData
	ssoClient        *SSOOIDCClient
	clientID         string
	clientSecret     string
	region           string
	cancelFunc       context.CancelFunc
	authMethod       string // "google", "github", "builder-id", "idc"
	startURL         string // Used for IDC
	codeVerifier     string // Used for social auth PKCE
	codeChallenge    string // Used for social auth PKCE
}

type OAuthWebHandler struct {
	cfg              *config.Config
	sessions         map[string]*webAuthSession
	mu               sync.RWMutex
	onTokenObtained  func(*KiroTokenData)
}

func NewOAuthWebHandler(cfg *config.Config) *OAuthWebHandler {
	return &OAuthWebHandler{
		cfg:      cfg,
		sessions: make(map[string]*webAuthSession),
	}
}

func (h *OAuthWebHandler) SetTokenCallback(callback func(*KiroTokenData)) {
	h.onTokenObtained = callback
}

func (h *OAuthWebHandler) RegisterRoutes(router gin.IRouter) {
	oauth := router.Group("/v0/oauth/kiro")
	{
		oauth.GET("", h.handleSelect)
		oauth.GET("/start", h.handleStart)
		oauth.GET("/callback", h.handleCallback)
		oauth.GET("/social/callback", h.handleSocialCallback)
		oauth.GET("/status", h.handleStatus)
		oauth.POST("/import", h.handleImportToken)
		oauth.POST("/refresh", h.handleManualRefresh)
	}
}

func generateStateID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (h *OAuthWebHandler) handleSelect(c *gin.Context) {
	h.renderSelectPage(c)
}

func (h *OAuthWebHandler) handleStart(c *gin.Context) {
	method := c.Query("method")
	
	if method == "" {
		c.Redirect(http.StatusFound, "/v0/oauth/kiro")
		return
	}

	switch method {
	case "google", "github":
		// Google/GitHub social login is not supported for third-party apps
		// due to AWS Cognito redirect_uri restrictions
		h.renderError(c, "Google/GitHub login is not available for third-party applications. Please use AWS Builder ID or import your token from Kiro IDE.")
	case "builder-id":
		h.startBuilderIDAuth(c)
	case "idc":
		h.startIDCAuth(c)
	default:
		h.renderError(c, fmt.Sprintf("Unknown authentication method: %s", method))
	}
}

func (h *OAuthWebHandler) startSocialAuth(c *gin.Context, method string) {
	stateID, err := generateStateID()
	if err != nil {
		h.renderError(c, "Failed to generate state parameter")
		return
	}

	codeVerifier, codeChallenge, err := generatePKCE()
	if err != nil {
		h.renderError(c, "Failed to generate PKCE parameters")
		return
	}

	socialClient := NewSocialAuthClient(h.cfg)
	
	var provider string
	if method == "google" {
		provider = string(ProviderGoogle)
	} else {
		provider = string(ProviderGitHub)
	}

	redirectURI := h.getSocialCallbackURL(c)
	authURL := socialClient.buildLoginURL(provider, redirectURI, codeChallenge, stateID)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)

	session := &webAuthSession{
		stateID:       stateID,
		authMethod:    method,
		authURL:       authURL,
		status:        statusPending,
		startedAt:     time.Now(),
		expiresIn:     600,
		codeVerifier:  codeVerifier,
		codeChallenge: codeChallenge,
		region:        "us-east-1",
		cancelFunc:    cancel,
	}

	h.mu.Lock()
	h.sessions[stateID] = session
	h.mu.Unlock()

	go func() {
		<-ctx.Done()
		h.mu.Lock()
		if session.status == statusPending {
			session.status = statusFailed
			session.error = "Authentication timed out"
		}
		h.mu.Unlock()
	}()

	c.Redirect(http.StatusFound, authURL)
}

func (h *OAuthWebHandler) getSocialCallbackURL(c *gin.Context) string {
	scheme := "http"
	if c.Request.TLS != nil || c.GetHeader("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s/v0/oauth/kiro/social/callback", scheme, c.Request.Host)
}

func (h *OAuthWebHandler) startBuilderIDAuth(c *gin.Context) {
	stateID, err := generateStateID()
	if err != nil {
		h.renderError(c, "Failed to generate state parameter")
		return
	}

	region := defaultIDCRegion
	startURL := builderIDStartURL

	ssoClient := NewSSOOIDCClient(h.cfg)

	regResp, err := ssoClient.RegisterClientWithRegion(c.Request.Context(), region)
	if err != nil {
		log.Errorf("OAuth Web: failed to register client: %v", err)
		h.renderError(c, fmt.Sprintf("Failed to register client: %v", err))
		return
	}

	authResp, err := ssoClient.StartDeviceAuthorizationWithIDC(
		c.Request.Context(),
		regResp.ClientID,
		regResp.ClientSecret,
		startURL,
		region,
	)
	if err != nil {
		log.Errorf("OAuth Web: failed to start device authorization: %v", err)
		h.renderError(c, fmt.Sprintf("Failed to start device authorization: %v", err))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(authResp.ExpiresIn)*time.Second)

	session := &webAuthSession{
		stateID:         stateID,
		deviceCode:      authResp.DeviceCode,
		userCode:        authResp.UserCode,
		authURL:         authResp.VerificationURIComplete,
		verificationURI: authResp.VerificationURI,
		expiresIn:       authResp.ExpiresIn,
		interval:        authResp.Interval,
		status:          statusPending,
		startedAt:       time.Now(),
		ssoClient:       ssoClient,
		clientID:        regResp.ClientID,
		clientSecret:    regResp.ClientSecret,
		region:          region,
		authMethod:      "builder-id",
		startURL:        startURL,
		cancelFunc:      cancel,
	}

	h.mu.Lock()
	h.sessions[stateID] = session
	h.mu.Unlock()

	go h.pollForToken(ctx, session)

	h.renderStartPage(c, session)
}

func (h *OAuthWebHandler) startIDCAuth(c *gin.Context) {
	startURL := c.Query("startUrl")
	region := c.Query("region")

	if startURL == "" {
		h.renderError(c, "Missing startUrl parameter for IDC authentication")
		return
	}
	if region == "" {
		region = defaultIDCRegion
	}

	stateID, err := generateStateID()
	if err != nil {
		h.renderError(c, "Failed to generate state parameter")
		return
	}

	ssoClient := NewSSOOIDCClient(h.cfg)

	regResp, err := ssoClient.RegisterClientWithRegion(c.Request.Context(), region)
	if err != nil {
		log.Errorf("OAuth Web: failed to register client: %v", err)
		h.renderError(c, fmt.Sprintf("Failed to register client: %v", err))
		return
	}

	authResp, err := ssoClient.StartDeviceAuthorizationWithIDC(
		c.Request.Context(),
		regResp.ClientID,
		regResp.ClientSecret,
		startURL,
		region,
	)
	if err != nil {
		log.Errorf("OAuth Web: failed to start device authorization: %v", err)
		h.renderError(c, fmt.Sprintf("Failed to start device authorization: %v", err))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(authResp.ExpiresIn)*time.Second)

	session := &webAuthSession{
		stateID:         stateID,
		deviceCode:      authResp.DeviceCode,
		userCode:        authResp.UserCode,
		authURL:         authResp.VerificationURIComplete,
		verificationURI: authResp.VerificationURI,
		expiresIn:       authResp.ExpiresIn,
		interval:        authResp.Interval,
		status:          statusPending,
		startedAt:       time.Now(),
		ssoClient:       ssoClient,
		clientID:        regResp.ClientID,
		clientSecret:    regResp.ClientSecret,
		region:          region,
		authMethod:      "idc",
		startURL:        startURL,
		cancelFunc:      cancel,
	}

	h.mu.Lock()
	h.sessions[stateID] = session
	h.mu.Unlock()

	go h.pollForToken(ctx, session)

	h.renderStartPage(c, session)
}

func (h *OAuthWebHandler) pollForToken(ctx context.Context, session *webAuthSession) {
	defer session.cancelFunc()

	interval := time.Duration(session.interval) * time.Second
	if interval < time.Duration(pollIntervalSeconds)*time.Second {
		interval = time.Duration(pollIntervalSeconds) * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			h.mu.Lock()
			if session.status == statusPending {
				session.status = statusFailed
				session.error = "Authentication timed out"
			}
			h.mu.Unlock()
			return
		case <-ticker.C:
			tokenResp, err := h.ssoClient(session).CreateTokenWithRegion(
				ctx,
				session.clientID,
				session.clientSecret,
				session.deviceCode,
				session.region,
			)

			if err != nil {
				errStr := err.Error()
				if errStr == ErrAuthorizationPending.Error() {
					continue
				}
				if errStr == ErrSlowDown.Error() {
					interval += 5 * time.Second
					ticker.Reset(interval)
					continue
				}

				h.mu.Lock()
				session.status = statusFailed
				session.error = errStr
				session.completedAt = time.Now()
				h.mu.Unlock()

				log.Errorf("OAuth Web: token polling failed: %v", err)
				return
			}

			expiresAt := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
			profileArn := session.ssoClient.fetchProfileArn(ctx, tokenResp.AccessToken)
			email := FetchUserEmailWithFallback(ctx, h.cfg, tokenResp.AccessToken)

			tokenData := &KiroTokenData{
					AccessToken:  tokenResp.AccessToken,
					RefreshToken: tokenResp.RefreshToken,
					ProfileArn:   profileArn,
					ExpiresAt:    expiresAt.Format(time.RFC3339),
					AuthMethod:   session.authMethod,
					Provider:     "AWS",
					ClientID:     session.clientID,
					ClientSecret: session.clientSecret,
					Email:        email,
					Region:       session.region,
					StartURL:     session.startURL,
				}

			h.mu.Lock()
			session.status = statusSuccess
			session.completedAt = time.Now()
			session.expiresAt = expiresAt
			session.tokenData = tokenData
			h.mu.Unlock()

			if h.onTokenObtained != nil {
				h.onTokenObtained(tokenData)
			}

			// Save token to file
			h.saveTokenToFile(tokenData)

			log.Infof("OAuth Web: authentication successful for %s", email)
			return
		}
	}
}

// saveTokenToFile saves the token data to the auth directory
func (h *OAuthWebHandler) saveTokenToFile(tokenData *KiroTokenData) {
	// Get auth directory from config or use default
	authDir := ""
	if h.cfg != nil && h.cfg.AuthDir != "" {
		var err error
		authDir, err = util.ResolveAuthDir(h.cfg.AuthDir)
		if err != nil {
			log.Errorf("OAuth Web: failed to resolve auth directory: %v", err)
		}
	}

	// Fall back to default location
	if authDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Errorf("OAuth Web: failed to get home directory: %v", err)
			return
		}
		authDir = filepath.Join(home, ".cli-proxy-api")
	}

	// Create directory if not exists
	if err := os.MkdirAll(authDir, 0700); err != nil {
		log.Errorf("OAuth Web: failed to create auth directory: %v", err)
		return
	}

	// Generate filename using the unified function
	fileName := GenerateTokenFileName(tokenData)

	authFilePath := filepath.Join(authDir, fileName)
	
	// Convert to storage format and save
	storage := &KiroTokenStorage{
		Type:         "kiro",
		AccessToken:  tokenData.AccessToken,
		RefreshToken: tokenData.RefreshToken,
		ProfileArn:   tokenData.ProfileArn,
		ExpiresAt:    tokenData.ExpiresAt,
		AuthMethod:   tokenData.AuthMethod,
		Provider:     tokenData.Provider,
		LastRefresh:  time.Now().Format(time.RFC3339),
		ClientID:     tokenData.ClientID,
		ClientSecret: tokenData.ClientSecret,
		Region:       tokenData.Region,
		StartURL:     tokenData.StartURL,
		Email:        tokenData.Email,
	}
	
	if err := storage.SaveTokenToFile(authFilePath); err != nil {
		log.Errorf("OAuth Web: failed to save token to file: %v", err)
		return
	}
	
	log.Infof("OAuth Web: token saved to %s", authFilePath)
}

func (h *OAuthWebHandler) ssoClient(session *webAuthSession) *SSOOIDCClient {
	return session.ssoClient
}

func (h *OAuthWebHandler) handleCallback(c *gin.Context) {
	stateID := c.Query("state")
	errParam := c.Query("error")

	if errParam != "" {
		h.renderError(c, errParam)
		return
	}

	if stateID == "" {
		h.renderError(c, "Missing state parameter")
		return
	}

	h.mu.RLock()
	session, exists := h.sessions[stateID]
	h.mu.RUnlock()

	if !exists {
		h.renderError(c, "Invalid or expired session")
		return
	}

	if session.status == statusSuccess {
		h.renderSuccess(c, session)
	} else if session.status == statusFailed {
		h.renderError(c, session.error)
	} else {
		c.Redirect(http.StatusFound, "/v0/oauth/kiro/start")
	}
}

func (h *OAuthWebHandler) handleSocialCallback(c *gin.Context) {
	stateID := c.Query("state")
	code := c.Query("code")
	errParam := c.Query("error")

	if errParam != "" {
		h.renderError(c, errParam)
		return
	}

	if stateID == "" {
		h.renderError(c, "Missing state parameter")
		return
	}

	if code == "" {
		h.renderError(c, "Missing authorization code")
		return
	}

	h.mu.RLock()
	session, exists := h.sessions[stateID]
	h.mu.RUnlock()

	if !exists {
		h.renderError(c, "Invalid or expired session")
		return
	}

	if session.authMethod != "google" && session.authMethod != "github" {
		h.renderError(c, "Invalid session type for social callback")
		return
	}

	socialClient := NewSocialAuthClient(h.cfg)
	redirectURI := h.getSocialCallbackURL(c)

	tokenReq := &CreateTokenRequest{
		Code:         code,
		CodeVerifier: session.codeVerifier,
		RedirectURI:  redirectURI,
	}

	tokenResp, err := socialClient.CreateToken(c.Request.Context(), tokenReq)
	if err != nil {
		log.Errorf("OAuth Web: social token exchange failed: %v", err)
		h.mu.Lock()
		session.status = statusFailed
		session.error = fmt.Sprintf("Token exchange failed: %v", err)
		session.completedAt = time.Now()
		h.mu.Unlock()
		h.renderError(c, session.error)
		return
	}

	expiresIn := tokenResp.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	expiresAt := time.Now().Add(time.Duration(expiresIn) * time.Second)

	email := ExtractEmailFromJWT(tokenResp.AccessToken)

	var provider string
	if session.authMethod == "google" {
		provider = string(ProviderGoogle)
	} else {
		provider = string(ProviderGitHub)
	}

	tokenData := &KiroTokenData{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		ProfileArn:   tokenResp.ProfileArn,
		ExpiresAt:    expiresAt.Format(time.RFC3339),
		AuthMethod:   session.authMethod,
		Provider:     provider,
		Email:        email,
		Region:       "us-east-1",
	}

	h.mu.Lock()
	session.status = statusSuccess
	session.completedAt = time.Now()
	session.expiresAt = expiresAt
	session.tokenData = tokenData
	h.mu.Unlock()

	if session.cancelFunc != nil {
		session.cancelFunc()
	}

	if h.onTokenObtained != nil {
		h.onTokenObtained(tokenData)
	}

	// Save token to file
	h.saveTokenToFile(tokenData)

	log.Infof("OAuth Web: social authentication successful for %s via %s", email, provider)
	h.renderSuccess(c, session)
}

func (h *OAuthWebHandler) handleStatus(c *gin.Context) {
	stateID := c.Query("state")
	if stateID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing state parameter"})
		return
	}

	h.mu.RLock()
	session, exists := h.sessions[stateID]
	h.mu.RUnlock()

	if !exists {
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
		return
	}

	response := gin.H{
		"status": string(session.status),
	}

	switch session.status {
	case statusPending:
		elapsed := time.Since(session.startedAt).Seconds()
		remaining := float64(session.expiresIn) - elapsed
		if remaining < 0 {
			remaining = 0
		}
		response["remaining_seconds"] = int(remaining)
	case statusSuccess:
		response["completed_at"] = session.completedAt.Format(time.RFC3339)
		response["expires_at"] = session.expiresAt.Format(time.RFC3339)
	case statusFailed:
		response["error"] = session.error
		response["failed_at"] = session.completedAt.Format(time.RFC3339)
	}

	c.JSON(http.StatusOK, response)
}

func (h *OAuthWebHandler) renderStartPage(c *gin.Context, session *webAuthSession) {
	tmpl, err := template.New("start").Parse(oauthWebStartPageHTML)
	if err != nil {
		log.Errorf("OAuth Web: failed to parse template: %v", err)
		c.String(http.StatusInternalServerError, "Template error")
		return
	}

	data := map[string]interface{}{
		"AuthURL":   session.authURL,
		"UserCode":  session.userCode,
		"ExpiresIn": session.expiresIn,
		"StateID":   session.stateID,
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(c.Writer, data); err != nil {
		log.Errorf("OAuth Web: failed to render template: %v", err)
	}
}

func (h *OAuthWebHandler) renderSelectPage(c *gin.Context) {
	tmpl, err := template.New("select").Parse(oauthWebSelectPageHTML)
	if err != nil {
		log.Errorf("OAuth Web: failed to parse select template: %v", err)
		c.String(http.StatusInternalServerError, "Template error")
		return
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(c.Writer, nil); err != nil {
		log.Errorf("OAuth Web: failed to render select template: %v", err)
	}
}

func (h *OAuthWebHandler) renderError(c *gin.Context, errMsg string) {
	tmpl, err := template.New("error").Parse(oauthWebErrorPageHTML)
	if err != nil {
		log.Errorf("OAuth Web: failed to parse error template: %v", err)
		c.String(http.StatusInternalServerError, "Template error")
		return
	}

	data := map[string]interface{}{
		"Error": errMsg,
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Status(http.StatusBadRequest)
	if err := tmpl.Execute(c.Writer, data); err != nil {
		log.Errorf("OAuth Web: failed to render error template: %v", err)
	}
}

func (h *OAuthWebHandler) renderSuccess(c *gin.Context, session *webAuthSession) {
	tmpl, err := template.New("success").Parse(oauthWebSuccessPageHTML)
	if err != nil {
		log.Errorf("OAuth Web: failed to parse success template: %v", err)
		c.String(http.StatusInternalServerError, "Template error")
		return
	}

	data := map[string]interface{}{
		"ExpiresAt": session.expiresAt.Format(time.RFC3339),
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(c.Writer, data); err != nil {
		log.Errorf("OAuth Web: failed to render success template: %v", err)
	}
}

func (h *OAuthWebHandler) CleanupExpiredSessions() {
	h.mu.Lock()
	defer h.mu.Unlock()

	now := time.Now()
	for id, session := range h.sessions {
		if session.status != statusPending && now.Sub(session.completedAt) > 30*time.Minute {
			delete(h.sessions, id)
		} else if session.status == statusPending && now.Sub(session.startedAt) > defaultSessionExpiry {
			session.cancelFunc()
			delete(h.sessions, id)
		}
	}
}

func (h *OAuthWebHandler) GetSession(stateID string) (*webAuthSession, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	session, exists := h.sessions[stateID]
	return session, exists
}

// ImportTokenRequest represents the request body for token import
type ImportTokenRequest struct {
	RefreshToken string `json:"refreshToken"`
}

// handleImportToken handles manual refresh token import from Kiro IDE
func (h *OAuthWebHandler) handleImportToken(c *gin.Context) {
	var req ImportTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "Invalid request body",
		})
		return
	}

	refreshToken := strings.TrimSpace(req.RefreshToken)
	if refreshToken == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "Refresh token is required",
		})
		return
	}

	// Validate token format
	if !strings.HasPrefix(refreshToken, "aorAAAAAG") {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   "Invalid token format. Token should start with aorAAAAAG...",
		})
		return
	}

	// Create social auth client to refresh and validate the token
	socialClient := NewSocialAuthClient(h.cfg)

	// Refresh the token to validate it and get access token
	tokenData, err := socialClient.RefreshSocialToken(c.Request.Context(), refreshToken)
	if err != nil {
		log.Errorf("OAuth Web: token refresh failed during import: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   fmt.Sprintf("Token validation failed: %v", err),
		})
		return
	}

	// Set the original refresh token (the refreshed one might be empty)
	if tokenData.RefreshToken == "" {
		tokenData.RefreshToken = refreshToken
	}
	tokenData.AuthMethod = "social"
	tokenData.Provider = "imported"

	// Notify callback if set
	if h.onTokenObtained != nil {
		h.onTokenObtained(tokenData)
	}

	// Save token to file
	h.saveTokenToFile(tokenData)

	// Generate filename for response using the unified function
	fileName := GenerateTokenFileName(tokenData)

	log.Infof("OAuth Web: token imported successfully")
	c.JSON(http.StatusOK, gin.H{
		"success":  true,
		"message":  "Token imported successfully",
		"fileName": fileName,
	})
}

// handleManualRefresh handles manual token refresh requests from the web UI.
// This allows users to trigger a token refresh when needed, without waiting
// for the automatic 30-second check and 20-minute-before-expiry refresh cycle.
// Uses the same refresh logic as kiro_executor.Refresh for consistency.
func (h *OAuthWebHandler) handleManualRefresh(c *gin.Context) {
	authDir := ""
	if h.cfg != nil && h.cfg.AuthDir != "" {
		var err error
		authDir, err = util.ResolveAuthDir(h.cfg.AuthDir)
		if err != nil {
			log.Errorf("OAuth Web: failed to resolve auth directory: %v", err)
		}
	}

	if authDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"success": false,
				"error":   "Failed to get home directory",
			})
			return
		}
		authDir = filepath.Join(home, ".cli-proxy-api")
	}

	// Find all kiro token files in the auth directory
	files, err := os.ReadDir(authDir)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   fmt.Sprintf("Failed to read auth directory: %v", err),
		})
		return
	}

	var refreshedCount int
	var errors []string

	for _, file := range files {
		if file.IsDir() {
			continue
		}
		name := file.Name()
		if !strings.HasPrefix(name, "kiro-") || !strings.HasSuffix(name, ".json") {
			continue
		}

		filePath := filepath.Join(authDir, name)
		data, err := os.ReadFile(filePath)
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: read error - %v", name, err))
			continue
		}

		var storage KiroTokenStorage
		if err := json.Unmarshal(data, &storage); err != nil {
			errors = append(errors, fmt.Sprintf("%s: parse error - %v", name, err))
			continue
		}

		if storage.RefreshToken == "" {
			errors = append(errors, fmt.Sprintf("%s: no refresh token", name))
			continue
		}

		// Refresh token using the same logic as kiro_executor.Refresh
		tokenData, err := h.refreshTokenData(c.Request.Context(), &storage)
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: refresh failed - %v", name, err))
			continue
		}

		// Update storage with new token data
		storage.AccessToken = tokenData.AccessToken
		if tokenData.RefreshToken != "" {
			storage.RefreshToken = tokenData.RefreshToken
		}
		storage.ExpiresAt = tokenData.ExpiresAt
		storage.LastRefresh = time.Now().Format(time.RFC3339)
		if tokenData.ProfileArn != "" {
			storage.ProfileArn = tokenData.ProfileArn
		}

		// Write updated token back to file
		updatedData, err := json.MarshalIndent(storage, "", "  ")
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: marshal error - %v", name, err))
			continue
		}

		tmpFile := filePath + ".tmp"
		if err := os.WriteFile(tmpFile, updatedData, 0600); err != nil {
			errors = append(errors, fmt.Sprintf("%s: write error - %v", name, err))
			continue
		}
		if err := os.Rename(tmpFile, filePath); err != nil {
			errors = append(errors, fmt.Sprintf("%s: rename error - %v", name, err))
			continue
		}

		log.Infof("OAuth Web: manually refreshed token in %s, expires at %s", name, tokenData.ExpiresAt)
		refreshedCount++

		// Notify callback if set
		if h.onTokenObtained != nil {
			h.onTokenObtained(tokenData)
		}
	}

	if refreshedCount == 0 && len(errors) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"error":   fmt.Sprintf("All refresh attempts failed: %v", errors),
		})
		return
	}

	response := gin.H{
		"success":        true,
		"message":        fmt.Sprintf("Refreshed %d token(s)", refreshedCount),
		"refreshedCount": refreshedCount,
	}
	if len(errors) > 0 {
		response["warnings"] = errors
	}

	c.JSON(http.StatusOK, response)
}

// refreshTokenData refreshes a token using the appropriate method based on auth type.
// This mirrors the logic in kiro_executor.Refresh for consistency.
func (h *OAuthWebHandler) refreshTokenData(ctx context.Context, storage *KiroTokenStorage) (*KiroTokenData, error) {
	ssoClient := NewSSOOIDCClient(h.cfg)

	switch {
	case storage.ClientID != "" && storage.ClientSecret != "" && storage.AuthMethod == "idc" && storage.Region != "":
		// IDC refresh with region-specific endpoint
		log.Debugf("OAuth Web: using SSO OIDC refresh for IDC (region=%s)", storage.Region)
		return ssoClient.RefreshTokenWithRegion(ctx, storage.ClientID, storage.ClientSecret, storage.RefreshToken, storage.Region, storage.StartURL)

	case storage.ClientID != "" && storage.ClientSecret != "" && storage.AuthMethod == "builder-id":
		// Builder ID refresh with default endpoint
		log.Debugf("OAuth Web: using SSO OIDC refresh for AWS Builder ID")
		return ssoClient.RefreshToken(ctx, storage.ClientID, storage.ClientSecret, storage.RefreshToken)

	default:
		// Fallback to Kiro's OAuth refresh endpoint (for social auth: Google/GitHub)
		log.Debugf("OAuth Web: using Kiro OAuth refresh endpoint")
		oauth := NewKiroOAuth(h.cfg)
		return oauth.RefreshToken(ctx, storage.RefreshToken)
	}
}
