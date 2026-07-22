package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestGetSessionAffinityStats(t *testing.T) {
	gin.SetMode(gin.TestMode)

	selector := coreauth.NewSessionAffinitySelectorWithConfig(coreauth.SessionAffinityConfig{
		Fallback: &coreauth.RoundRobinSelector{},
		TTL:      time.Hour,
	})
	defer selector.Stop()
	manager := coreauth.NewManager(nil, selector, nil)
	handler := &Handler{authManager: manager}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	handler.GetSessionAffinityStats(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	var response sessionAffinityStatsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !response.Enabled {
		t.Fatal("enabled = false, want true")
	}
	if response.ActiveBindings != 0 {
		t.Fatalf("active_bindings = %d, want 0", response.ActiveBindings)
	}
	if response.ActiveSessions != 0 {
		t.Fatalf("active_sessions = %d, want 0", response.ActiveSessions)
	}
}

func TestGetSessionAffinityStatsDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)

	handler := &Handler{authManager: coreauth.NewManager(nil, nil, nil)}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	handler.GetSessionAffinityStats(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	var response sessionAffinityStatsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Enabled {
		t.Fatal("enabled = true, want false")
	}
	if response.ActiveBindings != 0 {
		t.Fatalf("active_bindings = %d, want 0", response.ActiveBindings)
	}
	if response.ActiveSessions != 0 {
		t.Fatalf("active_sessions = %d, want 0", response.ActiveSessions)
	}
}

func TestGetSessionAffinityStatsManagerUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)

	handler := &Handler{}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	handler.GetSessionAffinityStats(ctx)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
	var response map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response["error"] != "core auth manager unavailable" {
		t.Fatalf("error = %q, want core auth manager unavailable", response["error"])
	}
}
