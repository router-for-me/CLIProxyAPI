package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/multiuser"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

func (s *Server) initializeHub(cfg *config.Config) {
	if !cfg.MultiUser.Enabled {
		return
	}
	if err := cfg.MultiUser.Validate(); err != nil {
		s.hubInitErr = err
		return
	}
	if cfg.Home.Enabled {
		s.hubInitErr = fmt.Errorf("multi-user mode runs on a standalone proxy, not a Home worker")
		return
	}
	dir := strings.TrimSpace(cfg.MultiUser.DataDir)
	if dir == "" {
		authDir, err := filepath.Abs(filepath.Clean(cfg.AuthDir))
		if err != nil {
			s.hubInitErr = err
			return
		}
		dir = authDir + "-hub"
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(filepath.Dir(s.configFilePath), dir)
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		s.hubInitErr = err
		return
	}
	authDir, _ := filepath.Abs(cfg.AuthDir)
	if relative, errRel := filepath.Rel(authDir, absDir); errRel == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		s.hubInitErr = fmt.Errorf("multi-user.data-dir must be outside oauth.auth-dir so account data is not watched as credentials")
		return
	}
	store, err := multiuser.Open(absDir, time.Duration(cfg.MultiUser.SessionTTLHours)*time.Hour)
	if err != nil {
		s.hubInitErr = fmt.Errorf("initialize multi-user: %w", err)
		log.WithError(err).Error("multi-user initialization failed")
		return
	}
	s.hub = &multiuser.Controller{Store: store}
	store.PrepareJournal()
	if s.accessManager == nil {
		s.accessManager = sdkaccess.NewManager()
	}
	usage.RegisterNamedPlugin("multi-user:"+absDir, store)
}

func (s *Server) registerHubRoutes() {
	if s.hub == nil {
		return
	}
	s.engine.GET("/hub.html", func(c *gin.Context) { c.Redirect(http.StatusFound, "/management.html") })
	s.registerManagementV8Routes()
	root := s.engine.Group("/v8/hub")
	root.Use(jsonGzipMiddleware())
	root.Use(s.hub.AuditMiddleware())
	member := root.Group("")
	member.Use(s.hub.Session())
	s.hub.RegisterRoutes(root, member)
	member.GET("/marketplace", s.mgmt.HubMarketplace)
	member.GET("/marketplace/performance", s.mgmt.HubMarketplacePerformance)
	member.GET("/credentials", s.mgmt.HubCredentials)
	member.POST("/credentials", s.mgmt.HubUploadCredential)
	member.PATCH("/credentials/:id", s.mgmt.HubPatchCredential)
	member.GET("/credentials/:id/download", s.mgmt.HubDownloadCredential)
	member.PATCH("/credentials/:id/fields", s.mgmt.HubPatchCredentialFields)
	member.POST("/credentials/:id/refresh", s.mgmt.HubRefreshCredential)
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		member.Handle(method, "/credentials/:id/models", s.mgmt.CredentialModels)
	}
	member.POST("/credentials/:id/models/discover", s.mgmt.DiscoverCredentialModels)
	member.POST("/credentials/:id/api-call", s.mgmt.HubQuotaAPICall)
	member.DELETE("/credentials/:id", s.mgmt.HubDeleteCredential)
	member.POST("/credentials/:id/claim", s.mgmt.HubClaimCredential)
	member.POST("/providers", s.mgmt.HubCreateProvider)
	member.POST("/providers/discover", s.mgmt.HubDiscoverProviderModels)
	member.PATCH("/providers/:id", s.mgmt.HubPatchProvider)
	member.PATCH("/channels/:id", s.mgmt.HubPatchChannel)
	member.DELETE("/channels/:id", s.mgmt.HubDeleteChannel)
	member.GET("/oauth/auth-url", s.mgmt.HubOAuth)
	member.GET("/oauth/status", s.mgmt.GetAuthStatus)
	member.DELETE("/oauth/session", s.mgmt.CancelAuthSession)
	member.GET("/oauth/callback", s.mgmt.GetOAuthCallback)
	member.POST("/oauth/callback", s.mgmt.PostOAuthCallback)
	s.mgmt.MigrateHubNaming()
}

func hubProxyPath(path string) bool {
	return strings.HasPrefix(path, "/v1/") || strings.HasPrefix(path, "/v1beta/") || strings.HasPrefix(path, "/openai/v1/") || strings.HasPrefix(path, "/backend-api/codex/")
}

func (s *Server) hubRequestMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if s.hubInitErr != nil && hubProxyPath(c.Request.URL.Path) {
			c.AbortWithStatusJSON(503, gin.H{"error": "multi-user initialization failed"})
			return
		}
		if s.hub == nil {
			c.Next()
			return
		}
		if strings.HasPrefix(c.Request.URL.Path, "/v8/management") {
			provided := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
			if user, ok := s.hub.Store.AuthenticateSession(provided); ok {
				if user.Role != "admin" {
					c.AbortWithStatusJSON(403, gin.H{"error": "administrator access required"})
					return
				}
				c.Set("hubActor", user.Username)
			} else {
				c.Set("hubActor", "management-key")
			}
			c.Next()
			if c.Request.Method != "GET" && c.Request.Method != "HEAD" {
				if err := s.hub.Store.Audit(multiuser.AuditEvent{Actor: c.GetString("hubActor"), Action: c.Request.Method + " " + c.FullPath(), Target: c.Param("path"), Status: c.Writer.Status(), IP: c.ClientIP()}); err != nil {
					log.WithError(err).Error("multi-user: failed to persist management audit")
				}
			}
			return
		}
		if !hubProxyPath(c.Request.URL.Path) {
			c.Next()
			return
		}
		if strings.HasPrefix(c.Request.URL.Path, "/v1/realtime") || strings.HasPrefix(c.Request.URL.Path, "/v1/live") {
			c.AbortWithStatusJSON(http.StatusNotImplemented, gin.H{"error": "Realtime and live calls are not available in multi-user mode; use chat, messages or responses with a channel-prefixed model"})
			return
		}
		provider := &multiuser.AccessProvider{Store: s.hub.Store}
		result, errAuth := provider.Authenticate(c.Request.Context(), c.Request)
		if errAuth != nil {
			c.AbortWithStatusJSON(errAuth.HTTPStatusCode(), gin.H{"error": errAuth.Message})
			return
		}
		var blockedModels []string
		_ = json.Unmarshal([]byte(result.Metadata["key_blocked_models"]), &blockedModels)
		c.Request = c.Request.WithContext(coreauth.WithBlockedModels(c.Request.Context(), blockedModels))
		keyAutoBalance := result.Metadata["key_auto_balance"] == "true"
		if keyAutoBalance {
			c.Request = c.Request.WithContext(coreauth.WithBareModelsAllowed(c.Request.Context()))
		}
		a := usage.Attribution{SiteID: result.Metadata["site_id"], Consumer: result.Metadata["username"], KeyID: result.Metadata["key_id"], KeyName: result.Metadata["key_name"], Balanced: keyAutoBalance}
		c.Request = c.Request.WithContext(usage.WithAttribution(c.Request.Context(), a))
		started := time.Now()
		model := ""
		if c.Request.Method == http.MethodPost && c.ContentType() == "application/json" {
			body, errRead := io.ReadAll(c.Request.Body)
			if errRead != nil {
				c.AbortWithStatusJSON(400, gin.H{"error": "could not read request"})
				return
			}
			c.Request.Body = io.NopCloser(bytes.NewReader(body))
			model = gjson.GetBytes(body, "model").String()
		} else {
			model = c.Query("model")
		}
		c.Next()
		if c.Request.Method == http.MethodPost || (c.Request.Method == http.MethodGet && c.Writer.Status() == http.StatusSwitchingProtocols) {
			event := multiuser.CallEvent{ID: logging.GetGinRequestID(c), At: started.UTC(), Consumer: a.Consumer, KeyID: a.KeyID, KeyName: a.KeyName, Endpoint: c.Request.Method + " " + c.Request.URL.Path, Model: model, Status: c.Writer.Status(), LatencyMS: time.Since(started).Milliseconds()}
			if err := s.hub.Store.RecordCall(event); err != nil {
				log.WithError(err).Error("multi-user: failed to persist call")
			}
		}
	}
}
