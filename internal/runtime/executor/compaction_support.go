package executor

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

// remoteCompactionV2ProtocolError identifies a failure in the compaction
// response contract. It deliberately remains status-bearing for downstream
// clients while preventing credential rotation and cooldown side effects.
type remoteCompactionV2ProtocolError struct {
	statusErr
}

func (remoteCompactionV2ProtocolError) IsRequestScoped() bool { return true }

func (e remoteCompactionV2ProtocolError) Unwrap() error { return e.statusErr }

func newRemoteCompactionV2ProtocolError(code int, message string) error {
	return remoteCompactionV2ProtocolError{statusErr: statusErr{code: code, msg: message, requestScoped: true}}
}

func rejectUnsupportedCompaction(opts cliproxyexecutor.Options, payloads ...[]byte) error {
	if opts.Alt == "responses/compact" {
		return statusErr{code: http.StatusNotImplemented, msg: "/responses/compact not supported"}
	}
	if helps.ResponsesHasCompactionTrigger(payloads...) {
		return newRemoteCompactionV2ProtocolError(http.StatusNotImplemented, "remote compaction v2 is not supported for this provider")
	}
	return nil
}

func remoteCompactionV2MissingItemErr(payload []byte) error {
	typed, total := helps.ResponsesOutputItemCounts(payload, "compaction")
	if typed == 1 {
		return nil
	}
	return newRemoteCompactionV2ProtocolError(http.StatusBadGateway, fmt.Sprintf("remote compaction v2 expected exactly one compaction output item, got %d from %d output items", typed, total))
}

// validateRemoteCompactionV2Response enforces the terminal response contract
// for a compaction_trigger request. An incomplete (or any other non-completed)
// terminal event is an upstream failure even when it happens to contain a
// compaction item; that item must never be replayed as a successful state.
//
// eventType is the SSE event type when the response came from a stream. For a
// non-stream response it may be empty; in that case the response status fields
// are used when present.
func validateRemoteCompactionV2Response(payload []byte, eventType string) error {
	eventType = strings.TrimSpace(eventType)
	if eventType != "" && eventType != "response.completed" {
		if eventType == "response.incomplete" {
			return remoteCompactionV2IncompleteErr()
		}
		return newRemoteCompactionV2ProtocolError(http.StatusBadGateway, fmt.Sprintf("remote compaction v2 expected response.completed, got %s", eventType))
	}

	status := ""
	if eventType != "" {
		// SSE terminal events carry the actual response object under response;
		// do not let an envelope status hide a missing response.status field.
		status = strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "response.status").String()))
	} else {
		status = strings.ToLower(strings.TrimSpace(remoteCompactionV2ResponseStatus(payload)))
	}
	if status == "incomplete" {
		return remoteCompactionV2IncompleteErr()
	}
	if status == "" {
		return newRemoteCompactionV2ProtocolError(http.StatusBadGateway, "remote compaction v2 response is missing completed status")
	}
	if status != "completed" {
		return newRemoteCompactionV2ProtocolError(http.StatusBadGateway, fmt.Sprintf("remote compaction v2 expected completed response status, got %s", status))
	}
	return remoteCompactionV2MissingItemErr(payload)
}

func remoteCompactionV2IncompleteErr() error {
	return newRemoteCompactionV2ProtocolError(http.StatusBadGateway, "remote compaction v2 received response.incomplete")
}

func remoteCompactionV2TerminalError(eventType string) error {
	eventType = strings.TrimSpace(eventType)
	if eventType == "response.incomplete" {
		return remoteCompactionV2IncompleteErr()
	}
	if eventType == "" {
		return newRemoteCompactionV2ProtocolError(http.StatusBadGateway, "remote compaction v2 upstream terminated without response.completed")
	}
	return newRemoteCompactionV2ProtocolError(http.StatusBadGateway, fmt.Sprintf("remote compaction v2 expected response.completed, got %s", eventType))
}

func isRemoteCompactionV2TerminalEvent(eventType string) bool {
	switch strings.TrimSpace(eventType) {
	case "response.completed", "response.incomplete", "response.failed", "response.done", "error":
		return true
	default:
		return false
	}
}

func remoteCompactionV2ResponseStatus(payload []byte) string {
	for _, path := range []string{"response.status", "status"} {
		if result := gjson.GetBytes(payload, path); result.Exists() && result.Type == gjson.String {
			return result.String()
		}
	}
	return ""
}

// remoteCompactionV2EventType extracts a terminal event type from either a
// single Responses event or an SSE body. It is used before SSE aggregation so
// the completed/incomplete distinction cannot be lost.
func remoteCompactionV2EventType(payload []byte) string {
	trimmed := bytes.TrimSpace(payload)
	if eventType := strings.TrimSpace(gjson.GetBytes(trimmed, "type").String()); eventType != "" && eventType != "response" {
		return eventType
	}
	for _, line := range bytes.Split(trimmed, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, dataTag) {
			continue
		}
		eventType := gjson.GetBytes(bytes.TrimSpace(line[len(dataTag):]), "type").String()
		if isRemoteCompactionV2TerminalEvent(eventType) {
			return eventType
		}
	}
	return ""
}
