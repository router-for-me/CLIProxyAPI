package management

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// generateCustomPKCE creates a S256 PKCE code verifier and challenge pair.
func generateCustomPKCE() (verifier, challenge string, err error) {
	b := make([]byte, 32)
	if _, errRead := io.ReadFull(rand.Reader, b); errRead != nil {
		return "", "", fmt.Errorf("failed to generate random bytes: %w", errRead)
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)
	h := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(h[:])
	return verifier, challenge, nil
}

// customOAuthSession holds in-flight state for a custom OAuth flow.
type customOAuthSession struct {
	Provider     config.CustomOAuthProvider
	State        string
	CodeVerifier string // PKCE code verifier (empty when PKCE disabled)
	RedirectURI  string // redirect_uri sent to the provider
}

var (
	customOAuthSessionsMu sync.Mutex
	customOAuthSessions   = make(map[string]*customOAuthSession)
)

func registerCustomOAuthSession(state string, session *customOAuthSession) {
	customOAuthSessionsMu.Lock()
	defer customOAuthSessionsMu.Unlock()
	customOAuthSessions[state] = session
}

func getCustomOAuthSession(state string) *customOAuthSession {
	customOAuthSessionsMu.Lock()
	defer customOAuthSessionsMu.Unlock()
	return customOAuthSessions[state]
}

func deleteCustomOAuthSession(state string) {
	customOAuthSessionsMu.Lock()
	defer customOAuthSessionsMu.Unlock()
	delete(customOAuthSessions, state)
}

// RequestCustomOAuthToken initiates a custom OAuth2 Authorization Code flow.
// It accepts the provider name as a URL parameter: GET /v0/management/custom-oauth-auth-url/:name
func (h *Handler) RequestCustomOAuthToken(c *gin.Context) {
	providerName := strings.TrimSpace(c.Param("name"))
	if providerName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provider name is required"})
		return
	}

	if h.cfg == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "config not loaded"})
		return
	}

	// Find the matching custom OAuth provider configuration.
	var provider *config.CustomOAuthProvider
	for i := range h.cfg.CustomOAuth {
		p := h.cfg.CustomOAuth[i]
		if strings.EqualFold(strings.TrimSpace(p.Name), providerName) {
			provider = &p
			break
		}
	}
	if provider == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("custom OAuth provider %q not found", providerName)})
		return
	}

	state, errState := misc.GenerateRandomState()
	if errState != nil {
		log.Errorf("Failed to generate state: %v", errState)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate state"})
		return
	}

	// Compute redirect_uri.
	redirectURI := strings.TrimSpace(provider.RedirectURL)
	if redirectURI == "" {
		// Derive redirect URI from the server's own address.
		scheme := "http"
		if h.cfg.TLS.Enable {
			scheme = "https"
		}
		if forwarded := strings.TrimSpace(strings.Split(c.GetHeader("X-Forwarded-Proto"), ",")[0]); forwarded != "" {
			scheme = forwarded
		}
		host := c.Request.Host
		if host == "" {
			host = fmt.Sprintf("127.0.0.1:%d", h.cfg.Port)
		}
		callbackPath := strings.TrimSpace(provider.CallbackPath)
		if callbackPath == "" {
			callbackPath = "/custom/" + providerName + "/callback"
		}
		if !strings.HasPrefix(callbackPath, "/") {
			callbackPath = "/" + callbackPath
		}
		redirectURI = fmt.Sprintf("%s://%s%s", scheme, host, callbackPath)
	}

	// Build authorization URL.
	authURL, err := url.Parse(strings.TrimSpace(provider.AuthURL))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("invalid auth-url: %v", err)})
		return
	}

	var codeVerifier string
	q := authURL.Query()
	q.Set("client_id", strings.TrimSpace(provider.ClientID))
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("state", state)

	if scopes := strings.TrimSpace(provider.Scopes); scopes != "" {
		q.Set("scope", scopes)
	}

	if provider.PKCE {
		verifier, challenge, errPKCE := generateCustomPKCE()
		if errPKCE != nil {
			log.Errorf("Failed to generate PKCE: %v", errPKCE)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate PKCE"})
			return
		}
		codeVerifier = verifier
		q.Set("code_challenge", challenge)
		q.Set("code_challenge_method", "S256")
	}

	authURL.RawQuery = q.Encode()

	// Register the session for the callback handler.
	RegisterOAuthSession(state, providerName)
	registerCustomOAuthSession(state, &customOAuthSession{
		Provider:     *provider,
		State:        state,
		CodeVerifier: codeVerifier,
		RedirectURI:  redirectURI,
	})

	// Start background goroutine to wait for the callback and exchange the code.
	go h.waitForCustomOAuthCallback(state, provider)

	c.JSON(http.StatusOK, gin.H{
		"status": "ok",
		"url":    authURL.String(),
		"state":  state,
	})
}

// waitForCustomOAuthCallback polls for the OAuth callback file and exchanges the code for tokens.
func (h *Handler) waitForCustomOAuthCallback(state string, provider *config.CustomOAuthProvider) {
	defer func() {
		deleteCustomOAuthSession(state)
	}()

	providerName := strings.ToLower(strings.TrimSpace(provider.Name))
	waitFile := filepath.Join(h.cfg.AuthDir, fmt.Sprintf(".oauth-%s-%s.oauth", providerName, state))
	deadline := time.Now().Add(5 * time.Minute)

	for {
		if !IsOAuthSessionPending(state, providerName) {
			return
		}
		if time.Now().After(deadline) {
			log.Error("custom OAuth flow timed out")
			SetOAuthSessionError(state, "OAuth flow timed out")
			return
		}
		data, errRead := os.ReadFile(waitFile)
		if errRead == nil {
			var m map[string]string
			_ = json.Unmarshal(data, &m)
			_ = os.Remove(waitFile)

			if errStr := m["error"]; errStr != "" {
				log.Errorf("Custom OAuth authentication failed: %s", errStr)
				SetOAuthSessionError(state, "Authentication failed")
				return
			}
			code := strings.TrimSpace(m["code"])
			if code == "" {
				log.Error("Custom OAuth callback missing code")
				SetOAuthSessionError(state, "Authentication failed: code not found")
				return
			}

			// Exchange the code for tokens.
			session := getCustomOAuthSession(state)
			redirectURI := ""
			codeVerifier := ""
			if session != nil {
				redirectURI = session.RedirectURI
				codeVerifier = session.CodeVerifier
			}

			ctx := context.Background()
			tokenData, errExchange := h.exchangeCustomOAuthCode(ctx, provider, code, redirectURI, codeVerifier)
			if errExchange != nil {
				log.Errorf("Failed to exchange custom OAuth code: %v", errExchange)
				SetOAuthSessionError(state, fmt.Sprintf("Token exchange failed: %v", errExchange))
				return
			}

			// Save as an auth file.
			fileName := fmt.Sprintf("%s-%d.json", providerName, time.Now().UnixNano())
			record := &coreauth.Auth{
				ID:       fileName,
				Provider: providerName,
				FileName: fileName,
				Metadata: tokenData,
			}
			savedPath, errSave := h.saveTokenRecord(ctx, record)
			if errSave != nil {
				log.Errorf("Failed to save custom OAuth tokens: %v", errSave)
				SetOAuthSessionError(state, "Failed to save tokens")
				return
			}

			log.Infof("Custom OAuth authentication successful! Token saved to %s", savedPath)
			CompleteOAuthSession(state)
			CompleteOAuthSessionsByProvider(providerName)
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// exchangeCustomOAuthCode performs the standard OAuth2 token exchange.
func (h *Handler) exchangeCustomOAuthCode(ctx context.Context, provider *config.CustomOAuthProvider, code, redirectURI, codeVerifier string) (map[string]any, error) {
	tokenURL := strings.TrimSpace(provider.TokenURL)
	if tokenURL == "" {
		return nil, fmt.Errorf("token-url is not configured")
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("client_id", strings.TrimSpace(provider.ClientID))
	if secret := strings.TrimSpace(provider.ClientSecret); secret != "" {
		form.Set("client_secret", secret)
	}
	if redirectURI != "" {
		form.Set("redirect_uri", redirectURI)
	}
	if codeVerifier != "" {
		form.Set("code_verifier", codeVerifier)
	}

	req, errNew := http.NewRequestWithContext(ctx, "POST", tokenURL, strings.NewReader(form.Encode()))
	if errNew != nil {
		return nil, fmt.Errorf("failed to create token request: %w", errNew)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, errDo := http.DefaultClient.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("token request failed: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Warnf("failed to close token response body: %v", errClose)
		}
	}()

	body, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return nil, fmt.Errorf("failed to read token response: %w", errRead)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("token endpoint returned %d: %s", resp.StatusCode, string(body))
	}

	var result map[string]any
	if errJSON := json.Unmarshal(body, &result); errJSON != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", errJSON)
	}

	// Enrich with provider metadata.
	result["type"] = strings.ToLower(strings.TrimSpace(provider.Name))
	if expiresIn, ok := result["expires_in"].(float64); ok {
		result["expired"] = time.Now().Add(time.Duration(expiresIn) * time.Second).UTC().Format(time.RFC3339)
	}
	result["last_refresh"] = time.Now().UTC().Format(time.RFC3339)

	return result, nil
}
