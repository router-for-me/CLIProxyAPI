package cliproxy

import (
	"context"
	"errors"
	"strings"
	"sync"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/keepalive"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// cacheKeepaliveProbeProvider is the auth provider a Claude Code keepalive probe
// routes through when the observation recorded none.
const cacheKeepaliveProbeProvider = "claude"

// cacheKeepaliveProber sends a probe through the ordinary execution path so it
// receives the same translation, cloaking, header and cache_control handling a
// real request gets. The credential is pinned to the one the session is bound to.
type cacheKeepaliveProber struct {
	manager *coreauth.Manager
}

func (p cacheKeepaliveProber) Probe(ctx context.Context, probe keepalive.ProbeRequest) (keepalive.ProbeResult, error) {
	if p.manager == nil {
		return keepalive.ProbeResult{}, errors.New("cache keepalive: no auth manager")
	}
	provider := strings.TrimSpace(probe.Provider)
	if provider == "" || strings.EqualFold(provider, "mixed") {
		provider = cacheKeepaliveProbeProvider
	}
	claudeFormat := sdktranslator.FromString("claude")
	req := coreexecutor.Request{Model: probe.Model, Payload: probe.Body}
	opts := coreexecutor.Options{
		Stream:          false,
		OriginalRequest: probe.Body,
		SourceFormat:    claudeFormat,
		ResponseFormat:  claudeFormat,
		Headers:         probe.Headers,
		Metadata: map[string]any{
			coreexecutor.PinnedAuthMetadataKey:     probe.AuthID,
			coreexecutor.RequestedModelMetadataKey: probe.Model,
			keepalive.ProbeMetadataKey:             true,
		},
	}
	resp, err := p.manager.Execute(ctx, []string{provider}, req, opts)
	if err != nil {
		return keepalive.ProbeResult{}, err
	}
	usage := gjson.GetBytes(resp.Payload, "usage")
	return keepalive.ProbeResult{
		CacheReadInputTokens:     usage.Get("cache_read_input_tokens").Int(),
		CacheCreationInputTokens: usage.Get("cache_creation_input_tokens").Int(),
		Diagnosis:                claudeCacheDiagnosis(resp.Payload),
	}, nil
}

// claudeCacheDiagnosis extracts the upstream cache-miss explanation when the
// account carries the cache-diagnosis beta and the response supplied one. The
// field has moved between response shapes, so every known location is tried and
// an absent diagnosis is simply empty.
func claudeCacheDiagnosis(payload []byte) string {
	for _, path := range []string{
		"usage.cache_diagnosis.reason",
		"usage.cache_diagnosis",
		"cache_diagnosis.reason",
		"cache_diagnosis",
	} {
		value := gjson.GetBytes(payload, path)
		if !value.Exists() {
			continue
		}
		if value.IsObject() || value.IsArray() {
			return strings.TrimSpace(value.Raw)
		}
		if text := strings.TrimSpace(value.String()); text != "" {
			return text
		}
	}
	return ""
}

// cacheKeepaliveBinding answers whether the session is still bound to the
// credential the probe was recorded against.
type cacheKeepaliveBinding struct {
	manager *coreauth.Manager
}

func (b cacheKeepaliveBinding) SessionBinding(provider, sessionID, model string) (string, keepalive.BindingState) {
	if b.manager == nil {
		return "", keepalive.BindingUnknown
	}
	lookup, ok := b.manager.SessionBindingLookup()
	if !ok {
		// Routing is not session-sticky, so there is no binding to lose.
		return "", keepalive.BindingUnknown
	}
	authID, bound := lookup.BoundAuthID(provider, sessionID, model)
	if !bound {
		return "", keepalive.BindingLost
	}
	return authID, keepalive.BindingBound
}

// cacheKeepaliveLastApplied guards the announcement of the keepalive settings so
// startup and the config-runtime pass that follows it do not log the same state
// twice. It tracks the process-wide scheduler, which is itself a package global.
var (
	cacheKeepaliveAnnounceMu sync.Mutex
	cacheKeepaliveAnnounced  *internalconfig.ClaudeCodeCacheKeepaliveConfig
)

// announceCacheKeepalive reports whether the settings changed since the last call.
func announceCacheKeepalive(settings internalconfig.ClaudeCodeCacheKeepaliveConfig) bool {
	cacheKeepaliveAnnounceMu.Lock()
	defer cacheKeepaliveAnnounceMu.Unlock()
	previous := cacheKeepaliveAnnounced
	cacheKeepaliveAnnounced = &settings
	if previous == nil {
		return true
	}
	return previous.Enabled != settings.Enabled ||
		previous.BeforeExpiry != settings.BeforeExpiry ||
		previous.OnlyWhenAgentsActive != settings.OnlyWhenAgentsActive ||
		previous.Liveness != settings.Liveness ||
		previous.MaxProbes != settings.MaxProbes ||
		previous.MaxTokens != settings.MaxTokens
}

// applyCacheKeepaliveConfig installs or reconfigures the prompt-cache keepalive
// scheduler. It is safe to call on every config reload.
func (s *Service) applyCacheKeepaliveConfig(cfg *config.Config) {
	if s == nil {
		return
	}
	settings := internalconfig.ClaudeCodeCacheKeepaliveConfig{}
	if cfg != nil {
		settings = cfg.ClaudeCode.CacheKeepalive
	}
	settings = settings.WithDefaults()

	changed := announceCacheKeepalive(settings)
	if !settings.Enabled {
		if keepalive.Default() != nil {
			log.Info("cache-keepalive: disabled")
		}
		keepalive.SetDefault(nil)
		return
	}
	if s.coreManager == nil {
		log.Warn("cache-keepalive: enabled but no auth manager is available; keepalive stays off")
		keepalive.SetDefault(nil)
		return
	}

	runtimeCfg := keepalive.Config{
		Enabled:              true,
		BeforeExpiry:         settings.BeforeExpiry,
		OnlyWhenAgentsActive: settings.OnlyWhenAgentsActive,
		MaxProbes:            settings.MaxProbes,
		MaxTokens:            settings.MaxTokens,
	}

	var liveness keepalive.Liveness
	switch settings.Liveness {
	case internalconfig.ClaudeCodeKeepaliveLivenessAlways:
		liveness = keepalive.AlwaysLive{}
	default:
		liveness = keepalive.NewClaudeCodeTasksLiveness(settings.TaskStateDirs, settings.TaskOutputDirs)
	}

	scheduler := keepalive.Default()
	if scheduler == nil {
		scheduler = keepalive.New(runtimeCfg)
		scheduler.SetProber(cacheKeepaliveProber{manager: s.coreManager})
		scheduler.SetBinding(cacheKeepaliveBinding{manager: s.coreManager})
		scheduler.SetLiveness(liveness)
		keepalive.SetDefault(scheduler)
	} else {
		scheduler.SetProber(cacheKeepaliveProber{manager: s.coreManager})
		scheduler.SetBinding(cacheKeepaliveBinding{manager: s.coreManager})
		scheduler.SetLiveness(liveness)
		scheduler.ApplyConfig(runtimeCfg)
	}
	if changed {
		log.Infof("cache-keepalive: enabled | before-expiry=%s only-when-agents-active=%t liveness=%s max-probes=%d max-tokens=%d",
			settings.BeforeExpiry, settings.OnlyWhenAgentsActive, settings.Liveness, settings.MaxProbes, settings.MaxTokens)
	}
}
