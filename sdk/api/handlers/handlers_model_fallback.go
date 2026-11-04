package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/context"
)

// ModelFallbackHeader names the response header that carries the model which served
// a request after routing.model-fallback moved it away from the requested model.
const ModelFallbackHeader = "X-CPA-Fallback-Model"

// modelAttemptFailure describes why one model attempt did not produce a response.
type modelAttemptFailure struct {
	// msg is the client-facing error of this attempt.
	msg *interfaces.ErrorMessage
	// upstreamErr is set only when the attempt failed inside the auth manager, after the
	// existing credential retry and cooldown logic gave up. Only these failures can
	// move the request to the next model of its chain.
	upstreamErr error
	// unresolved reports that no provider serves the attempted model.
	unresolved bool
}

func terminalModelAttemptFailure(msg *interfaces.ErrorMessage) *modelAttemptFailure {
	if msg == nil {
		return nil
	}
	return &modelAttemptFailure{msg: msg}
}

// unresolvedModelAttemptFailure wraps a provider resolution failure. Only the
// "unknown provider for model" case is unresolved; image/speech-only validation
// errors are request faults and stay final.
func unresolvedModelAttemptFailure(msg *interfaces.ErrorMessage) *modelAttemptFailure {
	if msg == nil {
		return nil
	}
	unresolved := msg.Error != nil && strings.Contains(msg.Error.Error(), `"code":"model_not_found"`)
	return &modelAttemptFailure{msg: msg, unresolved: unresolved}
}

func upstreamModelAttemptFailure(msg *interfaces.ErrorMessage, upstreamErr error) *modelAttemptFailure {
	if msg == nil {
		return nil
	}
	if msg.DirectResponse {
		return terminalModelAttemptFailure(msg)
	}
	return &modelAttemptFailure{msg: msg, upstreamErr: upstreamErr}
}

// modelFallbackApplies reports whether the request may use a fallback chain.
func (h *BaseAPIHandler) modelFallbackApplies(requestedModel string, execOptions modelExecutionOptions) bool {
	targets, _ := h.modelFallbackPlan(requestedModel, execOptions)
	return len(targets) > 0
}

// modelFallbackPlan returns the fallback models for requestedModel and the conditions
// that allow moving to them, from a single configuration snapshot. Home dispatch and
// forced-provider executions keep their single-model behavior.
func (h *BaseAPIHandler) modelFallbackPlan(requestedModel string, execOptions modelExecutionOptions) ([]string, config.ModelFallbackConditions) {
	if h == nil || strings.TrimSpace(execOptions.ForcedProvider) != "" {
		return nil, config.ModelFallbackConditions{}
	}
	if h.AuthManager != nil && h.AuthManager.HomeEnabled() {
		return nil, config.ModelFallbackConditions{}
	}
	cfg := h.CurrentConfig()
	if cfg == nil {
		return nil, config.ModelFallbackConditions{}
	}
	targets := modelFallbackTargetsFromConfig(cfg.ModelFallback, requestedModel)
	if len(targets) == 0 {
		return nil, config.ModelFallbackConditions{}
	}
	return targets, cfg.ModelFallback.Conditions()
}

// modelFallbackTargetsFromConfig returns the chain for requestedModel without
// duplicates or the requested model itself, capped at max-hops.
func modelFallbackTargetsFromConfig(fallback config.ModelFallbackConfig, requestedModel string) []string {
	chain := fallback.Targets(requestedModel)
	if len(chain) == 0 {
		return nil
	}
	maxHops := fallback.EffectiveMaxHops()
	visited := map[string]struct{}{strings.ToLower(strings.TrimSpace(requestedModel)): {}}
	targets := make([]string, 0, len(chain))
	for _, target := range chain {
		key := strings.ToLower(target)
		if _, seen := visited[key]; seen {
			continue
		}
		visited[key] = struct{}{}
		targets = append(targets, target)
		if len(targets) >= maxHops {
			break
		}
	}
	return targets
}

// runModelFallback executes requestedModel and, while its failures match
// routing.model-fallback.on, the models of its fallback chain. attempt must run the
// full single-model execution (including credential retries) for the given model.
// It returns the model that produced the response, or the most informative failure.
func (h *BaseAPIHandler) runModelFallback(ctx context.Context, requestedModel string, execOptions modelExecutionOptions, attempt func(model string) *modelAttemptFailure) (string, *modelAttemptFailure) {
	targets, conditions := h.modelFallbackPlan(requestedModel, execOptions)
	return runModelFallbackPlan(ctx, requestedModel, targets, conditions, attempt)
}

func runModelFallbackPlan(ctx context.Context, requestedModel string, targets []string, conditions config.ModelFallbackConditions, attempt func(string) *modelAttemptFailure) (string, *modelAttemptFailure) {
	failure := attempt(requestedModel)
	if failure == nil || len(targets) == 0 {
		return requestedModel, failure
	}
	result := failure
	from := requestedModel
	for _, target := range targets {
		if ctx != nil && ctx.Err() != nil {
			return requestedModel, terminalModelAttemptFailure(executionErrorMessage(ctx.Err()))
		}
		switch {
		case failure.upstreamErr != nil:
			// The auth manager already exhausted every credential of this model.
			reason, eligible := modelFallbackReason(ctx, conditions, failure.upstreamErr)
			if !eligible {
				return requestedModel, result
			}
			modelFallbackLogEntry(ctx).Infof("model fallback: %s -> %s (reason=%s, requested=%s)", from, target, reason, requestedModel)
		case failure.unresolved && conditions.AuthUnavailable:
			// No provider currently serves this model, e.g. all of its credentials are removed.
			modelFallbackLogEntry(ctx).Infof("model fallback: %s -> %s (reason=%s, requested=%s)", from, target, config.ModelFallbackOnAuthUnavailable, requestedModel)
		default:
			// Validation, interceptor, and plugin-route failures are final.
			return requestedModel, result
		}
		from = target
		failure = attempt(target)
		if failure == nil {
			modelFallbackLogEntry(ctx).Infof("model fallback: request for %s served by %s", requestedModel, target)
			return target, nil
		}
		if failure.upstreamErr == nil && !failure.unresolved {
			// A deliberate rejection (for example by an interceptor) is returned as is.
			return target, failure
		}
		result = preferredModelFallbackFailure(result, failure)
	}
	if from != requestedModel {
		modelFallbackLogEntry(ctx).Warnf("model fallback: no model in the chain of %s could serve the request (last tried %s)", requestedModel, from)
	}
	return requestedModel, result
}

// preferredModelFallbackFailure keeps the requested model's failure, which carries its
// own retry timing, unless that failure never reached an upstream (no credential could
// be selected) and the fallback failure did.
func preferredModelFallbackFailure(primary, candidate *modelAttemptFailure) *modelAttemptFailure {
	if primary == nil {
		return candidate
	}
	if candidate != nil && (errors.Is(candidate.upstreamErr, context.Canceled) || errors.Is(candidate.upstreamErr, context.DeadlineExceeded)) {
		return candidate
	}
	if candidate == nil || candidate.upstreamErr == nil {
		return primary
	}
	if primary.upstreamErr == nil || (isAuthSelectionUnavailable(primary.upstreamErr) && !isAuthSelectionUnavailable(candidate.upstreamErr)) {
		return candidate
	}
	return primary
}

// modelFallbackReason reports whether err matches the configured fallback conditions.
func modelFallbackReason(ctx context.Context, conditions config.ModelFallbackConditions, err error) (string, bool) {
	if err == nil {
		return "", false
	}
	if ctx != nil && ctx.Err() != nil {
		return "", false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || isRequestTerminated(err) {
		return "", false
	}
	status := statusFromError(err)
	// Never let a quota marker or custom programmatic config bypass request faults.
	switch status {
	case 400, 401, 403, 404, 413, 422:
		return "", false
	}
	if status > 0 && (conditions.Statuses[status] || (conditions.Any5xx && status >= http.StatusInternalServerError && status <= 599)) {
		return strconv.Itoa(status), true
	}
	if conditions.QuotaExhausted && isQuotaExhaustedError(status, err) {
		return config.ModelFallbackOnQuotaExhausted, true
	}
	if conditions.AuthUnavailable && (isModelCooldownError(err) || isAuthSelectionUnavailable(err)) {
		return config.ModelFallbackOnAuthUnavailable, true
	}
	return "", false
}

func isRequestTerminated(err error) bool {
	var terminated *coreexecutor.RequestTerminatedError
	if errors.As(err, &terminated) {
		return true
	}
	type directResponseError interface {
		DirectResponse() bool
	}
	var direct directResponseError
	return errors.As(err, &direct) && direct != nil && direct.DirectResponse()
}

func isModelCooldownError(err error) bool {
	type modelCooldownMarker interface {
		IsModelCooldown() bool
	}
	var marker modelCooldownMarker
	return errors.As(err, &marker) && marker != nil && marker.IsModelCooldown()
}

// quotaErrorMarkers are provider quota and rate-limit signals that may arrive with a
// non-429 status (for example OpenAI insufficient_quota, Gemini RESOURCE_EXHAUSTED,
// Codex usage_limit_reached).
var quotaErrorMarkers = []string{
	"insufficient_quota",
	"quota",
	"resource_exhausted",
	"usage_limit",
	"usage limit",
	"rate_limit",
	"rate limit",
	"billing_hard_limit",
}

// isQuotaExhaustedError normalizes quota exhaustion across providers: 429 responses,
// all-credentials cooldowns, and 402 responses that carry an explicit quota marker.
func isQuotaExhaustedError(status int, err error) bool {
	if status == http.StatusTooManyRequests || isModelCooldownError(err) {
		return true
	}
	if status != http.StatusPaymentRequired {
		return false
	}
	text := strings.ToLower(err.Error())
	for _, marker := range quotaErrorMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func modelFallbackLogEntry(ctx context.Context) *log.Entry {
	entry := log.NewEntry(log.StandardLogger())
	if ctx != nil {
		if requestID := logging.GetRequestID(ctx); requestID != "" {
			entry = entry.WithField("request_id", requestID)
		}
	}
	return entry
}

// withModelFallbackHeader marks responses served by a fallback model.
func withModelFallbackHeader(headers http.Header, requestedModel, servedModel string) http.Header {
	if servedModel == "" || servedModel == requestedModel || headers.Get(ModelFallbackHeader) != "" {
		return headers
	}
	if headers == nil {
		headers = make(http.Header)
	}
	headers.Set(ModelFallbackHeader, servedModel)
	return headers
}

// streamFallbackPlanKey pins the fallback policy before the HTTP consumer decides
// whether it may send pre-first-token heartbeats. A reload cannot change that
// decision while this request is bootstrapping.
type streamFallbackPlanKey struct{}

type streamFallbackPlan struct {
	model      string
	targets    []string
	conditions config.ModelFallbackConditions
}
