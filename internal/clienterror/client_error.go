// Package clienterror classifies upstream failures caused by the client request.
package clienterror

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// StatusClientClosedRequest is the nginx-style status used when the client
// aborts the request before the proxy finishes (context.Canceled).
const StatusClientClosedRequest = 499

var requestFaultCodes = map[string]struct{}{
	"cyber_policy":                {},
	"context_length_exceeded":     {},
	"message_too_big":             {},
	"string_above_max_length":     {},
	"invalid_prompt":              {},
	"invalid_value":               {},
	"unsupported_value":           {},
	"invalid_request_error":       {},
	"previous_response_not_found": {},
}

var requestFaultTypes = map[string]struct{}{
	"invalid_request":       {},
	"invalid_request_error": {},
	"bad_request_error":     {},
	"invalid_prompt":        {},
}

// HTTPStatusFromError extracts an HTTP status from err.
// Explicit StatusCode() values win. Otherwise context.Canceled maps to 499
// and context.DeadlineExceeded maps to 504. Returns 0 when unknown.
func HTTPStatusFromError(err error) int {
	if err == nil {
		return 0
	}
	type statusCoder interface {
		StatusCode() int
	}
	var sc statusCoder
	if errors.As(err, &sc) && sc != nil {
		if code := sc.StatusCode(); code > 0 {
			return code
		}
	}
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		return http.StatusRequestEntityTooLarge
	}
	if errors.Is(err, context.Canceled) {
		return StatusClientClosedRequest
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return http.StatusGatewayTimeout
	}
	return 0
}

// HTTPStatusFromErrorOr is like HTTPStatusFromError but returns fallback when
// the error does not carry a known status.
func HTTPStatusFromErrorOr(err error, fallback int) int {
	if code := HTTPStatusFromError(err); code > 0 {
		return code
	}
	return fallback
}

// IsRequestFault reports whether an upstream failure is caused by the request
// and therefore must not rotate or penalize credentials.
func IsRequestFault(status int, err error) bool {
	if status <= 0 && err != nil {
		type statusCoder interface {
			StatusCode() int
		}
		var statusErr statusCoder
		if errors.As(err, &statusErr) && statusErr != nil {
			status = statusErr.StatusCode()
		}
	}
	// Payment and rate-limit statuses are authoritative even when an upstream
	// pairs them with a generic invalid_request_error body. The credential must
	// remain eligible for cooldown and rotation.
	if status == http.StatusPaymentRequired || status == http.StatusTooManyRequests {
		return false
	}
	// DeepSeek reports an invalid API key as 401 with the authentication_error
	// type alongside the same generic code. Preserve that credential failure
	// classification without weakening generic request-fault handling.
	if status == http.StatusUnauthorized && hasAuthenticationErrorBody(err) {
		return false
	}
	// Claude's missing thread state is tied to the stale continuation ID rather
	// than the selected credential. Let the client replay the full conversation.
	if IsClaudeThreadNotFound(status, err) {
		return true
	}
	// Devin's MCP tool configuration and content-policy rejections are caused by
	// the request payload rather than credential permissions.
	if IsDevinRequestPermissionDenied(status, err) {
		return true
	}
	// Anthropic reports an exhausted API credit balance as 400 with an
	// invalid_request_error body. Preserve credential cooldown and rotation
	// without weakening generic 400 request-fault handling.
	if IsCreditBalanceTooLow(status, err) {
		return false
	}
	// Model not found indicates a credential-model capability mismatch rather than
	// a caller request error. Preserve rotation and cooldown for the model.
	if hasModelNotFoundErrorBody(err) {
		return false
	}
	if hasRequestFaultBody(err) {
		return true
	}
	if err != nil && (IsItemNotPersisted(err.Error()) || IsGeminiFilterBlockError(err) || IsClaudePolicyRefusalError(err)) {
		return true
	}
	switch status {
	case http.StatusBadRequest,
		http.StatusConflict,
		http.StatusRequestEntityTooLarge,
		http.StatusUnprocessableEntity:
		return true
	default:
		return false
	}
}

// IsItemNotPersisted matches the upstream 404 raised when a request references a
// response item the upstream never stored because `store` was false. The upstream
// sends this as a plain-text message rather than a JSON body, so it cannot be
// recognized through the structured identifiers above.
//
// The request can only succeed once the client rebuilds it without the stale
// reference, so it is a request fault: rotating credentials cannot help, and the
// client must be told rather than left to retry the same broken input.
func IsItemNotPersisted(message string) bool {
	lower := strings.ToLower(message)
	return strings.Contains(lower, "item with id") &&
		strings.Contains(lower, "not found") &&
		strings.Contains(lower, "items are not persisted when `store` is set to false")
}

// GeminiFilterBlockMessagePrefix is the leading text of Gemini's mid-stream content filter block frame.
const GeminiFilterBlockMessagePrefix = "This request was blocked by Gemini's filters."

// GeminiFilterBlockPolicyURL is the policy URL embedded in Gemini's mid-stream content filter block frame.
const GeminiFilterBlockPolicyURL = "https://policies.google.com/terms/generative-ai/use-policy"

// GeminiFilterBlockMessage inspects a raw Gemini / Antigravity JSON payload and returns the filter
// block message when the payload is an injected mid-stream Gemini content filter frame.
//
// Upstream injects this frame inside an HTTP 200 SSE stream with only candidates[0].content.parts[0].text
// and without content.role, responseId, modelVersion, or usageMetadata.
func GeminiFilterBlockMessage(payload []byte) (string, bool) {
	if len(payload) == 0 || !bytes.Contains(payload, []byte(GeminiFilterBlockMessagePrefix)) || !ojson.ValidBytes(payload) {
		return "", false
	}
	for _, path := range []string{"responseId", "response.responseId", "modelVersion", "response.modelVersion", "usageMetadata", "response.usageMetadata", "cpaUsageMetadata", "response.cpaUsageMetadata"} {
		if ojson.GetBytesNoCopy(payload, path).Exists() {
			return "", false
		}
	}
	for _, cPath := range []string{"candidates", "response.candidates"} {
		candidates := ojson.GetBytesNoCopy(payload, cPath)
		if !candidates.Exists() || candidates.Get("1").Exists() {
			continue
		}
		candidate := candidates.Get("0")
		if !candidate.Exists() {
			continue
		}
		if role := strings.TrimSpace(candidate.Get("content.role").String()); role != "" {
			return "", false
		}
		if finishReason := strings.TrimSpace(candidate.Get("finishReason").String()); finishReason != "" {
			return "", false
		}
		if candidate.Get("index").Exists() || candidate.Get("safetyRatings").Exists() || candidate.Get("citationMetadata").Exists() {
			return "", false
		}
		parts := candidate.Get("content.parts")
		if !parts.Exists() || parts.Get("1").Exists() {
			return "", false
		}
		part := parts.Get("0")
		if part.Get("thought").Exists() || part.Get("thoughtSignature").Exists() || part.Get("functionCall").Exists() {
			return "", false
		}
		text := strings.TrimSpace(part.Get("text").String())
		if isGeminiFilterBlockText(text) {
			return text, true
		}
	}
	return "", false
}

// IsGeminiFilterBlockError reports whether err carries a Gemini mid-stream content filter block message.
func IsGeminiFilterBlockError(err error) bool {
	if err == nil {
		return false
	}
	if isGeminiFilterBlockText(err.Error()) {
		return true
	}
	type responseBodyProvider interface {
		ResponseBody() []byte
	}
	var responseBody responseBodyProvider
	if errors.As(err, &responseBody) && responseBody != nil && len(responseBody.ResponseBody()) > 0 {
		if _, ok := GeminiFilterBlockMessage(responseBody.ResponseBody()); ok {
			return true
		}
		if isGeminiFilterBlockText(string(responseBody.ResponseBody())) {
			return true
		}
	}
	return false
}

func isGeminiFilterBlockText(text string) bool {
	trimmed := strings.TrimSpace(text)
	return strings.HasPrefix(trimmed, GeminiFilterBlockMessagePrefix) &&
		strings.Contains(trimmed, GeminiFilterBlockPolicyURL)
}

// ClaudePolicyRefusalMessage inspects a Claude Messages JSON payload (either a non-stream
// message object or a streaming message_delta event) and returns the refusal explanation
// when stop_details indicates a policy/cyber refusal block.
func ClaudePolicyRefusalMessage(payload []byte) (string, bool) {
	if len(payload) == 0 || !bytes.Contains(payload, []byte("stop_details")) || !ojson.ValidBytes(payload) {
		return "", false
	}
	for _, prefix := range []string{"delta", ""} {
		stopReasonPath := "stop_reason"
		stopDetailsPath := "stop_details"
		if prefix != "" {
			stopReasonPath = prefix + ".stop_reason"
			stopDetailsPath = prefix + ".stop_details"
		}
		stopReason := strings.ToLower(strings.TrimSpace(ojson.GetBytesNoCopy(payload, stopReasonPath).String()))
		details := ojson.GetBytesNoCopy(payload, stopDetailsPath)
		if !details.Exists() || details.Type != ojson.JSON {
			continue
		}
		detailsType := strings.ToLower(strings.TrimSpace(details.Get("type").String()))
		category := strings.ToLower(strings.TrimSpace(details.Get("category").String()))
		explanation := strings.TrimSpace(details.Get("explanation").String())
		if stopReason == "refusal" || detailsType == "refusal" || category == "cyber" {
			if explanation != "" {
				if category != "" {
					return "claude refusal (" + category + "): " + explanation, true
				}
				return "claude refusal: " + explanation, true
			}
			if category != "" {
				return "claude refusal (" + category + ")", true
			}
			if raw := strings.TrimSpace(string(details.Raw)); raw != "" {
				return "claude refusal: " + raw, true
			}
			return "claude refusal", true
		}
	}
	return "", false
}

// IsClaudePolicyRefusalError reports whether err represents a Claude policy/cyber refusal block.
func IsClaudePolicyRefusalError(err error) bool {
	if err == nil {
		return false
	}
	if isClaudePolicyRefusalText(err.Error()) {
		return true
	}
	if _, ok := ClaudePolicyRefusalMessage([]byte(err.Error())); ok {
		return true
	}
	type responseBodyProvider interface {
		ResponseBody() []byte
	}
	var responseBody responseBodyProvider
	if errors.As(err, &responseBody) && responseBody != nil && len(responseBody.ResponseBody()) > 0 {
		if _, ok := ClaudePolicyRefusalMessage(responseBody.ResponseBody()); ok {
			return true
		}
		if isClaudePolicyRefusalText(string(responseBody.ResponseBody())) {
			return true
		}
	}
	return false
}

func isClaudePolicyRefusalText(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	return lower == "claude refusal" ||
		strings.HasPrefix(lower, "claude refusal (") ||
		strings.HasPrefix(lower, "claude refusal:") ||
		(strings.Contains(lower, "triggered restrictions on violative cyber content") &&
			strings.Contains(lower, "anthropic's usage policy"))
}

// IsClaudeThreadNotFound reports the Claude 404 response for a stale
// previous_message_id continuation from either a structured JSON response
// or a plain-text error message.
func IsClaudeThreadNotFound(status int, err error) bool {
	if status != http.StatusNotFound || err == nil {
		return false
	}
	body := strings.TrimSpace(err.Error())
	type responseBodyProvider interface {
		ResponseBody() []byte
	}
	var responseBody responseBodyProvider
	if errors.As(err, &responseBody) && responseBody != nil && len(responseBody.ResponseBody()) > 0 {
		body = strings.TrimSpace(string(responseBody.ResponseBody()))
	}
	if body == "" {
		return false
	}
	if ojson.Valid(body) {
		msg := strings.ToLower(ojson.Get(body, "error.message").String())
		return strings.EqualFold(strings.TrimSpace(ojson.Get(body, "error.type").String()), "not_found_error") &&
			strings.Contains(msg, "thread state") &&
			strings.Contains(msg, "previous_message_id")
	}
	lower := strings.ToLower(body)
	return strings.Contains(lower, "thread state") && strings.Contains(lower, "previous_message_id")
}

// IsDevinRequestPermissionDenied reports whether a 403 error from Devin is
// deterministically caused by the request payload (such as the MCP tool
// configuration filter or the prompt content policy filter) rather than
// credential permissions.
func IsDevinRequestPermissionDenied(status int, err error) bool {
	if status <= 0 && err != nil {
		status = HTTPStatusFromError(err)
	}
	if status != http.StatusForbidden || err == nil {
		return false
	}
	if isDevinRequestPermissionDeniedText(err.Error()) {
		return true
	}
	type responseBodyProvider interface {
		ResponseBody() []byte
	}
	var responseBody responseBodyProvider
	if errors.As(err, &responseBody) && responseBody != nil && len(responseBody.ResponseBody()) > 0 {
		return isDevinRequestPermissionDeniedText(string(responseBody.ResponseBody()))
	}
	return false
}

func isDevinRequestPermissionDeniedText(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" || !strings.Contains(lower, "permission_denied") {
		return false
	}
	return strings.Contains(lower, "unable to process request due to an mcp configuration issue") ||
		strings.Contains(lower, "blocked by our content policy")
}

// IsCreditBalanceTooLow reports the Anthropic 400 response returned when a
// credential's API credit balance is exhausted.
func IsCreditBalanceTooLow(status int, err error) bool {
	if status <= 0 && err != nil {
		status = HTTPStatusFromError(err)
	}
	if status != http.StatusBadRequest || err == nil {
		return false
	}
	return hasCreditBalanceTooLowBody(err)
}

func hasCreditBalanceTooLowBody(err error) bool {
	if err == nil {
		return false
	}
	body := strings.TrimSpace(err.Error())
	type responseBodyProvider interface {
		ResponseBody() []byte
	}
	var responseBody responseBodyProvider
	if errors.As(err, &responseBody) && responseBody != nil && len(responseBody.ResponseBody()) > 0 {
		body = strings.TrimSpace(string(responseBody.ResponseBody()))
	}
	if body == "" || !ojson.Valid(body) {
		return false
	}
	for _, path := range []string{"error.message", "message", "response.error.message", "body.error.message"} {
		msg := strings.ToLower(strings.TrimSpace(ojson.Get(body, path).String()))
		if strings.HasPrefix(msg, "your credit balance is too low") {
			return true
		}
	}
	return false
}

func hasModelNotFoundErrorBody(err error) bool {
	if err == nil {
		return false
	}
	body := strings.TrimSpace(err.Error())
	if body == "" || !ojson.Valid(body) {
		return false
	}
	for _, path := range []string{"error.code", "code", "response.error.code", "body.error.code"} {
		code := strings.ToLower(strings.TrimSpace(ojson.Get(body, path).String()))
		if code == "model_not_found" || code == "model_not_found_error" {
			return true
		}
	}
	return false
}

func hasAuthenticationErrorBody(err error) bool {
	if err == nil {
		return false
	}
	body := strings.TrimSpace(err.Error())
	if body == "" || !ojson.Valid(body) {
		return false
	}
	for _, path := range []string{"error.type", "type", "response.error.type", "body.error.type"} {
		if errType := strings.ToLower(strings.TrimSpace(ojson.Get(body, path).String())); errType == "authentication_error" {
			return true
		}
	}
	return false
}

func hasRequestFaultBody(err error) bool {
	if err == nil {
		return false
	}
	body := strings.TrimSpace(err.Error())
	if body == "" || !ojson.Valid(body) {
		return false
	}
	for _, path := range []string{"error.code", "code", "response.error.code", "body.error.code"} {
		code := strings.ToLower(strings.TrimSpace(ojson.Get(body, path).String()))
		if _, ok := requestFaultCodes[code]; ok {
			return true
		}
	}
	for _, path := range []string{"error.type", "type", "response.error.type", "body.error.type"} {
		errType := strings.ToLower(strings.TrimSpace(ojson.Get(body, path).String()))
		if _, ok := requestFaultTypes[errType]; ok {
			return true
		}
	}
	return false
}

// IsClientCancellation reports whether an HTTP status code or error represents
// a client-initiated cancellation (HTTP 499 StatusClientClosedRequest or context.Canceled).
func IsClientCancellation(status int, err error) bool {
	if status == StatusClientClosedRequest {
		return true
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return true
		}
		type statusCoder interface {
			StatusCode() int
		}
		var sc statusCoder
		if errors.As(err, &sc) && sc != nil && sc.StatusCode() == StatusClientClosedRequest {
			return true
		}
		lower := strings.ToLower(err.Error())
		if strings.Contains(lower, "context canceled") || strings.Contains(lower, "client closed request") {
			return true
		}
	}
	return false
}
