package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/cachestats"
)

func seedCacheStats(t *testing.T) {
	t.Helper()
	store := cachestats.NewStore(cachestats.Config{
		Enabled:            true,
		MaxSessions:        10,
		PerSessionRequests: 10,
		IdleTTL:            time.Hour,
	})
	base := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	store.Record(cachestats.Observation{
		SessionID: "session-a", Model: "claude-sonnet-5", AuthID: "auth-1", At: base,
		InputTokens: 2, OutputTokens: 4, MaxTokens: 1024,
		CacheCreationTokens: 35314, CacheCreation1hTokens: 35314,
	})
	store.Record(cachestats.Observation{
		SessionID: "session-a", Model: "claude-sonnet-5", AuthID: "auth-1", At: base.Add(time.Minute),
		InputTokens: 2, OutputTokens: 9, MaxTokens: 1024, CacheReadTokens: 35314,
	})
	store.Record(cachestats.Observation{
		SessionID: "session-a", Model: "claude-sonnet-5", AuthID: "auth-1", At: base.Add(2 * time.Minute),
		InputTokens: 2, OutputTokens: 9, MaxTokens: 1024, CacheReadTokens: 10112,
		CacheMissReason: "messages_changed", CacheMissedTokens: 25202,
	})
	cachestats.SetDefault(store)
	t.Cleanup(func() { cachestats.SetDefault(nil) })
}

func newCacheStatsRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	handler := &Handler{}
	engine := gin.New()
	engine.GET("/v0/management/cache-stats", handler.GetCacheStats)
	engine.GET("/v0/management/cache-stats/sessions/:id", handler.GetCacheStatsSession)
	engine.DELETE("/v0/management/cache-stats", handler.DeleteCacheStats)
	return engine
}

func doCacheStatsRequest(t *testing.T, engine *gin.Engine, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(method, target, nil))
	return recorder
}

func TestGetCacheStatsReturnsSummaryAndSessions(t *testing.T) {
	seedCacheStats(t)
	recorder := doCacheStatsRequest(t, newCacheStatsRouter(t), http.MethodGet, "/v0/management/cache-stats")

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	var response cacheStatsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !response.Enabled {
		t.Error("enabled = false, want true")
	}
	if response.Global.Requests != 3 || response.Global.Hits != 1 || response.Global.Misses != 1 || response.Global.T0s != 1 {
		t.Errorf("global = %+v, want 3 requests / 1 hit / 1 miss / 1 T0", response.Global)
	}
	if response.Global.LostTokens != 25202 {
		t.Errorf("global lost tokens = %d, want 25202", response.Global.LostTokens)
	}
	if len(response.Models) != 1 || response.Models[0].Key != "claude-sonnet-5" {
		t.Errorf("models = %+v, want one claude-sonnet-5 group", response.Models)
	}
	if len(response.Auths) != 1 || response.Auths[0].Key != "auth-1" {
		t.Errorf("auths = %+v, want one auth-1 group", response.Auths)
	}
	if len(response.Sessions) != 1 || response.Sessions[0].ID != "session-a" {
		t.Fatalf("sessions = %+v, want one session-a", response.Sessions)
	}
	if response.Sessions[0].Regime != cachestats.Regime1h {
		t.Errorf("regime = %q, want 1h", response.Sessions[0].Regime)
	}
}

func TestGetCacheStatsSessionReturnsRequestSequence(t *testing.T) {
	seedCacheStats(t)
	recorder := doCacheStatsRequest(t, newCacheStatsRouter(t), http.MethodGet, "/v0/management/cache-stats/sessions/session-a")

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	var detail cachestats.SessionDetail
	if err := json.Unmarshal(recorder.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(detail.Requests) != 3 {
		t.Fatalf("requests = %d, want 3", len(detail.Requests))
	}
	wantTiers := []cachestats.Tier{cachestats.TierT0, cachestats.TierHit, cachestats.TierMiss}
	for i, want := range wantTiers {
		if detail.Requests[i].Tier != want {
			t.Errorf("request %d tier = %q, want %q", i+1, detail.Requests[i].Tier, want)
		}
	}
	if detail.Requests[2].MissReason != "messages_changed" {
		t.Errorf("miss reason = %q, want messages_changed", detail.Requests[2].MissReason)
	}
	if detail.Summary.ID != "session-a" {
		t.Errorf("summary id = %q, want session-a", detail.Summary.ID)
	}
}

func TestGetCacheStatsSessionUnknownID(t *testing.T) {
	seedCacheStats(t)
	recorder := doCacheStatsRequest(t, newCacheStatsRouter(t), http.MethodGet, "/v0/management/cache-stats/sessions/missing")

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["error"] != "session not found" {
		t.Errorf("error = %q, want \"session not found\"", body["error"])
	}
}

func TestDeleteCacheStatsResets(t *testing.T) {
	seedCacheStats(t)
	engine := newCacheStatsRouter(t)
	recorder := doCacheStatsRequest(t, engine, http.MethodDelete, "/v0/management/cache-stats")

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	var response cacheStatsResetResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Status != "ok" || response.ClearedSessions != 1 {
		t.Fatalf("reset response = %+v, want ok/1", response)
	}

	after := doCacheStatsRequest(t, engine, http.MethodGet, "/v0/management/cache-stats")
	var summary cacheStatsResponse
	if err := json.Unmarshal(after.Body.Bytes(), &summary); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if summary.Global.Requests != 0 || len(summary.Sessions) != 0 {
		t.Fatalf("store not empty after reset: %+v", summary)
	}
}
