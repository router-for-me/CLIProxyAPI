package codearts

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
)

type sessionStatus string

const (
	sPending   sessionStatus = "pending"
	sWaitingCB sessionStatus = "waiting_callback"
	sPolling   sessionStatus = "polling"
	sSuccess   sessionStatus = "success"
	sFailed    sessionStatus = "failed"
)

type webSession struct {
	stateID    string
	ticketID   string
	identifier string
	status     sessionStatus
	startedAt  time.Time
	error      string
	token      *CodeArtsTokenData
	cancel     context.CancelFunc
}

// OAuthWebHandler handles CodeArts OAuth web login flow.
type OAuthWebHandler struct {
	cfg      *config.Config
	sessions map[string]*webSession
	// Map ticket_id -> stateID for callback lookup
	ticketToState map[string]string
	mu            sync.RWMutex
	auth          *CodeArtsAuth
}

// NewOAuthWebHandler creates a new CodeArts OAuth web handler.
func NewOAuthWebHandler(cfg *config.Config) *OAuthWebHandler {
	return &OAuthWebHandler{
		cfg:           cfg,
		sessions:      make(map[string]*webSession),
		ticketToState: make(map[string]string),
		auth:          NewCodeArtsAuth(nil),
	}
}

// RegisterRoutes registers CodeArts OAuth web routes.
func (h *OAuthWebHandler) RegisterRoutes(router gin.IRouter) {
	oauth := router.Group("/v0/oauth/codearts")
	{
		oauth.GET("", h.handleIndex)
		oauth.GET("/start", h.handleStart)
		oauth.GET("/callback", h.handleCallback)
		oauth.GET("/status", h.handleStatus)
	}
	// Root-level callback: HuaweiCloud redirects to http://127.0.0.1:{port}/callback
	router.GET("/callback", h.handleCallback)
}

func generateState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func generateTicketID() string {
	b := make([]byte, 32)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}

func (h *OAuthWebHandler) handleIndex(c *gin.Context) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, codeArtsLoginPage)
}

func (h *OAuthWebHandler) handleStart(c *gin.Context) {
	stateID, err := generateState()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate state"})
		return
	}

	ticketID := generateTicketID()

	port := h.cfg.Port
	if port == 0 {
		port = 8318
	}

	sess := &webSession{
		stateID:   stateID,
		ticketID:  ticketID,
		status:    sWaitingCB,
		startedAt: time.Now(),
	}

	h.mu.Lock()
	h.sessions[stateID] = sess
	h.ticketToState[ticketID] = stateID
	h.mu.Unlock()

	loginURL := h.auth.AuthorizationURL(ticketID, port)

	log.Infof("CodeArts OAuth: session %s started, login URL: %s", stateID, loginURL)

	if c.GetHeader("Accept") == "application/json" {
		c.JSON(http.StatusOK, gin.H{"url": loginURL, "state": stateID})
		return
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, fmt.Sprintf(codeArtsWaitingPage, loginURL, stateID))
}

// handleCallback receives the callback from HuaweiCloud after user login.
// Python: GET /callback?identifier=XXX&redirect=YYY
// The redirect URL contains ticket_id which we use to match the correct session.
func (h *OAuthWebHandler) handleCallback(c *gin.Context) {
	identifier := c.Query("identifier")
	redirectURL := c.Query("redirect")

	log.Infof("CodeArts OAuth: callback received, identifier=%s, redirect=%s", identifier, redirectURL)

	if identifier == "" {
		c.JSON(http.StatusOK, gin.H{"success": false, "error": "lack argument identifier"})
		return
	}

	// Extract ticket_id from redirect URL to match the correct session
	var ticketFromRedirect string
	if redirectURL != "" {
		if parsed, err := url.Parse(redirectURL); err == nil {
			ticketFromRedirect = parsed.Query().Get("ticket_id")
		}
	}

	h.mu.Lock()
	var matchedSess *webSession

	// First try: match by ticket_id from redirect URL
	if ticketFromRedirect != "" {
		if stateID, ok := h.ticketToState[ticketFromRedirect]; ok {
			if sess, ok2 := h.sessions[stateID]; ok2 {
				sess.identifier = identifier
				sess.status = sPolling
				matchedSess = sess
				log.Infof("CodeArts OAuth: matched session by ticket_id=%s", ticketFromRedirect)
			}
		}
	}

	// Fallback: match the most recent waiting session
	if matchedSess == nil {
		var latestSess *webSession
		for _, sess := range h.sessions {
			if sess.status == sWaitingCB {
				if latestSess == nil || sess.startedAt.After(latestSess.startedAt) {
					latestSess = sess
				}
			}
		}
		if latestSess != nil {
			latestSess.identifier = identifier
			latestSess.status = sPolling
			matchedSess = latestSess
			log.Infof("CodeArts OAuth: matched session by fallback (latest waiting), ticket=%s", latestSess.ticketID)
		}
	}
	h.mu.Unlock()

	if matchedSess != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		matchedSess.cancel = cancel
		go h.pollLogin(ctx, matchedSess)
	} else {
		log.Warn("CodeArts OAuth: no matching session found for callback")
	}

	if redirectURL != "" {
		c.Redirect(http.StatusTemporaryRedirect, redirectURL)
	} else {
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusOK, `<!DOCTYPE html><html><head><meta charset="utf-8"><title>Authentication successful</title><script>setTimeout(function(){window.close();},3000);</script></head><body style="display:flex;justify-content:center;align-items:center;min-height:100vh;margin:0;font-family:sans-serif;background:#f5f5f5"><div style="text-align:center;padding:40px;background:white;border-radius:12px;box-shadow:0 2px 10px rgba(0,0,0,0.1)"><h1>✅ Authentication successful!</h1><p>You can close this tab.</p><p style="color:#666;font-size:14px">This tab will close automatically in 3 seconds.</p></div></body></html>`)
	}
}

func (h *OAuthWebHandler) pollLogin(ctx context.Context, sess *webSession) {
	if sess.cancel != nil {
		defer sess.cancel()
	}

	log.Infof("CodeArts OAuth: polling for login result, ticket=%s, identifier=%s", sess.ticketID, sess.identifier)

	// Poll with ticket_id + identifier (matching Python: poll_login_ticket)
	authResult, err := h.auth.PollForLoginResult(ctx, sess.ticketID, sess.identifier)
	if err != nil {
		h.mu.Lock()
		sess.status = sFailed
		sess.error = err.Error()
		h.mu.Unlock()
		log.Errorf("CodeArts OAuth: poll failed: %v", err)
		return
	}

	// Process login result: extract credential or exchange x_auth_token
	tokenData, err := h.auth.ProcessLoginResult(ctx, authResult)
	if err != nil {
		h.mu.Lock()
		sess.status = sFailed
		sess.error = err.Error()
		h.mu.Unlock()
		log.Errorf("CodeArts OAuth: process result failed: %v", err)
		return
	}

	h.mu.Lock()
	sess.status = sSuccess
	sess.token = tokenData
	h.mu.Unlock()

	// Save auth file
	h.saveTokenToFile(tokenData)
	log.Infof("CodeArts OAuth: authentication successful for user %s", tokenData.UserName)
}

func (h *OAuthWebHandler) handleStatus(c *gin.Context) {
	stateID := c.Query("state")
	if stateID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing state"})
		return
	}

	h.mu.RLock()
	sess, ok := h.sessions[stateID]
	h.mu.RUnlock()

	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
		return
	}

	switch sess.status {
	case sSuccess:
		msg := "Login successful! Token saved."
		if sess.token != nil && sess.token.UserName != "" {
			msg = fmt.Sprintf("Login successful! User: %s", sess.token.UserName)
		}
		c.JSON(http.StatusOK, gin.H{"status": "success", "message": msg})
	case sFailed:
		c.JSON(http.StatusOK, gin.H{"status": "failed", "error": sess.error})
	case sPolling:
		c.JSON(http.StatusOK, gin.H{"status": "pending", "message": "Polling for login result..."})
	default:
		c.JSON(http.StatusOK, gin.H{"status": "pending", "message": "Waiting for browser callback..."})
	}
}

func (h *OAuthWebHandler) saveTokenToFile(tokenData *CodeArtsTokenData) {
	authDir := ""
	if h.cfg != nil && h.cfg.AuthDir != "" {
		var err error
		authDir, err = util.ResolveAuthDir(h.cfg.AuthDir)
		if err != nil {
			log.Errorf("CodeArts OAuth: failed to resolve auth directory: %v", err)
		}
	}
	if authDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Errorf("CodeArts OAuth: failed to get home directory: %v", err)
			return
		}
		authDir = filepath.Join(home, ".cli-proxy-api")
	}
	if err := os.MkdirAll(authDir, 0700); err != nil {
		log.Errorf("CodeArts OAuth: failed to create auth directory: %v", err)
		return
	}

	fileName := "codearts-token.json"
	if tokenData.UserName != "" {
		fileName = fmt.Sprintf("codearts-%s.json", tokenData.UserName)
	}

	// Save in the same format as the file synthesizer expects:
	// { "type": "codearts", ... }
	storage := map[string]interface{}{
		"type":           "codearts",
		"ak":             tokenData.AK,
		"sk":             tokenData.SK,
		"security_token": tokenData.SecurityToken,
		"x_auth_token":   tokenData.XAuthToken,
		"expires_at":     tokenData.ExpiresAt.Format(time.RFC3339),
		"user_id":        tokenData.UserID,
		"user_name":      tokenData.UserName,
		"domain_id":      tokenData.DomainID,
		"email":          tokenData.Email,
		"last_refresh":   time.Now().Format(time.RFC3339),
	}

	data, err := json.MarshalIndent(storage, "", "  ")
	if err != nil {
		log.Errorf("CodeArts OAuth: failed to marshal token: %v", err)
		return
	}

	authFilePath := filepath.Join(authDir, fileName)
	if err := os.WriteFile(authFilePath, data, 0600); err != nil {
		log.Errorf("CodeArts OAuth: failed to write auth file: %v", err)
		return
	}
	log.Infof("CodeArts OAuth: token saved to %s", authFilePath)
}

// HTML templates
const codeArtsLoginPage = `<!DOCTYPE html>
<html><head><title>CodeArts IDE Login</title>
<style>
body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; display: flex; justify-content: center; align-items: center; min-height: 100vh; margin: 0; background: #f5f5f5; }
.card { background: white; border-radius: 12px; padding: 40px; box-shadow: 0 2px 10px rgba(0,0,0,0.1); text-align: center; max-width: 400px; }
h1 { color: #333; margin-bottom: 10px; }
p { color: #666; margin-bottom: 20px; }
a.btn { display: inline-block; background: #e53935; color: white; padding: 12px 32px; border-radius: 8px; text-decoration: none; font-size: 16px; }
a.btn:hover { background: #c62828; }
</style></head><body>
<div class="card">
<h1>&#x1f511; CodeArts IDE Login</h1>
<p>Login with your HuaweiCloud account to use CodeArts IDE models through CLIProxyAPI.</p>
<a class="btn" href="/v0/oauth/codearts/start">Start Login</a>
</div></body></html>`

const codeArtsWaitingPage = `<!DOCTYPE html>
<html><head><title>CodeArts IDE Login - Waiting</title>
<style>
body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; display: flex; justify-content: center; align-items: center; min-height: 100vh; margin: 0; background: #f5f5f5; }
.card { background: white; border-radius: 12px; padding: 40px; box-shadow: 0 2px 10px rgba(0,0,0,0.1); text-align: center; max-width: 500px; }
h1 { color: #333; margin-bottom: 10px; }
p { color: #666; margin-bottom: 20px; }
a.btn { display: inline-block; background: #e53935; color: white; padding: 12px 32px; border-radius: 8px; text-decoration: none; font-size: 16px; margin-bottom: 20px; }
a.btn:hover { background: #c62828; }
#status { padding: 12px; border-radius: 8px; background: #fff3e0; color: #e65100; }
.success { background: #e8f5e9 !important; color: #2e7d32 !important; }
.failed { background: #ffebee !important; color: #c62828 !important; }
</style></head><body>
<div class="card">
<h1>&#x1f511; CodeArts IDE Login</h1>
<p>Click the button below to open HuaweiCloud login page. After login, you will be redirected back here.</p>
<a class="btn" href="%s" target="_blank">Open HuaweiCloud Login</a>
<div id="status">&#x23f3; Waiting for login callback...</div>
</div>
<script>
var stateID = "%s";
function poll() {
  fetch("/v0/oauth/codearts/status?state=" + stateID)
    .then(function(r) { return r.json(); })
    .then(function(data) {
      var el = document.getElementById("status");
      if (data.status === "success") {
        el.className = "success";
        el.textContent = "\u2705 " + data.message;
      } else if (data.status === "failed") {
        el.className = "failed";
        el.textContent = "\u274c Error: " + data.error;
      } else {
        el.textContent = "\u23f3 " + (data.message || "Waiting...");
        setTimeout(poll, 3000);
      }
    })
    .catch(function() { setTimeout(poll, 5000); });
}
poll();
</script></body></html>`
