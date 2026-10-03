package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/quotatraffic"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestQuotaObservationsExcludeDisabledRemovedAndPreviousProviders(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	for _, auth := range []*coreauth.Auth{
		{ID: "observation-active", Index: "observation-active", Provider: "codex"},
		{ID: "observation-disabled", Index: "observation-disabled", Provider: "codex", Disabled: true},
		{ID: "observation-switched", Index: "observation-switched", Provider: "claude"},
	} {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, index := range []string{"observation-active", "observation-disabled", "observation-switched", "observation-removed"} {
		quotatraffic.Capture(index, "codex", http.Header{"X-Codex-Primary-Used-Percent": {"25"}}, at, "api_response_headers")
	}
	h := &Handler{authManager: manager}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v8/management/quota/observations", nil)
	h.GetQuotaObservations(c)
	var response struct {
		Items []quotatraffic.Observation `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != 200 || recorder.Header().Get("Cache-Control") != "no-store" || len(response.Items) != 1 || response.Items[0].AuthIndex != "observation-active" {
		t.Fatalf("incorrect observation scope: %s", recorder.Body.String())
	}
}
