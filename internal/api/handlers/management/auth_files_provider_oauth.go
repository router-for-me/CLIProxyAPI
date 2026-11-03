package management

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/antigravity"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kimi"
	metaauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/meta"
	xaiauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/xai"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
)

type codexOAuthService interface {
	GenerateAuthURL(state string, pkceCodes *codex.PKCECodes) (string, error)
	ExchangeCodeForTokens(ctx context.Context, code string, pkceCodes *codex.PKCECodes) (*codex.CodexAuthBundle, error)
	CreateTokenStorage(bundle *codex.CodexAuthBundle) *codex.CodexTokenStorage
}

type antigravityOAuthService interface {
	BuildAuthURL(state, redirectURI string) string
	ExchangeCodeForTokens(ctx context.Context, code, redirectURI string) (*antigravity.TokenResponse, error)
	FetchUserInfo(ctx context.Context, accessToken string) (string, error)
	FetchProjectID(ctx context.Context, accessToken string) (string, error)
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

	sessionProxyURL := oauthSessionProxyURLFromContext(c)
	cfg := h.oauthConfigWithProxy(sessionProxyURL)
	// Initialize Claude auth service
	anthropicAuth := claude.NewClaudeAuthWithProxyURL(cfg, sessionProxyURL)

	// Generate authorization URL (then override redirect_uri to reuse server port)
	authURL, state, err := anthropicAuth.GenerateAuthURL(state, pkceCodes)
	if err != nil {
		log.Errorf("Failed to generate authorization URL: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate authorization url"})
		return
	}

	RegisterOAuthSessionWithProxy(state, "anthropic", sessionProxyURL)

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
		waitFile := filepath.Join(cfg.AuthDir, fmt.Sprintf(".oauth-anthropic-%s.oauth", state))
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
			log.Errorf("Failed to exchange authorization code for tokens: %s", redactSessionProxyInString(authErr.Error(), sessionProxyURL))
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
		fileName := claude.CredentialFileName(tokenStorage.Email, tokenStorage.OrganizationUUID, tokenStorage.AccountUUID)
		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "claude",
			FileName: fileName,
			Storage:  tokenStorage,
			Metadata: metadata,
		}
		applySessionProxyToAuthRecord(record, sessionProxyURL)
		if errGuard := claimOAuthSessionCommit(state, "anthropic"); errGuard != nil {
			return
		}
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Errorf("Failed to save authentication tokens: %v", errSave)
			FailOAuthSessionCommit(state, "Failed to save authentication tokens")
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

	sessionProxyURL := oauthSessionProxyURLFromContext(c)
	cfg := h.oauthConfigWithProxy(sessionProxyURL)
	// Initialize Codex auth service
	openaiAuth := newCodexOAuthService(cfg)

	// Generate authorization URL
	authURL, err := openaiAuth.GenerateAuthURL(state, pkceCodes)
	if err != nil {
		log.Errorf("Failed to generate authorization URL: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate authorization url"})
		return
	}

	RegisterOAuthSessionWithProxy(state, "codex", sessionProxyURL)
	sessionTarget, hasSessionTarget := oauthSessionTargetFromContext(c)
	if hasSessionTarget {
		SetOAuthSessionTarget(state, sessionTarget)
	}

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
		waitFile := filepath.Join(cfg.AuthDir, fmt.Sprintf(".oauth-codex-%s.oauth", state))
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
			SetOAuthSessionError(state, redactSessionProxyInString(oauthSessionErrorWithCause("Failed to exchange authorization code for tokens", errExchange), sessionProxyURL))
			log.Errorf("Failed to exchange authorization code for tokens: %s", redactSessionProxyInString(authErr.Error(), sessionProxyURL))
			return
		}

		// Extract additional info for filename generation
		claims, _ := codex.ParseJWTToken(bundle.TokenData.IDToken)
		planType := ""
		if bundle != nil && strings.TrimSpace(bundle.TokenData.PlanType) != "" {
			planType = strings.TrimSpace(bundle.TokenData.PlanType)
		}
		hashAccountID := ""
		if claims != nil {
			if pt := strings.TrimSpace(claims.CodexAuthInfo.ChatgptPlanType); pt != "" {
				planType = pt
			}
			if accountID := claims.GetAccountID(); accountID != "" {
				digest := sha256.Sum256([]byte(accountID))
				hashAccountID = hex.EncodeToString(digest[:])[:8]
			}
		}
		if planType == "" {
			planType = codex.DefaultPlanType
		}

		// Create token storage and persist
		tokenStorage := openaiAuth.CreateTokenStorage(bundle)
		tokenStorage.PlanType = planType
		fileName := codex.CredentialFileName(tokenStorage.Email, planType, hashAccountID, true)
		if target, okTarget := GetOAuthSessionTarget(state); okTarget && target.AuthID != "" {
			if !matchesOAuthRecoveryTarget(target, tokenStorage.Email, tokenStorage.AccountID, fileName) {
				SetOAuthSessionError(state, "Authenticated account does not match target credential")
				log.Warnf("Codex OAuth recovery rejected for %s: authenticated account does not match target credential", target.AuthID)
				return
			}
			if target.FileName != "" {
				fileName = target.FileName
			}
		}
		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "codex",
			FileName: fileName,
			Storage:  tokenStorage,
			Metadata: map[string]any{
				"email":      tokenStorage.Email,
				"account_id": tokenStorage.AccountID,
				"plan_type":  planType,
			},
			Attributes: map[string]string{
				"plan_type": planType,
			},
		}
		target, hasTarget := GetOAuthSessionTarget(state)
		if hasTarget && target.AuthID != "" {
			record.ID = target.AuthID
		}
		var hadPrevAuth bool
		var prevVersion uint64
		var prevEpoch uint64
		if h.authManager != nil {
			if existing, okExisting := h.authManager.GetByID(record.ID); okExisting && existing != nil {
				hadPrevAuth = true
				prevVersion = existing.CredentialVersion
				prevEpoch = existing.RegistrationEpoch
			}
		}
		applySessionProxyToAuthRecord(record, sessionProxyURL)
		if errGuard := claimOAuthSessionCommit(state, "codex"); errGuard != nil {
			return
		}
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil && strings.TrimSpace(savedPath) == "" {
			FailOAuthSessionCommit(state, "Failed to save authentication tokens")
			log.Errorf("Failed to save authentication tokens: %v", errSave)
			return
		}
		savedGrantHash := oauthGrantFingerprint(tokenStorage.AccessToken, tokenStorage.RefreshToken, tokenStorage.Email, tokenStorage.AccountID)
		var adopted bool
		var adoptedVersion uint64
		var adoptedEpoch uint64
		var adoptErr string
		if errSave != nil {
			adoptErr = "Credential saved to storage, but runtime adoption failed"
			log.Errorf("Codex credential saved to %s, but runtime adoption failed: %v", savedPath, errSave)
		} else if h.authManager != nil {
			if runtimeAuth, okRuntime := h.authManager.GetByID(record.ID); okRuntime && runtimeAuth != nil {
				if authGrantFingerprint(runtimeAuth) == savedGrantHash {
					adopted = true
					adoptedVersion = runtimeAuth.CredentialVersion
					adoptedEpoch = runtimeAuth.RegistrationEpoch
				} else {
					adoptErr = "Credential saved to storage, but runtime did not adopt the new credential version"
				}
			} else {
				adoptErr = "Credential saved to storage, but was not registered in active runtime"
			}
		}
		var verifyErr string
		if adopted && hasTarget && target.VerifyAfterLogin && h.authManager != nil {
			if _, errVerify := h.authManager.VerifyInference(ctx, record.ID, target.VerifyModel); errVerify != nil {
				verifyErr = coreauth.SanitizeRenewalMessage(errVerify.Error())
			}
		}
		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		if bundle.APIKey != "" {
			fmt.Println("API key obtained and saved")
		}
		fmt.Println("You can now use Codex services through this CLI")
		CompleteOAuthSessionWithOutcome(state, record.ID, savedGrantHash, strings.TrimSpace(savedPath) != "", adopted, hadPrevAuth, prevVersion, prevEpoch, adoptedVersion, adoptedEpoch, adoptErr, verifyErr)
	}()

	respPayload := gin.H{"status": "ok", "url": authURL, "state": state}
	if hasSessionTarget {
		respPayload["target_auth_id"] = sessionTarget.AuthID
	}
	c.JSON(200, respPayload)
}

func matchesOAuthRecoveryTarget(target oauthRecoveryTarget, email, accountID, candidateFileName string) bool {
	targetAccountID := strings.TrimSpace(target.AccountID)
	targetEmail := strings.TrimSpace(target.Email)
	gotAccountID := strings.TrimSpace(accountID)
	gotEmail := strings.TrimSpace(email)

	if targetAccountID != "" {
		if gotAccountID == "" || !strings.EqualFold(gotAccountID, targetAccountID) {
			return false
		}
	}
	if targetEmail != "" {
		if gotEmail == "" || !strings.EqualFold(gotEmail, targetEmail) {
			return false
		}
	}
	if targetAccountID == "" && targetEmail == "" {
		if gotAccountID == "" && gotEmail == "" {
			return false
		}
		if target.FileName != "" && candidateFileName != target.FileName && candidateFileName != target.AuthID {
			return false
		}
	}
	return true
}

func oauthGrantFingerprint(accessToken, refreshToken, email, accountID string) string {
	return coreauth.OAuthGrantFingerprint(accessToken, refreshToken, email, accountID)
}

func authGrantFingerprint(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	var accessToken, refreshToken, accountID string
	email := authEmail(auth)
	if storage, ok := auth.Storage.(*codex.CodexTokenStorage); ok && storage != nil {
		accessToken = strings.TrimSpace(storage.AccessToken)
		refreshToken = strings.TrimSpace(storage.RefreshToken)
		accountID = strings.TrimSpace(storage.AccountID)
	}
	if auth.Metadata != nil {
		if accessToken == "" {
			accessToken, _ = auth.Metadata["access_token"].(string)
		}
		if refreshToken == "" {
			refreshToken, _ = auth.Metadata["refresh_token"].(string)
		}
		if accountID == "" {
			accountID, _ = auth.Metadata["account_id"].(string)
		}
	}
	if (accessToken == "" || refreshToken == "" || accountID == "" || email == "") && auth.Storage != nil {
		if rawer, ok := auth.Storage.(interface{ RawJSON() []byte }); ok {
			if raw := rawer.RawJSON(); len(raw) > 0 {
				var rawMap map[string]any
				if json.Unmarshal(raw, &rawMap) == nil {
					if accessToken == "" {
						accessToken, _ = rawMap["access_token"].(string)
					}
					if refreshToken == "" {
						refreshToken, _ = rawMap["refresh_token"].(string)
					}
					if accountID == "" {
						accountID, _ = rawMap["account_id"].(string)
					}
					if email == "" {
						email, _ = rawMap["email"].(string)
					}
				}
			}
		}
	}
	return oauthGrantFingerprint(accessToken, refreshToken, email, accountID)
}

func (h *Handler) RequestAntigravityToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	fmt.Println("Initializing Antigravity authentication...")

	sessionProxyURL := oauthSessionProxyURLFromContext(c)
	cfg := h.oauthConfigWithProxy(sessionProxyURL)
	authSvc := newAntigravityOAuthService(cfg)

	state, errState := misc.GenerateRandomState()
	if errState != nil {
		log.Errorf("Failed to generate state parameter: %v", errState)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate state parameter"})
		return
	}

	redirectURI := fmt.Sprintf("http://localhost:%d/oauth-callback", antigravity.CallbackPort)
	authURL := authSvc.BuildAuthURL(state, redirectURI)

	RegisterOAuthSessionWithProxy(state, "antigravity", sessionProxyURL)

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

		waitFile := filepath.Join(cfg.AuthDir, fmt.Sprintf(".oauth-antigravity-%s.oauth", state))
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
			log.Errorf("Failed to exchange token: %s", redactSessionProxyInString(errToken.Error(), sessionProxyURL))
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
			log.Errorf("Failed to fetch user info: %s", redactSessionProxyInString(errInfo.Error(), sessionProxyURL))
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
				if sessionProxyURL != "" {
					log.Errorf("antigravity: failed to fetch project ID: %s", redactSessionProxyInString(errProject.Error(), sessionProxyURL))
					SetOAuthSessionError(state, "Failed to fetch project ID")
					return
				}
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
		applySessionProxyToAuthRecord(record, sessionProxyURL)
		if errGuard := claimOAuthSessionCommit(state, "antigravity"); errGuard != nil {
			return
		}
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Errorf("Failed to save token to file: %v", errSave)
			FailOAuthSessionCommit(state, "Failed to save token to file")
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

	sessionProxyURL := oauthSessionProxyURLFromContext(c)
	cfg := h.oauthConfigWithProxy(sessionProxyURL)
	state := fmt.Sprintf("xai-%d", time.Now().UnixNano())
	authSvc := xaiauth.NewXAIAuthWithProxyURL(cfg, sessionProxyURL)

	deviceFlow, errStartDeviceFlow := authSvc.StartDeviceFlow(ctx)
	if errStartDeviceFlow != nil {
		log.Errorf("Failed to start xAI device flow: %s", redactSessionProxyInString(errStartDeviceFlow.Error(), sessionProxyURL))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start device authorization flow"})
		return
	}
	authURL := strings.TrimSpace(deviceFlow.VerificationURIComplete)
	if authURL == "" {
		authURL = strings.TrimSpace(deviceFlow.VerificationURI)
	}

	RegisterOAuthSessionWithProxy(state, "xai", sessionProxyURL)

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
			log.Errorf("xAI authentication failed: %s", redactSessionProxyInString(errWaitForAuthorization.Error(), sessionProxyURL))
			SetOAuthSessionError(state, redactSessionProxyInString(oauthSessionErrorWithCause("Authentication failed", errWaitForAuthorization), sessionProxyURL))
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
		applySessionProxyToAuthRecord(record, sessionProxyURL)
		if errGuard := claimOAuthSessionCommit(state, "xai"); errGuard != nil {
			return
		}
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Errorf("Failed to save xAI token to file: %v", errSave)
			FailOAuthSessionCommit(state, "Failed to save token to file")
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

func (h *Handler) RequestMetaToken(c *gin.Context) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	fmt.Println("Initializing Meta authentication...")

	sessionProxyURL := oauthSessionProxyURLFromContext(c)
	cfg := h.oauthConfigWithProxy(sessionProxyURL)
	state := fmt.Sprintf("meta-%d", time.Now().UnixNano())
	authSvc := metaauth.NewMetaAuthWithProxyURL(cfg, sessionProxyURL)

	deviceFlow, errStartDeviceFlow := authSvc.StartDeviceFlow(ctx)
	if errStartDeviceFlow != nil {
		log.Errorf("Failed to start Meta device flow: %s", redactSessionProxyInString(errStartDeviceFlow.Error(), sessionProxyURL))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start device authorization flow"})
		return
	}
	authURL := strings.TrimSpace(deviceFlow.VerificationURIComplete)
	if authURL == "" {
		authURL = strings.TrimSpace(deviceFlow.VerificationURI)
	}

	RegisterOAuthSessionWithProxy(state, "meta", sessionProxyURL)

	go func() {
		pollCtx, cancelPoll := context.WithCancel(ctx)
		defer cancelPoll()
		go watchOAuthSessionCancel(pollCtx, cancelPoll, state, "meta")

		fmt.Println("Waiting for Meta authentication...")
		bundle, errWaitForAuthorization := authSvc.WaitForAuthorization(pollCtx, deviceFlow)
		if errWaitForAuthorization != nil {
			if !IsOAuthSessionPending(state, "meta") {
				return
			}
			log.Errorf("Meta authentication failed: %s", redactSessionProxyInString(errWaitForAuthorization.Error(), sessionProxyURL))
			SetOAuthSessionError(state, redactSessionProxyInString(oauthSessionErrorWithCause("Authentication failed", errWaitForAuthorization), sessionProxyURL))
			return
		}
		if !IsOAuthSessionPending(state, "meta") {
			return
		}

		tokenStorage := authSvc.CreateTokenStorage(bundle)
		if tokenStorage == nil || strings.TrimSpace(tokenStorage.AccessToken) == "" {
			log.Error("Meta token exchange returned empty access token")
			SetOAuthSessionError(state, "Failed to exchange token")
			return
		}

		record := buildMetaAuthRecord(bundle, tokenStorage)
		applySessionProxyToAuthRecord(record, sessionProxyURL)
		if errGuard := claimOAuthSessionCommit(state, "meta"); errGuard != nil {
			return
		}
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Errorf("Failed to save Meta token to file: %v", errSave)
			FailOAuthSessionCommit(state, "Failed to save token to file")
			return
		}

		CompleteOAuthSession(state)
		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		fmt.Println("You can now use Meta services through this CLI")
	}()

	response := gin.H{"status": "ok", "url": authURL, "state": state, "flow": "device"}
	if userCode := strings.TrimSpace(deviceFlow.UserCode); userCode != "" {
		response["user_code"] = userCode
	}
	if deviceFlow.ExpiresIn > 0 {
		response["expires_in"] = deviceFlow.ExpiresIn
	} else {
		response["expires_in"] = int(metaauth.MaxPollDuration / time.Second)
	}
	c.JSON(200, response)
}

func buildMetaAuthRecord(bundle *metaauth.MetaAuthBundle, tokenStorage *metaauth.MetaTokenStorage) *coreauth.Auth {
	fileName := metaauth.CredentialFileName(tokenStorage.Email, tokenStorage.DCAToken)
	label := strings.TrimSpace(tokenStorage.Email)
	if label == "" {
		label = "Meta"
	}

	metadata := map[string]any{
		"type":         "meta",
		"access_token": tokenStorage.AccessToken,
		"token_type":   tokenStorage.TokenType,
		"expires_in":   tokenStorage.ExpiresIn,
		"expired":      tokenStorage.Expired,
		"last_refresh": tokenStorage.LastRefresh,
		"base_url":     tokenStorage.BaseURL,
		"auth_kind":    "oauth",
	}
	if tokenStorage.DCAExpired != "" {
		metadata["dca_expired"] = tokenStorage.DCAExpired
	}
	if tokenStorage.DCAExpiresAt > 0 {
		metadata["dca_expires_at"] = tokenStorage.DCAExpiresAt
	}
	if tokenStorage.APIKey != "" {
		metadata["api_key"] = tokenStorage.APIKey
	}
	if tokenStorage.DCAToken != "" {
		metadata["dca_token"] = tokenStorage.DCAToken
	}
	if tokenStorage.Email != "" {
		metadata["email"] = tokenStorage.Email
	}
	if tokenStorage.Name != "" {
		metadata["name"] = tokenStorage.Name
	}
	if bundle != nil && bundle.MintedKey != nil {
		metadata["subs_tier_name"] = bundle.MintedKey.SubsTierName
		metadata["subs_tier_id"] = bundle.MintedKey.SubsTierID
		metadata["is_subs_active"] = bundle.MintedKey.IsSubsActive
		metadata["has_payment_method"] = bundle.MintedKey.HasPaymentMethod
	}

	attrs := map[string]string{
		"auth_kind": "oauth",
		"base_url":  tokenStorage.BaseURL,
	}
	if tokenStorage.APIKey != "" {
		attrs["api_key"] = tokenStorage.APIKey
	}
	if tokenStorage.DCAToken != "" {
		attrs["dca_token"] = tokenStorage.DCAToken
	}
	if tokenStorage.Email != "" {
		attrs["email"] = tokenStorage.Email
	}

	return &coreauth.Auth{
		ID:         fileName,
		Provider:   "meta",
		FileName:   fileName,
		Label:      label,
		Storage:    tokenStorage,
		Metadata:   metadata,
		Attributes: attrs,
	}
}

func (h *Handler) RequestKimiToken(c *gin.Context) {
	domain := kimi.KimiDefaultDomain
	if qDomain := strings.TrimSpace(c.Query("domain")); qDomain != "" {
		domain = qDomain
	} else if qChan := strings.TrimSpace(c.Query("channel")); qChan != "" {
		domain = qChan
	} else if bodyDomain := oauthSessionDomainFromContext(c); bodyDomain != "" {
		domain = bodyDomain
	}
	h.requestKimiTokenWithDomain(c, domain)
}

func (h *Handler) RequestKimiAIToken(c *gin.Context) {
	h.requestKimiTokenWithDomain(c, kimi.KimiAIDomain)
}

func (h *Handler) requestKimiTokenWithDomain(c *gin.Context, domain string) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	isAI := kimi.IsKimiAIDomain(domain)
	displayName := "Kimi"
	providerName := "kimi"
	filePrefix := "kimi"
	statePrefix := "kmi"
	baseURL := kimi.KimiAPIBaseURL
	if isAI {
		displayName = "Kimi.ai"
		providerName = "kimi-ai"
		filePrefix = "kimi-ai"
		statePrefix = "kmi-ai"
		baseURL = kimi.KimiAIAPIBaseURL
	}

	fmt.Printf("Initializing %s authentication...\n", displayName)

	sessionProxyURL := oauthSessionProxyURLFromContext(c)
	cfg := h.oauthConfigWithProxy(sessionProxyURL)
	state := fmt.Sprintf("%s-%d", statePrefix, time.Now().UnixNano())
	// Initialize Kimi auth service
	kimiAuth := kimi.NewKimiAuthWithDomain(cfg, domain)

	// Generate authorization URL
	deviceFlow, errStartDeviceFlow := kimiAuth.StartDeviceFlow(ctx)
	if errStartDeviceFlow != nil {
		log.Errorf("Failed to generate authorization URL: %s", redactSessionProxyInString(errStartDeviceFlow.Error(), sessionProxyURL))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate authorization url"})
		return
	}
	authURL := deviceFlow.VerificationURIComplete
	if authURL == "" {
		authURL = deviceFlow.VerificationURI
	}

	RegisterOAuthSessionWithProxy(state, providerName, sessionProxyURL)

	go func() {
		pollCtx, cancelPoll := context.WithCancel(ctx)
		defer cancelPoll()
		go watchOAuthSessionCancel(pollCtx, cancelPoll, state, providerName)

		fmt.Printf("Waiting for %s authentication...\n", displayName)
		authBundle, errWaitForAuthorization := kimiAuth.WaitForAuthorization(pollCtx, deviceFlow)
		if errWaitForAuthorization != nil {
			if !IsOAuthSessionPending(state, providerName) {
				return
			}
			SetOAuthSessionError(state, redactSessionProxyInString(oauthSessionErrorWithCause("Authentication failed", errWaitForAuthorization), sessionProxyURL))
			fmt.Printf("%s authentication failed: %s\n", displayName, redactSessionProxyInString(errWaitForAuthorization.Error(), sessionProxyURL))
			return
		}
		if !IsOAuthSessionPending(state, providerName) {
			return
		}

		// Create token storage
		tokenStorage := kimiAuth.CreateTokenStorage(authBundle)
		if isAI {
			tokenStorage.Type = providerName
		}

		metadata := map[string]any{
			"type":          providerName,
			"access_token":  authBundle.TokenData.AccessToken,
			"refresh_token": authBundle.TokenData.RefreshToken,
			"token_type":    authBundle.TokenData.TokenType,
			"scope":         authBundle.TokenData.Scope,
			"timestamp":     time.Now().UnixMilli(),
			"domain":        domain,
			"base_url":      baseURL,
		}
		if authBundle.TokenData.ExpiresAt > 0 {
			expired := time.Unix(authBundle.TokenData.ExpiresAt, 0).UTC().Format(time.RFC3339)
			metadata["expired"] = expired
		}
		if strings.TrimSpace(authBundle.DeviceID) != "" {
			metadata["device_id"] = strings.TrimSpace(authBundle.DeviceID)
		}

		fileName := fmt.Sprintf("%s-%d.json", filePrefix, time.Now().UnixMilli())
		record := &coreauth.Auth{
			ID:       fileName,
			Provider: providerName,
			FileName: fileName,
			Label:    fmt.Sprintf("%s User", displayName),
			Storage:  tokenStorage,
			Metadata: metadata,
			Attributes: map[string]string{
				"base_url": baseURL,
				"domain":   domain,
			},
		}
		applySessionProxyToAuthRecord(record, sessionProxyURL)
		if errGuard := claimOAuthSessionCommit(state, providerName); errGuard != nil {
			return
		}
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Errorf("Failed to save authentication tokens: %v", errSave)
			FailOAuthSessionCommit(state, "Failed to save authentication tokens")
			return
		}

		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		fmt.Printf("You can now use %s services through this CLI\n", displayName)
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

	session, ok := oauthSessions.Get(state)
	if !ok {
		c.JSON(http.StatusOK, gin.H{"status": "error", "error": "unknown or expired state"})
		return
	}
	if session.Completed {
		resp := gin.H{"status": "ok"}
		if session.AuthID != "" {
			resp["auth_id"] = session.AuthID
			resp["saved"] = session.Saved
			var adoptedAuth *coreauth.Auth
			var adoptErr string
			if h != nil && h.authManager != nil {
				if candidate, okAuth := h.authManager.GetByID(session.AuthID); okAuth && candidate != nil {
					if session.SavedGrantHash != "" {
						if authGrantFingerprint(candidate) == session.SavedGrantHash ||
							candidate.AdoptedGrantHash == session.SavedGrantHash {
							adoptedAuth = candidate
							if !session.Adopted {
								oauthSessions.MarkAdopted(state, candidate.CredentialVersion, candidate.RegistrationEpoch)
								session.Adopted = true
								session.AdoptedVersion = candidate.CredentialVersion
								session.AdoptedEpoch = candidate.RegistrationEpoch
								session.AdoptError = ""
							}
						} else if session.Adopted {
							adoptErr = "Credential was replaced in active runtime after login"
						}
					} else if session.Adopted {
						if (session.AdoptedVersion == 0 && session.AdoptedEpoch == 0) ||
							(candidate.RegistrationEpoch == session.AdoptedEpoch && candidate.CredentialVersion == session.AdoptedVersion) {
							adoptedAuth = candidate
						}
					}
				}
			}
			if adoptErr == "" {
				adoptErr = session.AdoptError
			}
			if adoptedAuth != nil {
				nowUTC := time.Now().UTC()
				resp["adopted"] = true
				resp["recovery_stage"] = adoptedAuth.RecoveryStageAt(nowUTC)
				resp["renewal_state"] = adoptedAuth.RenewalStateAt(nowUTC)
				resp["requires_sign_in"] = adoptedAuth.RequiresSignIn
				resp["inference_verified"] = adoptedAuth.InferenceVerified
				resp["renewal_verified"] = adoptedAuth.RenewalVerified
				resp["quota_blocked"] = adoptedAuth.RecoveryStageAt(nowUTC) == coreauth.RecoveryStageQuotaBlocked
			} else if session.Saved {
				resp["adopted"] = false
				resp["recovery_stage"] = coreauth.RecoveryStageCredentialSaved
				if adoptErr != "" {
					resp["adoption_error"] = adoptErr
				}
			}
			if session.VerifyError != "" {
				resp["verification_error"] = session.VerifyError
			}
		}
		c.JSON(http.StatusOK, resp)
		return
	}
	if session.Status != "" {
		c.JSON(http.StatusOK, gin.H{"status": "error", "error": session.Status})
		return
	}
	if session.Committing || session.SIWCCommitting {
		c.JSON(http.StatusOK, gin.H{"status": "wait"})
		return
	}
	provider := session.Provider
	isPlugin := session.Source == oauthSessionSourcePlugin
	metadata := session.Metadata
	sessionProxyURL := strings.TrimSpace(session.ProxyURL)
	h.mu.Lock()
	host := h.pluginHost
	h.mu.Unlock()
	if isPlugin && host != nil && host.HasAuthProvider(provider) {
		ctx := PopulateAuthContext(context.Background(), c)
		if sessionProxyURL != "" {
			ctx = pluginhost.WithOAuthProxyURL(ctx, sessionProxyURL)
		}
		resp, handled, errPoll := host.PollLogin(ctx, provider, state, metadata)
		if handled {
			if errPoll != nil {
				message := redactSessionProxyInString(strings.TrimSpace(errPoll.Error()), sessionProxyURL)
				writePluginPollFailureStatus(c, state, message)
				return
			}
			switch resp.Status {
			case "", pluginapi.AuthLoginStatusPending:
				writeNonCommitPluginPollStatus(c, state, true)
				return
			case pluginapi.AuthLoginStatusError:
				message := redactSessionProxyInString(strings.TrimSpace(resp.Message), sessionProxyURL)
				writePluginPollFailureStatus(c, state, message)
				return
			case pluginapi.AuthLoginStatusSuccess:
				if errGuard := claimOAuthSessionCommit(state, provider); errGuard != nil {
					writeNonCommitPluginPollStatus(c, state, false)
					return
				}
				records := pluginLoginPollAuthsWithProvider(host, provider, resp)
				if len(records) == 0 {
					FailOAuthSessionCommit(state, "Authentication failed")
					c.JSON(http.StatusOK, gin.H{"status": "error", "error": "Authentication failed"})
					return
				}
				if target, okTarget := GetOAuthSessionTarget(state); okTarget && target.AuthID != "" {
					if !applyPluginOAuthRecoveryTarget(target, provider, records) {
						FailOAuthSessionCommit(state, "Authenticated account does not match target credential")
						c.JSON(http.StatusOK, gin.H{"status": "error", "error": "Authenticated account does not match target credential"})
						return
					}
				}
				for _, record := range records {
					applySessionProxyToAuthRecord(record, sessionProxyURL)
				}
				if errSave := h.savePluginLoginRecords(ctx, records); errSave != nil {
					log.WithError(errSave).WithField("provider", provider).Error("failed to save plugin auth tokens")
					FailOAuthSessionCommit(state, "Failed to save authentication tokens")
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

func writePluginPollFailureStatus(c *gin.Context, state, message string) {
	errStatus, completed, committing, ok := oauthSessions.FailPendingPoll(state, message)
	if !ok {
		c.JSON(http.StatusOK, gin.H{"status": "error", "error": "unknown or expired state"})
		return
	}
	if completed {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
		return
	}
	if committing {
		c.JSON(http.StatusOK, gin.H{"status": "wait"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "error", "error": errStatus})
}

func writeNonCommitPluginPollStatus(c *gin.Context, state string, allowPendingWait bool) {
	session, ok := oauthSessions.Get(state)
	if !ok {
		c.JSON(http.StatusOK, gin.H{"status": "error", "error": "unknown or expired state"})
		return
	}
	if session.Completed {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
		return
	}
	if session.Status != "" {
		c.JSON(http.StatusOK, gin.H{"status": "error", "error": session.Status})
		return
	}
	if session.Committing || session.SIWCCommitting || allowPendingWait {
		c.JSON(http.StatusOK, gin.H{"status": "wait"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "error", "error": "unknown or expired state"})
}

func pluginLoginPollAuths(host *pluginhost.Host, resp pluginapi.AuthLoginPollResponse) []*coreauth.Auth {
	return pluginLoginPollAuthsWithProvider(host, "", resp)
}

func pluginLoginPollAuthsWithProvider(host *pluginhost.Host, provider string, resp pluginapi.AuthLoginPollResponse) []*coreauth.Auth {
	if host == nil {
		return nil
	}
	authDatas := resp.Auths
	if len(authDatas) == 0 {
		authDatas = []pluginapi.AuthData{resp.Auth}
	}
	records := make([]*coreauth.Auth, 0, len(authDatas))
	trimmedSessionProvider := strings.ToLower(strings.TrimSpace(provider))
	for _, authData := range authDatas {
		if strings.TrimSpace(authData.Provider) == "" && trimmedSessionProvider != "" {
			authData.Provider = trimmedSessionProvider
		}
		effectivePortal := trimmedSessionProvider
		for _, key := range []string{"oauth_provider", "portal"} {
			if authData.Attributes != nil {
				if v := strings.ToLower(strings.TrimSpace(authData.Attributes[key])); v != "" {
					effectivePortal = v
					break
				}
			}
			if authData.Metadata != nil {
				if raw, ok := authData.Metadata[key].(string); ok {
					if v := strings.ToLower(strings.TrimSpace(raw)); v != "" {
						effectivePortal = v
						break
					}
				}
			}
		}
		if effectivePortal != "" && !strings.EqualFold(strings.TrimSpace(authData.Provider), effectivePortal) {
			if authData.Metadata == nil {
				authData.Metadata = make(map[string]any)
			}
			if _, exists := authData.Metadata["oauth_provider"]; !exists {
				authData.Metadata["oauth_provider"] = effectivePortal
			}
			if authData.Attributes == nil {
				authData.Attributes = make(map[string]string)
			}
			if strings.TrimSpace(authData.Attributes["oauth_provider"]) == "" {
				authData.Attributes["oauth_provider"] = effectivePortal
			}
		}
		record := host.AuthDataToCoreAuth(authData, "", "")
		if record == nil {
			return nil
		}
		records = append(records, record)
	}
	return records
}

func authIdentityEmailAndAccountID(auth *coreauth.Auth) (email string, accountID string) {
	if auth == nil {
		return "", ""
	}
	if auth.Metadata != nil {
		accountID, _ = auth.Metadata["account_id"].(string)
	}
	if strings.TrimSpace(accountID) == "" && auth.Attributes != nil {
		accountID = auth.Attributes["account_id"]
	}
	accountID = strings.TrimSpace(accountID)
	email = strings.TrimSpace(authEmail(auth))
	if (accountID == "" || email == "") && auth.Storage != nil {
		if rawer, ok := auth.Storage.(interface{ RawJSON() []byte }); ok {
			if raw := rawer.RawJSON(); len(raw) > 0 {
				var rawMap map[string]any
				if json.Unmarshal(raw, &rawMap) == nil {
					if accountID == "" {
						rawAcc, _ := rawMap["account_id"].(string)
						accountID = strings.TrimSpace(rawAcc)
					}
					if email == "" {
						rawEmail, _ := rawMap["email"].(string)
						email = strings.TrimSpace(rawEmail)
					}
				}
			}
		}
	}
	return email, accountID
}

func applyPluginOAuthRecoveryTarget(target oauthRecoveryTarget, provider string, records []*coreauth.Auth) bool {
	if len(records) != 1 || records[0] == nil {
		return false
	}
	record := records[0]
	gotEmail, gotAccountID := authIdentityEmailAndAccountID(record)
	candidateFileName := firstNonEmpty(strings.TrimSpace(record.FileName), strings.TrimSpace(record.ID))

	if !matchesOAuthRecoveryTarget(target, gotEmail, gotAccountID, candidateFileName) {
		return false
	}

	record.ID = target.AuthID
	targetFileName := strings.TrimSpace(target.FileName)
	if targetFileName == "" {
		targetFileName = target.AuthID
	}
	targetPath := strings.TrimSpace(target.Path)
	if targetPath == "" && strings.ContainsAny(target.AuthID, "/\\") {
		targetFileName = target.AuthID
	}
	record.FileName = targetFileName
	if record.Attributes == nil {
		record.Attributes = make(map[string]string)
	}
	if targetPath != "" {
		record.Attributes[coreauth.AttributePath] = targetPath
		record.Attributes[coreauth.AttributeSource] = targetPath
	} else {
		delete(record.Attributes, coreauth.AttributePath)
		record.Attributes[coreauth.AttributeSource] = targetFileName
	}
	if trimmedProvider := strings.ToLower(strings.TrimSpace(provider)); trimmedProvider != "" {
		if record.Metadata == nil {
			record.Metadata = make(map[string]any)
		}
		record.Metadata["oauth_provider"] = trimmedProvider
		record.Attributes["oauth_provider"] = trimmedProvider
		if setter, ok := record.Storage.(interface{ SetMetadata(map[string]any) }); ok {
			setter.SetMetadata(record.Metadata)
		}
	}
	return true
}

type savedPluginRecordRollback struct {
	savedPath          string
	diskPath           string
	existedOnDisk      bool
	prevDiskBytes      []byte
	savedDiskBytes     []byte
	hadRuntimeAuth     bool
	hadRecordIDAuth    bool
	hadFileDerivedAuth bool
	prevAuth           *coreauth.Auth
	record             *coreauth.Auth
}

func (h *Handler) candidatePluginRecordPath(record *coreauth.Auth) string {
	if h == nil || record == nil {
		return ""
	}
	if record.Attributes != nil {
		if p := strings.TrimSpace(record.Attributes[coreauth.AttributePath]); p != "" {
			return p
		}
	}
	name := firstNonEmpty(strings.TrimSpace(record.FileName), strings.TrimSpace(record.ID))
	if name == "" {
		return ""
	}
	if filepath.IsAbs(name) {
		return name
	}
	if cfg := h.configSnapshot(); cfg != nil && strings.TrimSpace(cfg.AuthDir) != "" {
		return filepath.Join(cfg.AuthDir, name)
	}
	return ""
}

func (h *Handler) savePluginLoginRecords(ctx context.Context, records []*coreauth.Auth) error {
	if h != nil {
		h.pluginLoginSaveMu.Lock()
		defer h.pluginLoginSaveMu.Unlock()
	}
	rollbacks := make([]savedPluginRecordRollback, 0, len(records))
	for _, record := range records {
		var rb savedPluginRecordRollback
		candidatePath := h.candidatePluginRecordPath(record)
		if candidatePath != "" {
			prevBytes, errRead := os.ReadFile(candidatePath)
			if errRead == nil {
				rb.existedOnDisk = true
				rb.prevDiskBytes = prevBytes
			} else if !os.IsNotExist(errRead) {
				h.rollbackSavedPluginLoginRecords(ctx, rollbacks)
				return fmt.Errorf("read existing auth file %s: %w", candidatePath, errRead)
			}
		}
		if h != nil && h.authManager != nil && record != nil {
			if recID := strings.TrimSpace(record.ID); recID != "" {
				if _, ok := h.authManager.GetByID(recID); ok {
					rb.hadRecordIDAuth = true
				}
			}
			if fileID := h.authIDForPath(candidatePath); fileID != "" {
				if _, ok := h.authManager.GetByID(fileID); ok {
					rb.hadFileDerivedAuth = true
				}
			}
			for _, candidateID := range []string{strings.TrimSpace(record.ID), h.authIDForPath(candidatePath)} {
				if candidateID == "" {
					continue
				}
				if existing, ok := h.authManager.GetByID(candidateID); ok && existing != nil {
					rb.hadRuntimeAuth = true
					rb.prevAuth = existing.Clone()
					break
				}
			}
		}
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		rb.savedPath = strings.TrimSpace(savedPath)
		rb.diskPath = firstNonEmpty(rb.savedPath, candidatePath)
		rb.record = record
		if record != nil && record.Storage != nil {
			if rawer, ok := record.Storage.(interface{ RawJSON() []byte }); ok {
				rb.savedDiskBytes = rawer.RawJSON()
			}
		}
		if rb.savedPath != "" || rb.existedOnDisk || rb.hadRuntimeAuth {
			rollbacks = append(rollbacks, rb)
		}
		if errSave != nil {
			h.rollbackSavedPluginLoginRecords(ctx, rollbacks)
			return errSave
		}
	}
	return nil
}

func jsonPayloadsEqualOrBytesEqual(left, right []byte) bool {
	if strings.TrimSpace(string(left)) == strings.TrimSpace(string(right)) {
		return true
	}
	var leftVal, rightVal any
	if json.Unmarshal(left, &leftVal) != nil || json.Unmarshal(right, &rightVal) != nil {
		return false
	}
	return reflect.DeepEqual(leftVal, rightVal)
}

var errPluginRollbackSuperseded = errors.New("plugin login rollback superseded")

func (h *Handler) restoreOverwrittenPluginRecord(ctx context.Context, rb savedPluginRecordRollback, diskPath string) {
	if diskPath != "" && len(rb.prevDiskBytes) > 0 {
		if errWrite := os.WriteFile(diskPath, rb.prevDiskBytes, 0o600); errWrite != nil {
			log.WithError(errWrite).WithField("path", diskPath).Warn("failed to restore overwritten plugin auth token")
		}
	}
	if h == nil || (rb.prevAuth == nil && len(rb.prevDiskBytes) == 0) {
		return
	}
	store := h.tokenStoreWithBaseDir()
	if store == nil {
		return
	}
	if _, isFileStore := store.(*sdkAuth.FileTokenStore); isFileStore && diskPath != "" && len(rb.prevDiskBytes) > 0 {
		return
	}
	var diskMeta map[string]any
	if len(rb.prevDiskBytes) > 0 {
		_ = json.Unmarshal(rb.prevDiskBytes, &diskMeta)
	}
	var restoreRecord *coreauth.Auth
	if rb.prevAuth != nil {
		restoreRecord = rb.prevAuth.Clone()
		if len(diskMeta) > 0 {
			restoreRecord.Metadata = diskMeta
		}
	} else {
		provider := ""
		if diskMeta != nil {
			provider, _ = diskMeta["type"].(string)
		}
		if provider == "" && rb.record != nil {
			provider = rb.record.Provider
		}
		fileName := filepath.Base(diskPath)
		id := h.authIDForPath(diskPath)
		if rb.record != nil {
			if fileName == "" || fileName == "." {
				fileName = rb.record.FileName
			}
			if id == "" {
				id = rb.record.ID
			}
		}
		restoreRecord = &coreauth.Auth{
			ID:       firstNonEmpty(id, fileName),
			FileName: fileName,
			Provider: provider,
			Metadata: diskMeta,
			Attributes: map[string]string{
				coreauth.AttributePath: diskPath,
			},
		}
	}
	if len(diskMeta) > 0 {
		if disabled, ok := diskMeta["disabled"].(bool); ok {
			restoreRecord.Disabled = disabled
			if disabled {
				restoreRecord.Status = coreauth.StatusDisabled
			} else if restoreRecord.Status == coreauth.StatusDisabled {
				restoreRecord.Status = coreauth.StatusActive
			}
		}
	}
	if len(rb.prevDiskBytes) > 0 && h.pluginHost != nil {
		if restored := h.pluginHost.AuthDataToCoreAuth(pluginapi.AuthData{
			Provider:    restoreRecord.Provider,
			ID:          restoreRecord.ID,
			FileName:    restoreRecord.FileName,
			Label:       restoreRecord.Label,
			Prefix:      restoreRecord.Prefix,
			ProxyURL:    restoreRecord.ProxyURL,
			Disabled:    restoreRecord.Disabled,
			StorageJSON: rb.prevDiskBytes,
			Metadata:    restoreRecord.Metadata,
			Attributes:  restoreRecord.Attributes,
		}, diskPath, restoreRecord.FileName); restored != nil {
			restoreRecord.Storage = restored.Storage
		}
	}
	if _, errRestore := store.Save(ctx, restoreRecord); errRestore != nil {
		log.WithError(errRestore).WithField("path", diskPath).Warn("failed to restore overwritten plugin auth token in store")
	}
}

func (h *Handler) rollbackSavedPluginLoginRecords(ctx context.Context, rollbacks []savedPluginRecordRollback) {
	for i := len(rollbacks) - 1; i >= 0; i-- {
		rb := rollbacks[i]
		diskPath := strings.TrimSpace(rb.diskPath)
		savedPath := strings.TrimSpace(rb.savedPath)
		if rb.hadRuntimeAuth && rb.prevAuth != nil && h != nil && h.authManager != nil {
			_, _, errModify := h.authManager.Modify(coreauth.WithSkipPersist(ctx), rb.prevAuth.ID, 0, func(current *coreauth.Auth) error {
				currFP := authGrantFingerprint(current)
				if currFP != "" && currFP != authGrantFingerprint(rb.record) && currFP != authGrantFingerprint(rb.prevAuth) {
					return errPluginRollbackSuperseded
				}
				if rb.existedOnDisk && diskPath != "" {
					currentBytes, errRead := os.ReadFile(diskPath)
					if errRead != nil {
						return errPluginRollbackSuperseded
					}
					matchesSaved := len(rb.savedDiskBytes) == 0 || jsonPayloadsEqualOrBytesEqual(currentBytes, rb.savedDiskBytes)
					if !matchesSaved && !jsonPayloadsEqualOrBytesEqual(currentBytes, rb.prevDiskBytes) {
						return errPluginRollbackSuperseded
					}
					if matchesSaved {
						h.restoreOverwrittenPluginRecord(ctx, rb, diskPath)
					}
				} else if savedPath != "" {
					if diskPath != "" {
						if currentBytes, errRead := os.ReadFile(diskPath); errRead == nil && len(rb.savedDiskBytes) > 0 && !jsonPayloadsEqualOrBytesEqual(currentBytes, rb.savedDiskBytes) {
							return errPluginRollbackSuperseded
						}
					}
					h.restoreOverwrittenPluginRecord(ctx, rb, diskPath)
				}
				*current = *rb.prevAuth.Clone()
				return nil
			})
			if !errors.Is(errModify, errPluginRollbackSuperseded) {
				removeNewID := func(extraID string) {
					if extraID == "" || extraID == rb.prevAuth.ID {
						return
					}
					_ = h.authManager.RemoveIf(ctx, extraID, func(current *coreauth.Auth) bool {
						if current == nil {
							return false
						}
						currFP := authGrantFingerprint(current)
						return currFP == "" || currFP == authGrantFingerprint(rb.record)
					})
				}
				if !rb.hadFileDerivedAuth {
					removeNewID(h.authIDForPath(savedPath))
				}
				if !rb.hadRecordIDAuth && rb.record != nil {
					removeNewID(strings.TrimSpace(rb.record.ID))
				}
			}
			continue
		}

		if !rb.hadRuntimeAuth && h != nil && h.authManager != nil && rb.record != nil {
			primaryID := firstNonEmpty(h.authIDForPath(savedPath), strings.TrimSpace(rb.record.ID))
			if primaryID != "" {
				superseded := false
				diskHandled := false
				removeMatching := func(current *coreauth.Auth) bool {
					if current != nil {
						currFP := authGrantFingerprint(current)
						if currFP != "" && currFP != authGrantFingerprint(rb.record) {
							superseded = true
							return false
						}
					}
					if !diskHandled {
						if rb.existedOnDisk && diskPath != "" {
							if currentBytes, errRead := os.ReadFile(diskPath); errRead == nil {
								matchesSaved := len(rb.savedDiskBytes) == 0 || jsonPayloadsEqualOrBytesEqual(currentBytes, rb.savedDiskBytes)
								if !matchesSaved && !jsonPayloadsEqualOrBytesEqual(currentBytes, rb.prevDiskBytes) {
									superseded = true
									return false
								}
								if matchesSaved {
									h.restoreOverwrittenPluginRecord(ctx, rb, diskPath)
								}
							}
						} else if savedPath != "" {
							if diskPath != "" {
								if currentBytes, errRead := os.ReadFile(diskPath); errRead == nil && len(rb.savedDiskBytes) > 0 && !jsonPayloadsEqualOrBytesEqual(currentBytes, rb.savedDiskBytes) {
									superseded = true
									return false
								}
							}
							if errDelete := h.deleteTokenRecord(ctx, savedPath); errDelete != nil {
								log.WithError(errDelete).WithField("path", savedPath).Warn("failed to roll back plugin auth token")
							}
						}
						diskHandled = true
					}
					return current != nil
				}
				_ = h.authManager.RemoveIf(ctx, primaryID, removeMatching)
				if !superseded {
					if altID := strings.TrimSpace(rb.record.ID); altID != "" && altID != primaryID {
						_ = h.authManager.RemoveIf(ctx, altID, removeMatching)
					}
				}
				continue
			}
		}

		diskSuperseded := false
		if rb.existedOnDisk && diskPath != "" {
			currentBytes, errRead := os.ReadFile(diskPath)
			if errRead == nil {
				matchesSaved := len(rb.savedDiskBytes) == 0 || jsonPayloadsEqualOrBytesEqual(currentBytes, rb.savedDiskBytes)
				if !matchesSaved {
					if !jsonPayloadsEqualOrBytesEqual(currentBytes, rb.prevDiskBytes) {
						diskSuperseded = true
					}
				} else {
					h.restoreOverwrittenPluginRecord(ctx, rb, diskPath)
				}
			}
		} else if savedPath != "" {
			if diskPath != "" {
				if currentBytes, errRead := os.ReadFile(diskPath); errRead == nil && len(rb.savedDiskBytes) > 0 && !jsonPayloadsEqualOrBytesEqual(currentBytes, rb.savedDiskBytes) {
					diskSuperseded = true
				}
			}
			if !diskSuperseded {
				if errDelete := h.deleteTokenRecord(ctx, savedPath); errDelete != nil {
					log.WithError(errDelete).WithField("path", savedPath).Warn("failed to roll back plugin auth token")
				}
			}
		}
		if !diskSuperseded && !rb.hadRuntimeAuth && savedPath != "" {
			h.removeAuthsForPath(ctx, savedPath, savedPath)
		}
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
