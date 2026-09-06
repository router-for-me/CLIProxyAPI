package cliproxy

import (
	"bytes"
	"context"
	"errors"
	"slices"
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
	reason, missedTokens := claudeCacheMissReason(resp.Payload)
	return keepalive.ProbeResult{
		CacheReadInputTokens:     usage.Get("cache_read_input_tokens").Int(),
		CacheCreationInputTokens: usage.Get("cache_creation_input_tokens").Int(),
		Diagnosis:                reason,
		CacheMissedInputTokens:   missedTokens,
	}, nil
}

// claudeCacheMissReason reads the cache-miss diagnostics a Claude response
// carries when the account has the cache-diagnosis beta.
//
// Two confirmed shapes, both captured on the wire:
//
//	non-streaming: {"usage":{...},"diagnostics":{"cache_miss_reason":{"type":"messages_changed","cache_missed_input_tokens":25154}}}
//	streaming:     the identical object inside the message_start event's "message"
//
// This duplicates applyClaudeCacheMissReason in
// internal/runtime/executor/helps/claude_cache_diagnostics.go on the cache-stats
// branch. That file is not on this branch, so the two paths are read here
// directly; fold them into the shared helper once both land.
func claudeCacheMissReason(payload []byte) (string, int64) {
	if len(payload) == 0 {
		return "", 0
	}
	if gjson.ValidBytes(payload) {
		root := gjson.ParseBytes(payload)
		// Non-streaming: diagnostics sits beside usage at the top level.
		if reason, tokens := cacheMissReasonAt(root); reason != "" || tokens != 0 {
			return reason, tokens
		}
		// A whole message_start object handed over directly.
		if reason, tokens := cacheMissReasonAt(root.Get("message")); reason != "" || tokens != 0 {
			return reason, tokens
		}
		return "", 0
	}
	// Streaming: scan the SSE for the message_start event that carries it.
	for _, line := range bytes.Split(payload, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		event := bytes.TrimSpace(line[len("data:"):])
		if len(event) == 0 || !gjson.ValidBytes(event) {
			continue
		}
		root := gjson.ParseBytes(event)
		if root.Get("type").String() != "message_start" {
			continue
		}
		if reason, tokens := cacheMissReasonAt(root.Get("message")); reason != "" || tokens != 0 {
			return reason, tokens
		}
	}
	return "", 0
}

func cacheMissReasonAt(node gjson.Result) (string, int64) {
	reason := node.Get("diagnostics.cache_miss_reason")
	if !reason.Exists() {
		return "", 0
	}
	return strings.TrimSpace(reason.Get("type").String()), reason.Get("cache_missed_input_tokens").Int()
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
		previous.BeforeExpiry5m != settings.BeforeExpiry5m ||
		previous.Probe5m != settings.Probe5m ||
		!slices.Equal(previous.Probe5mModels, settings.Probe5mModels) ||
		previous.OnlyWhenAgentsActive != settings.OnlyWhenAgentsActive ||
		previous.Liveness != settings.Liveness ||
		previous.AgentIdleWindow != settings.AgentIdleWindow ||
		previous.MaxProbes != settings.MaxProbes ||
		previous.MaxProbes5m != settings.MaxProbes5m ||
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
		BeforeExpiry5m:       settings.BeforeExpiry5m,
		Probe5m:              settings.Probe5m,
		Probe5mModels:        settings.Probe5mModels,
		OnlyWhenAgentsActive: settings.OnlyWhenAgentsActive,
		AgentIdleWindow:      settings.AgentIdleWindow,
		MaxProbes:            settings.MaxProbes,
		MaxProbes5m:          settings.MaxProbes5m,
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
		log.Infof("cache-keepalive: enabled | before-expiry=%s before-expiry-5m=%s probe-5m=%s only-when-agents-active=%t liveness=%s agent-idle-window=%s max-probes=%d max-probes-5m=%d max-tokens=%d",
			settings.BeforeExpiry, settings.BeforeExpiry5m, settings.Probe5m, settings.OnlyWhenAgentsActive, settings.Liveness,
			settings.AgentIdleWindow, settings.MaxProbes, settings.MaxProbes5m, settings.MaxTokens)
	}
}
