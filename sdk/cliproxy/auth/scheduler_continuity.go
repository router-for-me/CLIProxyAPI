package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// HasSchedulerContinuity reports whether the active plugin advertises continuity.
func (m *Manager) HasSchedulerContinuity() bool {
	if m == nil {
		return false
	}
	m.mu.RLock()
	scheduler := m.pluginScheduler
	m.mu.RUnlock()
	if state, ok := scheduler.(interface{ HasSchedulerContinuity() bool }); ok {
		return state.HasSchedulerContinuity()
	}
	return false
}

type schedulerContinuityProvider interface {
	SchedulerContinuity(context.Context, pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error)
}

type continuityNotifierKey struct{}
type continuityBudgetKey struct{}
type continuityBudget struct{ started, deadline time.Time }

// WithContinuityBudget retains a single wait deadline across bootstrap retries.
func WithContinuityBudget(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Value(continuityBudgetKey{}).(*continuityBudget); ok {
		return ctx
	}
	return context.WithValue(ctx, continuityBudgetKey{}, &continuityBudget{})
}

type continuityRejected429Key struct{}
type continuityRejectedCauseKey struct{}

// WithContinuityRejected429 records a definite rejected attempt before output.
func WithContinuityRejected429(ctx context.Context, cause ...error) context.Context {
	ctx = context.WithValue(ctx, continuityRejected429Key{}, true)
	if len(cause) > 0 && cause[0] != nil {
		ctx = context.WithValue(ctx, continuityRejectedCauseKey{}, cause[0])
	}
	return ctx
}

// ConfirmedUpstreamQuotaRejection distinguishes an attempted upstream 429 from
// a local executor rejection before any transport was attempted.
func ConfirmedUpstreamQuotaRejection(err error) bool {
	return err != nil && hasUpstreamExecutionAttempt(err) && statusCodeFromError(err) == 429
}

// WithContinuityWaitNotifier reports entry into an actual pre-admission wait.
func WithContinuityWaitNotifier(ctx context.Context, notify chan<- struct{}) context.Context {
	return context.WithValue(ctx, continuityNotifierKey{}, notify)
}

func notifyContinuityWait(ctx context.Context) {
	if notify, ok := ctx.Value(continuityNotifierKey{}).(chan<- struct{}); ok && notify != nil {
		select {
		case notify <- struct{}{}:
		default:
		}
	}
}

func waitContinuityTimer(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// waitForSchedulerContinuity runs only before the first upstream attempt. The
// decision is rebuilt from live candidates at every wakeup, never from a stale
// snapshot. Unsupported plugins and Home retain their existing behavior.
func (m *Manager) waitForSchedulerContinuity(ctx context.Context, providers []string, model string, opts cliproxyexecutor.Options) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	budget, _ := ctx.Value(continuityBudgetKey{}).(*continuityBudget)
	if budget == nil {
		budget = &continuityBudget{}
	}
	previous, _ := ctx.Value(continuityRejected429Key{}).(bool)
	if previous {
		ready, err := m.waitAfterConfirmed429(ctx, providers, model, opts)
		if err != nil || ready {
			return ready, err
		}
		if cause, ok := ctx.Value(continuityRejectedCauseKey{}).(error); ok {
			return false, cause
		}
		return false, &Error{Code: "scheduler_continuity_replay_declined", Message: "scheduler did not confirm quota recovery for the rejected attempt", HTTPStatus: 429}
	}
	_, waited, err := runContinuityGate(ctx, budget, func() (pluginapi.SchedulerContinuityResponse, bool, error) {
		return m.schedulerContinuityDecision(ctx, providers, model, opts, false)
	}, time.Now, waitContinuityTimer)
	return waited, err
}

func runContinuityGate(ctx context.Context, budget *continuityBudget, decide func() (pluginapi.SchedulerContinuityResponse, bool, error), now func() time.Time, wait func(context.Context, time.Duration) error) (string, bool, error) {
	waited := false
	for {
		if err := ctx.Err(); err != nil {
			return "", waited, err
		}
		if !budget.deadline.IsZero() && !now().Before(budget.deadline) {
			return "", waited, &Error{Code: "scheduler_continuity_timeout", Message: "scheduler continuity wait expired", HTTPStatus: 503}
		}
		resp, handled, err := decide()
		if err != nil {
			return "", waited, fmt.Errorf("scheduler continuity: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return "", waited, err
		}
		if !budget.deadline.IsZero() && handled && resp.Action != "disabled" && resp.MaxWaitMS >= 1000 && resp.MaxWaitMS <= int64((24*time.Hour)/time.Millisecond) {
			configuredDeadline := budget.started.Add(time.Duration(resp.MaxWaitMS) * time.Millisecond)
			if configuredDeadline.Before(budget.deadline) {
				budget.deadline = configuredDeadline
			}
		}
		if !budget.deadline.IsZero() && !now().Before(budget.deadline) {
			return "", waited, &Error{Code: "scheduler_continuity_timeout", Message: "scheduler continuity wait expired", HTTPStatus: 503}
		}
		if !handled || resp.Action != "wait" {
			return resp.Action, waited, nil
		}
		if !budget.deadline.IsZero() && !now().Before(budget.deadline) {
			return "", waited, &Error{Code: "scheduler_continuity_timeout", Message: "scheduler continuity wait expired", HTTPStatus: 503}
		}
		if resp.MaxWaitMS < 1000 || resp.MaxWaitMS > int64((24*time.Hour)/time.Millisecond) {
			return "", waited, &Error{Code: "scheduler_continuity_invalid", Message: "invalid scheduler continuity wait budget", HTTPStatus: 503}
		}
		if budget.started.IsZero() {
			budget.started = now()
		}
		newDeadline := budget.started.Add(time.Duration(resp.MaxWaitMS) * time.Millisecond)
		if budget.deadline.IsZero() || newDeadline.Before(budget.deadline) {
			budget.deadline = newDeadline
		}
		remaining := budget.deadline.Sub(now())
		if remaining <= 0 {
			return "", waited, &Error{Code: "scheduler_continuity_timeout", Message: "scheduler continuity wait expired", HTTPStatus: 503}
		}
		retryMS := resp.RetryAfterMS
		if retryMS < 1000 {
			retryMS = 1000
		}
		if retryMS > int64((5*time.Minute)/time.Millisecond) {
			retryMS = int64((5 * time.Minute) / time.Millisecond)
		}
		delay := time.Duration(retryMS) * time.Millisecond
		if delay > remaining {
			delay = remaining
		}
		notifyContinuityWait(ctx)
		waited = true
		if err := wait(ctx, delay); err != nil {
			return "", waited, err
		}
	}
}

func (m *Manager) schedulerContinuityDecision(ctx context.Context, providers []string, model string, opts cliproxyexecutor.Options, previous429 bool) (pluginapi.SchedulerContinuityResponse, bool, error) {
	providerSet := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		key := canonicalSchedulingProvider(provider)
		if key != "codex" {
			return pluginapi.SchedulerContinuityResponse{}, false, nil
		}
		providerSet[key] = struct{}{}
	}
	pinned := pinnedAuthIDFromMetadata(opts.Metadata)
	eligibility := authSelectionEligibilityForRequest(ctx, opts)
	modelKey := strings.TrimSpace(model)
	if modelKey != "" {
		parsed := thinking.ParseSuffix(modelKey)
		if parsed.ModelName != "" {
			modelKey = strings.TrimSpace(parsed.ModelName)
		}
	}
	registryRef := registry.GetGlobalRegistry()
	m.mu.RLock()
	scheduler, ok := m.pluginScheduler.(schedulerContinuityProvider)
	if !ok || scheduler == nil {
		m.mu.RUnlock()
		return pluginapi.SchedulerContinuityResponse{}, false, nil
	}
	candidates := make([]*Auth, 0, len(m.auths))
	for _, candidate := range m.auths {
		if candidate == nil || candidate.Disabled || candidate.Status == StatusDisabled {
			continue
		}
		if pinned != "" && candidate.ID != pinned {
			continue
		}
		if !eligibility.allows(candidate) {
			continue
		}
		providerKey := canonicalSchedulingProvider(executorKeyFromAuth(candidate))
		if _, ok := providerSet[providerKey]; !ok {
			continue
		}
		if _, ok := m.executorLocked(providerKey); !ok {
			continue
		}
		if modelKey != "" && !m.authSupportsRouteModel(registryRef, candidate, model) {
			continue
		}
		// Preserve only native quota cooldowns for reassessment. Disabled,
		// unauthorized, expired, overload and other failures remain native errors.
		selectionModel := m.selectionModelForAuth(candidate, model)
		blocked, reason, _ := isAuthBlockedForModelForRequest(ctx, candidate, selectionModel, time.Now(), opts.Headers)
		if blocked && reason != blockReasonCooldown {
			continue
		}
		if blocked {
			// Prefer the selected model's current error over a stale credential-level
			// LastError. Never reinterpret a current non-quota failure as quota.
			var relevantError *Error
			matchedModel := false
			for stateModel, state := range candidate.ModelStates {
				if state != nil && canonicalModelKey(stateModel) == canonicalModelKey(selectionModel) {
					matchedModel = true
					relevantError = state.LastError
					break
				}
			}
			if !matchedModel {
				relevantError = candidate.LastError
			}
			if relevantError != nil && relevantError.StatusCode() != 0 && relevantError.StatusCode() != 429 {
				continue
			}
		}
		candidates = append(candidates, candidate.Clone())
	}
	m.mu.RUnlock()
	if len(candidates) == 0 && !previous429 {
		return pluginapi.SchedulerContinuityResponse{}, false, nil
	}
	requestID, _ := opts.Metadata[cliproxyexecutor.RequestLifecycleIDMetadataKey].(string)
	visibleCandidates := schedulerAuthCandidates(candidates)
	for i, candidate := range candidates {
		visibleCandidates[i].Metadata = map[string]any{"auth_index": candidate.EnsureIndex()}
	}
	req := pluginapi.SchedulerContinuityRequest{RequestID: requestID, PreviousAttemptRejected429: previous429, Pick: pluginapi.SchedulerPickRequest{
		Providers: schedulerProviders("mixed", providers), Model: model, Stream: opts.Stream,
		Options: schedulerOptions(opts), Candidates: visibleCandidates,
	}}
	return scheduler.SchedulerContinuity(ctx, req)
}

// waitAfterConfirmed429 reconciles asynchronous usage delivery for a bounded
// interval. It permits redispatch only after the plugin positively identifies a
// five-hour exhaustion and subsequently reports a ready account.
func (m *Manager) waitAfterConfirmed429(ctx context.Context, providers []string, model string, opts cliproxyexecutor.Options) (bool, error) {
	deadline := time.Now().Add(2 * time.Second)
	previous := true
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		resp, handled, err := m.schedulerContinuityDecision(ctx, providers, model, opts, previous)
		previous = false
		if err != nil {
			return false, fmt.Errorf("scheduler continuity: %w", err)
		}
		if !handled || resp.Action == "disabled" {
			return false, nil
		}
		if resp.Action == "wait" && (resp.Reason == pluginapi.ContinuityReasonFiveHourExhausted || resp.Reason == pluginapi.ContinuityReasonFiveHourRecheck) {
			budget, _ := ctx.Value(continuityBudgetKey{}).(*continuityBudget)
			if budget == nil {
				budget = &continuityBudget{}
			}
			first := true
			action, _, err := runContinuityGate(ctx, budget, func() (pluginapi.SchedulerContinuityResponse, bool, error) {
				if first {
					first = false
					return resp, true, nil
				}
				return m.schedulerContinuityDecision(ctx, providers, model, opts, false)
			}, time.Now, waitContinuityTimer)
			return action == "ready", err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false, nil
		}
		if remaining > 100*time.Millisecond {
			remaining = 100 * time.Millisecond
		}
		if err := waitContinuityTimer(ctx, remaining); err != nil {
			return false, err
		}
	}
}
