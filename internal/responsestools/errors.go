package responsestools

import (
	"errors"
	"fmt"
	"net/http"
)

// Reason identifies one fixed bridge failure cause for diagnostics. Payloads,
// prompts, grammars, and credentials must never be attached to a Reason.
type Reason string

const (
	ReasonJSONSyntax           Reason = "json_syntax"
	ReasonHistoryLink          Reason = "history_link"
	ReasonUnsupportedProtocol  Reason = "unsupported_protocol"
	ReasonUnsupportedGrammar   Reason = "unsupported_grammar"
	ReasonOpaqueHistory        Reason = "opaque_history"
	ReasonDeclarationBudget    Reason = "declaration_budget"
	ReasonAttemptBudget        Reason = "attempt_budget"
	ReasonSharedCapacity       Reason = "shared_capacity"
	ReasonUpstreamContract     Reason = "upstream_contract"
	ReasonSchemaPolicy         Reason = "schema_policy"
	ReasonCustomForcedCall     Reason = "custom_forced_call"
	ReasonAmbiguousIdentity    Reason = "ambiguous_identity"
	ReasonInvalidCustomInput   Reason = "invalid_custom_input"
	ReasonInvalidConfiguration Reason = "invalid_configuration"
)

// ToolCompatibilityError is a typed bridge failure. Request-scoped errors must
// never rotate credentials or cool them down; only genuine upstream failures
// keep the existing retry behavior.
type ToolCompatibilityError struct {
	Reason Reason
	status int
	Err    error
}

func (e *ToolCompatibilityError) Error() string {
	if e == nil {
		return "responses tools error"
	}
	if e.Err != nil {
		return fmt.Sprintf("responses tools %s: %v", string(e.Reason), e.Err)
	}
	return fmt.Sprintf("responses tools %s", string(e.Reason))
}

func (e *ToolCompatibilityError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// StatusCode exposes the HTTP status for handler and conductor error mapping.
func (e *ToolCompatibilityError) StatusCode() int {
	if e == nil || e.status == 0 {
		return http.StatusUnprocessableEntity
	}
	return e.status
}

// IsRequestScoped reports that the failure belongs to this request, not to
// the credential or upstream health.
func (e *ToolCompatibilityError) IsRequestScoped() bool {
	return true
}

// Status keeps the older handler-facing accessor compatible.
func (e *ToolCompatibilityError) Status() int {
	return e.StatusCode()
}

// RequestScoped keeps the older package-local accessor compatible.
func (e *ToolCompatibilityError) RequestScoped() bool {
	return e.IsRequestScoped()
}

// AvailabilityNeutral reports that the failure must neither penalize the
// credential nor count as a successful quota refresh.
func (e *ToolCompatibilityError) AvailabilityNeutral() bool {
	return true
}

func newError(reason Reason, status int, err error) *ToolCompatibilityError {
	return &ToolCompatibilityError{Reason: reason, status: status, Err: err}
}

func syntaxError(err error) *ToolCompatibilityError {
	return newError(ReasonJSONSyntax, http.StatusBadRequest, err)
}

func unprocessableError(reason Reason, err error) *ToolCompatibilityError {
	return newError(reason, http.StatusUnprocessableEntity, err)
}

func budgetError(reason Reason, err error) *ToolCompatibilityError {
	return newError(reason, http.StatusRequestEntityTooLarge, err)
}

func capacityError(err error) *ToolCompatibilityError {
	return newError(ReasonSharedCapacity, http.StatusTooManyRequests, err)
}

func upstreamError(reason Reason, err error) *ToolCompatibilityError {
	return newError(reason, http.StatusBadGateway, err)
}

// IsRequestScopedError reports whether err is a bridge request-scoped error.
func IsRequestScopedError(err error) bool {
	var compat *ToolCompatibilityError
	if errors.As(err, &compat) && compat != nil {
		return true
	}
	var scoped interface{ IsRequestScoped() bool }
	return errors.As(err, &scoped) && scoped.IsRequestScoped()
}

func jsonErrorf(format string, args ...any) error {
	return fmt.Errorf(format, args...)
}
