package auth

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// cooldownReprobeConfigPollInterval bounds how often the re-probe loop
// re-reads the configured interval while re-probing is disabled.
const cooldownReprobeConfigPollInterval = time.Minute

// reprobeCandidate identifies one expired cooldown that should be actively
// re-verified with a lightweight upstream request.
type reprobeCandidate struct {
	auth *Auth
	// routeModel is the model key to probe for model-scoped cooldowns. Empty
	// means the credential-level cooldown expired and any registered model works.
	routeModel string
}

// startCooldownReprobe launches a background loop that periodically re-verifies
// expired cooldowns with a minimal synthetic request, reducing the chance that
// a client request acts as the canary. Only one loop is kept alive, and it is
// only started when Home mode is disabled.
func (m *Manager) startCooldownReprobe(parent context.Context) {
	if m == nil || parent == nil || m.HomeEnabled() {
		return
	}
	m.mu.Lock()
	cancelPrev := m.reprobeCancel
	m.reprobeCancel = nil
	m.mu.Unlock()
	if cancelPrev != nil {
		cancelPrev()
	}
	ctx, cancel := context.WithCancel(parent)
	m.mu.Lock()
	m.reprobeCancel = cancel
	m.mu.Unlock()
	go m.runCooldownReprobe(ctx)
}

// stopCooldownReprobe cancels the background re-probe loop, if running.
func (m *Manager) stopCooldownReprobe() {
	if m == nil {
		return
	}
	m.mu.Lock()
	cancel := m.reprobeCancel
	m.reprobeCancel = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// cooldownReprobeInterval reads the configured sweep interval from the latest
// runtime config. A non-positive value disables re-probing.
func (m *Manager) cooldownReprobeInterval() time.Duration {
	cfg := m.runtimeConfigSnapshot()
	if cfg == nil {
		return 0
	}
	seconds := cfg.CooldownReprobeIntervalSeconds
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func (m *Manager) runCooldownReprobe(ctx context.Context) {
	lastSweep := time.Now()
	for {
		wait := cooldownReprobeConfigPollInterval
		if interval := m.cooldownReprobeInterval(); interval > 0 {
			if remaining := interval - time.Since(lastSweep); remaining < wait {
				wait = max(remaining, 0)
			}
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		interval := m.cooldownReprobeInterval()
		if ctx.Err() != nil || interval <= 0 || time.Since(lastSweep) < interval {
			continue
		}
		m.reprobeExpiredCooldowns(ctx)
		lastSweep = time.Now()
	}
}

// reprobeExpiredCooldowns scans for expired cooldown timers and probes each
// candidate once. Probes run sequentially so a slow upstream cannot fan out.
func (m *Manager) reprobeExpiredCooldowns(ctx context.Context) {
	candidates := m.collectReprobeCandidates(time.Now())
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			return
		}
		m.runReprobeCandidate(ctx, candidate)
	}
}

// reprobeExpiredTimer reports whether a cooldown marker with recorded retry
// timers has outlived all of them. Untimed permanent states never qualify.
func reprobeExpiredTimer(unavailable, quotaExceeded bool, nextRetryAfter, nextRecoverAt, now time.Time) bool {
	if !unavailable && !quotaExceeded {
		return false
	}
	if nextRetryAfter.IsZero() && nextRecoverAt.IsZero() {
		return false
	}
	return !nextRetryAfter.After(now) && !nextRecoverAt.After(now)
}

// collectReprobeCandidates returns auths whose recorded cooldown timers have
// elapsed but whose cooldown flags have not yet been cleared by traffic.
func (m *Manager) collectReprobeCandidates(now time.Time) []reprobeCandidate {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var candidates []reprobeCandidate
	for _, a := range m.auths {
		candidates = append(candidates, m.reprobeCandidatesForAuth(a, now)...)
	}
	return candidates
}

// reprobeCandidatesForAuth emits the probe candidates a single auth warrants
// right now. The caller must hold m.mu.
func (m *Manager) reprobeCandidatesForAuth(a *Auth, now time.Time) []reprobeCandidate {
	if a == nil || a.Disabled || a.Status == StatusDisabled || m.cooldownDisabledForAuth(a) || hasUnauthorizedAuthFailure(a) {
		return nil
	}
	if len(a.ModelStates) == 0 {
		if reprobeExpiredTimer(a.Unavailable, a.Quota.Exceeded, a.NextRetryAfter, a.Quota.NextRecoverAt, now) {
			return []reprobeCandidate{{auth: a.Clone()}}
		}
		return nil
	}
	// A still-active credential-level gate (e.g. credential quota) makes
	// per-model probes pointless until its own timer elapses.
	if blocked, _, _ := isAuthBlockedForModel(a, "", now); blocked {
		return nil
	}
	capable := reprobeChatCapableModels(a.ID)
	models := make([]string, 0, len(a.ModelStates))
	for model := range a.ModelStates {
		models = append(models, model)
	}
	sort.Strings(models)
	var candidates []reprobeCandidate
	emitted := false
	for _, model := range models {
		state := a.ModelStates[model]
		if state == nil || state.Status == StatusDisabled {
			continue
		}
		if _, ok := capable[canonicalModelKey(model)]; !ok {
			continue
		}
		if reprobeExpiredTimer(state.Unavailable, state.Quota.Exceeded, state.NextRetryAfter, state.Quota.NextRecoverAt, now) {
			candidates = append(candidates, reprobeCandidate{auth: a.Clone(), routeModel: model})
			emitted = true
		}
	}
	if !emitted && a.Unavailable {
		// Aggregate unavailability outlived the per-model timers (for
		// example after a state restore); verify the credential once so it
		// can rejoin the pool.
		if routeModel := firstReprobeModel(a, now); routeModel != "" {
			candidates = append(candidates, reprobeCandidate{auth: a.Clone(), routeModel: routeModel})
		}
	}
	return candidates
}

// reprobeCandidateStillWarranted reports whether the live auth still needs
// the collected probe. The caller must hold m.mu.
func (m *Manager) reprobeCandidateStillWarranted(live *Auth, candidate reprobeCandidate, now time.Time) bool {
	for _, reEmitted := range m.reprobeCandidatesForAuth(live, now) {
		if reEmitted.routeModel == candidate.routeModel {
			return true
		}
	}
	return false
}

// firstReprobeModel picks a deterministic registered model for probing an auth
// whose cooldown did not record a model key. Models still blocked by a live
// restriction are skipped so the probe cannot clear another cooldown early.
func firstReprobeModel(auth *Auth, now time.Time) string {
	if auth == nil {
		return ""
	}
	models := modelsForRegisteredAuth(auth.ID)
	if len(models) == 0 {
		return ""
	}
	capable := reprobeChatCapableModels(auth.ID)
	sort.Strings(models)
	for _, model := range models {
		if _, ok := capable[model]; !ok {
			continue
		}
		if blocked, _, _ := isAuthBlockedForModel(auth, model, now); !blocked {
			return model
		}
	}
	return ""
}

// reprobeChatCapableModels returns the canonical model keys registered for the
// auth whose catalog metadata marks them as chat-capable. Models whose
// capability is unknown (unregistered or user-defined compat entries) are
// excluded so a synthetic chat ping cannot re-cool a non-chat model.
func reprobeChatCapableModels(authID string) map[string]struct{} {
	reg := registry.GetGlobalRegistry()
	if reg == nil {
		return nil
	}
	infos := reg.GetModelsForClient(authID)
	capable := make(map[string]struct{}, len(infos))
	for _, info := range infos {
		if !reprobeChatCapableInfo(info) {
			continue
		}
		if key := canonicalModelKey(info.ID); key != "" {
			capable[key] = struct{}{}
		}
	}
	return capable
}

// reprobeChatCapableInfo reports whether catalog metadata proves the model can
// serve a chat completion. Image-endpoint, output-non-text, non-chat-method,
// compat, and user-defined models do not qualify because a chat-shaped probe
// would be rejected upstream.
func reprobeChatCapableInfo(info *registry.ModelInfo) bool {
	if info == nil || info.UserDefined || info.IsCompat {
		return false
	}
	if info.Type == registry.OpenAIImageModelType {
		return false
	}
	if len(info.SupportedOutputModalities) > 0 && !hasReprobeTextOutput(info.SupportedOutputModalities) {
		return false
	}
	if len(info.SupportedGenerationMethods) > 0 && !hasReprobeChatMethod(info.SupportedGenerationMethods) {
		return false
	}
	return true
}

func hasReprobeTextOutput(modalities []string) bool {
	for _, modality := range modalities {
		if strings.EqualFold(modality, "text") {
			return true
		}
	}
	return false
}

func hasReprobeChatMethod(methods []string) bool {
	for _, method := range methods {
		if strings.EqualFold(method, "generateContent") || strings.EqualFold(method, "streamGenerateContent") {
			return true
		}
	}
	return false
}

// runReprobeCandidate executes a single synthetic probe and records the result
// through the normal result path so cooldown bookkeeping, persistence, and
// scheduler updates stay identical to real traffic.
func (m *Manager) runReprobeCandidate(ctx context.Context, candidate reprobeCandidate) {
	auth := candidate.auth
	if m == nil || auth == nil || ctx.Err() != nil {
		return
	}
	routeModel := strings.TrimSpace(candidate.routeModel)
	if routeModel == "" {
		routeModel = firstReprobeModel(auth, time.Now())
	}
	if routeModel == "" {
		return
	}
	upstreamModel := strings.TrimSpace(m.ResolveExecutionModel(auth, routeModel))
	if upstreamModel == "" {
		upstreamModel = routeModel
	}
	provider := executorKeyFromAuth(auth)

	m.mu.RLock()
	executor, okExecutor := m.executorLocked(provider)
	m.mu.RUnlock()
	if !okExecutor || executor == nil {
		return
	}

	execCtx := ctx
	if rt := m.roundTripperFor(auth); rt != nil {
		execCtx = context.WithValue(execCtx, roundTripperContextKey{}, rt)
		execCtx = context.WithValue(execCtx, "cliproxy.roundtripper", rt)
	}
	execCtx = newUpstreamAttemptContext(execCtx)

	stateModel := m.stateModelForExecution(auth, routeModel, upstreamModel, false)

	execOpts := cliproxyexecutor.Options{
		Stream:         false,
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
		Metadata: map[string]any{
			cliproxyexecutor.RequestedModelMetadataKey:  routeModel,
			cliproxyexecutor.CooldownReprobeMetadataKey: true,
		},
	}

	m.mu.RLock()
	stillCandidate := m.reprobeCandidateStillWarranted(m.auths[auth.ID], candidate, time.Now())
	m.mu.RUnlock()
	if !stillCandidate {
		return
	}

	prepared, errPrepare := m.prepareRequestAuth(execCtx, executor, auth)
	if errPrepare != nil {
		result := Result{AuthID: auth.ID, Provider: provider, Success: false, Error: resultErrorFromError(errPrepare), Options: execOpts, CredentialVersion: auth.CredentialVersion, RegistrationEpoch: auth.RegistrationEpoch}
		m.MarkResult(execCtx, result)
		return
	}
	if prepared != nil {
		auth = prepared
	}
	executor = executorForAuth(executor, auth)

	payload, errPayload := buildReprobePayload(upstreamModel)
	if errPayload != nil {
		logEntryWithRequestID(execCtx).Debugf("cooldown re-probe payload build failed for %s: %v", provider, errPayload)
		return
	}
	execOpts.OriginalRequest = payload
	req := cliproxyexecutor.Request{Model: upstreamModel, Payload: payload}

	// Skip the probe when the live credential no longer warrants one: real
	// traffic may have cleared or refreshed the cooldown since collection.
	m.mu.RLock()
	stillCandidate = m.reprobeCandidateStillWarranted(m.auths[auth.ID], candidate, time.Now())
	m.mu.RUnlock()
	if !stillCandidate {
		return
	}

	resp, errExec := executor.Execute(execCtx, auth, req, execOpts)
	_ = resp
	result := Result{AuthID: auth.ID, Provider: provider, Model: stateModel, RouteModel: routeModel, Success: errExec == nil, Options: execOpts, CredentialVersion: auth.CredentialVersion, RegistrationEpoch: auth.RegistrationEpoch}
	if errExec != nil {
		result.Error = resultErrorFromError(errExec)
		if ra := retryAfterFromError(errExec); ra != nil {
			result.RetryAfter = ra
		}
		if isCredentialScopedError(errExec) {
			result.CredentialScope = true
		}
		action, okAction := matchRequestScopedErrorAction(auth, errExec, m.runtimeConfigSnapshot())
		applyRequestScopedActionToResult(action, okAction, &result)
	}
	m.MarkResult(execCtx, result)
	logEntryWithRequestID(execCtx).Debugf("cooldown re-probe for %s auth %s model %s finished (success=%v)", provider, auth.ID, routeModel, errExec == nil)
}

// buildReprobePayload builds a minimal OpenAI-compatible chat completion that
// executors translate into each provider's native format.
func buildReprobePayload(model string) ([]byte, error) {
	return json.Marshal(map[string]any{
		"model":      model,
		"messages":   []map[string]string{{"role": "user", "content": "ping"}},
		"max_tokens": 1,
		"stream":     false,
	})
}
