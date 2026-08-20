package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/clienterror"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

// Ordered failover implements ai-hub-ollw AC#2-#4: iterate an ordered candidate
// pool in config order; within each candidate, the existing executeMixed*Once
// loop rotates credentials per maxRetryCredentials/cooling rules; advance to
// the next candidate only on retryable pre-first-byte errors; stop permanently
// on 400/401/403/404/413/model-not-found/profile errors and on ANY error after
// the first byte has been observed in a streaming response.
//
// The chain is resolved once per Execute/ExecuteStream/ExecuteCount call from
// the live OAuth model-alias table. When no ordered candidate pool exists for
// the requested alias, the call delegates to the legacy single-model path
// without observable behavior change.

// permanentOrderedStopError reports whether err represents a permanent stop for
// the ordered chain: the request itself is invalid, the auth profile is bad, or
// the payload is too large. These errors must NOT advance to the next candidate.
func permanentOrderedStopError(err error) bool {
	if err == nil {
		return false
	}
	var authErr *Error
	if errors.As(err, &authErr) && authErr != nil && authErr.Code == "model_excluded" {
		return true
	}
	if isInvalidGrantError(err) {
		return true
	}
	status := statusCodeFromError(err)
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound,
		http.StatusRequestEntityTooLarge, http.StatusGone:
		return true
	}
	if isModelSupportError(err) {
		// Model-support errors from the candidate itself are terminal for that
		// candidate but not for the chain; the conductor falls through to the
		// next candidate. HOWEVER: when the *profile* is bad (e.g. the model
		// does not exist at all on this provider), the error is permanent.
		// We rely on isRequestInvalidError above to catch the shape variants.
	}
	if status == http.StatusBadRequest && !isRequestShapeFaultError(err) {
		// A generic 400 is neither a permanent-stop taxonomy member nor
		// retryable pre-first-byte: the chain stops at this candidate and the
		// error is returned verbatim (no fallback trace annotation), because
		// only identified request-shape faults prove the request is broken
		// across every candidate.
		return false
	}
	if isRequestInvalidError(err) {
		return true
	}
	return false
}

// isRequestShapeFaultError reports whether err carries an identified
// request-shape fault: either a prefixed plain-text translation/cloaking
// failure or a structured upstream request-fault body. A bare status match
// does not qualify — providers disagree on payload validation.
func isRequestShapeFaultError(err error) bool {
	if err == nil {
		return false
	}
	if isRequestScopedError(err) {
		return true
	}
	message := ""
	var authErr *Error
	if errors.As(err, &authErr) && authErr != nil {
		message = authErr.Message
	} else {
		message = err.Error()
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return false
	}
	if strings.HasPrefix(message, "invalid_request_error:") {
		return true
	}
	// Body-only classification: JSON request-fault codes/types qualify, plain
	// text does not.
	return clienterror.HasRequestFaultBodyString(message)
}

// retryablePreFirstByteError reports whether err is safe to retry by advancing
// to the next ordered candidate. The error must have occurred BEFORE the first
// byte of the response was emitted.
func retryablePreFirstByteError(err error) bool {
	if err == nil {
		return false
	}
	if permanentOrderedStopError(err) {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		// Client cancellation is never retried.
		return false
	}
	status := statusCodeFromError(err)
	switch status {
	case http.StatusTooManyRequests, http.StatusRequestTimeout,
		http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout,
		http.StatusVariantAlsoNegotiates,         // 506
		http.StatusInsufficientStorage,           // 507
		http.StatusLoopDetected,                  // 508
		http.StatusNotExtended,                   // 510
		http.StatusNetworkAuthenticationRequired, // 511
		509,                                      // bandwidth limit exceeded (non-RFC, Cloudflare/Apache)
		529:                                      // Cloudflare "service is overloaded"
		return true
	}

	// Empty-stream bootstrap errors are retryable pre-first-byte by construction.
	var authErr *Error
	if errors.As(err, &authErr) && authErr != nil {
		if authErr.Code == "empty_stream" {
			return true
		}
		if authErr.Retryable {
			return true
		}
	}
	// Stream bootstrap errors that aren't permanent and don't carry a status
	// are network/transport errors — treat as retryable pre-first-byte.
	var bootstrap *streamBootstrapError
	if errors.As(err, &bootstrap) && bootstrap != nil {
		return retryablePreFirstByteError(bootstrap.cause)
	}
	return false
}

// executeWithOrderedFailover is the non-streaming entry point. It resolves the
// ordered candidate chain for (channel, requestedModel) and, when present,
// walks it in order. The inner credential rotation is delegated to the existing
// executeMixedOnce loop.
func (m *Manager) executeWithOrderedFailover(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if excludedErr := m.routeGuardExcludedModel(providers, req, opts); excludedErr != nil {
		return cliproxyexecutor.Response{}, excludedErr
	}
	_, maxRetryCredentials, _ := m.retrySettings()
	chain := m.orderedCandidateChainForRequest(providers, req, opts)
	tracker := newRouteAttemptTracker()
	if len(chain) <= 1 {
		// No ordered pool: preserve legacy behavior exactly.
		tried := make(map[string]struct{})
		return m.executeMixedOnce(ctx, providers, req, opts, maxRetryCredentials, tracker, tried)
	}
	var lastErr error
	for idx, candidate := range chain {
		candidateReq := req
		candidateReq.Model = candidate.UpstreamModel
		candidateOpts := withOrderedCandidateMetadata(opts, idx, candidate)
		tried := make(map[string]struct{})
		resp, err := m.executeMixedOnce(ctx, providers, candidateReq, candidateOpts, maxRetryCredentials, tracker, tried)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if permanentOrderedStopError(err) {
			return resp, annotateOrderedError(err, chain, idx, "permanent")
		}
		if !retryablePreFirstByteError(err) {
			// Non-retryable but not a permanent-stop taxonomy either (e.g.
			// an unknown 4xx). Stop the chain and return the last error
			// verbatim; do not fabricate a fallback trace.
			return resp, err
		}
		logOrderedFailoverAdvance(ctx, chain, idx, err)
		continue
	}
	if lastErr != nil {
		return cliproxyexecutor.Response{}, annotateOrderedError(lastErr, chain, len(chain)-1, "exhausted")
	}
	return cliproxyexecutor.Response{}, &Error{Code: "auth_not_found", Message: "no auth available"}
}

// executeCountWithOrderedFailover mirrors executeWithOrderedFailover for the
// count_tokens path.
func (m *Manager) executeCountWithOrderedFailover(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if excludedErr := m.routeGuardExcludedModel(providers, req, opts); excludedErr != nil {
		return cliproxyexecutor.Response{}, excludedErr
	}
	_, maxRetryCredentials, _ := m.retrySettings()
	chain := m.orderedCandidateChainForRequest(providers, req, opts)
	tracker := newRouteAttemptTracker()
	if len(chain) <= 1 {
		tried := make(map[string]struct{})
		return m.executeCountMixedOnce(ctx, providers, req, opts, maxRetryCredentials, tracker, tried)
	}
	var lastErr error
	for idx, candidate := range chain {
		candidateReq := req
		candidateReq.Model = candidate.UpstreamModel
		candidateOpts := withOrderedCandidateMetadata(opts, idx, candidate)
		tried := make(map[string]struct{})
		resp, err := m.executeCountMixedOnce(ctx, providers, candidateReq, candidateOpts, maxRetryCredentials, tracker, tried)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if permanentOrderedStopError(err) {
			return resp, annotateOrderedError(err, chain, idx, "permanent")
		}
		if !retryablePreFirstByteError(err) {
			return resp, err
		}
		logOrderedFailoverAdvance(ctx, chain, idx, err)
		continue
	}
	if lastErr != nil {
		return cliproxyexecutor.Response{}, annotateOrderedError(lastErr, chain, len(chain)-1, "exhausted")
	}
	return cliproxyexecutor.Response{}, &Error{Code: "auth_not_found", Message: "no auth available"}
}

// executeStreamWithOrderedFailover mirrors executeWithOrderedFailover for the
// streaming path. Critically, once a candidate returns a non-nil StreamResult
// without error, the chain does NOT fall back to the next candidate on any
// downstream chunk-level error — the bytes have already been emitted to the
// client.
func (m *Manager) executeStreamWithOrderedFailover(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if excludedErr := m.routeGuardExcludedModel(providers, req, opts); excludedErr != nil {
		return nil, excludedErr
	}
	_, maxRetryCredentials, _ := m.retrySettings()
	chain := m.orderedCandidateChainForRequest(providers, req, opts)
	tracker := newRouteAttemptTracker()
	if len(chain) <= 1 {
		tried := make(map[string]struct{})
		return m.executeStreamMixedOnce(ctx, providers, req, opts, maxRetryCredentials, tracker, tried)
	}
	var lastErr error
	for idx, candidate := range chain {
		candidateReq := req
		candidateReq.Model = candidate.UpstreamModel
		candidateOpts := withOrderedCandidateMetadata(opts, idx, candidate)
		tried := make(map[string]struct{})
		streamResult, err := m.executeStreamMixedOnce(ctx, providers, candidateReq, candidateOpts, maxRetryCredentials, tracker, tried)
		if err == nil {
			// Bytes are flowing from this candidate. No further fallback is
			// permitted after this point — the client already received bytes.
			return streamResult, nil
		}
		lastErr = err
		if permanentOrderedStopError(err) {
			return nil, annotateOrderedError(err, chain, idx, "permanent")
		}
		if !retryablePreFirstByteError(err) {
			return nil, err
		}
		logOrderedFailoverAdvance(ctx, chain, idx, err)
		continue
	}
	if lastErr != nil {
		return nil, annotateOrderedError(lastErr, chain, len(chain)-1, "exhausted")
	}
	return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
}

func logOrderedFailoverAdvance(ctx context.Context, chain []OrderedCandidate, idx int, err error) {
	from := ""
	to := ""
	if idx >= 0 && idx < len(chain) {
		from = chain[idx].UpstreamModel
	}
	if idx+1 < len(chain) {
		to = chain[idx+1].UpstreamModel
	}
	logEntryWithRequestID(ctx).WithFields(log.Fields{
		"from":        from,
		"to":          to,
		"reason":      "retryable",
		"status":      statusCodeFromError(err),
		"chain_index": idx,
		"chain_len":   len(chain),
	}).Debug("ordered failover advanced")
}

// orderedCandidateChainForRequest resolves the ordered candidate pool from the
// live OAuth model-alias table using the request's channel and requested model.
// Returns nil (no chain) when the model is a direct upstream name, in which
// case legacy behavior is preserved.
func (m *Manager) orderedCandidateChainForRequest(providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) []OrderedCandidate {
	if m == nil {
		return nil
	}
	if len(providers) == 0 {
		return nil
	}
	requestedModel := strings.TrimSpace(authSelectionModelFromOptions(opts, req.Model))
	if requestedModel == "" {
		return nil
	}
	// The chain is channel-scoped. Use the first provider's channel mapping —
	// cross-channel ordered pools are not part of ai-hub-ollw's contract.
	channel := m.orderedChainChannelForProviders(providers)
	if channel == "" {
		return nil
	}
	return m.resolveOrderedCandidates(channel, requestedModel)
}

func (m *Manager) orderedChainChannelForProviders(providers []string) string {
	for _, provider := range providers {
		channel := OAuthModelAliasChannel(strings.TrimSpace(provider), "oauth")
		if channel != "" {
			return channel
		}
	}
	return ""
}

// withOrderedCandidateMetadata attaches the ordered candidate index to the
// request metadata so observability hooks and tests can see which chain step
// produced a result.
func withOrderedCandidateMetadata(opts cliproxyexecutor.Options, idx int, candidate OrderedCandidate) cliproxyexecutor.Options {
	if opts.Metadata == nil {
		opts.Metadata = make(map[string]any, 4)
	}
	meta := make(map[string]any, len(opts.Metadata)+4)
	for k, v := range opts.Metadata {
		meta[k] = v
	}
	meta[orderedCandidateIndexMetadataKey] = idx
	meta[orderedCandidateChannelMetadataKey] = candidate.Channel
	meta[orderedCandidateUpstreamMetadataKey] = candidate.UpstreamModel
	meta[orderedCandidateAliasMetadataKey] = candidate.ConfigAlias
	opts.Metadata = meta
	return opts
}

const (
	orderedCandidateIndexMetadataKey       = "cliproxy.ordered_candidate.index"
	orderedCandidateChannelMetadataKey     = "cliproxy.ordered_candidate.channel"
	orderedCandidateUpstreamMetadataKey    = "cliproxy.ordered_candidate.upstream_model"
	orderedCandidateAliasMetadataKey       = "cliproxy.ordered_candidate.alias"
	orderedCandidateReasonMetadataKey      = "cliproxy.ordered_candidate.reason"
	orderedCandidateChainLengthMetadataKey = "cliproxy.ordered_candidate.chain_length"
)

// annotateOrderedError stamps the returned error with secret-safe structured
// fallback observability (fallback_attempted=true, from/to/reason/status/
// chain_index) without leaking tokens or credentials. The underlying error is
// preserved via errors.As so callers can still inspect the cause.
func annotateOrderedError(err error, chain []OrderedCandidate, idx int, reason string) error {
	if err == nil || len(chain) == 0 {
		return err
	}
	status := statusCodeFromError(err)
	from := ""
	to := ""
	if idx > 0 && idx-1 < len(chain) {
		from = chain[idx-1].UpstreamModel
	} else if idx == 0 {
		// The first candidate was the origin; record the alias as the source.
		from = chain[0].ConfigAlias
	}
	if idx >= 0 && idx < len(chain) {
		to = chain[idx].UpstreamModel
	}
	return &orderedFailoverError{
		cause:       err,
		attempted:   true,
		fromModel:   from,
		toModel:     to,
		reason:      reason,
		status:      status,
		chainIndex:  idx,
		chainLength: len(chain),
	}
}

// orderedFailoverShouldFallThrough reports whether a failed ordered chain
// result should be retried by the legacy executeMixedOnce loop. Exhausted
// chains (all candidates retried pre-first-byte) fall through so cooldown and
// credential rotation over the full pool stay in effect. Permanent stops are
// terminal for the request.
func (m *Manager) orderedFailoverShouldFallThrough(err error) bool {
	if err == nil {
		return false
	}
	var ordered *orderedFailoverError
	if !errors.As(err, &ordered) || ordered == nil {
		return false
	}
	return ordered.reason == "exhausted"
}

// orderedFailoverError wraps an upstream error with secret-safe structured
// fallback observability. It implements the Error, Unwrap, and Headers
// interfaces so existing HTTP error rendering works unchanged.
type orderedFailoverError struct {
	cause       error
	attempted   bool
	fromModel   string
	toModel     string
	reason      string
	status      int
	chainIndex  int
	chainLength int
}

func (e *orderedFailoverError) Error() string {
	if e == nil || e.cause == nil {
		return ""
	}
	return e.cause.Error()
}

func (e *orderedFailoverError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *orderedFailoverError) Headers() http.Header {
	if e == nil {
		return nil
	}
	h := make(http.Header)
	h.Set("X-Fallback-Attempted", boolStr(e.attempted))
	h.Set("X-Fallback-From-Model", sanitizeForHeader(e.fromModel))
	h.Set("X-Fallback-To-Model", sanitizeForHeader(e.toModel))
	h.Set("X-Fallback-Reason", sanitizeForHeader(e.reason))
	if e.status > 0 {
		h.Set("X-Fallback-Status", intStr(e.status))
	}
	h.Set("X-Fallback-Chain-Index", intStr(e.chainIndex))
	h.Set("X-Fallback-Chain-Length", intStr(e.chainLength))
	return h
}

// FallbackTrace returns a copy of the structured fallback observability fields
// suitable for structured logging (logrus WithFields) without secrets. This is
// the public read API for observability consumers.
func (e *orderedFailoverError) FallbackTrace() (attempted bool, fromModel, toModel, reason string, status, chainIndex, chainLength int) {
	if e == nil {
		return false, "", "", "", 0, 0, 0
	}
	return e.attempted, e.fromModel, e.toModel, e.reason, e.status, e.chainIndex, e.chainLength
}

func boolStr(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func intStr(v int) string {
	// tiny helper to avoid pulling fmt for this hot path
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v >= 10 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	i--
	buf[i] = byte('0' + v)
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// sanitizeForHeader strips CR/LF and control bytes so the value is safe to
// embed in an HTTP header. Model names and reasons are bounded and not
// sensitive, but we still defend against injection.
func sanitizeForHeader(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(value))
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
