package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
)

const (
	maxHomeConcurrencyTupleFieldLength = 256
	asciiWhitespace                    = " \t\r\n\v\f"
)

var ErrMalformedHomeConcurrencyTuple = errors.New("malformed Home concurrency tuple")

// HomeConcurrencyBusyError is a trusted, Home-originated concurrency admission failure.
type HomeConcurrencyBusyError struct {
	cause      *Error
	retryAfter time.Duration
}

// NewHomeConcurrencyBusyError creates a typed Home concurrency busy error.
func NewHomeConcurrencyBusyError(message string, retryAfter time.Duration) error {
	message = strings.TrimSpace(message)
	if message == "" {
		message = "credential concurrency limit exceeded"
	}
	return newHomeConcurrencyBusyError(&Error{
		Code:       "credential_concurrency_exceeded",
		Message:    message,
		Retryable:  true,
		HTTPStatus: http.StatusTooManyRequests,
	}, retryAfter)
}

func newHomeConcurrencyBusyError(cause *Error, retryAfter time.Duration) *HomeConcurrencyBusyError {
	return &HomeConcurrencyBusyError{cause: cause, retryAfter: retryAfter}
}

func (e *HomeConcurrencyBusyError) Error() string {
	if e == nil || e.cause == nil {
		return ""
	}
	return e.cause.Error()
}

// Unwrap preserves the Home error's code, retryability, and status for errors.As callers.
func (e *HomeConcurrencyBusyError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *HomeConcurrencyBusyError) StatusCode() int {
	if e == nil || e.cause == nil {
		return 0
	}
	return e.cause.StatusCode()
}

func (e *HomeConcurrencyBusyError) RetryAfter() *time.Duration {
	if e == nil || e.retryAfter <= 0 {
		return nil
	}
	value := e.retryAfter
	return &value
}

func (e *HomeConcurrencyBusyError) SafeResponseHeaders() http.Header {
	if e == nil {
		return nil
	}
	return safeRetryAfterHeader(e.retryAfter)
}

type homeConcurrencyTuple struct {
	Accounted    bool   `json:"accounted"`
	CredentialID string `json:"credential_id"`
	Model        string `json:"model"`
}

func validateAccountedHomeConcurrencyTuple(tuple homeConcurrencyTuple) error {
	model, validModel := validCanonicalHomeConcurrencyModelKey(tuple.Model)
	if !tuple.Accounted || !validHomeConcurrencyTupleField(tuple.CredentialID) || !validModel || tuple.Model != model {
		return ErrMalformedHomeConcurrencyTuple
	}
	return nil
}

// canonicalHomeConcurrencyModelKey removes recognized reasoning suffixes from a Home limiter model key.
func canonicalHomeConcurrencyModelKey(model string) string {
	if !utf8.ValidString(model) {
		return ""
	}
	trimmed := strings.ToLower(strings.Trim(model, asciiWhitespace))
	if !strings.HasSuffix(trimmed, ")") {
		return trimmed
	}
	open := strings.LastIndexByte(trimmed, '(')
	if open < 0 {
		return trimmed
	}
	suffix := trimmed[open+1 : len(trimmed)-1]
	if !recognizedHomeConcurrencySuffix(suffix) {
		return trimmed
	}
	base := strings.Trim(trimmed[:open], asciiWhitespace)
	if base == "" {
		return trimmed
	}
	return base
}

func validCanonicalHomeConcurrencyModelKey(model string) (string, bool) {
	key := canonicalHomeConcurrencyModelKey(model)
	return key, key != "" && utf8.ValidString(key) && len(key) <= maxHomeConcurrencyTupleFieldLength
}

func recognizedHomeConcurrencySuffix(value string) bool {
	if value == "-1" {
		return true
	}
	switch strings.ToLower(value) {
	case "none", "auto", "minimal", "low", "medium", "high", "xhigh", "max":
		return true
	}
	if value == "" || len(value) > 10 {
		return false
	}
	var parsed int64
	for index := 0; index < len(value); index++ {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
		parsed = parsed*10 + int64(value[index]-'0')
		if parsed > 2_147_483_647 {
			return false
		}
	}
	return true
}

func validHomeConcurrencyTupleField(value string) bool {
	return value != "" && utf8.ValidString(value) && strings.TrimSpace(value) == value && len(value) <= maxHomeConcurrencyTupleFieldLength
}

func installHomeConcurrencyScope(registry *executionregistry.Registry, pending *executionregistry.PendingDispatch, tuple homeConcurrencyTuple, base executionregistry.ScopeSpec) (*executionregistry.Scope, error) {
	if registry == nil || pending == nil {
		return nil, executionregistry.ErrInvalidPendingDispatch
	}
	if !tuple.Accounted {
		base.Accounted = false
		return registry.Install(pending, base)
	}
	if errValidate := validateAccountedHomeConcurrencyTuple(tuple); errValidate != nil {
		return nil, errValidate
	}

	base.CredentialID = tuple.CredentialID
	base.Model = tuple.Model
	base.Accounted = true
	return registry.Install(pending, base)
}

type homeDispatchConcurrencyEnvelope struct {
	Tuple   homeConcurrencyTuple
	Present bool
}

func decodeHomeDispatchConcurrencyEnvelope(raw []byte) (homeDispatchConcurrencyEnvelope, error) {
	if !utf8.Valid(raw) {
		return homeDispatchConcurrencyEnvelope{}, errors.New("Home response is not valid UTF-8")
	}

	var fields map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(raw, &fields); errUnmarshal != nil || fields == nil {
		return homeDispatchConcurrencyEnvelope{}, errors.New("Home response is not a JSON object")
	}

	envelope := homeDispatchConcurrencyEnvelope{}
	rawTuple, present := fields["concurrency"]
	if !present {
		return envelope, nil
	}
	envelope.Present = true
	if errUnmarshal := json.Unmarshal(rawTuple, &envelope.Tuple); errUnmarshal != nil {
		return envelope, errUnmarshal
	}
	if errValidate := validateAccountedHomeConcurrencyTuple(envelope.Tuple); errValidate != nil {
		return envelope, errValidate
	}
	return envelope, nil
}

func canonicalHomeDispatchModel(responseModel, requestedModel string) string {
	if model := strings.TrimSpace(responseModel); model != "" {
		return model
	}
	return requestedModel
}

func decodeHomeDispatchError(raw []byte) error {
	var fields map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(raw, &fields); errUnmarshal != nil || fields == nil {
		return nil
	}
	rawError, present := fields["error"]
	if !present {
		return nil
	}

	var detail *homeErrorDetail
	if errUnmarshal := json.Unmarshal(rawError, &detail); errUnmarshal != nil || detail == nil {
		return &Error{Code: "invalid_auth", Message: "home returned malformed error payload", HTTPStatus: http.StatusBadGateway}
	}
	code := strings.TrimSpace(detail.Type)
	if code == "" {
		code = strings.TrimSpace(detail.Code)
	}
	if code == "" {
		return &Error{Code: "invalid_auth", Message: "home returned malformed error payload", HTTPStatus: http.StatusBadGateway}
	}
	message := strings.TrimSpace(detail.Message)
	if message == "" {
		message = "home returned error"
	}

	result := &Error{Code: code, Message: message, Retryable: detail.Retryable, HTTPStatus: http.StatusBadGateway}
	switch strings.ToLower(code) {
	case "model_not_found":
		result.HTTPStatus = http.StatusNotFound
	case "authentication_error", "unauthorized", "no_credentials", "invalid_credential":
		result.HTTPStatus = http.StatusUnauthorized
	case "credential_concurrency_exceeded", "credential_model_concurrency_exceeded":
		result.HTTPStatus = http.StatusTooManyRequests
		return newHomeConcurrencyBusyError(result, time.Duration(detail.RetryAfterMS)*time.Millisecond)
	case "concurrency_protocol_required", "concurrency_tracker_unavailable", "concurrency_node_unavailable":
		result.HTTPStatus = http.StatusServiceUnavailable
	}
	return result
}

func invalidHomeConcurrencyResponse(message string) error {
	return &Error{Code: "invalid_home_concurrency", Message: message, HTTPStatus: http.StatusBadGateway}
}

func verifyAccountedHomeConcurrencyIdentity(tuple homeConcurrencyTuple, auth *Auth, authIndex string) error {
	if !tuple.Accounted {
		return nil
	}
	if auth == nil || auth.ID != tuple.CredentialID || authIndex != tuple.CredentialID {
		return invalidHomeConcurrencyResponse("Home concurrency identity does not match dispatched auth")
	}
	return nil
}

// SafeResponseHeaders returns trusted response headers only for CPA's concrete Home busy error.
func SafeResponseHeaders(err error) http.Header {
	var busy *HomeConcurrencyBusyError
	if !errors.As(err, &busy) || busy == nil {
		return nil
	}
	return busy.SafeResponseHeaders()
}

func safeRetryAfterHeader(retryAfter time.Duration) http.Header {
	if retryAfter <= 0 {
		return nil
	}
	seconds := int64(retryAfter / time.Second)
	if retryAfter%time.Second != 0 {
		seconds++
	}
	if seconds < 1 {
		seconds = 1
	}
	return http.Header{"Retry-After": []string{strconv.FormatInt(seconds, 10)}}
}

func homeConcurrencyInstallError(err error) error {
	if errors.Is(err, ErrMalformedHomeConcurrencyTuple) {
		return invalidHomeConcurrencyResponse(err.Error())
	}
	return &Error{Code: "home_unavailable", Message: fmt.Sprintf("home execution registry unavailable: %v", err), Retryable: true, HTTPStatus: http.StatusServiceUnavailable}
}
