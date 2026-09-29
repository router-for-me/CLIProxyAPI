package perfmetrics

import (
	"context"
	"errors"
	"strings"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
)

type Outcome string

const (
	OutcomeSuccess Outcome = "success"
	OutcomeFailure Outcome = "failure"
	OutcomeIgnored Outcome = "ignored"
)

// ClassifyRelayOutcome decides whether one finished relay counts as a health
// sample. Business rejections and client cancellations are not samples; the
// classification is independent of retries, channel disabling and billing.
func ClassifyRelayOutcome(ctx context.Context, info *relaycommon.RelayInfo, apiErr *types.NewAPIError) Outcome {
	if info == nil || info.PerformanceBusinessRejection {
		return OutcomeIgnored
	}
	if ctx != nil && ctx.Err() == context.Canceled {
		return OutcomeIgnored
	}
	if apiErr != nil && errors.Is(apiErr, context.Canceled) {
		return OutcomeIgnored
	}
	stream := info.StreamStatus.OutcomeSnapshot()
	if stream.Response == relaycommon.ResponseOutcomeFailed {
		return classifyFailure(false, stream.ErrorCode, stream.ErrorType, stream.ErrorStatus)
	}
	if apiErr != nil {
		root := rootAPIError(apiErr)
		local := root.GetErrorType() == types.ErrorTypeNewAPIError
		return classifyFailure(local, string(root.GetErrorCode()), root.ToOpenAIError().Type, root.StatusCode)
	}
	deadlineExceeded := info.StreamStatus != nil && errors.Is(info.StreamStatus.EndError, context.DeadlineExceeded)
	if stream.Response == relaycommon.ResponseOutcomeCancelled || stream.EndReason == relaycommon.StreamEndReasonPingFail {
		return OutcomeIgnored
	}
	if stream.EndReason == relaycommon.StreamEndReasonClientGone && !deadlineExceeded {
		return OutcomeIgnored
	}
	if stream.Response == relaycommon.ResponseOutcomeIncomplete {
		switch stream.IncompleteReason {
		case "max_output_tokens", "max_tokens":
			return OutcomeSuccess
		case "content_filter", "safety", "content_policy_violation":
			return OutcomeIgnored
		default:
			return OutcomeFailure
		}
	}
	if stream.HasErrors || deadlineExceeded {
		return OutcomeFailure
	}
	switch stream.EndReason {
	case relaycommon.StreamEndReasonTimeout, relaycommon.StreamEndReasonScannerErr, relaycommon.StreamEndReasonPanic:
		return OutcomeFailure
	}
	if stream.ExpectsTerminal && stream.Response == relaycommon.ResponseOutcomeUnknown && stream.EndReason != relaycommon.StreamEndReasonDone {
		return OutcomeFailure
	}
	return OutcomeSuccess
}

// rootAPIError follows host wrappers back to the error that describes what
// actually happened, e.g. an upstream credential error re-wrapped as a local
// invalid request.
func rootAPIError(apiErr *types.NewAPIError) *types.NewAPIError {
	for {
		var inner *types.NewAPIError
		if !errors.As(apiErr.Unwrap(), &inner) || inner == apiErr {
			return apiErr
		}
		apiErr = inner
	}
}

func classifyFailure(local bool, code, errorType string, status int) Outcome {
	code, errorType = strings.ToLower(code), strings.ToLower(errorType)
	if local {
		if strings.HasPrefix(code, "violation_fee.") {
			return OutcomeIgnored
		}
		switch types.ErrorCode(code) {
		case types.ErrorCodeInvalidRequest, types.ErrorCodeSensitiveWordsDetected, types.ErrorCodeReadRequestBodyFailed,
			types.ErrorCodeConvertRequestFailed, types.ErrorCodeAccessDenied, types.ErrorCodeBadRequestBody,
			types.ErrorCodeInsufficientUserQuota, types.ErrorCodePreConsumeTokenQuotaFailed, types.ErrorCodePromptBlocked:
			return OutcomeIgnored
		}
		return OutcomeFailure
	}
	// Specific codes take precedence over broad protocol types, because
	// providers also report invalid credentials as invalid_request_error.
	for _, value := range []string{code, errorType} {
		switch value {
		case "invalid_api_key", "api_key_invalid", "api_key_expired", "api_key_service_blocked", "invalid_authentication",
			"authentication_error", "unauthenticated", "permission_denied", "permission_error", "access_denied",
			"insufficient_quota", "quota_exceeded", "resource_exhausted", "rate_limit_exceeded", "rate_limit_error",
			"overloaded_error", "server_error", "internal_error", "service_unavailable", "model_not_found",
			"insufficient_user_quota", "pre_consume_token_quota_failed":
			return OutcomeFailure
		case "context_length_exceeded", "invalid_request", "invalid_request_error", "invalid_argument",
			"sensitive_words_detected", "prompt_blocked", "content_filter", "content_policy_violation", "safety":
			return OutcomeIgnored
		}
		if strings.HasPrefix(value, "violation_fee.") {
			return OutcomeIgnored
		}
	}
	switch status {
	case 400, 405, 409, 413, 415, 422:
		return OutcomeIgnored
	}
	return OutcomeFailure
}
