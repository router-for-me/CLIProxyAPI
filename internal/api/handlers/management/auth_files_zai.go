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
	zaiauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/zai"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// RequestZAIToken starts the ZCode CLI OAuth flow against Z.AI international.
func (h *Handler) RequestZAIToken(c *gin.Context) {
	h.requestZAIToken(c, zaiauth.ProviderZAI)
}

// RequestBigModelToken starts the ZCode CLI OAuth flow against Zhipu BigModel (China mainland).
func (h *Handler) RequestBigModelToken(c *gin.Context) {
	h.requestZAIToken(c, zaiauth.ProviderBigModel)
}

// requestZAIToken runs the poll-based ZCode CLI OAuth flow for the given identity
// provider. The provider may also be overridden via the "provider" query parameter.
func (h *Handler) requestZAIToken(c *gin.Context, provider string) {
	ctx := context.Background()
	ctx = PopulateAuthContext(ctx, c)

	if p := strings.TrimSpace(c.Query("provider")); p != "" {
		provider = p
	}
	provider = zaiauth.NormalizeProvider(provider)

	fmt.Printf("Initializing Z.AI authentication (provider: %s)...\n", provider)

	state := fmt.Sprintf("zai-%d", time.Now().UnixNano())
	zaiAuth := zaiauth.NewZAIAuth(h.cfg, provider, "", 0)

	init, errInit := zaiAuth.StartFlow(ctx)
	if errInit != nil {
		log.Errorf("Failed to generate authorization URL: %v", errInit)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate authorization url"})
		return
	}

	RegisterOAuthSession(state, "zai")

	go func() {
		fmt.Println("Waiting for authentication...")
		stop := make(chan struct{})
		defer close(stop)
		// Both Z.AI international and BigModel use a loopback callback. When the
		// browser runs on a different host, it cannot reach 127.0.0.1, so also
		// accept the manually pasted callback that /oauth-callback writes to disk.
		go h.watchManualZAICallback(state, zaiAuth, stop)
		ready, errWait := zaiAuth.WaitForAuthorization(ctx, init)
		if errWait != nil {
			SetOAuthSessionError(state, "Authentication failed")
			fmt.Printf("Authentication failed: %v\n", errWait)
			return
		}

		apiKey, baseURL, errMint := zaiAuth.MintAPIKey(ctx, ready)
		if errMint != nil {
			SetOAuthSessionError(state, "Failed to provision API key")
			fmt.Printf("Failed to provision API key: %v\n", errMint)
			return
		}

		tokenStorage := zaiAuth.CreateTokenStorage(ready, apiKey, baseURL)

		metadata := map[string]any{
			"type":         "zai",
			"provider":     provider,
			"access_token": apiKey,
			"base_url":     baseURL,
			"timestamp":    time.Now().UnixMilli(),
		}
		if strings.TrimSpace(ready.ZAIAccessToken) != "" {
			metadata["zai_access_token"] = ready.ZAIAccessToken
		}
		if strings.TrimSpace(ready.Email) != "" {
			metadata["email"] = ready.Email
		}
		if strings.TrimSpace(ready.Name) != "" {
			metadata["name"] = ready.Name
		}

		fileName := zaiauth.CredentialFileName(provider, ready.UserID, ready.Email)
		label := strings.TrimSpace(ready.Email)
		if label == "" {
			label = strings.TrimSpace(ready.Name)
		}
		if label == "" {
			label = fmt.Sprintf("Z.AI (%s)", provider)
		}

		record := &coreauth.Auth{
			ID:       fileName,
			Provider: "zai",
			FileName: fileName,
			Label:    label,
			Storage:  tokenStorage,
			Metadata: metadata,
		}
		savedPath, errSave := h.saveTokenRecord(ctx, record)
		if errSave != nil {
			log.Errorf("Failed to save authentication tokens: %v", errSave)
			SetOAuthSessionError(state, "Failed to save authentication tokens")
			return
		}

		fmt.Printf("Authentication successful! Token saved to %s\n", savedPath)
		fmt.Println("You can now use Z.AI services through this CLI")
		CompleteOAuthSession(state)
	}()

	c.JSON(200, gin.H{"status": "ok", "url": init.AuthorizeURL, "state": state})
}

// watchManualZAICallback polls for the callback file written by the management
// /oauth-callback handler and injects the pasted authorization code into the
// pending BigModel flow, so logins complete when a remote browser cannot reach
// the loopback listener. It stops when the flow finishes (stop is closed).
func (h *Handler) watchManualZAICallback(state string, auth *zaiauth.ZAIAuth, stop <-chan struct{}) {
	path := filepath.Join(h.cfg.AuthDir, fmt.Sprintf(".oauth-zai-%s.oauth", state))
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			data, errRead := os.ReadFile(path)
			if errRead != nil {
				continue
			}
			_ = os.Remove(path)
			var m map[string]string
			_ = json.Unmarshal(data, &m)
			// The file is keyed by this session's state, so it always belongs to
			// this flow. Deliver the code, or fail the flow promptly when the
			// callback carried an OAuth error / no code instead of timing out.
			if code := strings.TrimSpace(m["code"]); code != "" {
				auth.InjectCallback(code)
			} else {
				auth.InjectError(strings.TrimSpace(m["error"]))
			}
			return
		}
	}
}
