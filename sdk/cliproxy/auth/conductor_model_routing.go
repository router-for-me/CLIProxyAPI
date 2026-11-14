package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/modelrouting"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type modelRoutingCandidateAttempt struct {
	candidate RoutingCandidate
	request   cliproxyexecutor.Request
	options   cliproxyexecutor.Options
	rank      int
	selection *modelRoutingSelectionEvidence
}

type modelRoutingSelectionEvidence struct {
	mu     sync.RWMutex
	authID string
}

func (e *modelRoutingSelectionEvidence) record(authID string) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.authID = strings.TrimSpace(authID)
	e.mu.Unlock()
}

func (e *modelRoutingSelectionEvidence) selectedAuthID() string {
	if e == nil {
		return ""
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.authID
}

type modelRoutingRuleMatch struct {
	ruleID      string
	failureKind modelrouting.FailureKind
}

type modelRoutingAttemptEvidence struct {
	Rank          int    `json:"rank"`
	RouteSelector string `json:"route_selector"`
	Outcome       string `json:"outcome"`
	RuleID        string `json:"rule_id,omitempty"`
	HTTPStatus    int    `json:"http_status,omitempty"`
	ErrorCode     string `json:"error_code,omitempty"`
	FailureKind   string `json:"failure_kind,omitempty"`
	AttemptedAt   string `json:"attempted_at"`
}

type modelRoutingTrace struct {
	attempts []modelRoutingAttemptEvidence
}

type modelRoutingAcquisitionScope struct {
	ctx    context.Context
	cancel context.CancelFunc
	once   sync.Once
}

// modelRoutingAcquisitionContext carries the acquisition scope without
// allowing its deadline to reach the connected upstream execution.
type modelRoutingAcquisitionContext struct {
	scope *modelRoutingAcquisitionScope
	base  context.Context
}

type modelRoutingAcquisitionContextKey struct{}

func withModelRoutingAcquisition(ctx context.Context, scope *modelRoutingAcquisitionScope) context.Context {
	if scope == nil {
		return ctx
	}
	return context.WithValue(ctx, modelRoutingAcquisitionContextKey{}, &modelRoutingAcquisitionContext{scope: scope, base: ctx})
}

func modelRoutingAcquisitionFromContext(ctx context.Context) *modelRoutingAcquisitionScope {
	value, _ := ctx.Value(modelRoutingAcquisitionContextKey{}).(*modelRoutingAcquisitionContext)
	if value == nil {
		return nil
	}
	return value.scope
}

func modelRoutingBaseContextFromContext(ctx context.Context) context.Context {
	value, _ := ctx.Value(modelRoutingAcquisitionContextKey{}).(*modelRoutingAcquisitionContext)
	if value == nil {
		return nil
	}
	return value.base
}

// modelRoutingSelectionContext bounds credential selection and preparation by
// the acquisition deadline while connected execution uses the caller context.
func modelRoutingSelectionContext(ctx context.Context) context.Context {
	if acquisition := modelRoutingAcquisitionFromContext(ctx); acquisition != nil {
		return acquisition.contextOr(ctx)
	}
	return ctx
}

func (m *Manager) newModelRoutingAcquisitionScope(ctx context.Context, bootstrap bool) (*modelRoutingAcquisitionScope, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if bootstrap {
		return &modelRoutingAcquisitionScope{ctx: ctx}, nil
	}
	seconds := m.modelRoutingFailurePolicy().CredentialAcquisitionTimeoutSeconds
	if seconds < 1 {
		return nil, routingContractError("failure_policy_invalid", "active failure policy has no positive credential acquisition timeout", http.StatusInternalServerError)
	}
	acquisitionCtx, cancel := context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
	return &modelRoutingAcquisitionScope{ctx: acquisitionCtx, cancel: cancel}, nil

}

func (s *modelRoutingAcquisitionScope) contextOr(ctx context.Context) context.Context {
	if s != nil && s.ctx != nil {
		return s.ctx
	}
	return ctx

}

func (s *modelRoutingAcquisitionScope) finish() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
	})
}

func (m *Manager) initialRoutingCandidates(
	ctx context.Context,
	providers []string,
	req cliproxyexecutor.Request,
	opts cliproxyexecutor.Options,
) ([]modelRoutingCandidateAttempt, error) {
	requestedModel := strings.TrimSpace(authSelectionModelFromOptions(opts, req.Model))
	candidates, matched, errCandidates := m.modelRoutingCandidates(requestedModel, opts)
	if errCandidates != nil {
		return nil, errCandidates
	}
	if !matched || len(candidates) == 0 {
		return nil, routingContractError("route_not_selectable", "no candidate exists in the active projection", http.StatusServiceUnavailable)
	}

	providerSet := normalizedRoutingProviderSet(providers)
	protocol, supportedProtocol := routingProtocol(opts.SourceFormat)
	if !supportedProtocol {
		return nil, routingContractError("protocol_not_supported", fmt.Sprintf("request protocol %q has no routing executor", opts.SourceFormat), http.StatusBadRequest)
	}
	attempts := make([]modelRoutingCandidateAttempt, 0, len(candidates))
	protocolCandidateFound := false
	for index, candidate := range candidates {
		// Aliases and explicit selectors own their route channel. A direct model
		// with multiple routes is narrowed by the provider set resolved by the
		// existing request surface before health/quota selection.
		if candidate.Alias == "" && strings.TrimSpace(opts.Headers.Get(probeRouteSelectorHeader)) == "" && len(providerSet) > 0 {
			if _, allowed := providerSet[strings.ToLower(candidate.Channel)]; !allowed {
				continue
			}
		}
		if !containsRoutingValue(candidate.Protocols, protocol) {
			continue
		}
		protocolCandidateFound = true
		preparedReq, errPrepare := prepareRoutingCandidateRequest(req, opts, candidate)
		if errPrepare != nil {
			return nil, errPrepare
		}
		selection := &modelRoutingSelectionEvidence{}
		candidateOpts := m.candidateOptions(opts, index, candidate, selection.record)
		if !m.routingCandidateKnownSelectable(ctx, candidate, candidateOpts) {
			continue
		}
		attempts = append(attempts, modelRoutingCandidateAttempt{
			candidate: candidate,
			request:   preparedReq,
			options:   candidateOpts,
			rank:      index + 1,
			selection: selection,
		})
	}
	if !protocolCandidateFound {
		return nil, routingContractError("protocol_not_supported", fmt.Sprintf("no projected candidate supports protocol %q", protocol), http.StatusBadRequest)
	}
	if len(attempts) == 0 {
		return nil, routingContractError("route_not_selectable", "all projected candidates are blocked by known health, quota, credential, or executor state", http.StatusServiceUnavailable)
	}
	return attempts, nil
}

func normalizedRoutingProviderSet(providers []string) map[string]struct{} {
	result := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		provider = strings.ToLower(strings.TrimSpace(provider))
		if provider != "" {
			result[provider] = struct{}{}
		}
	}
	return result
}

// executeWithModelRouting performs at most one upstream attempt per ranked
// candidate and advances only when a configured typed rule matches.
func (m *Manager) executeWithModelRouting(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	attempts, errSelect := m.initialRoutingCandidates(ctx, providers, req, opts)
	if errSelect != nil {
		return cliproxyexecutor.Response{}, errSelect
	}
	policy := m.modelRoutingFailurePolicy()
	tracker := newRouteAttemptTracker()
	trace := &modelRoutingTrace{}
	causes := make([]error, 0, len(attempts))
	var firstAttempt modelRoutingCandidateAttempt
	for _, attempt := range attempts {
		if errCaller := ctx.Err(); errCaller != nil {
			return cliproxyexecutor.Response{}, m.newModelRoutingExecutionError(joinModelRoutingCauses(causes, errCaller), firstAttempt, trace)
		}
		acquisition, errContext := m.newModelRoutingAcquisitionScope(ctx, attempt.candidate.bootstrap)
		if errContext != nil {
			return cliproxyexecutor.Response{}, m.newModelRoutingExecutionError(joinModelRoutingCauses(causes, errContext), firstAttempt, trace)
		}
		response, errExecute := m.executeMixedOnce(
			withModelRoutingAcquisition(ctx, acquisition),
			[]string{attempt.candidate.Channel},
			attempt.request,
			attempt.options,
			1,
			0,
			0,
			tracker,
		)
		acquisition.finish()
		if len(causes) == 0 {
			firstAttempt = attempt
		}
		if errExecute == nil {
			trace.recordSuccess(attempt)
			return m.addModelRoutingExecutionHeaders(response, attempt, trace)
		}
		match, eligible := modelRoutingRuleMatch{}, false
		if !attempt.candidate.bootstrap {
			match, eligible = modelRoutingFailoverMatch(ctx, errExecute, policy)
		}
		trace.recordFailure(attempt, errExecute, match)
		causes = append(causes, errExecute)
		if !eligible || len(causes) == len(attempts) {
			return response, m.newModelRoutingExecutionError(joinModelRoutingCauses(causes), firstAttempt, trace)
		}
	}
	if cause := joinModelRoutingCauses(causes); cause != nil {
		return cliproxyexecutor.Response{}, m.newModelRoutingExecutionError(cause, firstAttempt, trace)
	}
	return cliproxyexecutor.Response{}, routingContractError("route_not_selectable", "no projected candidate was attempted", http.StatusServiceUnavailable)
}

func (m *Manager) executeCountWithModelRouting(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	attempts, errSelect := m.initialRoutingCandidates(ctx, providers, req, opts)
	if errSelect != nil {
		return cliproxyexecutor.Response{}, errSelect
	}
	policy := m.modelRoutingFailurePolicy()
	tracker := newRouteAttemptTracker()
	trace := &modelRoutingTrace{}
	causes := make([]error, 0, len(attempts))
	var firstAttempt modelRoutingCandidateAttempt
	for _, attempt := range attempts {
		if errCaller := ctx.Err(); errCaller != nil {
			return cliproxyexecutor.Response{}, m.newModelRoutingExecutionError(joinModelRoutingCauses(causes, errCaller), firstAttempt, trace)
		}
		acquisition, errContext := m.newModelRoutingAcquisitionScope(ctx, attempt.candidate.bootstrap)
		if errContext != nil {
			return cliproxyexecutor.Response{}, m.newModelRoutingExecutionError(joinModelRoutingCauses(causes, errContext), firstAttempt, trace)
		}
		response, errExecute := m.executeCountMixedOnce(
			withModelRoutingAcquisition(ctx, acquisition),
			[]string{attempt.candidate.Channel},
			attempt.request,
			attempt.options,
			1,
			0,
			0,
			tracker,
		)
		acquisition.finish()
		if len(causes) == 0 {
			firstAttempt = attempt
		}
		if errExecute == nil {
			trace.recordSuccess(attempt)
			return m.addModelRoutingExecutionHeaders(response, attempt, trace)
		}
		match, eligible := modelRoutingRuleMatch{}, false
		if !attempt.candidate.bootstrap {
			match, eligible = modelRoutingFailoverMatch(ctx, errExecute, policy)
		}
		trace.recordFailure(attempt, errExecute, match)
		causes = append(causes, errExecute)
		if !eligible || len(causes) == len(attempts) {
			return response, m.newModelRoutingExecutionError(joinModelRoutingCauses(causes), firstAttempt, trace)
		}
	}
	if cause := joinModelRoutingCauses(causes); cause != nil {
		return cliproxyexecutor.Response{}, m.newModelRoutingExecutionError(cause, firstAttempt, trace)
	}
	return cliproxyexecutor.Response{}, routingContractError("route_not_selectable", "no projected candidate was attempted", http.StatusServiceUnavailable)
}

func (m *Manager) executeStreamWithModelRouting(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	attempts, errSelect := m.initialRoutingCandidates(ctx, providers, req, opts)
	if errSelect != nil {
		return nil, errSelect
	}
	policy := m.modelRoutingFailurePolicy()
	tracker := newRouteAttemptTracker()
	trace := &modelRoutingTrace{}
	causes := make([]error, 0, len(attempts))
	var firstAttempt modelRoutingCandidateAttempt
	for _, attempt := range attempts {
		if errCaller := ctx.Err(); errCaller != nil {
			return nil, m.newModelRoutingExecutionError(joinModelRoutingCauses(causes, errCaller), firstAttempt, trace)
		}
		acquisition, errContext := m.newModelRoutingAcquisitionScope(ctx, attempt.candidate.bootstrap)
		if errContext != nil {
			return nil, m.newModelRoutingExecutionError(joinModelRoutingCauses(causes, errContext), firstAttempt, trace)
		}
		homeRetryLimit := -1
		streamCtx, cancelStream := context.WithCancel(ctx)
		result, errExecute := m.executeStreamMixedOnce(
			withModelRoutingAcquisition(streamCtx, acquisition),
			[]string{attempt.candidate.Channel},
			attempt.request,
			attempt.options,
			1,
			&homeRetryLimit,
			0,
			0,
			tracker,
		)
		acquisition.finish()
		if len(causes) == 0 {
			firstAttempt = attempt
		}
		if errExecute == nil {
			if result == nil || result.Chunks == nil {
				cancelStream()
				contractErr := routingContractError("empty_stream", "upstream returned no stream source", http.StatusBadGateway)
				trace.recordFailure(attempt, contractErr, modelRoutingRuleMatch{})
				return nil, m.newModelRoutingExecutionError(contractErr, attempt, trace)
			}
			trace.recordStarted(attempt)
			result, errHeaders := m.addModelRoutingStreamExecutionHeaders(result, attempt, trace)
			if errHeaders != nil {
				cancelStream()
				return nil, errHeaders
			}
			return m.ownModelRoutingStream(streamCtx, cancelStream, result, attempt, trace), nil
		}
		cancelStream()
		match, eligible := modelRoutingRuleMatch{}, false
		if !attempt.candidate.bootstrap {
			match, eligible = modelRoutingFailoverMatch(ctx, errExecute, policy)
		}
		trace.recordFailure(attempt, errExecute, match)
		causes = append(causes, errExecute)
		if !eligible || len(causes) == len(attempts) {
			return nil, m.newModelRoutingExecutionError(joinModelRoutingCauses(causes), firstAttempt, trace)
		}
	}
	if cause := joinModelRoutingCauses(causes); cause != nil {
		return nil, m.newModelRoutingExecutionError(cause, firstAttempt, trace)
	}
	return nil, routingContractError("route_not_selectable", "no projected candidate was attempted", http.StatusServiceUnavailable)
}

func (m *Manager) ownModelRoutingStream(
	ctx context.Context,
	cancel context.CancelFunc,
	result *cliproxyexecutor.StreamResult,
	attempt modelRoutingCandidateAttempt,
	trace *modelRoutingTrace,
) *cliproxyexecutor.StreamResult {
	out := make(chan cliproxyexecutor.StreamChunk, 1)
	var terminal sync.Once
	complete := func(err error) {
		terminal.Do(func() {
			if err == nil {
				trace.recordTerminalSuccess(attempt)
				return
			}
			trace.recordTerminalFailure(attempt, err)
		})
	}
	go func() {
		defer close(out)
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				complete(ctx.Err())
				chunk := cliproxyexecutor.StreamChunk{Err: m.newModelRoutingExecutionError(ctx.Err(), attempt, trace)}
				select {
				case out <- chunk:
				default:
				}
				return
			case chunk, open := <-result.Chunks:
				if !open {
					return
				}
				if chunk.Err != nil {
					complete(chunk.Err)
					chunk.Err = m.newModelRoutingExecutionError(chunk.Err, attempt, trace)
				}
				select {
				case out <- chunk:
				case <-ctx.Done():
					return
				}
				if chunk.Err != nil {
					return
				}
			}
		}
	}()
	return &cliproxyexecutor.StreamResult{
		Headers:           result.Headers.Clone(),
		DownstreamHeaders: result.DownstreamHeaders.Clone(),
		Chunks:            out,
		Complete:          complete,
	}
}

type modelRoutingExecutionError struct {
	cause   error
	headers http.Header
}

func (m *Manager) newModelRoutingExecutionError(cause error, attempt modelRoutingCandidateAttempt, trace *modelRoutingTrace) error {
	if cause == nil {
		return nil
	}
	headers, errHeaders := m.modelRoutingExecutionHeaders(attempt, trace)
	if errHeaders != nil {
		cause = errors.Join(cause, errHeaders)
		headers = make(http.Header)
	}
	if safe := SafeResponseHeaders(cause); len(safe) > 0 {
		headers = mergeModelRoutingHeaders(safe, headers)
	}
	return &modelRoutingExecutionError{cause: cause, headers: headers}
}

func (e *modelRoutingExecutionError) Error() string {
	if e == nil || e.cause == nil {
		return ""
	}
	return e.cause.Error()
}

func (e *modelRoutingExecutionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *modelRoutingExecutionError) SafeResponseHeaders() http.Header {
	if e == nil {
		return nil
	}
	return e.headers.Clone()
}

func (m *Manager) addModelRoutingExecutionHeaders(response cliproxyexecutor.Response, attempt modelRoutingCandidateAttempt, trace *modelRoutingTrace) (cliproxyexecutor.Response, error) {
	headers, errHeaders := m.modelRoutingExecutionHeaders(attempt, trace)
	if errHeaders != nil {
		return response, errHeaders
	}
	response.DownstreamHeaders = mergeModelRoutingHeaders(response.DownstreamHeaders, headers)
	return response, nil
}

func (m *Manager) addModelRoutingStreamExecutionHeaders(response *cliproxyexecutor.StreamResult, attempt modelRoutingCandidateAttempt, trace *modelRoutingTrace) (*cliproxyexecutor.StreamResult, error) {
	if response == nil {
		return nil, routingContractError("empty_stream", "upstream returned no stream result", http.StatusBadGateway)
	}
	headers, errHeaders := m.modelRoutingExecutionHeaders(attempt, trace)
	if errHeaders != nil {
		return nil, errHeaders
	}
	response.DownstreamHeaders = mergeModelRoutingHeaders(response.DownstreamHeaders, headers)
	return response, nil
}

func (m *Manager) modelRoutingExecutionHeaders(attempt modelRoutingCandidateAttempt, trace *modelRoutingTrace) (http.Header, error) {
	headers := make(http.Header)
	if attempt.candidate.RouteSelector != "" {
		headers = m.modelRoutingResponseHeaders(
			attempt.candidate,
			attempt.selection.selectedAuthID(),
		)
	}
	headers.Set(failureModeHeader, "classified_candidate_failover")
	headers.Set(attemptsHeader, strconv.Itoa(trace.len()))
	evidence, errEvidence := trace.marshal()
	if errEvidence != nil {
		return nil, fmt.Errorf("encode model routing attempt evidence: %w", errEvidence)
	}
	headers.Set(failoverEvidenceHeader, string(evidence))
	return headers, nil
}

func (t *modelRoutingTrace) len() int {
	if t == nil {
		return 0
	}
	return len(t.attempts)
}

func (t *modelRoutingTrace) marshal() ([]byte, error) {
	if t == nil {
		return json.Marshal([]modelRoutingAttemptEvidence{})
	}
	return json.Marshal(t.attempts)
}

func (t *modelRoutingTrace) recordSuccess(attempt modelRoutingCandidateAttempt) {
	if t == nil {
		return
	}
	t.attempts = append(t.attempts, modelRoutingAttemptEvidence{
		Rank:          attempt.rank,
		RouteSelector: attempt.candidate.RouteSelector,
		Outcome:       "success",
		AttemptedAt:   time.Now().UTC().Format(time.RFC3339Nano),
	})
}

func (t *modelRoutingTrace) recordStarted(attempt modelRoutingCandidateAttempt) {
	if t == nil {
		return
	}
	t.attempts = append(t.attempts, modelRoutingAttemptEvidence{
		Rank:          attempt.rank,
		RouteSelector: attempt.candidate.RouteSelector,
		Outcome:       "streaming",
		AttemptedAt:   time.Now().UTC().Format(time.RFC3339Nano),
	})
}

func (t *modelRoutingTrace) recordTerminalSuccess(attempt modelRoutingCandidateAttempt) {
	if !t.replaceStreamingTerminal(attempt, "success", nil) {
		t.recordSuccess(attempt)
	}
}

func (t *modelRoutingTrace) recordTerminalFailure(attempt modelRoutingCandidateAttempt, err error) {
	facts := modelRoutingFailureFactsFromError(err)
	if !t.replaceStreamingTerminal(attempt, "failure", &facts) {
		t.recordFailure(attempt, err, modelRoutingRuleMatch{})
	}
}

func (t *modelRoutingTrace) replaceStreamingTerminal(attempt modelRoutingCandidateAttempt, outcome string, facts *modelRoutingFailureFacts) bool {
	if t == nil {
		return false
	}
	for index := len(t.attempts) - 1; index >= 0; index-- {
		entry := &t.attempts[index]
		if entry.Outcome != "streaming" || entry.Rank != attempt.rank || entry.RouteSelector != attempt.candidate.RouteSelector {
			continue
		}
		entry.Outcome = outcome
		if facts != nil {
			entry.HTTPStatus = facts.httpStatus
			entry.ErrorCode = facts.errorCode
		}
		return true
	}
	return false
}

func (t *modelRoutingTrace) recordFailure(attempt modelRoutingCandidateAttempt, err error, match modelRoutingRuleMatch) {
	if t == nil {
		return
	}
	facts := modelRoutingFailureFactsFromError(err)
	t.attempts = append(t.attempts, modelRoutingAttemptEvidence{
		Rank:          attempt.rank,
		RouteSelector: attempt.candidate.RouteSelector,
		Outcome:       "failure",
		RuleID:        match.ruleID,
		HTTPStatus:    facts.httpStatus,
		ErrorCode:     facts.errorCode,
		FailureKind:   string(match.failureKind),
		AttemptedAt:   time.Now().UTC().Format(time.RFC3339Nano),
	})
}

type modelRoutingFailureFacts struct {
	httpStatus   int
	errorCode    string
	failureKinds []modelrouting.FailureKind
}

func modelRoutingFailureFactsFromError(err error) modelRoutingFailureFacts {
	facts := modelRoutingFailureFacts{httpStatus: statusCodeFromError(err)}
	var authErr *Error
	if errors.As(err, &authErr) && authErr != nil {
		facts.errorCode = strings.TrimSpace(authErr.Code)
	}
	if isCredentialScopedError(err) {
		facts.failureKinds = append(facts.failureKinds, modelrouting.FailureKindCredential)
	}
	if isEmptyPreResponseError(err) {
		facts.failureKinds = append(facts.failureKinds, modelrouting.FailureKindEmptyPreResponse)
	}
	var networkErr net.Error
	hasNetworkError := errors.As(err, &networkErr) && networkErr != nil
	if isConnectionLifecycleError(err) || (facts.httpStatus == 0 && hasNetworkError && !networkErr.Timeout()) {
		facts.failureKinds = append(facts.failureKinds, modelrouting.FailureKindTransport)
	}
	if errors.Is(err, context.DeadlineExceeded) || (hasNetworkError && networkErr.Timeout()) {
		facts.failureKinds = append(facts.failureKinds, modelrouting.FailureKindUpstreamTimeout)
	}
	return facts
}

func isEmptyPreResponseError(err error) bool {
	var bootstrap *streamBootstrapError
	if !errors.As(err, &bootstrap) || bootstrap == nil {
		return false
	}
	var authErr *Error
	return errors.As(err, &authErr) && authErr != nil && authErr.Code == "empty_stream"
}

func modelRoutingFailoverMatch(ctx context.Context, err error, policy modelrouting.FailurePolicy) (modelRoutingRuleMatch, bool) {
	if err == nil || !policy.AutomaticFailover || policy.AutomaticRetry || isExecutionResultRecordError(err) || isRoutingContractFailure(err) || isRequestTerminatedError(err) || isRequestStopError(err) || isRequestScopedError(err) || isRequestInvalidError(err) {
		return modelRoutingRuleMatch{}, false
	}
	if ctx != nil && ctx.Err() != nil {
		return modelRoutingRuleMatch{}, false
	}
	if errors.Is(err, context.Canceled) {
		return modelRoutingRuleMatch{}, false
	}
	facts := modelRoutingFailureFactsFromError(err)
	for _, rule := range policy.FailoverRules {
		if containsModelRoutingStatus(rule.HTTPStatuses, facts.httpStatus) || containsRoutingValue(rule.ErrorCodes, facts.errorCode) {
			return modelRoutingRuleMatch{ruleID: rule.RuleID}, true
		}
		for _, kind := range facts.failureKinds {
			if containsModelRoutingFailureKind(rule.FailureKinds, kind) {
				return modelRoutingRuleMatch{ruleID: rule.RuleID, failureKind: kind}, true
			}
		}
	}
	return modelRoutingRuleMatch{}, false
}

func containsModelRoutingStatus(values []int, target int) bool {
	if target == 0 {
		return false
	}
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func containsModelRoutingFailureKind(values []modelrouting.FailureKind, target modelrouting.FailureKind) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func joinModelRoutingCauses(causes []error, additional ...error) error {
	joined := make([]error, 0, len(causes)+len(additional))
	for _, cause := range causes {
		if cause != nil {
			joined = append(joined, cause)
		}
	}
	for _, cause := range additional {
		if cause != nil {
			joined = append(joined, cause)
		}
	}
	switch len(joined) {
	case 0:
		return nil
	case 1:
		return joined[0]
	default:
		return errors.Join(joined...)
	}
}
