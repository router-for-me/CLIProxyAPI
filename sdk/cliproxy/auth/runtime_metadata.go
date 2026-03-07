package auth

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const (
	// Runtime metadata keys persisted in auth JSON files.
	MetadataKeyRuntimeStatus      = "runtime_status"
	MetadataKeyRuntimeStatusMsg   = "runtime_status_message"
	MetadataKeyRuntimeUnavailable = "runtime_unavailable"
	MetadataKeyRuntimeQuota       = "runtime_quota"
	MetadataKeyRuntimeLastError   = "runtime_last_error"
	MetadataKeyRuntimeNextRetry   = "runtime_next_retry_after"
	MetadataKeyRuntimeModelStates = "runtime_model_states"
)

var runtimeMetadataKeys = [...]string{
	MetadataKeyRuntimeStatus,
	MetadataKeyRuntimeStatusMsg,
	MetadataKeyRuntimeUnavailable,
	MetadataKeyRuntimeQuota,
	MetadataKeyRuntimeLastError,
	MetadataKeyRuntimeNextRetry,
	MetadataKeyRuntimeModelStates,
}

// CloneMetadata returns a shallow copy of metadata to avoid mutating caller-owned maps.
func CloneMetadata(src map[string]any) map[string]any {
	if len(src) == 0 {
		return map[string]any{}
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

// HasRuntimeStateMetadata reports whether metadata contains any persisted runtime state key.
func HasRuntimeStateMetadata(metadata map[string]any) bool {
	if len(metadata) == 0 {
		return false
	}
	for _, key := range runtimeMetadataKeys {
		if _, ok := metadata[key]; ok {
			return true
		}
	}
	return false
}

// ApplyRuntimeStateToMetadata stores runtime-only account/model state in namespaced metadata fields.
func ApplyRuntimeStateToMetadata(metadata map[string]any, auth *Auth) {
	if metadata == nil || auth == nil {
		return
	}

	metadata[MetadataKeyRuntimeStatus] = string(auth.Status)
	if msg := strings.TrimSpace(auth.StatusMessage); msg != "" {
		metadata[MetadataKeyRuntimeStatusMsg] = msg
	} else {
		delete(metadata, MetadataKeyRuntimeStatusMsg)
	}
	metadata[MetadataKeyRuntimeUnavailable] = auth.Unavailable

	if !auth.NextRetryAfter.IsZero() {
		metadata[MetadataKeyRuntimeNextRetry] = auth.NextRetryAfter.UTC().Format(time.RFC3339Nano)
	} else {
		delete(metadata, MetadataKeyRuntimeNextRetry)
	}

	if !isQuotaStateZero(auth.Quota) {
		metadata[MetadataKeyRuntimeQuota] = auth.Quota
	} else {
		delete(metadata, MetadataKeyRuntimeQuota)
	}

	if auth.LastError != nil {
		metadata[MetadataKeyRuntimeLastError] = auth.LastError
	} else {
		delete(metadata, MetadataKeyRuntimeLastError)
	}

	if len(auth.ModelStates) > 0 {
		metadata[MetadataKeyRuntimeModelStates] = auth.ModelStates
	} else {
		delete(metadata, MetadataKeyRuntimeModelStates)
	}
}

// RestoreRuntimeStateFromMetadata restores runtime-only state from namespaced metadata fields.
func RestoreRuntimeStateFromMetadata(auth *Auth, metadata map[string]any) {
	if auth == nil || metadata == nil {
		return
	}

	if persistedStatus := parseStatusAny(metadata[MetadataKeyRuntimeStatus]); persistedStatus != "" && !auth.Disabled {
		auth.Status = persistedStatus
	}
	if msg, ok := metadata[MetadataKeyRuntimeStatusMsg].(string); ok {
		auth.StatusMessage = strings.TrimSpace(msg)
	}
	if unavailable, okUnavailable := parseBoolAny(metadata[MetadataKeyRuntimeUnavailable]); okUnavailable {
		auth.Unavailable = unavailable
	}
	if nextRetry, okNextRetry := parseTimeValue(metadata[MetadataKeyRuntimeNextRetry]); okNextRetry {
		auth.NextRetryAfter = nextRetry
	}

	if quotaRaw, okQuota := metadata[MetadataKeyRuntimeQuota]; okQuota {
		var quota QuotaState
		if decodeAnyToStruct(quotaRaw, &quota) == nil {
			auth.Quota = quota
		}
	}
	if errRaw, okLastErr := metadata[MetadataKeyRuntimeLastError]; okLastErr {
		var lastErr Error
		if decodeAnyToStruct(errRaw, &lastErr) == nil && (lastErr.Message != "" || lastErr.Code != "" || lastErr.HTTPStatus > 0) {
			auth.LastError = &lastErr
		}
	}
	if statesRaw, okStates := metadata[MetadataKeyRuntimeModelStates]; okStates {
		var states map[string]*ModelState
		if decodeAnyToStruct(statesRaw, &states) == nil && len(states) > 0 {
			auth.ModelStates = states
		}
	}

	if auth.Disabled {
		auth.Status = StatusDisabled
	}
}

func parseStatusAny(raw any) Status {
	s, ok := raw.(string)
	if !ok {
		return ""
	}
	status := Status(strings.TrimSpace(s))
	switch status {
	case StatusUnknown, StatusActive, StatusPending, StatusRefreshing, StatusError, StatusDisabled:
		return status
	default:
		return ""
	}
}

func decodeAnyToStruct(raw any, target any) error {
	if raw == nil || target == nil {
		return errDecodeAnyNilInput
	}
	blob, errMarshal := json.Marshal(raw)
	if errMarshal != nil {
		return errMarshal
	}
	return json.Unmarshal(blob, target)
}

var errDecodeAnyNilInput = errors.New("decode any: nil input")

func isQuotaStateZero(state QuotaState) bool {
	return !state.Exceeded &&
		strings.TrimSpace(state.Reason) == "" &&
		state.NextRecoverAt.IsZero() &&
		state.BackoffLevel == 0
}
