package management

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/store/mongostate"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

type fakeErrorEventQueryStore struct {
	queryCalled bool
	lastQuery   mongostate.ErrorEventQuery
	result      mongostate.ErrorEventQueryResult
	err         error

	summaryCalled bool
	lastSummary   mongostate.ErrorEventSummaryQuery
	summaryResult mongostate.ErrorEventSummaryResult
	summaryErr    error
}

func (f *fakeErrorEventQueryStore) Query(_ context.Context, query mongostate.ErrorEventQuery) (mongostate.ErrorEventQueryResult, error) {
	f.queryCalled = true
	f.lastQuery = query
	return f.result, f.err
}

func (f *fakeErrorEventQueryStore) Insert(_ context.Context, _ *mongostate.ErrorEventRecord) error {
	return nil
}

func (f *fakeErrorEventQueryStore) Summarize(_ context.Context, query mongostate.ErrorEventSummaryQuery) (mongostate.ErrorEventSummaryResult, error) {
	f.summaryCalled = true
	f.lastSummary = query
	return f.summaryResult, f.summaryErr
}

type fakeDeletionProgressStore struct {
	lastKeys []string
	records  map[string]mongostate.CircuitBreakerDeletionRecord
	err      error
}

func (f *fakeDeletionProgressStore) Query(_ context.Context, _ mongostate.CircuitBreakerDeletionQuery) (mongostate.CircuitBreakerDeletionQueryResult, error) {
	return mongostate.CircuitBreakerDeletionQueryResult{}, nil
}

func (f *fakeDeletionProgressStore) GetByID(_ context.Context, _ string) (mongostate.CircuitBreakerDeletionRecord, error) {
	return mongostate.CircuitBreakerDeletionRecord{}, mongostate.ErrCircuitBreakerDeletionNotFound
}

func (f *fakeDeletionProgressStore) ApplyAction(_ context.Context, _ string, _ mongostate.CircuitBreakerDeletionAction) (mongostate.CircuitBreakerDeletionRecord, error) {
	return mongostate.CircuitBreakerDeletionRecord{}, mongostate.ErrCircuitBreakerDeletionConflict
}

func (f *fakeDeletionProgressStore) FindLatestByDedupeKeys(_ context.Context, dedupeKeys []string) (map[string]mongostate.CircuitBreakerDeletionRecord, error) {
	f.lastKeys = append([]string(nil), dedupeKeys...)
	return f.records, f.err
}

func TestListErrorEvents_StoreUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mongostate.SetGlobalErrorEventStore(nil)

	h := &Handler{}
	r := gin.New()
	r.GET("/v0/management/error-events", h.ListErrorEvents)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/error-events", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
}

func TestListErrorEvents_InvalidTimeAndStatusCode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &fakeErrorEventQueryStore{}
	mongostate.SetGlobalErrorEventStore(store)
	t.Cleanup(func() { mongostate.SetGlobalErrorEventStore(nil) })

	h := &Handler{}
	r := gin.New()
	r.GET("/v0/management/error-events", h.ListErrorEvents)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/error-events?start=bad", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid start status = %d, want %d", w.Code, http.StatusBadRequest)
	}

	req = httptest.NewRequest(http.MethodGet, "/v0/management/error-events?end=bad", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid end status = %d, want %d", w.Code, http.StatusBadRequest)
	}

	req = httptest.NewRequest(http.MethodGet, "/v0/management/error-events?status_code=bad", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid status_code status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestListErrorEvents_QueryFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &fakeErrorEventQueryStore{err: errors.New("query failed")}
	mongostate.SetGlobalErrorEventStore(store)
	t.Cleanup(func() { mongostate.SetGlobalErrorEventStore(nil) })

	h := &Handler{}
	r := gin.New()
	r.GET("/v0/management/error-events", h.ListErrorEvents)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/error-events", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
}

func TestListErrorEvents_SuccessAndQueryMapping(t *testing.T) {
	gin.SetMode(gin.TestMode)
	start := time.Date(2026, 4, 26, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 4, 27, 0, 0, 0, 0, time.UTC)
	store := &fakeErrorEventQueryStore{
		result: mongostate.ErrorEventQueryResult{
			Items: []mongostate.ErrorEventItem{
				{
					ID:              "abc",
					Provider:        "gemini",
					NormalizedModel: "gpt-5",
					FailureStage:    "request_execution",
					StatusCode:      503,
					RequestID:       "req-1",
					OccurredAt:      time.Now().UTC(),
					CreatedAt:       time.Now().UTC(),
					Failed:          true,
				},
			},
			Total:    1,
			Page:     2,
			PageSize: 5,
		},
	}
	mongostate.SetGlobalErrorEventStore(store)
	t.Cleanup(func() { mongostate.SetGlobalErrorEventStore(nil) })

	h := &Handler{}
	r := gin.New()
	r.GET("/v0/management/error-events", h.ListErrorEvents)

	url := "/v0/management/error-events?start=2026-04-26T00:00:00Z&end=2026-04-27T00:00:00Z&provider=Gemini&auth_id=a1&model=gpt-5&failure_stage=Request_Execution&error_code=UPSTREAM_TIMEOUT&status_code=503&request_id=req-1&page=2&page_size=5"
	req := httptest.NewRequest(http.MethodGet, url, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	if !store.queryCalled {
		t.Fatal("expected query to be called")
	}
	if store.lastQuery.Provider != "gemini" {
		t.Fatalf("provider = %q, want gemini", store.lastQuery.Provider)
	}
	if store.lastQuery.AuthID != "a1" {
		t.Fatalf("auth_id = %q, want a1", store.lastQuery.AuthID)
	}
	if store.lastQuery.Model != "gpt-5" {
		t.Fatalf("model = %q, want gpt-5", store.lastQuery.Model)
	}
	if store.lastQuery.FailureStage != "request_execution" {
		t.Fatalf("failure_stage = %q, want request_execution", store.lastQuery.FailureStage)
	}
	if store.lastQuery.ErrorCode != "upstream_timeout" {
		t.Fatalf("error_code = %q, want upstream_timeout", store.lastQuery.ErrorCode)
	}
	if store.lastQuery.StatusCode == nil || *store.lastQuery.StatusCode != 503 {
		t.Fatalf("status_code = %v, want 503", store.lastQuery.StatusCode)
	}
	if store.lastQuery.RequestID != "req-1" {
		t.Fatalf("request_id = %q, want req-1", store.lastQuery.RequestID)
	}
	if !store.lastQuery.Start.Equal(start) || !store.lastQuery.End.Equal(end) {
		t.Fatalf("time filter mismatch: start=%v end=%v", store.lastQuery.Start, store.lastQuery.End)
	}
	if store.lastQuery.Page != 2 || store.lastQuery.PageSize != 5 {
		t.Fatalf("pagination mismatch: page=%d page_size=%d", store.lastQuery.Page, store.lastQuery.PageSize)
	}

	var got mongostate.ErrorEventQueryResult
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if got.Total != 1 || len(got.Items) != 1 {
		t.Fatalf("result mismatch: %+v", got)
	}
}

func TestListErrorEvents_IncludesProgressMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)

	provider := "openai-compatibility"
	authID := "auth-progress-a"
	modelID := "gpt-4.1"
	scopeKey := mongostate.BuildCircuitBreakerDeletionDedupeKey(provider, authID, modelID)

	store := &fakeErrorEventQueryStore{
		result: mongostate.ErrorEventQueryResult{
			Items: []mongostate.ErrorEventItem{{
				ID:              "evt-1",
				Provider:        provider,
				AuthID:          authID,
				Model:           "alias-gpt-4.1",
				NormalizedModel: modelID,
				OccurredAt:      time.Now().UTC(),
				CreatedAt:       time.Now().UTC(),
				Failed:          true,
			}},
			Total:    1,
			Page:     1,
			PageSize: 20,
		},
	}
	mongostate.SetGlobalErrorEventStore(store)
	t.Cleanup(func() { mongostate.SetGlobalErrorEventStore(nil) })

	deletionStore := &fakeDeletionProgressStore{
		records: map[string]mongostate.CircuitBreakerDeletionRecord{
			scopeKey: {
				DedupeKey:       scopeKey,
				Provider:        provider,
				AuthID:          authID,
				Model:           "alias-gpt-4.1",
				NormalizedModel: modelID,
				Status:          mongostate.CircuitBreakerDeletionStatusPending,
				OpenCycles:      2,
			},
		},
	}
	mongostate.SetGlobalCircuitBreakerDeletionStore(deletionStore)
	t.Cleanup(func() { mongostate.SetGlobalCircuitBreakerDeletionStore(nil) })

	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(&config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:                           "compat-progress-a",
			BaseURL:                        "https://example.invalid/v1",
			CircuitBreakerFailureThreshold: 5,
		}},
		CircuitBreakerAutoRemoval: config.CircuitBreakerAutoRemovalConfig{
			AutoRemoveThreshold: 4,
		},
	})
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       authID,
		Provider: provider,
		Attributes: map[string]string{
			"api_key":      "test-key",
			"base_url":     "https://example.invalid/v1",
			"compat_name":  "compat-progress-a",
			"provider_key": "compat-progress-a",
		},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, provider, []*registry.ModelInfo{{ID: modelID}})
	reg.RecordFailure(authID, modelID, 5, 60)
	reg.RecordFailure(authID, modelID, 5, 60)
	t.Cleanup(func() {
		reg.ResetCircuitBreaker(authID, modelID)
		reg.UnregisterClient(authID)
	})

	h := &Handler{cfg: &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:                           "compat-progress-a",
			BaseURL:                        "https://example.invalid/v1",
			CircuitBreakerFailureThreshold: 5,
		}},
		CircuitBreakerAutoRemoval: config.CircuitBreakerAutoRemovalConfig{
			AutoRemoveThreshold: 4,
		},
	}, authManager: manager}
	r := gin.New()
	r.GET("/v0/management/error-events", h.ListErrorEvents)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/error-events", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
	}

	var got struct {
		Items []struct {
			ID               string `json:"id"`
			ProgressScopeKey string `json:"progress_scope_key"`
		} `json:"items"`
		Meta struct {
			ProgressByScope map[string]struct {
				Breaker struct {
					Current   int    `json:"current"`
					Threshold int    `json:"threshold"`
					State     string `json:"state"`
				} `json:"breaker"`
				Deletion struct {
					Current   int    `json:"current"`
					Threshold int    `json:"threshold"`
					Enabled   bool   `json:"enabled"`
					Status    string `json:"status"`
				} `json:"deletion"`
			} `json:"progress_by_scope"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("items len = %d, want 1", len(got.Items))
	}
	if got.Items[0].ProgressScopeKey != scopeKey {
		t.Fatalf("progress_scope_key = %q, want %q", got.Items[0].ProgressScopeKey, scopeKey)
	}
	progress, ok := got.Meta.ProgressByScope[scopeKey]
	if !ok {
		t.Fatalf("missing progress scope %q in %+v", scopeKey, got.Meta.ProgressByScope)
	}
	if progress.Breaker.Current != 2 || progress.Breaker.Threshold != 5 || progress.Breaker.State != string(registry.CircuitClosed) {
		t.Fatalf("breaker progress = %+v, want current=2 threshold=5 state=closed", progress.Breaker)
	}
	if !progress.Deletion.Enabled || progress.Deletion.Current != 2 || progress.Deletion.Threshold != 4 || progress.Deletion.Status != mongostate.CircuitBreakerDeletionStatusPending {
		t.Fatalf("deletion progress = %+v, want enabled current=2 threshold=4 status=pending", progress.Deletion)
	}
	if len(deletionStore.lastKeys) != 1 || deletionStore.lastKeys[0] != scopeKey {
		t.Fatalf("deletion lookup keys = %v, want [%s]", deletionStore.lastKeys, scopeKey)
	}
}

func TestSummarizeErrorEvents_StoreUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mongostate.SetGlobalErrorEventStore(nil)

	h := &Handler{}
	r := gin.New()
	r.GET("/v0/management/error-events/summary", h.SummarizeErrorEvents)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/error-events/summary", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
}

func TestSummarizeErrorEvents_InvalidParams(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &fakeErrorEventQueryStore{}
	mongostate.SetGlobalErrorEventStore(store)
	t.Cleanup(func() { mongostate.SetGlobalErrorEventStore(nil) })

	h := &Handler{}
	r := gin.New()
	r.GET("/v0/management/error-events/summary", h.SummarizeErrorEvents)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/error-events/summary?start=bad", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid start status = %d, want %d", w.Code, http.StatusBadRequest)
	}

	req = httptest.NewRequest(http.MethodGet, "/v0/management/error-events/summary?end=bad", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid end status = %d, want %d", w.Code, http.StatusBadRequest)
	}

	req = httptest.NewRequest(http.MethodGet, "/v0/management/error-events/summary?status_code=bad", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid status_code status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestSummarizeErrorEvents_SuccessAndQueryMapping(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Now().UTC()
	store := &fakeErrorEventQueryStore{
		summaryResult: mongostate.ErrorEventSummaryResult{
			GroupBy: []string{"provider", "error_code"},
			Items: []mongostate.ErrorEventSummaryItem{
				{
					Provider:              "gemini",
					ErrorCode:             "upstream_timeout",
					Total:                 10,
					CircuitCountableTotal: 8,
					LatestOccurredAt:      &now,
				},
			},
		},
	}
	mongostate.SetGlobalErrorEventStore(store)
	t.Cleanup(func() { mongostate.SetGlobalErrorEventStore(nil) })

	h := &Handler{}
	r := gin.New()
	r.GET("/v0/management/error-events/summary", h.SummarizeErrorEvents)

	req := httptest.NewRequest(
		http.MethodGet,
		"/v0/management/error-events/summary?provider=Gemini&auth_id=a1&model=gpt-5&failure_stage=Request_Execution&error_code=UPSTREAM_TIMEOUT&status_code=503&start=2026-04-26T00:00:00Z&end=2026-04-27T00:00:00Z&group_by=provider,error_code&limit=20",
		nil,
	)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	if !store.summaryCalled {
		t.Fatal("expected summary query to be called")
	}
	if store.lastSummary.Provider != "gemini" || store.lastSummary.AuthID != "a1" {
		t.Fatalf("summary query mismatch: %+v", store.lastSummary)
	}
	if store.lastSummary.Model != "gpt-5" {
		t.Fatalf("model = %q, want gpt-5", store.lastSummary.Model)
	}
	if store.lastSummary.FailureStage != "request_execution" || store.lastSummary.ErrorCode != "upstream_timeout" {
		t.Fatalf("summary query mismatch: %+v", store.lastSummary)
	}
	if store.lastSummary.StatusCode == nil || *store.lastSummary.StatusCode != 503 {
		t.Fatalf("status_code = %v, want 503", store.lastSummary.StatusCode)
	}
	if store.lastSummary.Limit != 20 {
		t.Fatalf("limit = %d, want 20", store.lastSummary.Limit)
	}
	if len(store.lastSummary.GroupBy) != 2 || store.lastSummary.GroupBy[0] != "provider" || store.lastSummary.GroupBy[1] != "error_code" {
		t.Fatalf("group_by = %#v, want [provider error_code]", store.lastSummary.GroupBy)
	}

	var got mongostate.ErrorEventSummaryResult
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got.Items) != 1 || got.Items[0].Total != 10 {
		t.Fatalf("summary response mismatch: %+v", got)
	}
}

func TestSummarizeErrorEvents_IncludesProgressMetadataForAuthModelGroups(t *testing.T) {
	gin.SetMode(gin.TestMode)

	provider := "openai-compatibility"
	authID := "auth-progress-summary"
	modelID := "gpt-4.1"
	scopeKey := mongostate.BuildCircuitBreakerDeletionDedupeKey(provider, authID, modelID)

	store := &fakeErrorEventQueryStore{
		summaryResult: mongostate.ErrorEventSummaryResult{
			GroupBy: []string{"auth_id", "normalized_model"},
			Items: []mongostate.ErrorEventSummaryItem{{
				AuthID:          authID,
				NormalizedModel: modelID,
				Total:           9,
			}},
		},
	}
	mongostate.SetGlobalErrorEventStore(store)
	t.Cleanup(func() { mongostate.SetGlobalErrorEventStore(nil) })

	deletionStore := &fakeDeletionProgressStore{
		records: map[string]mongostate.CircuitBreakerDeletionRecord{
			scopeKey: {
				DedupeKey:       scopeKey,
				Provider:        provider,
				AuthID:          authID,
				NormalizedModel: modelID,
				Status:          mongostate.CircuitBreakerDeletionStatusFailed,
				OpenCycles:      3,
			},
		},
	}
	mongostate.SetGlobalCircuitBreakerDeletionStore(deletionStore)
	t.Cleanup(func() { mongostate.SetGlobalCircuitBreakerDeletionStore(nil) })

	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(&config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:                           "compat-progress-summary",
			BaseURL:                        "https://example.invalid/v1",
			CircuitBreakerFailureThreshold: 5,
		}},
		CircuitBreakerAutoRemoval: config.CircuitBreakerAutoRemovalConfig{
			AutoRemoveThreshold: 4,
		},
	})
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       authID,
		Provider: provider,
		Attributes: map[string]string{
			"api_key":      "test-key",
			"base_url":     "https://example.invalid/v1",
			"compat_name":  "compat-progress-summary",
			"provider_key": "compat-progress-summary",
		},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, provider, []*registry.ModelInfo{{ID: modelID}})
	reg.RecordFailure(authID, modelID, 5, 60)
	reg.RecordFailure(authID, modelID, 5, 60)
	reg.RecordFailure(authID, modelID, 5, 60)
	t.Cleanup(func() {
		reg.ResetCircuitBreaker(authID, modelID)
		reg.UnregisterClient(authID)
	})

	h := &Handler{cfg: &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:                           "compat-progress-summary",
			BaseURL:                        "https://example.invalid/v1",
			CircuitBreakerFailureThreshold: 5,
		}},
		CircuitBreakerAutoRemoval: config.CircuitBreakerAutoRemovalConfig{
			AutoRemoveThreshold: 4,
		},
	}, authManager: manager}
	r := gin.New()
	r.GET("/v0/management/error-events/summary", h.SummarizeErrorEvents)

	req := httptest.NewRequest(
		http.MethodGet,
		"/v0/management/error-events/summary?group_by=auth_id,normalized_model",
		nil,
	)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
	}

	var got struct {
		Items []struct {
			AuthID           string `json:"auth_id"`
			NormalizedModel  string `json:"normalized_model"`
			ProgressScopeKey string `json:"progress_scope_key"`
		} `json:"items"`
		Meta struct {
			ProgressByScope map[string]struct {
				Breaker struct {
					Current   int    `json:"current"`
					Threshold int    `json:"threshold"`
					State     string `json:"state"`
				} `json:"breaker"`
				Deletion struct {
					Current   int    `json:"current"`
					Threshold int    `json:"threshold"`
					Enabled   bool   `json:"enabled"`
					Status    string `json:"status"`
				} `json:"deletion"`
			} `json:"progress_by_scope"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("items len = %d, want 1", len(got.Items))
	}
	if got.Items[0].ProgressScopeKey != scopeKey {
		t.Fatalf("progress_scope_key = %q, want %q", got.Items[0].ProgressScopeKey, scopeKey)
	}
	progress, ok := got.Meta.ProgressByScope[scopeKey]
	if !ok {
		t.Fatalf("missing progress scope %q in %+v", scopeKey, got.Meta.ProgressByScope)
	}
	if progress.Breaker.Current != 3 || progress.Breaker.Threshold != 5 {
		t.Fatalf("breaker progress = %+v, want current=3 threshold=5", progress.Breaker)
	}
	if !progress.Deletion.Enabled || progress.Deletion.Current != 3 || progress.Deletion.Threshold != 4 || progress.Deletion.Status != mongostate.CircuitBreakerDeletionStatusFailed {
		t.Fatalf("deletion progress = %+v, want enabled current=3 threshold=4 status=failed", progress.Deletion)
	}
}
