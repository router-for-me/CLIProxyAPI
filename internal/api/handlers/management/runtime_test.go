package management

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestGetRuntimeInfoReturnsHostContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	startedAt := time.Date(2026, time.August, 28, 1, 2, 3, 0, time.UTC)
	handler := NewHandlerWithoutConfigFilePath(nil, nil)
	handler.SetRuntimeControl(RuntimeInfo{
		ContractVersion:  "1",
		ComponentVersion: "7.0.0-pinable.1",
		Commit:           "abc123",
		BuildTime:        "2026-08-28T00:00:00Z",
		PID:              4321,
		StartedAt:        startedAt,
	}, nil)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/v0/management/runtime-info", nil)
	handler.GetRuntimeInfo(context)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	want := `{"contract_version":"1","component_version":"7.0.0-pinable.1","commit":"abc123","build_time":"2026-08-28T00:00:00Z","pid":4321,"started_at":"2026-08-28T01:02:03Z"}`
	if got := recorder.Body.String(); got != want {
		t.Fatalf("body = %s, want %s", got, want)
	}
}

func TestPostRuntimeShutdownAcceptsLoopbackAndCancelsAfterResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	shutdownCalled := make(chan struct{}, 1)
	handler := NewHandlerWithoutConfigFilePath(nil, nil)
	handler.SetRuntimeControl(RuntimeInfo{}, func() { shutdownCalled <- struct{}{} })

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodPost, "/v0/management/runtime-shutdown", nil)
	request.RemoteAddr = "127.0.0.1:45678"
	context.Request = request
	handler.PostRuntimeShutdown(context)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusAccepted, recorder.Body.String())
	}
	select {
	case <-shutdownCalled:
	case <-time.After(time.Second):
		t.Fatal("shutdown callback was not called")
	}
}

func TestPostRuntimeShutdownRejectsRemoteClient(t *testing.T) {
	gin.SetMode(gin.TestMode)
	shutdownCalled := make(chan struct{}, 1)
	handler := NewHandlerWithoutConfigFilePath(nil, nil)
	handler.SetRuntimeControl(RuntimeInfo{}, func() { shutdownCalled <- struct{}{} })

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodPost, "/v0/management/runtime-shutdown", nil)
	request.RemoteAddr = "192.0.2.10:45678"
	context.Request = request
	handler.PostRuntimeShutdown(context)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusForbidden, recorder.Body.String())
	}
	select {
	case <-shutdownCalled:
		t.Fatal("shutdown callback was called for remote client")
	default:
	}
}

func TestPostRuntimeShutdownRejectsRemotePeerWithLoopbackForwardedHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	shutdownCalled := make(chan struct{}, 1)
	handler := NewHandlerWithoutConfigFilePath(nil, nil)
	handler.SetRuntimeControl(RuntimeInfo{}, func() { shutdownCalled <- struct{}{} })

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodPost, "/v0/management/runtime-shutdown", nil)
	request.RemoteAddr = "192.0.2.10:45678"
	request.Header.Set("X-Forwarded-For", "127.0.0.1")
	context.Request = request
	handler.PostRuntimeShutdown(context)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusForbidden, recorder.Body.String())
	}
	select {
	case <-shutdownCalled:
		t.Fatal("shutdown callback was called for remote peer with spoofed forwarding header")
	default:
	}
}
