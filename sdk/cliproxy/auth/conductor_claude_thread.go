package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/MoeForge/moego/ojson"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	cliproxysession "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
)

// withClaudeThreadAuth pins continuations before ordinary selection, including
// default round-robin. It never bypasses the selector's availability checks.
// It also reports whether it pinned the request to a Thread owner credential.
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

// claudeThreadReplayError translates terminal owner-credential unavailability on
// a pinned Claude Thread continuation into a 404 thread_not_found error so
// thread-aware clients replay the full conversation with thread create.
func claudeThreadReplayError(err error) error {
	if !shouldRewriteClaudeThreadOwnerUnavailable(err) {
		return err
	}
	log.WithError(err).Info("Claude Thread owner credential is unavailable; asking the client to replay with thread create")
	return &Error{
		Code:       "thread_not_found",
		Message:    "No thread state was found for the requested previous_message_id on an available credential. Replay the full conversation with thread create to start a new Thread.",
		HTTPStatus: http.StatusNotFound,
	}
}

func shouldRewriteClaudeThreadOwnerUnavailable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if isRequestTerminatedError(err) || isRequestStopError(err) {
		return false
	}
	var homeBusy *HomeConcurrencyBusyError
	if errors.As(err, &homeBusy) && homeBusy != nil {
		return false
	}
	if isTransientRateLimitError(err) {
		return false
	}
	if isRequestInvalidError(err) {
		return false
	}
	var cooldownErr *modelCooldownError
	if errors.As(err, &cooldownErr) && cooldownErr != nil {
		return true
	}
	var poolCooldownErr *poolCooldownTerminalError
	if errors.As(err, &poolCooldownErr) && poolCooldownErr != nil {
		return true
	}
	code := strings.ToLower(errorCodeFromError(err))
	switch code {
	case "user_credits_insufficient", "user_period_limit_exceeded":
		return false
	case "auth_not_found", "auth_unavailable", "model_cooldown", "session_auth_unavailable":
		return true
	}
	if isCredentialScopedError(err) || isInvalidGrantError(err) || isCreditBalanceTooLowError(err) {
		return true
	}
	switch statusCodeFromError(err) {
	case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden, http.StatusTooManyRequests:
		return true
	}
	return false
}
