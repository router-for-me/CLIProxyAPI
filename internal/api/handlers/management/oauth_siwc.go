package management

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const siwcBindTTL = 5 * time.Minute

type siwcOAuthService interface {
	GenerateSIWCAuthURL(state, nonce, deviceID, redirectURI string, pkce *codex.PKCECodes) (string, error)
	ExchangeSIWCCodeForTokens(ctx context.Context, code, clientID, redirectURI, deviceID string, pkce *codex.PKCECodes) (*codex.CodexSIWCTokenData, error)
}

// siwcBindSession is server-owned: callbacks cannot select a different target,
// redirect URI, PKCE verifier, or token endpoint. It never contains an auth snapshot.
type siwcBindSession struct {
	mu           sync.Mutex
	owner        *Handler
	manager      *coreauth.Manager
	service      siwcOAuthService
	targetID     string
	epoch        uint64
	accountID    string
	path         string
	authDir      string
	deviceID     string
	redirect     string
	pkce         *codex.PKCECodes
	ctx          context.Context
	cancel       context.CancelFunc
	exchanging   bool
	exchangeDone chan struct{}
}

func siwcBindEligible(auth *coreauth.Auth) bool {
	if auth == nil || !strings.EqualFold(auth.Provider, "codex") || auth.AuthSourceKind() != coreauth.AuthSourceFile || auth.AuthKind() != coreauth.AuthKindOAuth || isRuntimeOnlyAuth(auth) {
		return false
	}
	for _, key := range []string{"access_token", "refresh_token"} {
		value, _ := auth.Metadata[key].(string)
		if strings.TrimSpace(value) == "" {
			return false
		}
	}
	return strings.TrimSpace(authAttribute(auth, "path")) != ""
}

// resolveSIWCBindFile resolves only a server-selected auth path, confines it to
// auth-dir, and reads the current credential rather than trusting a cached token.
func resolveSIWCBindFile(authDir, path string) (string, map[string]any, error) {
	dir, errResolve := util.ResolveAuthDir(authDir)
	if errResolve != nil || strings.TrimSpace(dir) == "" {
		return "", nil, errors.New("auth directory is unavailable")
	}
	dir, errAbs := filepath.Abs(dir)
	if errAbs != nil {
		return "", nil, errAbs
	}
	dir, errDir := filepath.EvalSymlinks(dir)
	if errDir != nil {
		return "", nil, errDir
	}
	dir, errAbs = filepath.Abs(dir)
	if errAbs != nil {
		return "", nil, errAbs
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil, errors.New("credential must be an existing regular file")
	}
	if !filepath.IsAbs(path) {
		path, errAbs = filepath.Abs(path)
		if errAbs != nil {
			return "", nil, errAbs
		}
	}
	// Match the commit-time rule: directory aliases are allowed, but the
	// credential itself must not be a symlink.
	originalInfo, errLstat := os.Lstat(path)
	if errLstat != nil || !originalInfo.Mode().IsRegular() {
		return "", nil, errors.New("credential must be an existing regular file")
	}
	realPath, errPath := filepath.EvalSymlinks(path)
	if errPath != nil {
		return "", nil, errPath
	}
	realPath, errAbs = filepath.Abs(realPath)
	if errAbs != nil {
		return "", nil, errAbs
	}
	relative, errRel := filepath.Rel(dir, realPath)
	if errRel != nil || relative == "." || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", nil, errors.New("credential must belong to auth-dir")
	}
	info, errStat := os.Stat(realPath)
	if errStat != nil || !info.Mode().IsRegular() {
		return "", nil, errors.New("credential must be an existing regular file")
	}
	data, errRead := os.ReadFile(realPath)
	if errRead != nil {
		return "", nil, errRead
	}
	var metadata map[string]any
	if errJSON := json.Unmarshal(data, &metadata); errJSON != nil {
		return "", nil, errors.New("invalid credential JSON")
	}
	provider, _ := metadata["type"].(string)
	if !strings.EqualFold(strings.TrimSpace(provider), "codex") {
		return "", nil, errors.New("credential type must be codex")
	}
	for _, key := range []string{"access_token", "refresh_token"} {
		value, _ := metadata[key].(string)
		if strings.TrimSpace(value) == "" {
			return "", nil, fmt.Errorf("codex credential requires %s", key)
		}
	}
	return realPath, metadata, nil
}

// StartSIWCBind creates a v8-only OAuth session for an existing Codex credential.
// Remote browsers complete it by submitting the full loopback callback URL; no
// listener on the server's port 1455 is required or started by this endpoint.
func (h *Handler) StartSIWCBind(c *gin.Context) {
	var req struct {
		Name string `json:"name"`
	}
	if errBind := c.ShouldBindJSON(&req); errBind != nil {
		c.JSON(clienterror.HTTPStatusFromErrorOr(errBind, http.StatusBadRequest), gin.H{"error": "invalid request body"})
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	h.siwcCommitMu.Lock()
	defer h.siwcCommitMu.Unlock()
	h.mu.Lock()
	cfg, manager, factory := h.cfg.CloneForRuntime(), h.authManager, h.siwcOAuthFactory
	h.mu.Unlock()
	if cfg == nil || manager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "auth manager unavailable"})
		return
	}
	target, ok := manager.GetByID(req.Name)
	if !ok {
		for _, auth := range manager.List() {
			if auth.FileName == req.Name {
				target = auth
				break
			}
		}
	}
	if target == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth file not found"})
		return
	}
	if !siwcBindEligible(target) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "SIWC binding requires a file-backed Codex OAuth credential"})
		return
	}
	path, metadata, errPath := resolveSIWCBindFile(cfg.AuthDir, authAttribute(target, "path"))
	if errPath != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "SIWC binding target is unavailable or invalid"})
		return
	}
	accountID, _ := target.Metadata["account_id"].(string)
	if (accountID != "" && metadata["account_id"] != accountID) || (accountID == "" && metadata["account_id"] != nil && metadata["account_id"] != "") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "SIWC binding target is unavailable or invalid"})
		return
	}
	deviceID := uuid.NewString()
	if section, okSection := metadata["siwc"].(map[string]any); okSection {
		if existing, _ := section["device_id"].(string); codex.IsValidUUID(existing) {
			deviceID = strings.ToLower(strings.TrimSpace(existing))
		}
	}
	pkce, errPKCE := codex.GeneratePKCECodes()
	state, errState := codex.GenerateRandomBase64URL()
	nonce, errNonce := codex.GenerateRandomBase64URL()
	if errPKCE != nil || errState != nil || errNonce != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to initialize SIWC authorization"})
		return
	}
	var service siwcOAuthService = codex.NewCodexAuth(cfg)
	if factory != nil {
		service = factory(cfg)
	}
	authURL, errURL := service.GenerateSIWCAuthURL(state, nonce, deviceID, codex.SIWCRedirectURI, pkce)
	if errURL != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate SIWC authorization URL"})
		return
	}
	ctx, cancel := context.WithTimeout(PopulateAuthContext(context.Background(), c), siwcBindTTL)
	flow := &siwcBindSession{owner: h, manager: manager, service: service, targetID: target.ID, epoch: target.RegistrationEpoch,
		accountID: accountID, path: path, authDir: cfg.AuthDir, deviceID: deviceID, redirect: codex.SIWCRedirectURI, pkce: pkce, ctx: ctx, cancel: cancel}
	if !oauthSessions.registerSIWC(state, flow) {
		cancel()
		c.JSON(http.StatusConflict, gin.H{"error": "a SIWC binding is already pending for this credential"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "url": authURL, "state": state, "flow": "siwc_bind", "expires_in": int(siwcBindTTL.Seconds())})
}

func (s *oauthSessionStore) registerSIWC(state string, flow *siwcBindSession) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.purgeExpiredLocked(now)
	if _, exists := s.sessions[state]; exists {
		return false
	}
	for _, session := range s.sessions {
		if session.SIWC != nil && !session.Completed && session.Status == "" && session.SIWC.manager == flow.manager && session.SIWC.targetID == flow.targetID {
			return false
		}
	}
	s.sessions[state] = oauthSession{Provider: "codex", Source: oauthSessionSourceBuiltin, SIWC: flow, CreatedAt: now, ExpiresAt: now.Add(siwcBindTTL)}
	return true
}

// claimSIWCCommit keeps a session addressable until an already-started disk
// commit finishes. Expiration must not turn a durable successful bind into an
// unknown-state response. Cancellation is serialized by the session gate.
func (s *oauthSessionStore) claimSIWCCommit(state string, flow *siwcBindSession) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeExpiredLocked(time.Now())
	session, ok := s.sessions[state]
	if !ok || session.SIWC != flow || session.Completed || session.Status != "" || session.SIWCCommitting || flow.ctx.Err() != nil {
		return false
	}
	session.SIWCCommitting = true
	s.sessions[state] = session
	return true
}

type siwcCallbackRequest struct {
	oauthCallbackRequest
	ClientID string `json:"client_id"`
}

func (h *Handler) PostOAuthCallbackV8(c *gin.Context) {
	var req siwcCallbackRequest
	if errBind := c.ShouldBindJSON(&req); errBind != nil {
		c.JSON(clienterror.HTTPStatusFromErrorOr(errBind, http.StatusBadRequest), gin.H{"status": "error", "error": "invalid body"})
		return
	}
	h.handleOAuthCallbackV8(c, req)
}

func (h *Handler) GetOAuthCallbackV8(c *gin.Context) {
	h.handleOAuthCallbackV8(c, siwcCallbackRequest{oauthCallbackRequest: oauthCallbackRequest{
		Provider: c.Query("provider"), State: c.Query("state"), Code: c.Query("code"),
		Error: firstNonEmpty(c.Query("error"), c.Query("error_description")),
	}, ClientID: c.Query("client_id")})
}

func (h *Handler) handleOAuthCallbackV8(c *gin.Context, req siwcCallbackRequest) {
	state := strings.TrimSpace(req.State)
	var redirect *url.URL
	if raw := strings.TrimSpace(req.RedirectURL); raw != "" {
		var errParse error
		redirect, errParse = url.Parse(raw)
		if errParse != nil {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid redirect_url"})
			return
		}
		if state == "" {
			state = strings.TrimSpace(redirect.Query().Get("state"))
		}
	}
	session, ok := oauthSessions.Get(state)
	if !ok || session.SIWC == nil {
		h.handleOAuthCallback(c, req.oauthCallbackRequest)
		return
	}
	flow := session.SIWC
	if c.Request.Method == http.MethodGet {
		query := c.Request.URL.Query()
		for _, key := range []string{"provider", "state", "code", "client_id", "error"} {
			if len(query[key]) > 1 {
				c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "conflicting callback parameters"})
				return
			}
		}
	}
	if flow.owner != h {
		c.JSON(http.StatusNotFound, gin.H{"status": "error", "error": "unknown or expired state"})
		return
	}
	if provider := strings.TrimSpace(req.Provider); provider != "" {
		canonical, errProvider := NormalizeOAuthProvider(provider)
		if errProvider != nil || canonical != "codex" {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "provider does not match state"})
			return
		}
	}
	code, clientID, oauthError := strings.TrimSpace(req.Code), strings.TrimSpace(req.ClientID), strings.TrimSpace(req.Error)
	if redirect != nil {
		expected, _ := url.Parse(flow.redirect)
		if redirect.Scheme != expected.Scheme || redirect.Host != expected.Host || redirect.Path != expected.Path || redirect.User != nil || redirect.Fragment != "" {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "unexpected SIWC callback URL"})
			return
		}
		query := redirect.Query()
		for key, target := range map[string]*string{"state": &state, "code": &code, "client_id": &clientID, "error": &oauthError} {
			value := strings.TrimSpace(query.Get(key))
			if len(query[key]) > 1 || (value != "" && *target != "" && value != *target) {
				c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "conflicting callback parameters"})
				return
			}
			if *target == "" {
				*target = value
			}
		}
	}
	if oauthError == "" && (code == "" || clientID == "") {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "SIWC callback requires code and client_id"})
		return
	}
	flow.mu.Lock()
	defer flow.mu.Unlock()
	if flow.exchanging || flow.ctx.Err() != nil || !oauthSessions.IsPending(state, "codex") {
		c.JSON(http.StatusConflict, gin.H{"status": "error", "error": "SIWC session is no longer accepting callbacks"})
		return
	}
	if oauthError != "" {
		flow.fail(state, "SIWC authorization was denied or failed")
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
		return
	}
	flow.exchanging = true
	flow.exchangeDone = make(chan struct{})
	go flow.exchange(state, code, clientID)
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// fail intentionally never publishes upstream response bodies or callback values.
// Caller holds the session gate.
func (flow *siwcBindSession) fail(state, message string) {
	SetOAuthSessionError(state, message)
	flow.cancel()
	flow.pkce = nil
}

func (flow *siwcBindSession) exchange(state, code, clientID string) {
	defer close(flow.exchangeDone)
	// Copy under the gate so cancellation and terminal cleanup cannot race PKCE use.
	flow.mu.Lock()
	pkce := flow.pkce
	flow.mu.Unlock()
	tokenData, errExchange := flow.service.ExchangeSIWCCodeForTokens(flow.ctx, code, clientID, flow.redirect, flow.deviceID, pkce)
	flow.mu.Lock()
	flow.owner.siwcCommitMu.Lock()
	var persisted *coreauth.Auth
	defer func() {
		flow.owner.siwcCommitMu.Unlock()
		flow.mu.Unlock()
		if persisted != nil {
			flow.owner.mu.Lock()
			hook := flow.owner.postAuthPersistHook
			flow.owner.mu.Unlock()
			if hook != nil {
				if errHook := hook(context.Background(), persisted); errHook != nil {
					log.Warn("SIWC binding saved but post-auth notification failed")
				}
			}
		}
	}()
	if flow.ctx.Err() != nil || !oauthSessions.IsPending(state, "codex") {
		flow.pkce = nil
		return
	}
	if errExchange != nil || tokenData == nil {
		flow.fail(state, "Failed to exchange SIWC authorization code; start a new binding")
		return
	}
	path, _, errPath := resolveSIWCBindFile(flow.authDir, flow.path)
	if errPath != nil || path != flow.path {
		flow.fail(state, "SIWC binding target is no longer available")
		return
	}
	if !oauthSessions.claimSIWCCommit(state, flow) {
		flow.cancel()
		flow.pkce = nil
		return
	}
	updated, errBind := flow.manager.BindCodexSIWC(flow.ctx, flow.targetID, flow.epoch, flow.accountID, tokenData)
	if errBind != nil {
		flow.fail(state, "Failed to save SIWC binding; the target may have changed")
		return
	}
	// Binding is durable and published before the session reports success.
	persisted = updated
	CompleteOAuthSession(state)
	flow.cancel()
	flow.pkce = nil
}
