package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/observability"
)

func TestGetObservabilitySnapshot(t *testing.T) {
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Request = httptest.NewRequest(http.MethodGet, "/v0/management/observability", nil)

	(&Handler{}).GetObservabilitySnapshot(ginContext)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var snapshot observability.Snapshot
	if err := json.Unmarshal(recorder.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if !snapshot.CostEstimated {
		t.Fatal("cost_estimated = false, want true")
	}
	if snapshot.BootID == "" || snapshot.ProcessID <= 0 {
		t.Fatalf("process identity = boot %q pid %d, want populated", snapshot.BootID, snapshot.ProcessID)
	}
}

func TestGetObservabilitySnapshotResetsCursorForDifferentBoot(t *testing.T) {
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Request = httptest.NewRequest(http.MethodGet, "/v0/management/observability?after=999999&limit=1&boot_id=previous-process", nil)

	(&Handler{}).GetObservabilitySnapshot(ginContext)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var snapshot observability.Snapshot
	if err := json.Unmarshal(recorder.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if !snapshot.CursorReset {
		t.Fatal("cursor_reset = false, want true")
	}
	if len(snapshot.RecentEvents) > 1 {
		t.Fatalf("recent events = %d, want limit <= 1", len(snapshot.RecentEvents))
	}
}

func TestGetObservabilitySnapshotRejectsInvalidCursor(t *testing.T) {
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Request = httptest.NewRequest(http.MethodGet, "/v0/management/observability?after=nope", nil)

	(&Handler{}).GetObservabilitySnapshot(ginContext)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
}
