package management

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/antigravity"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kimi"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kiro"
	xaiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/xai"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
)

type codexOAuthService interface {
	GenerateAuthURL(state string, pkceCodes *codex.PKCECodes) (string, error)
	ExchangeCodeForTokens(ctx context.Context, code string, pkceCodes *codex.PKCECodes) (*codex.CodexAuthBundle, error)
	CreateTokenStorage(bundle *codex.CodexAuthBundle) *codex.CodexTokenStorage
}

func (h *Handler) RequestAnthropicToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	fmt.Println("Initializing Claude authentication...")

	// Generate PKCE codes
	pkceCodes, err := claude.GeneratePKCECodes()
	if err != nil {
		log.Errorf("Failed to generate PKCE codes: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate PKCE codes"})
		return
	}

	// Generate random state parameter
	state, err := misc.GenerateRandomState()
	if err != nil {
		log.Errorf("Failed to generate state parameter: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate state parameter"})
		return
	}

	// Initialize Claude auth service
	anthropicAuth := claude.NewClaudeAuth(h.cfg)

	// Generate authorization URL (then override redirect_uri to reuse server port)
	authURL, state, err := anthropicAuth.GenerateAuthURL(state, pkceCodes)
	if err != nil {
		log.Errorf("Failed to generate authorization URL: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate authorization url"})
		return
	}

	RegisterOAuthSession(state, "anthropic")

	isWebUI := isWebUIRequest(c)
	var forwarder *callbackForwarder
	if isWebUI {
		targetURL, errTarget := h.managementCallbackURL("/anthropic/callback")
		if errTarget != nil {
			log.WithError(errTarget).Error("failed to compute anthropic callback target")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "callback server unavailable"})
			return
		}
		var errStart error
		if forwarder, errStart = startCallbackForwarder(anthropicCallbackPort, "anthropic", targetURL); errStart != nil {
			log.WithError(errStart).Error("failed to start anthropic callback forwarder")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start callback server"})
			return
		}
	}

	go func() {
		if isWebUI {
			defer stopCallbackForwarderInstance(anthropicCallbackPort, forwarder)
		}

		// Helper: wait for callback file
		waitFile := filepath.Join(h.cfg.AuthDir, fmt.Sprintf(".oauth-anthropic-%s.oauth", state))
		waitForFile := func(path string, timeout time.Duration) (map[string]string, error) {
			deadline := time.Now().Add(timeout)
			for {
				if !IsOAuthSessionPending(state, "anthropic") {
					return nil, errOAuthSessionNotPending
				}
				if time.Now().After(deadline) {
					SetOAuthSessionError(state, "Timeout waiting for OAuth callback")
					return nil, fmt.Errorf("timeout waiting for OAuth callback")
				}
				data, errRead := os.ReadFile(path)
				if errRead == nil {
					var m map[string]string
					_ = json.Unmarshal(data, &m)
					_ = os.Remove(path)
					return m, nil
				}
				time.Sleep(500 * time.Millisecond)
			}
		}

		fmt.Println("Waiting for authentication callback...")
		// Wait up to 5 minutes
		resultMap, errWait := waitForFile(waitFile, 5*time.Minute)
		if errWait != nil {
			if errors.Is(errWait, errOAuthSessionNotPending) {
				return
			}
			authErr := claude.NewAuthenticationError(claude.ErrCallbackTimeout, errWait)
			log.Error(claude.GetUserFriendlyMessage(authErr))
			return
		}
		if errStr := resultMap["error"]; errStr != "" {
			oauthErr := claude.NewOAuthError(errStr, "", http.StatusBadRequest)
			log.Error(claude.GetUserFriendlyMessage(oauthErr))
			SetOAuthSessionError(state, "Bad request")
			return
		}
		if resultMap["state"] != state {
			authErr := claude.NewAuthenticationError(claude.ErrInvalidState, fmt.Errorf("expected %s, got %s", state, resultMap["state"]))
			log.Error(claude.GetUserFriendlyMessage(authErr))
			SetOAuthSessionError(state, "State code error")
			return
		}

		// Parse code (Claude may append state after '#')
		rawCode := resultMap["code"]
		code := strings.Split(rawCode, "#")[0]

		// Exchange code for tokens using internal auth service
		bundle, errExchange := anthropicAuth.ExchangeCodeForTokens(ctx, code, state, pkceCodes)
		if errExchange != nil {
			authErr := claude.NewAuthenticationError(claude.ErrCodeExchangeFailed, errExchange)
			log.Errorf("Failed to exchange authorization code for tokens: %v", authErr)
			SetOAuthSessionError(state, "Failed to exchange authorization code for tokens")
			return
		}

		// Create token storage
		tokenStorage := anthropicAuth.CreateTokenStorage(bundle)
		metadata := map[string]any{"email": tokenStorage.Email}
		if tokenStorage.AccountUUID != "" {
			metadata["account_uuid"] = tokenStorage.AccountUUID
		}
		if tokenStorage.OrganizationUUID != "" {
			metadata["organization_uuid"] = tokenStorage.OrganizationUUID
		}
		if tokenStorage.OrganizationName != "" {
			metadata["organization_name"] = tokenStorage.OrganizationName
		}
		if len(tokenStorage.DeviceIDs) > 0 {
			metadata[claude.ClaudeDeviceIDsMetadataKey] = append([]string(nil), tokenStorage.DeviceIDs...)
		}
		record := &coreauth.Auth{
			ID:       fmt.Sprintf("claude-%s.json", tokenStorage.Email),
			Provider: "claude",
			FileName: fmt.Sprintf("claude-%s.json", tokenStorage.Email),
			Storage:  tokenStorage,
			Metadata: metadata,
		}
		if errGuard := guardOAuthSessionPendingForSave(state, "anthropic"); errGuard != nil {
			return
		}
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Errorf("Failed to save authentication tokens: %v", errSave)
			SetOAuthSessionError(state, "Failed to save authentication tokens")
			return
		}

		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		if bundle.APIKey != "" {
			fmt.Println("API key obtained and saved")
		}
		fmt.Println("You can now use Claude services through this CLI")
		CompleteOAuthSession(state)
	}()

	c.JSON(200, gin.H{"status": "ok", "url": authURL, "state": state})
}

func (h *Handler) RequestCodexToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	fmt.Println("Initializing Codex authentication...")

	// Generate PKCE codes
	pkceCodes, err := codex.GeneratePKCECodes()
	if err != nil {
		log.Errorf("Failed to generate PKCE codes: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate PKCE codes"})
		return
	}

	// Generate random state parameter
	state, err := misc.GenerateRandomState()
	if err != nil {
		log.Errorf("Failed to generate state parameter: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate state parameter"})
		return
	}

	// Initialize Codex auth service
	openaiAuth := newCodexOAuthService(h.cfg)

	// Generate authorization URL
	authURL, err := openaiAuth.GenerateAuthURL(state, pkceCodes)
	if err != nil {
		log.Errorf("Failed to generate authorization URL: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate authorization url"})
		return
	}

	RegisterOAuthSession(state, "codex")

	isWebUI := isWebUIRequest(c)
	var forwarder *callbackForwarder
	if isWebUI {
		targetURL, errTarget := h.managementCallbackURL("/codex/callback")
		if errTarget != nil {
			log.WithError(errTarget).Error("failed to compute codex callback target")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "callback server unavailable"})
			return
		}
		var errStart error
		if forwarder, errStart = startCallbackForwarder(codexCallbackPort, "codex", targetURL); errStart != nil {
			log.WithError(errStart).Error("failed to start codex callback forwarder")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start callback server"})
			return
		}
	}

	go func() {
		if isWebUI {
			defer stopCallbackForwarderInstance(codexCallbackPort, forwarder)
		}

		// Wait for callback file
		waitFile := filepath.Join(h.cfg.AuthDir, fmt.Sprintf(".oauth-codex-%s.oauth", state))
		deadline := time.Now().Add(5 * time.Minute)
		var code string
		for {
			if !IsOAuthSessionPending(state, "codex") {
				return
			}
			if time.Now().After(deadline) {
				authErr := codex.NewAuthenticationError(codex.ErrCallbackTimeout, fmt.Errorf("timeout waiting for OAuth callback"))
				log.Error(codex.GetUserFriendlyMessage(authErr))
				SetOAuthSessionError(state, "Timeout waiting for OAuth callback")
				return
			}
			if data, errR := os.ReadFile(waitFile); errR == nil {
				var m map[string]string
				_ = json.Unmarshal(data, &m)
				_ = os.Remove(waitFile)
				if errStr := m["error"]; errStr != "" {
					oauthErr := codex.NewOAuthError(errStr, "", http.StatusBadRequest)
					log.Error(codex.GetUserFriendlyMessage(oauthErr))
					SetOAuthSessionError(state, "Bad Request")
					return
				}
				if m["state"] != state {
					authErr := codex.NewAuthenticationError(codex.ErrInvalidState, fmt.Errorf("expected %s, got %s", state, m["state"]))
					SetOAuthSessionError(state, "State code error")
					log.Error(codex.GetUserFriendlyMessage(authErr))
					return
				}
				code = m["code"]
				break
			}
			time.Sleep(500 * time.Millisecond)
		}

		log.Debug("Authorization code received, exchanging for tokens...")
		// Exchange code for tokens using internal auth service
		bundle, errExchange := openaiAuth.ExchangeCodeForTokens(ctx, code, pkceCodes)
		if errExchange != nil {
			authErr := codex.NewAuthenticationError(codex.ErrCodeExchangeFailed, errExchange)
			SetOAuthSessionError(state, oauthSessionErrorWithCause("Failed to exchange authorization code for tokens", errExchange))
			log.Errorf("Failed to exchange authorization code for tokens: %v", authErr)
			return
		}

		// Extract additional info for filename generation
		claims, _ := codex.ParseJWTToken(bundle.TokenData.IDToken)
		planType := ""
		hashAccountID := ""
		if claims != nil {
			planType = strings.TrimSpace(claims.CodexAuthInfo.ChatgptPlanType)
			if accountID := claims.GetAccountID(); accountID != "" {
				digest := sha256.Sum256([]byte(accountID))
				hashAccountID = hex.EncodeToString(digest[:])[:8]
			}
		}

		// Create token storage and persist
		tokenStorage := openaiAuth.CreateTokenStorage(bundle)
		fileName := codex.CredentialFileName(tokenStorage.Email, planType, hashAccountID, true)
		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "codex",
			FileName: fileName,
			Storage:  tokenStorage,
			Metadata: map[string]any{
				"email":      tokenStorage.Email,
				"account_id": tokenStorage.AccountID,
			},
		}
		if errGuard := guardOAuthSessionPendingForSave(state, "codex"); errGuard != nil {
			return
		}
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			SetOAuthSessionError(state, "Failed to save authentication tokens")
			log.Errorf("Failed to save authentication tokens: %v", errSave)
			return
		}
		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		if bundle.APIKey != "" {
			fmt.Println("API key obtained and saved")
		}
		fmt.Println("You can now use Codex services through this CLI")
		CompleteOAuthSession(state)
	}()

	c.JSON(200, gin.H{"status": "ok", "url": authURL, "state": state})
}

func (h *Handler) RequestAntigravityToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	fmt.Println("Initializing Antigravity authentication...")

	authSvc := antigravity.NewAntigravityAuth(h.cfg, nil)

	state, errState := misc.GenerateRandomState()
	if errState != nil {
		log.Errorf("Failed to generate state parameter: %v", errState)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate state parameter"})
		return
	}

	redirectURI := fmt.Sprintf("http://localhost:%d/oauth-callback", antigravity.CallbackPort)
	authURL := authSvc.BuildAuthURL(state, redirectURI)

	RegisterOAuthSession(state, "antigravity")

	isWebUI := isWebUIRequest(c)
	var forwarder *callbackForwarder
	if isWebUI {
		targetURL, errTarget := h.managementCallbackURL("/antigravity/callback")
		if errTarget != nil {
			log.WithError(errTarget).Error("failed to compute antigravity callback target")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "callback server unavailable"})
			return
		}
		var errStart error
		if forwarder, errStart = startCallbackForwarder(antigravity.CallbackPort, "antigravity", targetURL); errStart != nil {
			log.WithError(errStart).Error("failed to start antigravity callback forwarder")
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start callback server"})
			return
		}
	}

	go func() {
		if isWebUI {
			defer stopCallbackForwarderInstance(antigravity.CallbackPort, forwarder)
		}

		waitFile := filepath.Join(h.cfg.AuthDir, fmt.Sprintf(".oauth-antigravity-%s.oauth", state))
		deadline := time.Now().Add(5 * time.Minute)
		var authCode string
		for {
			if !IsOAuthSessionPending(state, "antigravity") {
				return
			}
			if time.Now().After(deadline) {
				log.Error("oauth flow timed out")
				SetOAuthSessionError(state, "OAuth flow timed out")
				return
			}
			if data, errReadFile := os.ReadFile(waitFile); errReadFile == nil {
				var payload map[string]string
				_ = json.Unmarshal(data, &payload)
				_ = os.Remove(waitFile)
				if errStr := strings.TrimSpace(payload["error"]); errStr != "" {
					log.Errorf("Authentication failed: %s", errStr)
					SetOAuthSessionError(state, "Authentication failed")
					return
				}
				if payloadState := strings.TrimSpace(payload["state"]); payloadState != "" && payloadState != state {
					log.Errorf("Authentication failed: state mismatch")
					SetOAuthSessionError(state, "Authentication failed: state mismatch")
					return
				}
				authCode = strings.TrimSpace(payload["code"])
				if authCode == "" {
					log.Error("Authentication failed: code not found")
					SetOAuthSessionError(state, "Authentication failed: code not found")
					return
				}
				break
			}
			time.Sleep(500 * time.Millisecond)
		}

		tokenResp, errToken := authSvc.ExchangeCodeForTokens(ctx, authCode, redirectURI)
		if errToken != nil {
			log.Errorf("Failed to exchange token: %v", errToken)
			SetOAuthSessionError(state, "Failed to exchange token")
			return
		}

		accessToken := strings.TrimSpace(tokenResp.AccessToken)
		if accessToken == "" {
			log.Error("antigravity: token exchange returned empty access token")
			SetOAuthSessionError(state, "Failed to exchange token")
			return
		}

		email, errInfo := authSvc.FetchUserInfo(ctx, accessToken)
		if errInfo != nil {
			log.Errorf("Failed to fetch user info: %v", errInfo)
			SetOAuthSessionError(state, "Failed to fetch user info")
			return
		}
		email = strings.TrimSpace(email)
		if email == "" {
			log.Error("antigravity: user info returned empty email")
			SetOAuthSessionError(state, "Failed to fetch user info")
			return
		}

		projectID := ""
		if accessToken != "" {
			fetchedProjectID, errProject := authSvc.FetchProjectID(ctx, accessToken)
			if errProject != nil {
				log.Warnf("antigravity: failed to fetch project ID: %v", errProject)
			} else {
				projectID = fetchedProjectID
				log.Infof("antigravity: obtained project ID %s", util.HideAPIKey(projectID))
			}
		}

		now := time.Now()
		metadata := map[string]any{
			"type":          "antigravity",
			"access_token":  tokenResp.AccessToken,
			"refresh_token": tokenResp.RefreshToken,
			"expires_in":    tokenResp.ExpiresIn,
			"timestamp":     now.UnixMilli(),
			"expired":       now.Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Format(time.RFC3339),
		}
		if email != "" {
			metadata["email"] = email
		}
		if projectID != "" {
			metadata["project_id"] = projectID
		}

		fileName := antigravity.CredentialFileName(email)
		label := strings.TrimSpace(email)
		if label == "" {
			label = "antigravity"
		}

		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "antigravity",
			FileName: fileName,
			Label:    label,
			Metadata: metadata,
		}
		if errGuard := guardOAuthSessionPendingForSave(state, "antigravity"); errGuard != nil {
			return
		}
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Errorf("Failed to save token to file: %v", errSave)
			SetOAuthSessionError(state, "Failed to save token to file")
			return
		}

		CompleteOAuthSession(state)
		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		if projectID != "" {
			fmt.Printf("Using GCP project: %s\n", util.HideAPIKey(projectID))
		}
		fmt.Println("You can now use Antigravity services through this CLI")
	}()

	c.JSON(200, gin.H{"status": "ok", "url": authURL, "state": state})
}

func (h *Handler) RequestXAIToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	fmt.Println("Initializing xAI authentication...")

	state := fmt.Sprintf("xai-%d", time.Now().UnixNano())
	authSvc := xaiauth.NewXAIAuth(h.cfg)

	deviceFlow, errStartDeviceFlow := authSvc.StartDeviceFlow(ctx)
	if errStartDeviceFlow != nil {
		log.Errorf("Failed to start xAI device flow: %v", errStartDeviceFlow)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start device authorization flow"})
		return
	}
	authURL := strings.TrimSpace(deviceFlow.VerificationURIComplete)
	if authURL == "" {
		authURL = strings.TrimSpace(deviceFlow.VerificationURI)
	}

	RegisterOAuthSession(state, "xai")

	go func() {
		pollCtx, cancelPoll := context.WithCancel(ctx)
		defer cancelPoll()
		go watchOAuthSessionCancel(pollCtx, cancelPoll, state, "xai")

		fmt.Println("Waiting for xAI authentication...")
		bundle, errWaitForAuthorization := authSvc.WaitForAuthorization(pollCtx, deviceFlow)
		if errWaitForAuthorization != nil {
			if !IsOAuthSessionPending(state, "xai") {
				return
			}
			log.Errorf("xAI authentication failed: %v", errWaitForAuthorization)
			SetOAuthSessionError(state, oauthSessionErrorWithCause("Authentication failed", errWaitForAuthorization))
			return
		}
		if !IsOAuthSessionPending(state, "xai") {
			return
		}

		tokenStorage := authSvc.CreateTokenStorage(bundle)
		if tokenStorage == nil || strings.TrimSpace(tokenStorage.AccessToken) == "" {
			log.Error("xAI token exchange returned empty access token")
			SetOAuthSessionError(state, "Failed to exchange token")
			return
		}

		fileName := xaiauth.CredentialFileName(tokenStorage.Email, tokenStorage.Subject)
		label := strings.TrimSpace(tokenStorage.Email)
		if label == "" {
			label = "xAI"
		}

		metadata := map[string]any{
			"type":           "xai",
			"access_token":   tokenStorage.AccessToken,
			"refresh_token":  tokenStorage.RefreshToken,
			"id_token":       tokenStorage.IDToken,
			"token_type":     tokenStorage.TokenType,
			"expires_in":     tokenStorage.ExpiresIn,
			"expired":        tokenStorage.Expire,
			"last_refresh":   tokenStorage.LastRefresh,
			"base_url":       tokenStorage.BaseURL,
			"token_endpoint": tokenStorage.TokenEndpoint,
			"auth_kind":      "oauth",
		}
		if tokenStorage.Email != "" {
			metadata["email"] = tokenStorage.Email
		}
		if tokenStorage.Subject != "" {
			metadata["sub"] = tokenStorage.Subject
		}

		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "xai",
			FileName: fileName,
			Label:    label,
			Storage:  tokenStorage,
			Metadata: metadata,
			Attributes: map[string]string{
				"auth_kind": "oauth",
				"base_url":  tokenStorage.BaseURL,
			},
		}
		if errGuard := guardOAuthSessionPendingForSave(state, "xai"); errGuard != nil {
			return
		}
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Errorf("Failed to save xAI token to file: %v", errSave)
			SetOAuthSessionError(state, "Failed to save token to file")
			return
		}

		CompleteOAuthSession(state)
		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		fmt.Println("You can now use xAI services through this CLI")
	}()

	response := gin.H{"status": "ok", "url": authURL, "state": state, "flow": "device"}
	if userCode := strings.TrimSpace(deviceFlow.UserCode); userCode != "" {
		response["user_code"] = userCode
	}
	if deviceFlow.ExpiresIn > 0 {
		response["expires_in"] = deviceFlow.ExpiresIn
	} else {
		response["expires_in"] = int(xaiauth.MaxPollDuration / time.Second)
	}
	c.JSON(200, response)
}

func (h *Handler) RequestKimiToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	fmt.Println("Initializing Kimi authentication...")

	state := fmt.Sprintf("kmi-%d", time.Now().UnixNano())
	// Initialize Kimi auth service
	kimiAuth := kimi.NewKimiAuth(h.cfg)

	// Generate authorization URL
	deviceFlow, errStartDeviceFlow := kimiAuth.StartDeviceFlow(ctx)
	if errStartDeviceFlow != nil {
		log.Errorf("Failed to generate authorization URL: %v", errStartDeviceFlow)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate authorization url"})
		return
	}
	authURL := deviceFlow.VerificationURIComplete
	if authURL == "" {
		authURL = deviceFlow.VerificationURI
	}

	RegisterOAuthSession(state, "kimi")

	go func() {
		pollCtx, cancelPoll := context.WithCancel(ctx)
		defer cancelPoll()
		go watchOAuthSessionCancel(pollCtx, cancelPoll, state, "kimi")

		fmt.Println("Waiting for authentication...")
		authBundle, errWaitForAuthorization := kimiAuth.WaitForAuthorization(pollCtx, deviceFlow)
		if errWaitForAuthorization != nil {
			if !IsOAuthSessionPending(state, "kimi") {
				return
			}
			SetOAuthSessionError(state, oauthSessionErrorWithCause("Authentication failed", errWaitForAuthorization))
			fmt.Printf("Authentication failed: %v\n", errWaitForAuthorization)
			return
		}
		if !IsOAuthSessionPending(state, "kimi") {
			return
		}

		// Create token storage
		tokenStorage := kimiAuth.CreateTokenStorage(authBundle)

		metadata := map[string]any{
			"type":          "kimi",
			"access_token":  authBundle.TokenData.AccessToken,
			"refresh_token": authBundle.TokenData.RefreshToken,
			"token_type":    authBundle.TokenData.TokenType,
			"scope":         authBundle.TokenData.Scope,
			"timestamp":     time.Now().UnixMilli(),
		}
		if authBundle.TokenData.ExpiresAt > 0 {
			expired := time.Unix(authBundle.TokenData.ExpiresAt, 0).UTC().Format(time.RFC3339)
			metadata["expired"] = expired
		}
		if strings.TrimSpace(authBundle.DeviceID) != "" {
			metadata["device_id"] = strings.TrimSpace(authBundle.DeviceID)
		}

		fileName := fmt.Sprintf("kimi-%d.json", time.Now().UnixMilli())
		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "kimi",
			FileName: fileName,
			Label:    "Kimi User",
			Storage:  tokenStorage,
			Metadata: metadata,
		}
		if errGuard := guardOAuthSessionPendingForSave(state, "kimi"); errGuard != nil {
			return
		}
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Errorf("Failed to save authentication tokens: %v", errSave)
			SetOAuthSessionError(state, "Failed to save authentication tokens")
			return
		}

		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		fmt.Println("You can now use Kimi services through this CLI")
		CompleteOAuthSession(state)
	}()

	response := gin.H{"status": "ok", "url": authURL, "state": state, "flow": "device"}
	if userCode := strings.TrimSpace(deviceFlow.UserCode); userCode != "" {
		response["user_code"] = userCode
	}
	if deviceFlow.ExpiresIn > 0 {
		response["expires_in"] = deviceFlow.ExpiresIn
	}
	c.JSON(200, response)
}

// watchOAuthSessionCancel cancels pollCtx once the OAuth session is no longer pending.
func watchOAuthSessionCancel(pollCtx context.Context, cancel context.CancelFunc, state, provider string) {
	if cancel == nil {
		return
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-pollCtx.Done():
			return
		case <-ticker.C:
			if !IsOAuthSessionPending(state, provider) {
				cancel()
				return
			}
		}
	}
}

// CancelAuthSession cancels a pending OAuth session identified by state.
// Protected by management auth. Safe for both callback and device-code flows:
// waiters check IsOAuthSessionPending and exit without saving credentials.
func (h *Handler) CancelAuthSession(c *gin.Context) {
	state := strings.TrimSpace(c.Query("state"))
	if state == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "missing state"})
		return
	}
	if err := ValidateOAuthState(state); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid state"})
		return
	}
	cancelled := CancelOAuthSession(state)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "cancelled": cancelled})
}

func (h *Handler) GetAuthStatus(c *gin.Context) {
	state := strings.TrimSpace(c.Query("state"))
	if state == "" {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
		return
	}
	if err := ValidateOAuthState(state); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid state"})
		return
	}

	provider, status, isPlugin, metadata, completed, ok := GetOAuthSessionDetails(state)
	if !ok {
		c.JSON(http.StatusOK, gin.H{"status": "error", "error": "unknown or expired state"})
		return
	}
	if completed {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
		return
	}
	if status != "" {
		c.JSON(http.StatusOK, gin.H{"status": "error", "error": status})
		return
	}
	h.mu.Lock()
	host := h.pluginHost
	h.mu.Unlock()
	if isPlugin && host != nil && host.HasAuthProvider(provider) {
		ctx := PopulateAuthContext(context.Background(), c)
		resp, handled, errPoll := host.PollLogin(ctx, provider, state, metadata)
		if handled {
			if errPoll != nil {
				message := strings.TrimSpace(errPoll.Error())
				if message == "" {
					message = "Authentication failed"
				}
				SetOAuthSessionError(state, message)
				c.JSON(http.StatusOK, gin.H{"status": "error", "error": message})
				return
			}
			switch resp.Status {
			case "", pluginapi.AuthLoginStatusPending:
				c.JSON(http.StatusOK, gin.H{"status": "wait"})
				return
			case pluginapi.AuthLoginStatusError:
				message := strings.TrimSpace(resp.Message)
				if message == "" {
					message = "Authentication failed"
				}
				SetOAuthSessionError(state, message)
				c.JSON(http.StatusOK, gin.H{"status": "error", "error": message})
				return
			case pluginapi.AuthLoginStatusSuccess:
				records := pluginLoginPollAuths(host, resp)
				if len(records) == 0 {
					SetOAuthSessionError(state, "Authentication failed")
					c.JSON(http.StatusOK, gin.H{"status": "error", "error": "Authentication failed"})
					return
				}
				if errSave := h.savePluginLoginRecords(ctx, records); errSave != nil {
					log.WithError(errSave).WithField("provider", provider).Error("failed to save plugin auth tokens")
					SetOAuthSessionError(state, "Failed to save authentication tokens")
					c.JSON(http.StatusOK, gin.H{"status": "error", "error": "Failed to save authentication tokens"})
					return
				}
				CompleteOAuthSession(state)
				c.JSON(http.StatusOK, gin.H{"status": "ok"})
				return
			default:
				c.JSON(http.StatusOK, gin.H{"status": "wait"})
				return
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{"status": "wait"})
}

func pluginLoginPollAuths(host *pluginhost.Host, resp pluginapi.AuthLoginPollResponse) []*coreauth.Auth {
	if host == nil {
		return nil
	}
	authDatas := resp.Auths
	if len(authDatas) == 0 {
		authDatas = []pluginapi.AuthData{resp.Auth}
	}
	records := make([]*coreauth.Auth, 0, len(authDatas))
	for _, authData := range authDatas {
		record := host.AuthDataToCoreAuth(authData, "", "")
		if record == nil {
			return nil
		}
		records = append(records, record)
	}
	return records
}

func (h *Handler) savePluginLoginRecords(ctx context.Context, records []*coreauth.Auth) error {
	savedPaths := make([]string, 0, len(records))
	for _, record := range records {
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if strings.TrimSpace(savedPath) != "" {
			savedPaths = append(savedPaths, savedPath)
		}
		if errSave != nil {
			h.rollbackSavedTokenRecords(ctx, savedPaths)
			return errSave
		}
	}
	return nil
}

func (h *Handler) rollbackSavedTokenRecords(ctx context.Context, savedPaths []string) {
	for i := len(savedPaths) - 1; i >= 0; i-- {
		path := strings.TrimSpace(savedPaths[i])
		if path == "" {
			continue
		}
		if errDelete := h.deleteTokenRecord(ctx, path); errDelete != nil {
			log.WithError(errDelete).WithField("path", path).Warn("failed to roll back plugin auth token")
		}
		h.removeAuthsForPath(ctx, path, path)
	}
}

// PopulateAuthContext extracts request info and adds it to the context
func PopulateAuthContext(ctx context.Context, c *gin.Context) context.Context {
	info := &coreauth.RequestInfo{
		Query:   c.Request.URL.Query(),
		Headers: c.Request.Header,
	}
	return coreauth.WithRequestInfo(ctx, info)
}

func (h *Handler) RequestKiroToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	method := "builder-id"
	region := kiro.DefaultAwsRegion
	startURL := kiro.DefaultStartURL
	ssoToken := ""
	apiKey := ""

	if c.Request.Method == http.MethodPost {
		var req struct {
			Method     string `json:"method"`
			AuthMethod string `json:"auth_method"`
			Region     string `json:"region"`
			StartURL   string `json:"start_url"`
			SsoToken   string `json:"sso_token"`
			APIKey     string `json:"api_key"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
			return
		}
		if req.Method != "" {
			method = req.Method
		} else if req.AuthMethod != "" {
			method = req.AuthMethod
		}
		if req.Region != "" {
			region = req.Region
		}
		if req.StartURL != "" {
			startURL = req.StartURL
		}
		ssoToken = req.SsoToken
		apiKey = req.APIKey
	} else {
		method = c.DefaultQuery("method", c.DefaultQuery("auth_method", method))
		region = c.DefaultQuery("region", region)
		startURL = c.DefaultQuery("start_url", startURL)
		ssoToken = c.DefaultQuery("sso_token", "")
		apiKey = c.DefaultQuery("api_key", "")
		if strings.TrimSpace(ssoToken) != "" || strings.TrimSpace(apiKey) != "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Kiro secrets must be submitted in a POST body"})
			return
		}
	}
	method = strings.ToLower(strings.TrimSpace(method))

	authSvc := kiro.NewKiroAuth(h.cfg, nil)

	switch method {
	case "iam-sso", "idc":
		sessionID, authorizeURL, expiresIn, err := authSvc.StartIamSsoAuth(ctx, startURL, region)
		if err != nil {
			log.Errorf("Failed to start Kiro IAM SSO flow: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"status":      "ok",
			"auth_method": "iam-sso",
			"session_id":  sessionID,
			"url":         authorizeURL,
			"expires_in":  expiresIn,
		})

	case "google", "github":
		redirectURI := kiro.SocialRedirectURI
		var forwarder *callbackForwarder
		if isWebUIRequest(c) {
			redirectURI = kiro.KiroSocialDefaultLoopbackURL()
			targetURL, errTarget := h.managementCallbackURL(kiro.KiroSocialDefaultLoopbackPath)
			if errTarget != nil {
				log.WithError(errTarget).Error("failed to compute Kiro callback target")
				c.JSON(http.StatusInternalServerError, gin.H{"error": "callback server unavailable"})
				return
			}
			forwarderStart, errStart := startCallbackForwarder(kiroCallbackPort, "kiro", targetURL)
			if errStart != nil {
				log.WithError(errStart).Warn("failed to start Kiro callback forwarder")
				c.JSON(http.StatusInternalServerError, gin.H{"error": "callback server unavailable"})
				return
			}
			forwarder = forwarderStart
		}
		sessionID, authorizationURL, expiresIn, state, err := authSvc.StartKiroSocialAuthWithRedirect(method, redirectURI)
		if err != nil {
			if forwarder != nil {
				stopCallbackForwarderInstance(kiroCallbackPort, forwarder)
			}
			log.Errorf("Failed to start Kiro %s social flow: %v", method, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if state != "" {
			RegisterOAuthSession(state, "kiro")
		}
		c.JSON(http.StatusOK, gin.H{
			"status":      "ok",
			"auth_method": method,
			"session_id":  sessionID,
			"state":       state,
			"url":         authorizationURL,
			"expires_in":  expiresIn,
		})

	case "microsoft-sso", "external_idp", "azuread":
		sessionID, authorizationURL, expiresIn, err := authSvc.StartMicrosoftSSOAuth()
		if err != nil {
			log.Errorf("Failed to start Kiro Microsoft SSO flow: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"status":      "ok",
			"auth_method": "microsoft-sso",
			"session_id":  sessionID,
			"url":         authorizationURL,
			"expires_in":  expiresIn,
		})

	case "sso-token":
		if strings.TrimSpace(ssoToken) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "sso_token parameter is required"})
			return
		}
		creds, err := authSvc.ImportFromSsoToken(ctx, ssoToken, region)
		if err != nil {
			log.Errorf("Failed to import Kiro SSO token: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if strings.TrimSpace(creds.ProfileArn) == "" {
			if profileArn, errProfile := authSvc.ResolveProfileArn(ctx, creds); errProfile == nil && profileArn != "" {
				creds.ProfileArn = profileArn
			}
		}
		email, userID, _ := authSvc.GetUserInfo(ctx, creds.AccessToken, creds.ProfileArn, creds.Region)
		fileName := kiro.CredentialFileName(email, userID)
		label := strings.TrimSpace(email)
		if label == "" {
			label = "Kiro AI (SSO Token)"
		}
		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "kiro",
			FileName: fileName,
			Label:    label,
			Metadata: kiro.NormalizeKiroMetadata(creds, email, userID),
		}
		savedPath, errSave := h.saveTokenRecord(c.Request.Context(), record)
		if errSave != nil {
			log.Errorf("Failed to save token record: %v", errSave)
			c.JSON(http.StatusInternalServerError, gin.H{"error": errSave.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"status":      "ok",
			"auth_method": "sso-token",
			"file_name":   fileName,
			"path":        savedPath,
		})

	case "auto-import":
		autoCreds, err := kiro.AutoDetectKiroToken()
		if err != nil {
			log.Errorf("Failed to auto-detect Kiro token: %v", err)
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error(), "found": false})
			return
		}
		now := time.Now()
		metadata := map[string]any{
			"type":          "kiro",
			"refresh_token": autoCreds.RefreshToken,
			"profile_arn":   autoCreds.ProfileArn,
			"auth_method":   "imported",
			"region":        autoCreds.Region,
			"client_id":     autoCreds.ClientId,
			"client_secret": autoCreds.ClientSecret,
			"timestamp":     now.UnixMilli(),
		}
		fileName := fmt.Sprintf("kiro-%d.json", now.Unix())
		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "kiro",
			FileName: fileName,
			Label:    "Kiro AI (Auto-Detected Local)",
			Metadata: metadata,
		}
		savedPath, errSave := h.saveTokenRecord(c.Request.Context(), record)
		if errSave != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": errSave.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"status":      "ok",
			"found":       true,
			"auth_method": "auto-import",
			"file_name":   fileName,
			"path":        savedPath,
		})

	case "api-key":
		if strings.TrimSpace(apiKey) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "api_key is required"})
			return
		}
		keyResult, errVal := kiro.ValidateKiroApiKey(apiKey, region)
		if errVal != nil {
			log.Errorf("Failed to validate Kiro API Key: %v", errVal)
			c.JSON(http.StatusBadRequest, gin.H{"error": errVal.Error()})
			return
		}
		now := time.Now()
		metadata := map[string]any{
			"type":         "kiro",
			"access_token": keyResult.ApiKey,
			"kiro_api_key": keyResult.ApiKey,
			"auth_method":  "api_key",
			"region":       keyResult.Region,
			"timestamp":    now.UnixMilli(),
		}
		fileName := fmt.Sprintf("kiro-%d.json", now.Unix())
		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "kiro",
			FileName: fileName,
			Label:    "Kiro AI (API Key)",
			Metadata: metadata,
		}
		savedPath, errSave := h.saveTokenRecord(c.Request.Context(), record)
		if errSave != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": errSave.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"status":      "ok",
			"auth_method": "api-key",
			"file_name":   fileName,
			"path":        savedPath,
		})

	case "builder-id":
		fmt.Println("Initializing Kiro AI authentication (device flow)...")

		state := fmt.Sprintf("kiro-%d", time.Now().UnixNano())
		reg, errReg := authSvc.RegisterClient(ctx, region)
		if errReg != nil {
			log.Errorf("Failed to register Kiro OIDC client: %v", errReg)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to register OIDC client"})
			return
		}

		deviceFlow, errStart := authSvc.StartDeviceAuthorization(ctx, reg.ClientID, reg.ClientSecret, startURL, region)
		if errStart != nil {
			log.Errorf("Failed to start Kiro device flow: %v", errStart)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start device authorization flow"})
			return
		}

		authURL := strings.TrimSpace(deviceFlow.VerificationURIComplete)
		if authURL == "" {
			authURL = strings.TrimSpace(deviceFlow.VerificationURI)
		}

		RegisterOAuthSession(state, "kiro")

		go func() {
			pollCtx, cancelPoll := context.WithCancel(ctx)
			defer cancelPoll()
			go watchOAuthSessionCancel(pollCtx, cancelPoll, state, "kiro")

			fmt.Println("Waiting for Kiro AI authentication...")
			interval := time.Duration(deviceFlow.Interval) * time.Second
			if interval < 2*time.Second {
				interval = 5 * time.Second
			}
			deadline := time.Now().Add(time.Duration(deviceFlow.ExpiresIn) * time.Second)

			var tokenResp *kiro.TokenResponse
			for {
				select {
				case <-pollCtx.Done():
					return
				default:
				}
				if time.Now().After(deadline) {
					SetOAuthSessionError(state, "Device code expired")
					return
				}
				resp, errPoll := authSvc.PollDeviceToken(pollCtx, reg.ClientID, reg.ClientSecret, deviceFlow.DeviceCode, region)
				if errPoll == nil && resp.AccessToken != "" {
					tokenResp = resp
					break
				}
				time.Sleep(interval)
			}

			if !IsOAuthSessionPending(state, "kiro") {
				return
			}

			now := time.Now()
			creds := &kiro.KiroCredentials{
				AccessToken:   tokenResp.AccessToken,
				RefreshToken:  tokenResp.RefreshToken,
				ProfileArn:    tokenResp.ProfileArn,
				AuthMethod:    "builder-id",
				ClientID:      reg.ClientID,
				ClientSecret:  reg.ClientSecret,
				Region:        region,
				ExpiresAt:     now.Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Unix(),
				LastRefreshed: now.Unix(),
			}

			if strings.TrimSpace(creds.ProfileArn) == "" {
				if profileArn, errProfile := authSvc.ResolveProfileArn(ctx, creds); errProfile == nil && profileArn != "" {
					creds.ProfileArn = profileArn
				}
			}

			email, userID, _ := authSvc.GetUserInfo(ctx, creds.AccessToken, creds.ProfileArn, creds.Region)
			fileName := kiro.CredentialFileName(email, userID)
			label := strings.TrimSpace(email)
			if label == "" {
				label = "Kiro AI (AWS Builder ID)"
			}

			record := &coreauth.Auth{
				ID:       fileName,
				Provider: "kiro",
				FileName: fileName,
				Label:    label,
				Metadata: kiro.NormalizeKiroMetadata(creds, email, userID),
			}
			if errGuard := guardOAuthSessionPendingForSave(state, "kiro"); errGuard != nil {
				return
			}
			savedPath, errSave := h.saveTokenRecord(ctx, record)
			if errSave != nil {
				log.Errorf("Failed to save token to file: %v", errSave)
				SetOAuthSessionError(state, "Failed to save token to file")
				return
			}

			CompleteOAuthSession(state)
			fmt.Printf("Kiro authentication successful! Token saved to %s\n", savedPath)
		}()

		c.JSON(http.StatusOK, gin.H{"status": "ok", "auth_method": "builder-id", "url": authURL, "state": state})
	}
}

func (h *Handler) saveKiroSocialCredential(ctx context.Context, authMethod string, res *kiro.MicrosoftSSOResult) (string, string, error) {
	if res == nil {
		return "", "", fmt.Errorf("invalid social callback state")
	}
	now := time.Now()
	creds := &kiro.KiroCredentials{
		AccessToken:   res.AccessToken,
		RefreshToken:  res.RefreshToken,
		ProfileArn:    res.ProfileArn,
		AuthMethod:    authMethod,
		ExpiresAt:     res.ExpiresAt,
		LastRefreshed: now.Unix(),
		Region:        kiro.DefaultAwsRegion,
	}

	authSvc := kiro.NewKiroAuth(h.cfg, nil)
	if strings.TrimSpace(creds.ProfileArn) == "" {
		if profileArn, errProfile := authSvc.ResolveProfileArn(ctx, creds); errProfile == nil && profileArn != "" {
			creds.ProfileArn = profileArn
		}
	}

	email, userID, _ := authSvc.GetUserInfo(ctx, creds.AccessToken, creds.ProfileArn, creds.Region)
	fileName := kiro.CredentialFileName(email, userID)
	label := strings.TrimSpace(email)
	if label == "" {
		label = fmt.Sprintf("Kiro AI (%s)", strings.ToUpper(authMethod))
	}

	record := &coreauth.Auth{
		ID:       fileName,
		Provider: "kiro",
		FileName: fileName,
		Label:    label,
		Metadata: kiro.NormalizeKiroMetadata(creds, email, userID),
	}
	savedPath, errSave := h.saveTokenRecord(ctx, record)
	if errSave != nil {
		return "", "", errSave
	}
	return fileName, savedPath, nil
}

func (h *Handler) HandleKiroSocialLoopbackCallback(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)
	callbackForwardersMu.Lock()
	forwarder := callbackForwarders[kiroCallbackPort]
	callbackForwardersMu.Unlock()
	defer stopCallbackForwarderInstance(kiroCallbackPort, forwarder)
	state := strings.TrimSpace(c.Query("state"))
	callbackURL := kiro.KiroSocialDefaultLoopbackURL()
	if c.Request != nil && c.Request.URL != nil && c.Request.URL.RawQuery != "" {
		callbackURL += "?" + c.Request.URL.RawQuery
	}

	authSvc := kiro.NewKiroAuth(h.cfg, nil)
	authMethod, progress, err := authSvc.ContinueKiroSocialAuthByState(callbackURL)
	if err != nil {
		if state != "" {
			SetOAuthSessionError(state, err.Error())
		}
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusBadRequest, `<html><head><meta charset="utf-8"><title>Kiro authentication failed</title></head><body><h1>Kiro authentication failed</h1><p>%s</p></body></html>`, html.EscapeString(err.Error()))
		return
	}
	if progress == nil || progress.Result == nil {
		if state != "" {
			SetOAuthSessionError(state, "invalid social callback state")
		}
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusInternalServerError, `<html><head><meta charset="utf-8"><title>Kiro authentication failed</title></head><body><h1>Kiro authentication failed</h1><p>Invalid social callback state.</p></body></html>`)
		return
	}
	if state != "" {
		if errGuard := guardOAuthSessionPendingForSave(state, "kiro"); errGuard != nil {
			c.Header("Content-Type", "text/html; charset=utf-8")
			c.String(http.StatusConflict, `<html><head><meta charset="utf-8"><title>Kiro authentication already handled</title></head><body><h1>Kiro authentication already handled</h1><p>This login session is no longer pending.</p></body></html>`)
			return
		}
	}
	if _, _, errSave := h.saveKiroSocialCredential(ctx, authMethod, progress.Result); errSave != nil {
		if state != "" {
			SetOAuthSessionError(state, "Failed to save Kiro social credentials")
		}
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusInternalServerError, `<html><head><meta charset="utf-8"><title>Kiro authentication failed</title></head><body><h1>Kiro authentication failed</h1><p>Failed to save Kiro social credentials.</p></body></html>`)
		return
	}
	if state != "" {
		CompleteOAuthSession(state)
	}
	writeKiroSocialLoopbackSuccess(c)
}

func writeKiroSocialLoopbackSuccess(c *gin.Context) {
	redirectURI := kiro.SocialRedirectURI
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, `<html><head><meta charset="utf-8"><title>Kiro authentication successful</title><script>window.location.assign(%q);</script></head><body><h1>Kiro authentication successful!</h1><p>Kiro should open automatically. If it does not, <a href="%s">Open Kiro IDE</a>.</p></body></html>`, redirectURI, html.EscapeString(redirectURI))
}

func (h *Handler) SubmitKiroCallback(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	var req struct {
		AuthMethod  string `json:"auth_method"`
		SessionID   string `json:"session_id"`
		CallbackURL string `json:"callback_url"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	if req.SessionID == "" || req.CallbackURL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "session_id and callback_url are required"})
		return
	}

	authSvc := kiro.NewKiroAuth(h.cfg, nil)
	req.AuthMethod = strings.ToLower(strings.TrimSpace(req.AuthMethod))

	switch req.AuthMethod {
	case "iam-sso", "idc":
		creds, err := authSvc.CompleteIamSsoAuth(ctx, req.SessionID, req.CallbackURL)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if strings.TrimSpace(creds.ProfileArn) == "" {
			if profileArn, errProfile := authSvc.ResolveProfileArn(ctx, creds); errProfile == nil && profileArn != "" {
				creds.ProfileArn = profileArn
			}
		}
		email, userID, _ := authSvc.GetUserInfo(ctx, creds.AccessToken, creds.ProfileArn, creds.Region)
		fileName := kiro.CredentialFileName(email, userID)
		label := strings.TrimSpace(email)
		if label == "" {
			label = "Kiro AI (AWS IAM SSO)"
		}
		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "kiro",
			FileName: fileName,
			Label:    label,
			Metadata: kiro.NormalizeKiroMetadata(creds, email, userID),
		}
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save token to file"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "stage": "complete", "file_name": fileName, "path": savedPath})

	case "microsoft-sso", "external_idp", "azuread":
		progress, err := authSvc.ContinueMicrosoftSSOAuth(req.SessionID, req.CallbackURL)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if progress.AuthorizationURL != "" {
			c.JSON(http.StatusOK, gin.H{"status": "ok", "stage": "provider", "url": progress.AuthorizationURL})
			return
		}
		if progress.Result != nil {
			now := time.Now()
			res := progress.Result
			creds := &kiro.KiroCredentials{
				AccessToken:   res.AccessToken,
				RefreshToken:  res.RefreshToken,
				ProfileArn:    res.ProfileArn,
				AuthMethod:    "external_idp",
				ClientID:      res.ClientID,
				TokenEndpoint: res.TokenEndpoint,
				IssuerURL:     res.IssuerURL,
				Scopes:        res.Scopes,
				ExpiresAt:     res.ExpiresAt,
				LastRefreshed: now.Unix(),
				Region:        kiro.DefaultAwsRegion,
			}
			if strings.TrimSpace(creds.ProfileArn) == "" {
				if profileArn, errProfile := authSvc.ResolveProfileArn(ctx, creds); errProfile == nil && profileArn != "" {
					creds.ProfileArn = profileArn
				}
			}
			email := res.Email
			userID := res.UserID
			if email == "" {
				email, userID, _ = authSvc.GetUserInfo(ctx, creds.AccessToken, creds.ProfileArn, creds.Region)
			}
			fileName := kiro.CredentialFileName(email, userID)
			label := strings.TrimSpace(email)
			if label == "" {
				label = "Kiro AI (Microsoft Enterprise SSO)"
			}
			record := &coreauth.Auth{
				ID:       fileName,
				Provider: "kiro",
				FileName: fileName,
				Label:    label,
				Metadata: kiro.NormalizeKiroMetadata(creds, email, userID),
			}
			savedPath, errSave := h.saveTokenRecord(ctx, record)
			if errSave != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save token to file"})
				return
			}
			c.JSON(http.StatusOK, gin.H{"status": "ok", "stage": "complete", "file_name": fileName, "path": savedPath})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "invalid progress state"})

	case "google", "github":
		progress, err := authSvc.ContinueKiroSocialAuth(req.SessionID, req.CallbackURL)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if progress == nil || progress.Result == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "invalid social callback state"})
			return
		}
		fileName, savedPath, errSave := h.saveKiroSocialCredential(ctx, req.AuthMethod, progress.Result)
		if errSave != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save Kiro social credentials"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok", "stage": "complete", "file_name": fileName, "path": savedPath})

	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported auth_method for callback"})
	}
}
