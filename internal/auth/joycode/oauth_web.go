package joycode

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
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
	jcPending sessionStatus = "pending"
	jcWaiting sessionStatus = "waiting"
	jcSuccess sessionStatus = "success"
	jcFailed  sessionStatus = "failed"
)

type jcWebSession struct {
	stateID   string
	authKey   string
	port      int
	status    sessionStatus
	startedAt time.Time
	error     string
	token     *JoyCodeTokenData
	cancel    context.CancelFunc
}

type OAuthWebHandler struct {
	cfg      *config.Config
	sessions map[string]*jcWebSession
	mu       sync.RWMutex
	auth     *JoyCodeAuth
}

func NewOAuthWebHandler(cfg *config.Config) *OAuthWebHandler {
	return &OAuthWebHandler{
		cfg:      cfg,
		sessions: make(map[string]*jcWebSession),
		auth:     NewJoyCodeAuth(nil),
	}
}

func (h *OAuthWebHandler) RegisterRoutes(router gin.IRouter) {
	oauth := router.Group("/v0/oauth/joycode")
	{
		oauth.GET("", h.handleIndex)
		oauth.GET("/start", h.handleStart)
		oauth.GET("/callback", h.handleCallback)
		oauth.GET("/status", h.handleStatus)
	}
	// JoyCode login page redirects to http://127.0.0.1:{port} with query params
	router.GET("/joycode/callback", h.handleCallback)
}

func generateJCState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func generateJCAuthKey() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (h *OAuthWebHandler) handleIndex(c *gin.Context) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, joyCodeLoginPage)
}

// HandleCallback is the public accessor for handleCallback, used by the root-path handler in server.go.
func (h *OAuthWebHandler) HandleCallback(c *gin.Context) {
	h.handleCallback(c)
}

// HandleRootCallback intercepts root-path requests that contain JoyCode auth parameters.
// JoyCode login redirects to http://127.0.0.1:{port}/?authKey=...&pt_key=...
func (h *OAuthWebHandler) HandleRootCallback(c *gin.Context) {
	if c.Request.URL.Path != "/" {
		c.Next()
		return
	}
	ptKey := c.Query("pt_key")
	if ptKey == "" {
		ptKey = c.Query("ptKey")
	}
	if ptKey == "" && c.Query("authKey") == "" {
		c.Next()
		return
	}
	h.handleCallback(c)
	c.Abort()
}

func (h *OAuthWebHandler) handleStart(c *gin.Context) {
	stateID, err := generateJCState()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate state"})
		return
	}

	authKey := generateJCAuthKey()

	port := h.cfg.Port
	if port == 0 {
		port = 8318
	}

	sess := &jcWebSession{
		stateID:   stateID,
		authKey:   authKey,
		port:      port,
		status:    jcWaiting,
		startedAt: time.Now(),
	}

	h.mu.Lock()
	h.sessions[stateID] = sess
	h.mu.Unlock()

	loginURL := fmt.Sprintf("https://joycode.jd.com/login/?ideAppName=JoyCode&fromIde=ide&redirect=0&authPort=%d&authKey=%s", port, authKey)

	log.Infof("JoyCode OAuth: session %s started, login URL: %s", stateID, loginURL)

	if c.GetHeader("Accept") == "application/json" {
		c.JSON(http.StatusOK, gin.H{"url": loginURL, "state": stateID})
		return
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, fmt.Sprintf(joyCodeWaitingPage, loginURL, stateID))
}

func (h *OAuthWebHandler) handleCallback(c *gin.Context) {
	authKey := c.Query("authKey")
	ptKey := c.Query("pt_key")
	if ptKey == "" {
		ptKey = c.Query("ptKey")
	}

	log.Infof("JoyCode OAuth: callback received, authKey=%s, ptKey_len=%d", authKey, len(ptKey))

	if ptKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing pt_key parameter"})
		return
	}

	h.mu.Lock()
	var matchedSess *jcWebSession

	if authKey != "" {
		for _, sess := range h.sessions {
			if sess.authKey == authKey {
				matchedSess = sess
				break
			}
		}
	}

	if matchedSess == nil {
		var latestSess *jcWebSession
		for _, sess := range h.sessions {
			if sess.status == jcWaiting {
				if latestSess == nil || sess.startedAt.After(latestSess.startedAt) {
					latestSess = sess
				}
			}
		}
		if latestSess != nil {
			matchedSess = latestSess
		}
	}

	if matchedSess != nil {
		matchedSess.status = jcPending
	}
	h.mu.Unlock()

	if matchedSess != nil {
		go h.verifyAndSave(matchedSess, ptKey)
	} else {
		log.Warn("JoyCode OAuth: no matching session found for callback")
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, `<!DOCTYPE html><html><head><meta charset="utf-8"><title>Authorization Successful</title><script>setTimeout(function(){window.close();},2000);</script></head><body style="display:flex;justify-content:center;align-items:center;min-height:100vh;margin:0;font-family:sans-serif;background:#f5f5f5"><div style="text-align:center;padding:40px;background:white;border-radius:12px;box-shadow:0 2px 10px rgba(0,0,0,0.1)"><h1 style="color:#2ecc71">&#10003; Authorization Successful</h1><p>Credential captured, syncing. Please return to the command line.</p></div></body></html>`)
}

func (h *OAuthWebHandler) verifyAndSave(sess *jcWebSession, ptKey string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sess.cancel = cancel

	log.Infof("JoyCode OAuth: verifying token for session %s", sess.stateID)

	tokenData, err := h.auth.VerifyToken(ctx, ptKey)
	if err != nil {
		h.mu.Lock()
		sess.status = jcFailed
		sess.error = err.Error()
		h.mu.Unlock()
		log.Errorf("JoyCode OAuth: token verification failed: %v", err)
		return
	}

	h.mu.Lock()
	sess.status = jcSuccess
	sess.token = tokenData
	h.mu.Unlock()

	h.saveTokenToFile(tokenData)
	log.Infof("JoyCode OAuth: authentication successful for user %s", tokenData.UserID)
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
	case jcSuccess:
		msg := "Login successful! Token saved."
		if sess.token != nil && sess.token.UserID != "" {
			msg = fmt.Sprintf("Login successful! User: %s", sess.token.UserID)
		}
		c.JSON(http.StatusOK, gin.H{"status": "success", "message": msg})
	case jcFailed:
		c.JSON(http.StatusOK, gin.H{"status": "failed", "error": sess.error})
	default:
		c.JSON(http.StatusOK, gin.H{"status": "pending", "message": "Waiting for browser callback..."})
	}
}

func (h *OAuthWebHandler) saveTokenToFile(tokenData *JoyCodeTokenData) {
	authDir := ""
	if h.cfg != nil && h.cfg.AuthDir != "" {
		var err error
		authDir, err = util.ResolveAuthDir(h.cfg.AuthDir)
		if err != nil {
			log.Errorf("JoyCode OAuth: failed to resolve auth directory: %v", err)
		}
	}
	if authDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Errorf("JoyCode OAuth: failed to get home directory: %v", err)
			return
		}
		authDir = filepath.Join(home, ".cli-proxy-api")
	}
	if err := os.MkdirAll(authDir, 0700); err != nil {
		log.Errorf("JoyCode OAuth: failed to create auth directory: %v", err)
		return
	}

	fileName := "joycode-token.json"
	if tokenData.UserID != "" {
		fileName = fmt.Sprintf("joycode-%s.json", tokenData.UserID)
	}

	storage := map[string]interface{}{
		"type":         "joycode",
		"ptKey":        tokenData.PTKey,
		"userId":       tokenData.UserID,
		"tenant":       tokenData.Tenant,
		"orgFullName":  tokenData.OrgFullName,
		"loginType":    tokenData.LoginType,
		"last_refresh": time.Now().Format(time.RFC3339),
	}

	data, err := json.MarshalIndent(storage, "", "  ")
	if err != nil {
		log.Errorf("JoyCode OAuth: failed to marshal token: %v", err)
		return
	}

	authFilePath := filepath.Join(authDir, fileName)
	if err := os.WriteFile(authFilePath, data, 0600); err != nil {
		log.Errorf("JoyCode OAuth: failed to write auth file: %v", err)
		return
	}
	log.Infof("JoyCode OAuth: token saved to %s", authFilePath)
}

const joyCodeLoginPage = `<!DOCTYPE html>
<html><head><title>JoyCode Login</title>
<style>
body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; display: flex; justify-content: center; align-items: center; min-height: 100vh; margin: 0; background: #f5f5f5; }
.card { background: white; border-radius: 12px; padding: 40px; box-shadow: 0 2px 10px rgba(0,0,0,0.1); text-align: center; max-width: 400px; }
h1 { color: #333; margin-bottom: 10px; }
p { color: #666; margin-bottom: 20px; }
a.btn { display: inline-block; background: #e53935; color: white; padding: 12px 32px; border-radius: 8px; text-decoration: none; font-size: 16px; }
a.btn:hover { background: #c62828; }
</style></head><body>
<div class="card">
<h1>&#x1f511; JoyCode Login</h1>
<p>Login with your JD account to use JoyCode models through CLIProxyAPI.</p>
<a class="btn" href="/v0/oauth/joycode/start">Start Login</a>
</div></body></html>`

const joyCodeWaitingPage = `<!DOCTYPE html>
<html><head><title>JoyCode Login - Waiting</title>
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
<h1>&#x1f511; JoyCode Login</h1>
<p>Click the button below to open JoyCode login page. After login, credentials will be captured automatically.</p>
<a class="btn" href="%s" target="_blank">Open JoyCode Login</a>
<div id="status">&#x23f3; Waiting for login callback...</div>
</div>
<script>
var stateID = "%s";
function poll() {
  fetch("/v0/oauth/joycode/status?state=" + stateID)
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
