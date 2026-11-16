package auth

import (
	"context"
	"errors"
	"net/http"

	"github.com/MoeForge/moego/ojson"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	cliproxysession "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
)

// withClaudeThreadAuth pins continuations before ordinary selection, including
// default round-robin. It never bypasses the selector's availability checks.
// The returned bool reports that the request was pinned to its thread owner.
func withClaudeThreadAuth(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Options, bool, error) {
	if opts.SourceFormat != sdktranslator.FormatClaude {
		return opts, false, nil
	}
	hasClaude := false
	for _, provider := range providers {
		if provider == "claude" {
			hasClaude = true
			break
		}
	}
	if !hasClaude {
		return opts, false, nil
	}
	previousID := ojson.GetBytesNoCopy(req.Payload, "thread.previous_message_id").String()
	if previousID == "" {
		return opts, false, nil
	}
	callerScope := cliproxysession.ClaudeThreadCallerScope(coreusage.APIKeyFromContext(ctx), opts.Metadata, req.Metadata)
	owner, found, errOwner := cliproxysession.ClaudeThreadAuth(ctx, callerScope, previousID)
	if errOwner != nil {
		if ctx != nil && ctx.Err() != nil {
			return opts, false, ctx.Err()
		}
		log.WithError(errOwner).Warn("Claude Thread credential binding lookup failed")
		return opts, false, &Error{Code: "thread_auth_unavailable", Message: "Claude Thread credential binding is temporarily unavailable", Retryable: true, HTTPStatus: http.StatusServiceUnavailable}
	}
	if !found {
		return opts, false, nil
	}
	if pinned := pinnedAuthIDFromMetadata(opts.Metadata); pinned != "" && pinned != owner {
		return opts, false, &Error{Code: "thread_auth_conflict", Message: "the pinned credential does not own this Claude Thread; replay with thread create", HTTPStatus: http.StatusConflict}
	}
	opts.Metadata = cloneRequestMetadata(opts.Metadata)
	if opts.Metadata == nil {
		opts.Metadata = make(map[string]any)
	}
	opts.Metadata[cliproxyexecutor.PinnedAuthMetadataKey] = owner
	return opts, true, nil
}

// claudeThreadOwnerUnavailableMessage keeps the wording Claude clients match
// for a stale continuation ("thread state" and "previous_message_id").
const claudeThreadOwnerUnavailableMessage = "No thread state was found for the requested previous_message_id on an available credential. Replay the full conversation with thread create to start a new Thread."

// claudeThreadReplayError turns a pinned continuation's terminal failure into
// thread_not_found when the owning credential cannot serve it. Thread state
// lives in the owner's upstream account and the continuation carries only the
// new messages, so no other credential can take it over; the client must
// replay with thread create, which ordinary selection then routes.
func claudeThreadReplayError(err error) error {
	if !isClaudeThreadOwnerUnavailable(err) {
		return err
	}
	log.WithError(err).Info("Claude Thread owner credential is unavailable; asking the client to replay with thread create")
	return &Error{Code: "thread_not_found", Message: claudeThreadOwnerUnavailableMessage, HTTPStatus: http.StatusNotFound}
}

// isClaudeThreadOwnerUnavailable reports failures that rotate credentials for
// ordinary requests: the owner is missing, disabled, cooling down, out of quota,
// or rejected its token. Request faults and short-lived failures (5xx, overload,
// transient rate limits, Home concurrency admission) keep their status and
// retry hints, so the client retries the same warm thread.
func isClaudeThreadOwnerUnavailable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if homeBusy, ok := errors.AsType[*HomeConcurrencyBusyError](err); ok && homeBusy != nil {
		return false
	}
	if isTransientRateLimitError(err) {
		return false
	}
	if isCredentialScopedError(err) || isAuthUnavailableError(err) || isInvalidGrantError(err) || isCreditBalanceTooLowError(err) {
		return true
	}
	if isRequestScopedError(err) {
		return false
	}
	var authErr *Error
	if errors.As(err, &authErr) && authErr != nil && (authErr.Code == "auth_not_found" || authErr.Code == "session_auth_unavailable") {
		return true
	}
	switch statusCodeFromError(err) {
	case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden, http.StatusTooManyRequests:
		return true
	}
	return false
}
