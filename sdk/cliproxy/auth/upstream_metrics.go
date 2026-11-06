package auth

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

var (
	upstreamSuccessCount atomic.Int64
	upstreamErrorCounts  sync.Map // status label -> *atomic.Int64
)

func recordUpstreamResult(result Result) {
	if result.Success {
		upstreamSuccessCount.Add(1)
		return
	}
	status := "unknown"
	if result.Error != nil && result.Error.HTTPStatus > 0 {
		status = strconv.Itoa(result.Error.HTTPStatus)
	}
	value, _ := upstreamErrorCounts.LoadOrStore(status, &atomic.Int64{})
	counter, _ := value.(*atomic.Int64)
	if counter != nil {
		counter.Add(1)
	}
}

// RecordDirectHttpUpstreamResult counts direct HTTP / WebSocket handshake
// outcomes that never flow through MarkResult / reportHomeResult /
// recordAvailabilityNeutralResult (for example /v1/alpha/search via
// codexAlphaSearch, and Codex Realtime / sideband dials that call
// PrepareHttpRequest + DialContext outside Manager.HttpRequest).
//
// When both err and resp are set (gorilla websocket dials often return a
// rejected handshake response alongside the dial error), prefer the HTTP
// status from resp so /metrics reflects the upstream status code.
func RecordDirectHttpUpstreamResult(auth *Auth, resp *http.Response, err error) {
	if auth == nil || strings.TrimSpace(auth.ID) == "" {
		return
	}
	result := Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
	}
	switch {
	case err != nil:
		if resp != nil && resp.StatusCode >= http.StatusBadRequest {
			result.Error = &Error{
				HTTPStatus: resp.StatusCode,
				Message:    resp.Status,
			}
		} else {
			result.Error = resultErrorFromError(err)
		}
	case resp == nil:
		result.Error = &Error{Message: "nil http response"}
	case resp.StatusCode >= http.StatusBadRequest:
		result.Error = &Error{
			HTTPStatus: resp.StatusCode,
			Message:    resp.Status,
		}
	default:
		result.Success = true
	}
	recordUpstreamResult(result)
}

// recordDirectHttpUpstreamResult is kept as an unexported alias for in-package callers.
func recordDirectHttpUpstreamResult(auth *Auth, resp *http.Response, err error) {
	RecordDirectHttpUpstreamResult(auth, resp, err)
}

// SnapshotUpstreamMetrics returns process-wide upstream success and error counts.
func SnapshotUpstreamMetrics() (success int64, errorsByStatus map[string]int64) {
	success = upstreamSuccessCount.Load()
	errorsByStatus = make(map[string]int64)
	upstreamErrorCounts.Range(func(key, value any) bool {
		label, _ := key.(string)
		counter, _ := value.(*atomic.Int64)
		if label == "" || counter == nil {
			return true
		}
		errorsByStatus[label] = counter.Load()
		return true
	})
	return success, errorsByStatus
}

// ResetUpstreamMetricsForTest clears process-wide upstream counters. Tests only.
func ResetUpstreamMetricsForTest() {
	upstreamSuccessCount.Store(0)
	upstreamErrorCounts.Range(func(key, _ any) bool {
		upstreamErrorCounts.Delete(key)
		return true
	})
}
