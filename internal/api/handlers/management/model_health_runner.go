package management

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// modelHealthProbeTimeout bounds a single model health-check probe (credential
// acquisition + one tiny generation). This is an intentional exception to the
// "no timeouts after a connection is established" rule: a health check must
// never pin the sweep goroutine against an unresponsive upstream for long, and
// there is no downstream caller to time out.
const modelHealthProbeTimeout = 45 * time.Second

// modelHealthSlowResponseMs is the response-time threshold (wall-clock from
// probe start to response) above which a successful probe is classified as
// "degraded" rather than "operational".
const modelHealthSlowResponseMs = 10_000

// modelHealthSlowTPS is the tokens-per-second floor below which a successful
// probe is classified as "degraded" (only applied when completion tokens were
// measured and elapsed > 0).
const modelHealthSlowTPS = 1.0

// modelHealthSweepRunning guards the probe runner against overlapping runs so
// a long sweep (many models) plus a "Run now" click does not double up.
var modelHealthSweepRunning sync.Mutex

// modelHealthLastRunAt records when the most recent sweep completed. Surfaced
// via GET /v0/management/model-health so the dashboard can show "last checked
// N ago" without paging the log. Guarded by modelHealthLastRunMu.
var (
	modelHealthLastRunMu  sync.RWMutex
	modelHealthLastRunAt  time.Time
	modelHealthLastRunFor string // human-readable summary of what was probed
)

// recordModelHealthRun marks the last-run timestamp + summary. Safe to call
// from the sweep goroutine or a "Run now" trigger.
func recordModelHealthRun(at time.Time, summary string) {
	modelHealthLastRunMu.Lock()
	modelHealthLastRunAt = at
	modelHealthLastRunFor = summary
	modelHealthLastRunMu.Unlock()
}

// ModelHealthLastRun returns the timestamp + summary of the most recent sweep.
// Used by the list handler to populate the dashboard header without paging.
func ModelHealthLastRun() (time.Time, string) {
	modelHealthLastRunMu.RLock()
	defer modelHealthLastRunMu.RUnlock()
	return modelHealthLastRunAt, modelHealthLastRunFor
}

// runHealthChecks probes every eligible live model id with a tiny inference
// request and records the outcome (status, response time, TPS, token usage) to
// the model_health snapshot + model_health_log history tables. It iterates the
// live registry (AvailableModelIDList → — the "Live" Global Model IDs), skips
// operator-excluded model ids, and reuses the production auth-manager execution
// path so the measured numbers reflect what a real caller sees.
//
// One probe per model id per sweep: it tries the model's providers in registry
// priority order (GetModelProviders) and stops at the first that returns a
// response, capturing that provider name. Each model is probed sequentially to
// keep upstream quota load bounded and predictable.
//
// This function is safe to call concurrently; an in-flight sweep blocks a
// second invocation via modelHealthSweepRunning so a manual "Run now" does not
// double up with the scheduled sweep.
func (h *Handler) runHealthChecks(ctx context.Context) {
	if !modelHealthSweepRunning.TryLock() {
		log.Debug("model health: sweep already in progress; skipping")
		return
	}
	defer modelHealthSweepRunning.Unlock()

	mhs, settings, ok := h.modelHealthProbeContext(ctx)
	if !ok {
		return
	}
	if !settings.Enabled {
		log.Debug("model health: sweep disabled by settings; skipping")
		return
	}

	liveIDs := registry.GetGlobalRegistry().AvailableModelIDList()
	if len(liveIDs) == 0 {
		log.Debug("model health: no live models registered; skipping sweep")
		recordModelHealthRun(time.Now(), "no live models")
		return
	}

	excluded := make(map[string]struct{}, len(settings.ExcludedModels))
	for _, m := range settings.ExcludedModels {
		if v := strings.TrimSpace(m); v != "" {
			excluded[strings.ToLower(v)] = struct{}{}
		}
	}

	started := time.Now()
	successCount, degradedCount, failCount, skippedCount := 0, 0, 0, 0
	for _, modelID := range liveIDs {
		if ctx.Err() != nil {
			break
		}
		if _, skip := excluded[strings.ToLower(modelID)]; skip {
			skippedCount++
			continue
		}
		row := h.probeModel(ctx, modelID, settings.MaxTokens)
		// Persist with a bounded-time context so a slow DB never pins the
		// sweep goroutine between probes (mirrors SyncLogSink's insert guard).
		persistCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := mhs.RecordResult(persistCtx, row); err != nil {
			log.WithError(err).WithField("model", modelID).Debug("model health: record result failed")
		}
		cancel()
		switch row.Status {
		case store.ModelHealthStatusOperational:
			successCount++
		case store.ModelHealthStatusDegraded:
			degradedCount++
		default:
			failCount++
		}
	}

	summary := fmt.Sprintf("probed %d, operational %d, degraded %d, unavailable %d, skipped %d",
		successCount+degradedCount+failCount, successCount, degradedCount, failCount, skippedCount)
	recordModelHealthRun(time.Now(), summary)
	log.WithField("elapsed", time.Since(started).String()).WithField("summary", summary).Debug("model health: sweep completed")
}

// modelHealthProbeContext resolves the store + current settings, returning
// false (after logging) when the PG backend is unavailable. Settings are
// fetched fresh each sweep so operator changes (enable/disable, interval,
// excludes) take effect on the next tick without a restart.
func (h *Handler) modelHealthProbeContext(ctx context.Context) (*store.ModelHealthStore, store.ModelHealthSettings, bool) {
	if h == nil {
		return nil, store.ModelHealthSettings{}, false
	}
	h.mu.Lock()
	s := h.pgModelHealth
	h.mu.Unlock()
	if s == nil {
		return nil, store.ModelHealthSettings{}, false
	}
	settings, err := s.GetSettings(ctx)
	if err != nil {
		log.WithError(err).Warn("model health: load settings failed; using defaults")
		settings = store.ModelHealthSettings{Enabled: true, IntervalSeconds: 900, ExcludedModels: []string{}, MaxTokens: 1}
	}
	return s, settings, true
}

// probeModel performs a single timed inference ping against one provider of
// the model and returns a ModelHealthRow ready to persist. It resolves the
// model's live providers via the registry and tries them in priority order;
// the first to respond wins (its provider name is recorded). If every provider
// fails, the last error is recorded with status "unavailable".
func (h *Handler) probeModel(ctx context.Context, modelID string, maxTokens int) store.ModelHealthRow {
	row := store.ModelHealthRow{
		ModelID:   modelID,
		Status:    store.ModelHealthStatusUnavailable,
		CheckedAt: time.Now().UTC(),
	}
	if maxTokens < 1 {
		maxTokens = 1
	}
	if h == nil || h.authManager == nil {
		row.ErrorMessage = "auth manager unavailable"
		return row
	}

	providers := registry.GetGlobalRegistry().GetModelProviders(modelID)
	if len(providers) == 0 {
		row.ErrorMessage = "no live provider serves this model"
		return row
	}

	// The fixed probe prompt surfaced verbatim in the recorded detail row. Kept
	// small + deterministic so the stored prompt_message is cheap to seal and
	// an operator can tell at a glance what the probe asked.
	const probePromptMessage = "ping"
	payload := []byte(fmt.Sprintf(
		`{"model":%q,"messages":[{"role":"user","content":%q}],"max_tokens":%d,"stream":false}`,
		modelID, probePromptMessage, maxTokens,
	))

	var lastErr error
	for _, provider := range providers {
		if ctx.Err() != nil {
			row.ErrorMessage = ctx.Err().Error()
			return row
		}
		probeCtx, cancel := context.WithTimeout(ctx, modelHealthProbeTimeout)
		req := cliproxyexecutor.Request{
			Model:   modelID,
			Payload: payload,
			Format:  sdktranslator.Format(""), // empty = the manager infers from SourceFormat/registry
		}
		// Capture which upstream auth the scheduler picked for this probe via
		// the selected-auth callback so the recorded detail row attributes the
		// outcome to a specific credential (not just the provider key).
		selectedAuthID := ""
		opts := cliproxyexecutor.Options{
			Stream:       false,
			SourceFormat: sdktranslator.FromString("openai"),
			Metadata: map[string]any{
				cliproxyexecutor.RequestedModelMetadataKey: modelID,
			},
		}
		opts.Metadata[cliproxyexecutor.SelectedAuthCallbackMetadataKey] = func(authID string) {
			selectedAuthID = authID
		}
		start := time.Now()
		resp, err := h.authManager.Execute(probeCtx, []string{provider}, req, opts)
		elapsed := time.Since(start)
		cancel()
		if err != nil {
			lastErr = err
			continue
		}
		row.Provider = provider
		row.ResponseTimeMs = elapsed.Milliseconds()
		promptTokens, completionTokens := parseOpenAIUsage(resp.Payload)
		row.PromptTokens = promptTokens
		row.CompletionTokens = completionTokens
		if elapsed > 0 && completionTokens > 0 {
			tps := float64(completionTokens) / elapsed.Seconds()
			row.TokensPerSecond = &tps
		}
		// Record the actual probe prompt + the model-generated completion text
		// so an operator auditing a check can see exactly what was asked and
		// answered. Both are truncated + sealed at rest by the store.
		row.PromptMessage = probePromptMessage
		row.Completion = extractOpenAICompletionText(resp.Payload)
		// Upstream detail: provider + the selected auth id + the resolved
		// upstream model (recorded by the manager into opts.Metadata after
		// execution; falls back to the requested model when unavailable).
		row.UpstreamProvider = provider
		row.UpstreamAuthID = selectedAuthID
		row.UpstreamModel = extractUpstreamModel(opts.Metadata, modelID)
		row.Status = classifyModelHealth(row.ResponseTimeMs, row.TokensPerSecond)
		row.Success = true
		return row
	}

	// Every provider failed.
	if lastErr != nil {
		row.ErrorMessage = truncateForHealthLog(lastErr.Error())
	}
	return row
}

// classifyModelHealth maps a successful probe's measured metrics to a status
// label. A successful but slow or low-TPS probe is "degraded"; otherwise
// "operational". TPS is only considered when measured (non-nil).
func classifyModelHealth(responseTimeMs int64, tps *float64) string {
	if responseTimeMs > modelHealthSlowResponseMs {
		return store.ModelHealthStatusDegraded
	}
	if tps != nil && *tps < modelHealthSlowTPS {
		return store.ModelHealthStatusDegraded
	}
	return store.ModelHealthStatusOperational
}

// parseOpenAIUsage extracts prompt_tokens and completion_tokens from an
// OpenAI-compatible chat-completion response body. Returns zeros when the
// usage block is absent (some providers omit it for tiny completions); the
// caller treats zero completion tokens as "TPS not measured".
func parseOpenAIUsage(payload []byte) (prompt, completion int) {
	if len(payload) == 0 {
		return 0, 0
	}
	usage := gjson.GetBytes(payload, "usage")
	if !usage.Exists() {
		return 0, 0
	}
	if v := usage.Get("prompt_tokens"); v.Exists() {
		prompt = int(v.Int())
	}
	if v := usage.Get("completion_tokens"); v.Exists() {
		completion = int(v.Int())
	}
	return prompt, completion
}

// truncateForHealthLog bounds an error message before it is persisted (and
// sealed) so a single verbose upstream error cannot bloat the log table. The
// final sealing happens in the store.
func truncateForHealthLog(s string) string {
	const max = 2048
	if len(s) > max {
		return s[:max] + "…[truncated]"
	}
	return s
}

// extractOpenAICompletionText pulls the generated assistant content out of an
// OpenAI-compatible chat-completion response body. Concatenates each choice's
// message.content (a real response may carry multiple choices; the health
// probe uses max_tokens so completion is usually a few tokens). Returns "" when
// the body has no choices/content (e.g. a provider that omits content for tiny
// completions); the caller treats that as "completion text not available".
func extractOpenAICompletionText(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	choices := gjson.GetBytes(payload, "choices")
	if !choices.IsArray() {
		return ""
	}
	var b strings.Builder
	choices.ForEach(func(_, choice gjson.Result) bool {
		if v := choice.Get("message.content"); v.Exists() && v.Type == gjson.String {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(v.String())
		}
		return true
	})
	return b.String()
}

// extractUpstreamModel reads the upstream/translated model that the auth
// manager resolved for the probe from the execution metadata, falling back to
// the original requested model when the manager did not record one. The
// manager sets SelectedAuthMetadataKey (+ the selected-auth callback); the
// resolved upstream model is reflected back through opts.Metadata under the
// requested-model key after execution completes.
func extractUpstreamModel(meta map[string]any, fallback string) string {
	if meta == nil {
		return fallback
	}
	if v, ok := meta[cliproxyexecutor.RequestedModelMetadataKey]; ok {
		if s, okStr := v.(string); okStr && s != "" {
			return s
		}
	}
	return fallback
}

// StartModelHealthSweep launches a background goroutine that periodically
// probes every eligible live model and records the outcomes. The cadence is
// re-read from settings on each tick so operator changes take effect without a
// restart. No-op (returns immediately) when the store is nil so callers can
// unconditionally invoke it. Mirrors StartSyncLogSweep / StartModelHealthRetentionSweep.
//
// It is launched from server.go wiring after the model-health store is set so
// the sweep only runs on PG-configured deployments.
func (h *Handler) StartModelHealthSweep() {
	if h == nil {
		return
	}
	h.mu.Lock()
	s := h.pgModelHealth
	h.mu.Unlock()
	if s == nil {
		return
	}
	go func() {
		// Run an initial probe shortly after startup so the dashboard is not
		// empty until the first interval elapses. A short delay lets the auth
		// manager finish wiring auths at boot.
		initialDelay := 5 * time.Second
		timer := time.NewTimer(initialDelay)
		defer timer.Stop()
		for {
			select {
			case <-timer.C:
				h.runHealthChecks(context.Background())
			}
			// Re-read the interval for the next tick.
			settings, err := s.GetSettings(context.Background())
			interval := time.Duration(settings.IntervalSeconds) * time.Second
			if err != nil || settings.IntervalSeconds <= 0 || interval <= 0 {
				interval = 15 * time.Minute
			}
			timer.Reset(interval)
		}
	}()
}
