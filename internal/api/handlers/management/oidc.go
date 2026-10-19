package management

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"
)

const (
	managementOIDCSessionCookie = "__Host-management-session"
	managementOIDCFlowCookie    = "__Host-management-login"
	managementOIDCMaxEntries    = 1024
	managementOIDCFlowLifetime  = 5 * time.Minute
	managementOIDCSessionMaxAge = time.Hour
)

type oidcFlow struct {
	state, nonce, verifier string
	expires                time.Time
}

type oidcSession struct {
	subject string
	expires time.Time
}

// managementOIDC is immutable apart from its bounded, process-local flows and sessions.
// Restarting the server revokes all browser sessions.
type managementOIDC struct {
	mu       sync.Mutex
	initMu   sync.Mutex
	issuer   string
	clientID string
	secret   string
	redirect string
	origin   string
	subjects map[string]struct{}
	flows    map[string]oidcFlow
	sessions map[string]oidcSession
	provider *oidc.Provider
	oauth    oauth2.Config
}

func newManagementOIDC() (*managementOIDC, error) {
	issuer := strings.TrimSpace(os.Getenv("MANAGEMENT_OIDC_ISSUER"))
	if issuer == "" {
		return nil, nil
	}

	redirect := strings.TrimSpace(os.Getenv("MANAGEMENT_OIDC_REDIRECT_URL"))
	issuerURL, err := managementOIDCHTTPSURL(issuer)
	if err != nil {
		return nil, fmt.Errorf("MANAGEMENT_OIDC_ISSUER must be an HTTPS URL without credentials, query, or fragment")
	}
	redirectURL, err := managementOIDCHTTPSURL(redirect)
	if err != nil || redirectURL.Path != "/v8/management/auth/oidc/callback" {
		return nil, fmt.Errorf("MANAGEMENT_OIDC_REDIRECT_URL must be HTTPS and use /v8/management/auth/oidc/callback")
	}

	clientID := strings.TrimSpace(os.Getenv("MANAGEMENT_OIDC_CLIENT_ID"))
	if clientID == "" {
		return nil, fmt.Errorf("MANAGEMENT_OIDC_CLIENT_ID is required")
	}
	subjects := make(map[string]struct{})
	for _, subject := range strings.Split(os.Getenv("MANAGEMENT_OIDC_ALLOWED_SUBJECTS"), ",") {
		if subject = strings.TrimSpace(subject); subject != "" {
			subjects[subject] = struct{}{}
		}
	}
	if len(subjects) == 0 {
		return nil, fmt.Errorf("MANAGEMENT_OIDC_ALLOWED_SUBJECTS must contain at least one subject")
	}

	return &managementOIDC{
		issuer:   issuerURL.String(),
		clientID: clientID,
		secret:   os.Getenv("MANAGEMENT_OIDC_CLIENT_SECRET"),
		redirect: redirectURL.String(),
		origin:   managementOIDCOrigin(redirectURL),
		subjects: subjects,
		flows:    make(map[string]oidcFlow),
		sessions: make(map[string]oidcSession),
	}, nil
}

func managementOIDCOrigin(u *url.URL) string {
	if u == nil || u.Hostname() == "" {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return scheme + "://" + host
}

func managementOIDCHTTPSURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("invalid HTTPS URL")
	}
	return u, nil
}

func (h *Handler) OIDCEnabled() bool {
	return h != nil && h.oidc != nil
}

func isV8ManagementRequest(c *gin.Context) bool {
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return false
	}
	path := c.Request.URL.Path
	return path == "/v8/management" || strings.HasPrefix(path, "/v8/management/")
}

func hasManagementCredential(c *gin.Context) bool {
	return c.GetHeader("Authorization") != "" || c.GetHeader("X-Management-Key") != ""
}

func randomManagementOIDCToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func setManagementOIDCCookie(c *gin.Context, name, value string, age int) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   age,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func (o *managementOIDC) pruneLocked(now time.Time) {
	for key, flow := range o.flows {
		if !now.Before(flow.expires) {
			delete(o.flows, key)
		}
	}
	for key, session := range o.sessions {
		if !now.Before(session.expires) {
			delete(o.sessions, key)
		}
	}
}

func (o *managementOIDC) session(c *gin.Context) (oidcSession, bool) {
	key, err := c.Cookie(managementOIDCSessionCookie)
	if err != nil {
		return oidcSession{}, false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.pruneLocked(time.Now())
	session, ok := o.sessions[key]
	return session, ok
}

// Browser-session requests must carry a non-simple header and come from the
// configured UI origin. Sec-Fetch-Site supplies a second browser signal.
func (o *managementOIDC) sameOrigin(c *gin.Context) bool {
	origin := c.GetHeader("Origin")
	if origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" ||
			parsed.Fragment != "" || managementOIDCOrigin(parsed) != o.origin {
			return false
		}
	}
	return c.GetHeader("X-Management-CSRF") == "1" &&
		c.GetHeader("Sec-Fetch-Site") != "cross-site"
}

func (o *managementOIDC) initialize(ctx context.Context) error {
	o.initMu.Lock()
	defer o.initMu.Unlock()
	if o.provider != nil {
		return nil
	}
	provider, err := oidc.NewProvider(ctx, o.issuer)
	if err != nil {
		return err
	}
	o.provider = provider
	o.oauth = oauth2.Config{
		ClientID:     o.clientID,
		ClientSecret: o.secret,
		RedirectURL:  o.redirect,
		Endpoint:     provider.Endpoint(),
		Scopes:       []string{oidc.ScopeOpenID},
	}
	return nil
}

func (h *Handler) ManagementOIDCSession(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	authenticated := false
	if h.OIDCEnabled() {
		_, authenticated = h.oidc.session(c)
	}
	c.JSON(http.StatusOK, gin.H{"oidcEnabled": h.OIDCEnabled(), "authenticated": authenticated})
}

func (h *Handler) ManagementOIDCLogin(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !h.OIDCEnabled() {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	o := h.oidc
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	if err := o.initialize(ctx); err != nil {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "identity provider unavailable"})
		return
	}
	binding, err := randomManagementOIDCToken()
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "could not start login"})
		return
	}
	state, err := randomManagementOIDCToken()
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "could not start login"})
		return
	}
	nonce, err := randomManagementOIDCToken()
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "could not start login"})
		return
	}
	flow := oidcFlow{
		state:    state,
		nonce:    nonce,
		verifier: oauth2.GenerateVerifier(),
		expires:  time.Now().Add(managementOIDCFlowLifetime),
	}
	o.mu.Lock()
	o.pruneLocked(time.Now())
	if len(o.flows) >= managementOIDCMaxEntries {
		o.mu.Unlock()
		c.AbortWithStatus(http.StatusTooManyRequests)
		return
	}
	o.flows[binding] = flow
	o.mu.Unlock()
	setManagementOIDCCookie(c, managementOIDCFlowCookie, binding, int(managementOIDCFlowLifetime.Seconds()))
	c.Redirect(http.StatusFound, o.oauth.AuthCodeURL(
		flow.state,
		oidc.Nonce(flow.nonce),
		oauth2.S256ChallengeOption(flow.verifier),
	))
}

func (h *Handler) ManagementOIDCCallback(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Referrer-Policy", "no-referrer")
	if !h.OIDCEnabled() {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	o := h.oidc
	binding, _ := c.Cookie(managementOIDCFlowCookie)
	setManagementOIDCCookie(c, managementOIDCFlowCookie, "", -1)
	o.mu.Lock()
	flow, ok := o.flows[binding]
	delete(o.flows, binding)
	o.mu.Unlock()
	if !ok || !time.Now().Before(flow.expires) || c.Query("state") != flow.state || c.Query("code") == "" || c.Query("error") != "" {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "invalid login transaction"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	if err := o.initialize(ctx); err != nil {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "identity provider unavailable"})
		return
	}
	token, err := o.oauth.Exchange(ctx, c.Query("code"), oauth2.VerifierOption(flow.verifier))
	if err != nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "login failed"})
		return
	}
	rawToken, ok := token.Extra("id_token").(string)
	if !ok || rawToken == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "login failed"})
		return
	}
	verifier := o.provider.Verifier(&oidc.Config{ClientID: o.clientID})
	idToken, err := verifier.Verify(ctx, rawToken)
	if err != nil || idToken.Nonce != flow.nonce {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "login failed"})
		return
	}
	if _, allowed := o.subjects[idToken.Subject]; !allowed {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "account is not allowed"})
		return
	}
	key, err := randomManagementOIDCToken()
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "could not create session"})
		return
	}
	expires := time.Now().Add(managementOIDCSessionMaxAge)
	if idToken.Expiry.Before(expires) {
		expires = idToken.Expiry
	}
	o.mu.Lock()
	o.pruneLocked(time.Now())
	if previous, err := c.Cookie(managementOIDCSessionCookie); err == nil {
		delete(o.sessions, previous)
	}
	if len(o.sessions) >= managementOIDCMaxEntries {
		o.mu.Unlock()
		c.AbortWithStatus(http.StatusTooManyRequests)
		return
	}
	o.sessions[key] = oidcSession{subject: idToken.Subject, expires: expires}
	o.mu.Unlock()
	setManagementOIDCCookie(c, managementOIDCSessionCookie, key, int(time.Until(expires).Seconds()))
	c.Redirect(http.StatusSeeOther, "/management.html")
}

func (h *Handler) ManagementOIDCLogout(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !h.OIDCEnabled() {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	o := h.oidc
	if !o.sameOrigin(c) {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	key, _ := c.Cookie(managementOIDCSessionCookie)
	o.mu.Lock()
	delete(o.sessions, key)
	o.mu.Unlock()
	setManagementOIDCCookie(c, managementOIDCSessionCookie, "", -1)
	c.Status(http.StatusNoContent)
}
