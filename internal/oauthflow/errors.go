package oauthflow

import (
	"fmt"
)

// FlowErrorKind categorizes failures in OAuth flows so callers can map them to provider-specific errors.
type FlowErrorKind string

const (
	FlowErrorKindPortInUse         FlowErrorKind = "port_in_use"
	FlowErrorKindServerStartFailed FlowErrorKind = "server_start_failed"
	FlowErrorKindAuthorizeURLFailed FlowErrorKind = "authorize_url_failed"
	FlowErrorKindCallbackTimeout   FlowErrorKind = "callback_timeout"
	FlowErrorKindProviderError     FlowErrorKind = "provider_error"
	FlowErrorKindInvalidState      FlowErrorKind = "invalid_state"
	FlowErrorKindCodeExchangeFailed FlowErrorKind = "code_exchange_failed"
)

// FlowError wraps an underlying error with a stable kind for callers to inspect.
type FlowError struct {
	Kind FlowErrorKind
	Err  error
}

func (e *FlowError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err == nil {
		return fmt.Sprintf("oauthflow: %s", e.Kind)
	}
	return fmt.Sprintf("oauthflow: %s: %v", e.Kind, e.Err)
}

func (e *FlowError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}
