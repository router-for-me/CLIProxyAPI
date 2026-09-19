package helps

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
)

// This file centralises the log-safe projection of websocket lifecycle values.
//
// Codex and xAI websocket lifecycle logs previously interpolated session IDs,
// auth IDs, full upstream URLs (including query strings that can carry
// credentials) and raw provider errors. Those values originate from
// client-supplied request headers, stored credentials and upstream responses,
// so they must never reach an application log.
//
// Every helper here converts an untrusted input into a value drawn from a
// closed set of in-repository literals, or into a fixed placeholder. No helper
// returns a substring of its argument, so a caller cannot reintroduce a leak by
// forwarding the result to a log call.

const (
	// redactedLogPlaceholder marks a value that was present but withheld.
	redactedLogPlaceholder = "[redacted]"
	// absentLogPlaceholder marks a value that was empty at the call site.
	absentLogPlaceholder = "none"
	// unknownLogPlaceholder marks a value that is neither empty nor allowlisted.
	unknownLogPlaceholder = "other"
)

// safeWebsocketLifecycleReasons is the closed set of lifecycle reason codes
// produced inside this repository. Reasons come from our own control flow
// rather than from a provider or a client, but they are still funnelled through
// an allowlist so a future caller cannot pass an upstream string through
// without also updating this list.
var safeWebsocketLifecycleReasons = []string{
	"auth_removed",
	"completed",
	"connection_closed",
	"context_done",
	"error",
	"executor_shutdown",
	"invalidated",
	"read_error",
	"send_error",
	"session_closed",
	"target_changed",
	"terminal_empty_incomplete",
	"terminal_failure",
	"test_invalidate",
	"unexpected_binary",
	"upstream_disconnected",
	"upstream_error",
}

// safeWebsocketEventTypes is the closed set of upstream event types that may be
// named in a log line. Event types arrive inside provider payloads, so an
// unlisted value is reported as unknownLogPlaceholder instead of being echoed.
var safeWebsocketEventTypes = []string{
	"error",
	"response.append",
	"response.completed",
	"response.create",
	"response.created",
	"response.done",
	"response.failed",
	"response.incomplete",
	"response.output_item.done",
}

// safeWebsocketCloseStages names the close call sites that may appear in a log
// line. Stages are in-repository literals chosen at each call site.
var safeWebsocketCloseStages = []string{
	"active",
	"duplicate",
	"lifecycle_bind_failure",
	"session_close",
	"stale",
	"stream",
	"stream_teardown",
}

// SafeWebsocketCloseStage maps a close stage onto the allowlist.
func SafeWebsocketCloseStage(stage string) string {
	trimmed := strings.TrimSpace(stage)
	if trimmed == "" {
		return absentLogPlaceholder
	}
	if matched := matchSafeLogLiteral(safeWebsocketCloseStages, trimmed); matched != "" {
		return matched
	}
	return unknownLogPlaceholder
}

// matchSafeLogLiteral returns the allowlist entry equal to value, or "" when
// there is no match. The returned string is the allowlist literal itself, never
// a slice of value, which is what keeps the caller's value out of the log.
func matchSafeLogLiteral(allowlist []string, value string) string {
	for i := range allowlist {
		if allowlist[i] == value {
			return allowlist[i]
		}
	}
	return ""
}

// SafeWebsocketLifecycleReason maps a lifecycle reason onto the allowlist.
func SafeWebsocketLifecycleReason(reason string) string {
	trimmed := strings.TrimSpace(reason)
	if trimmed == "" {
		return absentLogPlaceholder
	}
	if matched := matchSafeLogLiteral(safeWebsocketLifecycleReasons, trimmed); matched != "" {
		return matched
	}
	return unknownLogPlaceholder
}

// SafeWebsocketEventType maps an upstream event type onto the allowlist.
func SafeWebsocketEventType(eventType string) string {
	trimmed := strings.TrimSpace(eventType)
	if trimmed == "" {
		return absentLogPlaceholder
	}
	if matched := matchSafeLogLiteral(safeWebsocketEventTypes, trimmed); matched != "" {
		return matched
	}
	return unknownLogPlaceholder
}

// SafeWebsocketModel resolves a requested model name to the matching model ID
// held in the embedded model catalogue. The returned string is a
// catalogue-owned literal rather than the client-supplied request value. An
// unrecognised or dynamically discovered model is reported as
// unknownLogPlaceholder.
func SafeWebsocketModel(provider string, model string) string {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return absentLogPlaceholder
	}
	baseModel := strings.TrimSpace(thinking.ParseSuffix(trimmed).ModelName)
	if baseModel == "" {
		return unknownLogPlaceholder
	}
	var candidates []*registry.ModelInfo
	switch strings.TrimSpace(provider) {
	case "codex":
		candidates = append(candidates, registry.GetCodexFreeModels()...)
		candidates = append(candidates, registry.GetCodexTeamModels()...)
		candidates = append(candidates, registry.GetCodexPlusModels()...)
		candidates = append(candidates, registry.GetCodexProModels()...)
	case "xai":
		candidates = registry.GetXAIModels()
	default:
		return unknownLogPlaceholder
	}
	for _, candidate := range candidates {
		if candidate == nil {
			continue
		}
		catalogueID := strings.TrimSpace(candidate.ID)
		if catalogueID != "" && catalogueID == baseModel {
			return catalogueID
		}
	}
	return unknownLogPlaceholder
}

// SafeWebsocketErrorDiagnostic converts an error into a diagnostic built only
// from structural properties of the error: sentinel identity, the net.Error
// timeout flag, the numeric websocket close code and the numeric HTTP status.
//
// The error's own message is never read, so provider wording that embeds
// tokens, account identifiers or URLs cannot reach the log. Numeric codes are
// rendered from integers rather than from any part of the error string.
func SafeWebsocketErrorDiagnostic(err error) string {
	if err == nil {
		return absentLogPlaceholder
	}

	signals := make([]string, 0, 4)
	appendSignal := func(signal string) {
		if signal == "" {
			return
		}
		for i := range signals {
			if signals[i] == signal {
				return
			}
		}
		signals = append(signals, signal)
	}

	switch {
	case errors.Is(err, io.ErrUnexpectedEOF):
		appendSignal("unexpected_eof")
	case errors.Is(err, io.EOF):
		appendSignal("eof")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		appendSignal("timeout")
	}
	if errors.Is(err, context.Canceled) {
		appendSignal("canceled")
	}
	if errors.Is(err, net.ErrClosed) {
		appendSignal("connection_closed")
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr != nil && netErr.Timeout() {
		appendSignal("timeout")
	}
	var closeErr *websocket.CloseError
	if errors.As(err, &closeErr) && closeErr != nil {
		// Only the numeric close code is reported. closeErr.Text is provider
		// controlled and is deliberately dropped.
		appendSignal("close_code=" + safeNumericCode(closeErr.Code, 1000, 4999))
	}
	var statusProvider interface{ StatusCode() int }
	if errors.As(err, &statusProvider) && statusProvider != nil {
		appendSignal("status=" + safeNumericCode(statusProvider.StatusCode(), 100, 599))
	}

	if len(signals) == 0 {
		return redactedLogPlaceholder
	}
	return strings.Join(signals, ",")
}

// safeNumericCode renders an integer code that falls inside the supplied
// inclusive range. Out-of-range codes collapse to a placeholder so a hostile
// value cannot widen the log line.
func safeNumericCode(code int, minCode int, maxCode int) string {
	if code < minCode || code > maxCode {
		return unknownLogPlaceholder
	}
	return strconv.Itoa(code)
}
