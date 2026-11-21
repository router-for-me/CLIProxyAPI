package api

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	managementHandlers "github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/maintenance"
	log "github.com/sirupsen/logrus"
)

const drainPath = "/v8/management/server/drain"

// forkServerState holds the fork's Server fields. Server embeds it, so the
// upstream struct needs only one extra line.
type forkServerState struct {
	drain *maintenance.Controller
}

// installDrain wraps the finished HTTP server so maintenance can count and
// drain active work. NewServer calls it once, right after creating s.server.
func (s *Server) installDrain() {
	s.drain = maintenance.New()
	s.server.Handler = s.drain.Wrap(s.server.Handler, s.drainExempt)
	s.server.ConnContext = s.drain.ConnContext
	s.server.ConnState = s.drain.ConnState
}

// registerForkManagementV8Routes adds the fork's drain and routing status
// endpoints to the v8 management group.
func (s *Server) registerForkManagementV8Routes(v8 *gin.RouterGroup) {
	v8.GET("/server/drain", s.getDrain)
	v8.POST("/server/drain", s.beginDrain)
	v8.DELETE("/server/drain", s.cancelDrain)
	v8.POST("/server/drain/stop", s.stopDrained)
	v8.GET("/routing/status", s.mgmt.GetRoutingStatus)
}

func (s *Server) drainExempt(r *http.Request) bool {
	path := r.URL.Path
	if path == drainPath || path == drainPath+"/stop" {
		return true
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	// Only explicitly read-only endpoints bypass maintenance. In particular,
	// OAuth GETs can start exchanges and credential writes after returning.
	switch path {
	case "/", "/healthz", "/management.html",
		"/v8/management/config", "/v8/management/config.yaml", "/v8/management/routing/status",
		"/v8/management/server/latest-version", "/v0/management/config", "/v0/management/config.yaml",
		"/v0/management/latest-version":
		return true
	}
	return strings.HasPrefix(path, "/v8/management/config/")
}

func (s *Server) drainBlockers() []string {
	blockers := make([]string, 0)
	cfg := s.getConfig()
	if cfg == nil {
		return append(blockers, "configuration_unavailable")
	}
	if cfg.Home.Enabled || (s.handlers.AuthManager != nil && s.handlers.AuthManager.HomeEnabled()) {
		blockers = append(blockers, "home_enabled")
	}
	// Plugins can own work outside HTTP request lifetimes. Do not claim to drain it.
	if cfg.Plugins.Enabled {
		for _, item := range cfg.Plugins.Configs {
			if item.Enabled != nil && *item.Enabled {
				blockers = append(blockers, "plugins_enabled")
				break
			}
		}
	}
	if s.pluginHost != nil && s.pluginHost.HasLoadedPlugins() {
		blockers = append(blockers, "plugin_work_untracked")
	}
	if s.codexLiveHandler != nil && s.codexLiveHandler.ActiveSessions() > 0 {
		blockers = append(blockers, "realtime_sessions")
	}
	if managementHandlers.HasPendingOAuthSessions() {
		blockers = append(blockers, "oauth_sessions")
	}
	return blockers
}

func (s *Server) writeDrainStatus(c *gin.Context, code int) {
	status := s.drain.Status()
	blockers := s.drainBlockers()
	doc := ojson.NewDocString(`{}`)
	for key, value := range map[string]any{
		"schema_version": status.SchemaVersion, "instance_id": status.InstanceID,
		"draining": status.Draining, "stopping": status.Stopping, "lease_remaining_ms": status.LeaseRemainingMS,
		"active_requests": status.ActiveRequests, "websocket_sessions": status.WebsocketSessions,
		"active_websocket_turns": status.ActiveWebsocketTurns, "supported": len(blockers) == 0,
		"blockers": blockers, "ready": status.Ready && len(blockers) == 0,
	} {
		_ = doc.Set(key, value)
	}
	c.Header("Cache-Control", "no-store")
	body := doc.Bytes()
	// The stop handler flushes before closing the server. An explicit length
	// makes its reply complete without waiting for a final chunk after return.
	c.Header("Content-Length", strconv.Itoa(len(body)))
	c.Data(code, "application/json", body)
}

func drainOwner(c *gin.Context) (string, string, bool) {
	body, errRead := io.ReadAll(io.LimitReader(c.Request.Body, 4097))
	if errRead != nil || len(body) > 4096 || !ojson.ValidBytes(body) {
		c.String(http.StatusBadRequest, "invalid drain request")
		return "", "", false
	}
	doc := ojson.NewDoc(body)
	instance, owner := doc.Get("instance_id").String(), doc.Get("owner_token").String()
	if len(instance) != 36 || len(owner) < 32 || len(owner) > 128 {
		c.String(http.StatusBadRequest, "invalid drain owner")
		return "", "", false
	}
	return instance, owner, true
}

func (s *Server) getDrain(c *gin.Context) { s.writeDrainStatus(c, http.StatusOK) }

func (s *Server) beginDrain(c *gin.Context) {
	instance, owner, ok := drainOwner(c)
	if !ok {
		return
	}
	if len(s.drainBlockers()) > 0 {
		s.writeDrainStatus(c, http.StatusConflict)
		return
	}
	if _, ok = s.drain.Begin(instance, owner); !ok {
		s.writeDrainStatus(c, http.StatusConflict)
		return
	}
	s.writeDrainStatus(c, http.StatusOK)
}

func (s *Server) cancelDrain(c *gin.Context) {
	instance, owner, ok := drainOwner(c)
	if !ok {
		return
	}
	if !s.drain.Cancel(instance, owner) {
		s.writeDrainStatus(c, http.StatusConflict)
		return
	}
	s.writeDrainStatus(c, http.StatusOK)
}

// Stop is committed inside the proxy, rather than by killing a PID after a GET.
// This prevents lease expiration from admitting a response between ready and stop.
// A supervisor must restart the process on its newly selected retained binary.
func (s *Server) stopDrained(c *gin.Context) {
	instance, owner, ok := drainOwner(c)
	if !ok {
		return
	}
	if !s.drain.CommitStop(instance, owner, func() bool { return len(s.drainBlockers()) == 0 }) {
		s.writeDrainStatus(c, http.StatusConflict)
		return
	}
	s.writeDrainStatus(c, http.StatusAccepted)
	c.Writer.Flush()
	go func() {
		if errStop := s.Stop(context.Background()); errStop != nil {
			log.WithError(errStop).Error("failed to stop drained API server")
		}
	}()
}
