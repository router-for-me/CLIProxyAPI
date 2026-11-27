package engine

import (
	"context"
	"io"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	proxconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/prox/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/mgmt"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/router"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

var (
	defaultOnce   sync.Once
	defaultEngine *Engine
)

// Default returns the process-wide engine.
func Default() *Engine {
	defaultOnce.Do(func() {
		defaultEngine = New()
		coreusage.RegisterNamedPlugin(proxconfig.PluginID, defaultEngine)
	})
	return defaultEngine
}

// ApplyConfig reads plugins.configs.prox from the CPA config and applies it
// to the default engine, wiring manager as the credential source.
func ApplyConfig(cfg *config.Config, manager *coreauth.Manager) {
	e := Default()
	if manager != nil {
		e.SetHost(ManagerHost{Manager: manager})
	}
	enabled, raw := settingsFromConfig(cfg)
	if err := e.Apply(enabled, raw); err != nil {
		log.WithError(err).Warn("prox: invalid plugins.configs.prox settings; keeping previous settings")
	}
}

func settingsFromConfig(cfg *config.Config) (bool, []byte) {
	if cfg == nil || cfg.Plugins.Configs == nil {
		return false, nil
	}
	inst, ok := cfg.Plugins.Configs[proxconfig.PluginID]
	if !ok || inst.Enabled == nil || !*inst.Enabled {
		return false, nil
	}
	if inst.Raw.Kind == 0 {
		return true, nil
	}
	raw, err := yaml.Marshal(&inst.Raw)
	if err != nil {
		return true, nil
	}
	return true, raw
}

// Scheduler puts the prox engine in front of another plugin scheduler
// (normally CPA's plugin host). When prox declines a pick, next decides,
// and when next declines CPA's built-in routing decides.
type Scheduler struct {
	Engine *Engine
	Next   coreauth.PluginScheduler
}

// WrapScheduler returns the default engine in front of next. While prox is
// disabled it returns next unchanged, so stock CPA behavior is untouched.
// CPA re-installs the scheduler on every config apply, after ApplyConfig.
func WrapScheduler(next coreauth.PluginScheduler) coreauth.PluginScheduler {
	e := Default()
	if !e.Enabled() {
		return next
	}
	if isNilScheduler(next) {
		next = nil
	}
	return &Scheduler{Engine: e, Next: next}
}

func isNilScheduler(s coreauth.PluginScheduler) bool {
	if s == nil {
		return true
	}
	v := reflect.ValueOf(s)
	return v.Kind() == reflect.Pointer && v.IsNil()
}

// PickAuth implements coreauth.PluginScheduler.
func (s *Scheduler) PickAuth(ctx context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
	if s.Engine.Enabled() {
		if resp := s.Engine.Pick(req); resp.Handled {
			return resp, true, nil
		}
	}
	if s.nextHasScheduler() {
		return s.Next.PickAuth(ctx, req)
	}
	return pluginapi.SchedulerPickResponse{}, false, nil
}

func (s *Scheduler) nextHasScheduler() bool {
	if s.Next == nil {
		return false
	}
	if st, ok := s.Next.(interface{ HasScheduler() bool }); ok {
		return st.HasScheduler()
	}
	return true
}

// HasScheduler reports whether any scheduler is active.
func (s *Scheduler) HasScheduler() bool {
	return s.Engine.Enabled() || s.nextHasScheduler()
}

// SchedulerWantsAcrossPriorities implements coreauth.PluginSchedulerAcrossPriorities.
func (s *Scheduler) SchedulerWantsAcrossPriorities() bool {
	if s.Engine.ScheduleAcrossPriorities() {
		return true
	}
	if n, ok := s.Next.(coreauth.PluginSchedulerAcrossPriorities); ok && n != nil {
		return n.SchedulerWantsAcrossPriorities()
	}
	return false
}

// SchedulerWantsAllPriorityCandidates mirrors the plugin host's priority pool contract.
func (s *Scheduler) SchedulerWantsAllPriorityCandidates() bool {
	if s.SchedulerWantsAcrossPriorities() {
		return true
	}
	if n, ok := s.Next.(interface{ SchedulerWantsAllPriorityCandidates() bool }); ok && n != nil {
		return n.SchedulerWantsAllPriorityCandidates()
	}
	return false
}

// HasSchedulerContinuity delegates Codex continuity to the next scheduler.
func (s *Scheduler) HasSchedulerContinuity() bool {
	if n, ok := s.Next.(coreauth.PluginSchedulerContinuity); ok && n != nil {
		return n.HasSchedulerContinuity()
	}
	return false
}

// EvaluateSchedulerContinuity delegates Codex continuity to the next scheduler.
func (s *Scheduler) EvaluateSchedulerContinuity(ctx context.Context, req pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
	if n, ok := s.Next.(coreauth.PluginSchedulerContinuity); ok && n != nil {
		return n.EvaluateSchedulerContinuity(ctx, req)
	}
	return pluginapi.SchedulerContinuityResponse{}, false, nil
}

// ManagerHost reads credentials from CPA's in-memory auth manager, so tokens
// refreshed by CPA are used without re-reading files.
type ManagerHost struct {
	Manager *coreauth.Manager
}

// Roster implements Host.
func (h ManagerHost) Roster() ([]router.RosterEntry, error) {
	auths := h.Manager.List()
	out := make([]router.RosterEntry, 0, len(auths))
	for _, a := range auths {
		if a == nil {
			continue
		}
		a.EnsureIndex()
		accountType, account := a.AccountInfo()
		entry := router.RosterEntry{
			ID: a.ID, AuthIndex: a.Index, Provider: strings.TrimSpace(a.Provider), Name: a.FileName, Label: a.Label,
			Email: metaString(a, "email"), Account: account, AccountType: accountType, Status: string(a.Status),
			StatusMsg: a.StatusMessage, Disabled: a.Disabled, Unavailable: a.Unavailable, ModTime: a.UpdatedAt,
			NextRetry: a.NextRetryAfter, Path: attr(a, "path"), BaseURL: attr(a, "base_url"),
		}
		if entry.Name == "" {
			entry.Name = a.ID
		}
		if entry.BaseURL == "" {
			entry.BaseURL = metaString(a, "base_url")
		}
		entry.RuntimeOnly = entry.Path == ""
		if p := attr(a, "priority"); p != "" {
			entry.Priority, _ = strconv.Atoi(p)
		} else if v, ok := a.Metadata["priority"].(float64); ok {
			entry.Priority = int(v)
		}
		if entry.Path != "" {
			if info, err := os.Stat(entry.Path); err == nil {
				entry.Size = info.Size()
			}
		}
		out = append(out, entry)
	}
	return out, nil
}

// Credential implements Host. It returns the auth metadata (the auth file
// contents as CPA holds them) plus the account proxy and base URL.
func (h ManagerHost) Credential(authIndex string) ([]byte, error) {
	for _, a := range h.Manager.List() {
		if a == nil {
			continue
		}
		a.EnsureIndex()
		if a.Index != authIndex {
			continue
		}
		m := make(map[string]any, len(a.Metadata)+3)
		for k, v := range a.Metadata {
			m[k] = v
		}
		if _, ok := m["type"]; !ok {
			m["type"] = a.Provider
		}
		if a.ProxyURL != "" {
			m["proxy_url"] = a.ProxyURL
		}
		if b := attr(a, "base_url"); b != "" {
			m["base_url"] = b
		}
		if a.Disabled {
			m["disabled"] = true
		}
		return json.Marshal(m)
	}
	return nil, os.ErrNotExist
}

func attr(a *coreauth.Auth, key string) string {
	if a.Attributes == nil {
		return ""
	}
	return strings.TrimSpace(a.Attributes[key])
}

func metaString(a *coreauth.Auth, key string) string {
	if a.Metadata != nil {
		if v, ok := a.Metadata[key].(string); ok {
			return strings.TrimSpace(v)
		}
	}
	return attr(a, key)
}

// GinHandler serves the prox dashboard and JSON API. Requests under
// mgmt.APIBase must already be authenticated by the caller's middleware.
func GinHandler(c *gin.Context) {
	srv := Default().Server()
	if srv == nil {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "prox is not enabled; set plugins.configs.prox.enabled: true"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
	if err != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	resp := srv.Handle(pluginapi.ManagementRequest{
		Method: c.Request.Method, Path: c.Request.URL.Path, Headers: c.Request.Header,
		Query: c.Request.URL.Query(), Body: body,
	})
	for k, vs := range resp.Headers {
		for _, v := range vs {
			c.Writer.Header().Add(k, v)
		}
	}
	status := resp.StatusCode
	if status == 0 {
		status = http.StatusOK
	}
	c.Status(status)
	_, _ = c.Writer.Write(resp.Body)
	c.Abort()
}

// UIBase and APIBase are the dashboard and API mount points.
const (
	UIBase  = mgmt.ResourceBase
	APIBase = mgmt.APIBase
)
