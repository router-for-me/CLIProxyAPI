package auth

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type antigravityModelStateContextKey struct{}

type antigravityAttemptedModelsContextKey struct{}

type antigravityAttemptedModels struct {
	mu     sync.Mutex
	models map[string]string
}

func withAntigravityAttemptedModels(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Value(antigravityAttemptedModelsContextKey{}).(*antigravityAttemptedModels); ok {
		return ctx
	}
	return context.WithValue(ctx, antigravityAttemptedModelsContextKey{}, &antigravityAttemptedModels{})
}

func recordAntigravityAttemptedModel(ctx context.Context, authID, model string) {
	if ctx == nil || authID == "" {
		return
	}
	modelKey := canonicalModelKey(model)
	if modelKey == "" {
		return
	}
	tracker, ok := ctx.Value(antigravityAttemptedModelsContextKey{}).(*antigravityAttemptedModels)
	if !ok || tracker == nil {
		return
	}
	tracker.mu.Lock()
	if tracker.models == nil {
		tracker.models = make(map[string]string)
	}
	tracker.models[authID] = modelKey
	tracker.mu.Unlock()
}

func antigravityAttemptedModelForAuth(ctx context.Context, authID string) string {
	if ctx == nil || authID == "" {
		return ""
	}
	tracker, ok := ctx.Value(antigravityAttemptedModelsContextKey{}).(*antigravityAttemptedModels)
	if !ok || tracker == nil {
		return ""
	}
	tracker.mu.Lock()
	model := tracker.models[authID]
	tracker.mu.Unlock()
	return model
}

func recordAntigravityAttemptedModelFromAttempt(ctx, attemptCtx context.Context, provider, authID, fallbackModel string, err error) {
	if !strings.EqualFold(strings.TrimSpace(provider), "antigravity") || authID == "" {
		return
	}
	if model := antigravityResultModel(attemptCtx, Result{Provider: "antigravity"}); model != "" {
		recordAntigravityAttemptedModel(ctx, authID, model)
		return
	}
	if err != nil {
		if _, rejected := errors.AsType[antigravityTargetRejectedError](err); rejected {
			var cooldownErr *modelCooldownError
			if errors.As(err, &cooldownErr) && cooldownErr != nil && cooldownErr.model != "" {
				recordAntigravityAttemptedModel(ctx, authID, cooldownErr.model)
			}
			return
		}
	}
	if fallbackModel != "" {
		recordAntigravityAttemptedModel(ctx, authID, fallbackModel)
	}
}

func (m *Manager) withAntigravityModelState(ctx context.Context, auth *Auth) context.Context {
	if auth == nil || auth.Provider != "antigravity" || !m.localExecutionAllowed() {
		return ctx
	}
	return context.WithValue(ctx, antigravityModelStateContextKey{}, m)
}

// CheckAntigravityTargetModel checks model-level admission for a late variant
// without reinterpreting credential-level admission or reopening the catalog.
func CheckAntigravityTargetModel(ctx context.Context, auth *Auth, model string, now time.Time, useCredits bool) error {
	if ctx != nil && auth != nil {
		if m, ok := ctx.Value(antigravityModelStateContextKey{}).(*Manager); ok {
			m.mu.RLock()
			defer m.mu.RUnlock()
			// The source model's scheduler shard may have an older copy of the
			// target state. Use the live state only for the same credential.
			if current := m.auths[auth.ID]; current != nil && current.RegistrationEpoch == auth.RegistrationEpoch && current.CredentialVersion == auth.CredentialVersion {
				auth = current
			}
		}
	}
	if state := existingModelState(auth, model); state != nil {
		blocked, _, next := availabilityBlock(state.Unavailable, state.Quota.Exceeded, state.NextRetryAfter, state.Quota.NextRecoverAt, now)
		if state.Status == StatusDisabled || (blocked && next.IsZero()) {
			recordAntigravityAttemptedModel(ctx, auth.ID, model)
			return &Error{Message: "selected Antigravity model is unavailable", HTTPStatus: http.StatusServiceUnavailable}
		}
		if blocked && !(useCredits && antigravityCreditsAllowsTargetQuota(auth, state, now)) {
			recordAntigravityAttemptedModel(ctx, auth.ID, model)
			return &trustedRetryAfterError{cause: newModelCooldownError(model, "antigravity", next.Sub(now)), deadline: next}
		}
	}
	return nil
}

// An admitted credits retry can pay for model quota, but not bypass an
// independent, forced, or credential-level cooldown on the selected variant.
func antigravityCreditsAllowsTargetQuota(auth *Auth, state *ModelState, now time.Time) bool {
	if auth.Disabled || auth.Status == StatusDisabled || hasUnauthorizedAuthFailure(auth) ||
		!state.Quota.Exceeded || state.Quota.Reason != "quota" || !state.Quota.NextRecoverAt.After(now) ||
		state.NextRetryAfter.After(state.Quota.NextRecoverAt) || state.nonQuotaCooldownUntil.After(now) ||
		effectiveModelForceUntil(state).After(now) || !isQuotaCooldownResultError(state.LastError) {
		return false
	}
	var retryAfter *time.Duration
	if hint := state.LastError.Metadata[antigravityCreditsRetryHintKey]; hint != "" {
		if nanos, errParse := strconv.ParseInt(hint, 10, 64); errParse == nil {
			delay := time.Duration(nanos)
			retryAfter = &delay
		}
	}
	return isAntigravityCreditsQuota429(state.LastError, retryAfter)
}

const antigravityCreditsRetryHintKey = "antigravity_credits_retry_hint_ns"

// Keep the upstream retry hint for the original admission rules; the remaining
// cooldown is not the same as the retry hint received when it began.
func recordAntigravityCreditsRetryHint(stateErr *Error, result Result) {
	if result.Provider != "antigravity" || result.RetryAfter == nil || !isQuotaCooldownResultError(stateErr) {
		return
	}
	if stateErr.Metadata == nil {
		stateErr.Metadata = make(map[string]string)
	}
	stateErr.Metadata[antigravityCreditsRetryHintKey] = strconv.FormatInt(int64(*result.RetryAfter), 10)
}

const antigravityTargetRejectedCode = "antigravity_target_rejected"

type antigravityTargetRejectedError struct{ error }

func (e antigravityTargetRejectedError) Unwrap() error { return e.error }

// RejectAntigravityTarget marks an already-blocked target as a routing miss,
// preserving the status and retry information without generating new feedback.
func RejectAntigravityTarget(err error) error {
	return antigravityTargetRejectedError{error: err}
}

func antigravityResultModel(ctx context.Context, result Result) string {
	if result.Provider != "antigravity" {
		return ""
	}
	return canonicalModelKey(cliproxyexecutor.UpstreamAttemptModel(ctx))
}

func antigravityTargetRejected(result Result) bool {
	return result.Provider == "antigravity" && result.Error != nil && result.Error.Code == antigravityTargetRejectedCode
}

func antigravityRequestedTargetModel(ctx context.Context, auth *Auth, routeModel, checkModel string, opts cliproxyexecutor.Options, cfg *internalconfig.Config) string {
	if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "antigravity") {
		return ""
	}
	thinkingModel := strings.TrimSpace(checkModel)
	if thinkingModel == "" || (!thinking.ParseSuffix(thinkingModel).HasSuffix && thinking.ParseSuffix(routeModel).HasSuffix) {
		thinkingModel = strings.TrimSpace(routeModel)
	}
	if thinkingModel == "" {
		return ""
	}
	from := strings.TrimSpace(opts.SourceFormat.String())
	if from == "" {
		from = "antigravity"
	}
	level := thinking.RequestedAntigravityThinkingLevel(opts.OriginalRequest, from, thinkingModel)
	if level == "" {
		return ""
	}
	baseModel := strings.TrimSpace(thinking.ParseSuffix(checkModel).ModelName)
	if baseModel == "" {
		baseModel = strings.TrimSpace(checkModel)
	}
	if baseModel == "" {
		baseModel = strings.TrimSpace(thinking.ParseSuffix(routeModel).ModelName)
	}
	if baseModel == "" {
		return ""
	}
	selected := SelectAntigravityModel(ctx, auth, baseModel, string(level), cfg)
	if selected.ID == "" || sameCanonicalModelKey(selected.ID, checkModel) {
		return ""
	}
	return selected.ID
}

func isAuthBlockedByQuotaOrAuthForAntigravityRequest(ctx context.Context, auth *Auth, routeModel, checkModel string, opts cliproxyexecutor.Options, cfg *internalconfig.Config, now time.Time, clientHeaders ...http.Header) bool {
	if isAuthBlockedByQuotaOrAuthForModelForRequest(ctx, auth, checkModel, now, clientHeaders...) {
		return true
	}
	if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "antigravity") || len(auth.ModelStates) == 0 {
		return false
	}
	// Prefer the actual target model observed on this request over inferring from
	// the pre-interceptor request body so a cooldown on a different variant cannot
	// override the executed variant's classification.
	if observedModel := antigravityAttemptedModelForAuth(ctx, auth.ID); observedModel != "" {
		if sameCanonicalModelKey(observedModel, checkModel) {
			return false
		}
		return isAuthBlockedByQuotaOrAuthForModelForRequest(ctx, auth, observedModel, now, clientHeaders...)
	}
	requestedModel := antigravityRequestedTargetModel(ctx, auth, routeModel, checkModel, opts, cfg)
	if requestedModel != "" && !sameCanonicalModelKey(requestedModel, checkModel) {
		return isAuthBlockedByQuotaOrAuthForModelForRequest(ctx, auth, requestedModel, now, clientHeaders...)
	}
	return false
}
