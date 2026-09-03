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
		SessionID: "session-a", KeyedBy: cachestats.KeyedBySession, Provider: "claude",
		Model: "claude-sonnet-5", AuthID: "auth-1", At: base, Signal: cachestats.SignalFull,
		InputTokens: 2, PromptTokens: 35316, OutputTokens: 4, MaxTokens: 1024,
		CacheCreationTokens: 35314, CacheCreation1hTokens: 35314,
	})
	store.Record(cachestats.Observation{
		SessionID: "session-a", KeyedBy: cachestats.KeyedBySession, Provider: "claude",
		Model: "claude-sonnet-5", AuthID: "auth-1", At: base.Add(time.Minute), Signal: cachestats.SignalFull,
		InputTokens: 2, PromptTokens: 35316, OutputTokens: 9, MaxTokens: 1024, CacheReadTokens: 35314,
	})
	store.Record(cachestats.Observation{
		SessionID: "session-a", KeyedBy: cachestats.KeyedBySession, Provider: "claude",
		Model: "claude-sonnet-5", AuthID: "auth-1", At: base.Add(2 * time.Minute), Signal: cachestats.SignalFull,
		InputTokens: 2, PromptTokens: 10114, OutputTokens: 9, MaxTokens: 1024, CacheReadTokens: 10112,
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
	engine.GET("/v0/management/cache-stats/sessions/*id", handler.GetCacheStatsSession)
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

func seedMixedProviderCacheStats(t *testing.T) {
	t.Helper()
	store := cachestats.NewStore(cachestats.Config{
		Enabled: true, MaxSessions: 10, PerSessionRequests: 10, IdleTTL: time.Hour,
	})
	base := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	store.Record(cachestats.Observation{
		SessionID: "claude-session", KeyedBy: cachestats.KeyedBySession, Provider: "claude",
		Model: "claude-sonnet-5", AuthID: "auth-1", At: base, Signal: cachestats.SignalFull,
		InputTokens: 2, PromptTokens: 2, OutputTokens: 4,
		CacheCreationTokens: 35314, CacheCreation1hTokens: 35314,
	})
	store.Record(cachestats.Observation{
		SessionID: "openai-session", KeyedBy: cachestats.KeyedByAPIKeyModel, Provider: "openai-compatibility",
		Model: "gpt-x", AuthID: "auth-2", At: base.Add(time.Minute), Signal: cachestats.SignalRead,
		InputTokens: 1000, PromptTokens: 1000, OutputTokens: 10, CacheReadTokens: 800,
	})
	store.Record(cachestats.Observation{
		SessionID: "mystery-session", KeyedBy: cachestats.KeyedByAPIKeyModel, Provider: "mystery",
		Model: "mystery-1", AuthID: "auth-3", At: base.Add(2 * time.Minute), Signal: cachestats.SignalNone,
		InputTokens: 300, PromptTokens: 300, OutputTokens: 5,
	})
	cachestats.SetDefault(store)
	t.Cleanup(func() { cachestats.SetDefault(nil) })
}

func TestGetCacheStatsBreaksDownByProvider(t *testing.T) {
	seedMixedProviderCacheStats(t)
	recorder := doCacheStatsRequest(t, newCacheStatsRouter(t), http.MethodGet, "/v0/management/cache-stats")

	var response cacheStatsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Providers) != 3 {
		t.Fatalf("provider groups = %d, want 3", len(response.Providers))
	}
	keys := map[string]bool{}
	for _, group := range response.Providers {
		keys[group.Key] = true
	}
	for _, want := range []string{"claude", "openai-compatibility", "mystery"} {
		if !keys[want] {
			t.Errorf("provider group %q missing from %+v", want, response.Providers)
		}
	}
	// Three requests, but only the two providers with a cache signal are
	// classified, so the global hit rate is not diluted by the third.
	if response.Global.Requests != 3 {
		t.Errorf("global requests = %d, want 3", response.Global.Requests)
	}
	if response.Global.Classified != 2 {
		t.Errorf("global classified = %d, want 2", response.Global.Classified)
	}
}

func TestGetCacheStatsProviderFilter(t *testing.T) {
	seedMixedProviderCacheStats(t)
	recorder := doCacheStatsRequest(t, newCacheStatsRouter(t), http.MethodGet, "/v0/management/cache-stats?provider=openai-compatibility")

	var response cacheStatsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Provider != "openai-compatibility" {
		t.Errorf("echoed provider = %q", response.Provider)
	}
	if len(response.Sessions) != 1 || response.Sessions[0].ID != "openai-session" {
		t.Fatalf("filtered sessions = %+v, want only openai-session", response.Sessions)
	}
	if response.Global.Requests != 1 {
		t.Errorf("filtered global requests = %d, want 1", response.Global.Requests)
	}
	if response.Sessions[0].Signal != cachestats.SignalRead {
		t.Errorf("signal = %q, want read", response.Sessions[0].Signal)
	}
	if response.Sessions[0].KeyedBy != cachestats.KeyedByAPIKeyModel {
		t.Errorf("keyed_by = %q, want apikey-model", response.Sessions[0].KeyedBy)
	}
	if got := response.Sessions[0].CachedShare; got < 0.799 || got > 0.801 {
		t.Errorf("cached share = %v, want 0.8", got)
	}
}

func TestGetCacheStatsProviderFilterWithNoMatches(t *testing.T) {
	seedMixedProviderCacheStats(t)
	recorder := doCacheStatsRequest(t, newCacheStatsRouter(t), http.MethodGet, "/v0/management/cache-stats?provider=nope")

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	var response cacheStatsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Sessions) != 0 || response.Global.Requests != 0 {
		t.Fatalf("unknown provider returned data: %+v", response)
	}
	if response.Sessions == nil || response.Providers == nil {
		t.Error("empty collections must serialize as [] rather than null")
	}
}

func TestGetCacheStatsSessionReportsSignalAndCause(t *testing.T) {
	seedMixedProviderCacheStats(t)
	recorder := doCacheStatsRequest(t, newCacheStatsRouter(t), http.MethodGet, "/v0/management/cache-stats/sessions/mystery-session")

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	var detail cachestats.SessionDetail
	if err := json.Unmarshal(recorder.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if detail.Requests[0].Tier != cachestats.TierNA {
		t.Errorf("tier = %q, want n/a", detail.Requests[0].Tier)
	}
	if detail.Requests[0].Signal != cachestats.SignalNone {
		t.Errorf("signal = %q, want none", detail.Requests[0].Signal)
	}
	if detail.Requests[0].Provider != "mystery" {
		t.Errorf("provider = %q, want mystery", detail.Requests[0].Provider)
	}
}

// A fallback session key embeds the model name, which for gateway-style
// providers contains slashes. The route has to address it anyway.
func TestGetCacheStatsSessionWithSlashesInTheKey(t *testing.T) {
	store := cachestats.NewStore(cachestats.Config{
		Enabled: true, MaxSessions: 10, PerSessionRequests: 10, IdleTTL: time.Hour,
	})
	key := "apikey:31569dce16ee|bedrock/converse/zai.glm-5|integration-check/1.0"
	store.Record(cachestats.Observation{
		SessionID: key, KeyedBy: cachestats.KeyedByAPIKeyModel, Provider: "openai-compatibility",
		Model: "bedrock/converse/zai.glm-5", AuthID: "auth-1",
		At: time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC), Signal: cachestats.SignalRead,
		InputTokens: 12, PromptTokens: 12, OutputTokens: 2,
	})
	cachestats.SetDefault(store)
	t.Cleanup(func() { cachestats.SetDefault(nil) })

	engine := newCacheStatsRouter(t)
	recorder := doCacheStatsRequest(t, engine, http.MethodGet, "/v0/management/cache-stats/sessions/"+key)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a key containing slashes", recorder.Code)
	}
	var detail cachestats.SessionDetail
	if err := json.Unmarshal(recorder.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if detail.Summary.ID != key {
		t.Errorf("id = %q, want %q", detail.Summary.ID, key)
	}
	if detail.Requests[0].T0Cause != cachestats.T0CauseFirst {
		t.Errorf("t0_cause = %q, want first", detail.Requests[0].T0Cause)
	}

	// The short id is an equally valid handle, which is what a human types.
	short := doCacheStatsRequest(t, engine, http.MethodGet, "/v0/management/cache-stats/sessions/"+detail.Summary.ShortID)
	if short.Code != http.StatusOK {
		t.Fatalf("short id lookup status = %d, want 200", short.Code)
	}
	var byShort cachestats.SessionDetail
	if err := json.Unmarshal(short.Body.Bytes(), &byShort); err != nil {
		t.Fatalf("decode short id response: %v", err)
	}
	if byShort.Summary.ID != key {
		t.Errorf("short id resolved to %q, want %q", byShort.Summary.ID, key)
	}

	// A trailing slash carries no id and must not be treated as one.
	empty := doCacheStatsRequest(t, engine, http.MethodGet, "/v0/management/cache-stats/sessions/")
	if empty.Code != http.StatusBadRequest {
		t.Errorf("empty id status = %d, want 400", empty.Code)
	}
}
