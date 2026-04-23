package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/store/mongostate"
)

type fakeCircuitBreakerDeletionStore struct {
	queryCalled bool
	lastQuery   mongostate.CircuitBreakerDeletionQuery
	result      mongostate.CircuitBreakerDeletionQueryResult
	err         error
}

func (f *fakeCircuitBreakerDeletionStore) Query(_ context.Context, query mongostate.CircuitBreakerDeletionQuery) (mongostate.CircuitBreakerDeletionQueryResult, error) {
	f.queryCalled = true
	f.lastQuery = query
	return f.result, f.err
}

func TestGetCircuitBreakerDeletions_StoreUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mongostate.SetGlobalCircuitBreakerDeletionStore(nil)

	h := &Handler{}
	r := gin.New()
	r.GET("/v0/management/circuit-breaker/deletions", h.GetCircuitBreakerDeletions)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/circuit-breaker/deletions", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
}

func TestGetCircuitBreakerDeletions_InvalidStart(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &fakeCircuitBreakerDeletionStore{}
	mongostate.SetGlobalCircuitBreakerDeletionStore(store)
	t.Cleanup(func() { mongostate.SetGlobalCircuitBreakerDeletionStore(nil) })

	h := &Handler{}
	r := gin.New()
	r.GET("/v0/management/circuit-breaker/deletions", h.GetCircuitBreakerDeletions)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/circuit-breaker/deletions?start=bad", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestGetCircuitBreakerDeletions_Success(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &fakeCircuitBreakerDeletionStore{
		result: mongostate.CircuitBreakerDeletionQueryResult{
			Items:    []mongostate.CircuitBreakerDeletionItem{{ID: "abc", AuthID: "a1", Provider: "gemini", Model: "m1", CreatedAt: time.Now().UTC()}},
			Total:    1,
			Page:     2,
			PageSize: 5,
		},
	}
	mongostate.SetGlobalCircuitBreakerDeletionStore(store)
	t.Cleanup(func() { mongostate.SetGlobalCircuitBreakerDeletionStore(nil) })

	h := &Handler{}
	r := gin.New()
	r.GET("/v0/management/circuit-breaker/deletions", h.GetCircuitBreakerDeletions)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/circuit-breaker/deletions?provider=Gemini&auth_id=a1&model=m1&page=2&page_size=5", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if !store.queryCalled {
		t.Fatal("expected query to be called")
	}
	if store.lastQuery.Provider != "gemini" {
		t.Fatalf("provider = %q, want gemini", store.lastQuery.Provider)
	}
	if store.lastQuery.AuthID != "a1" || store.lastQuery.Model != "m1" {
		t.Fatalf("query filter mismatch: %+v", store.lastQuery)
	}
	if store.lastQuery.Page != 2 || store.lastQuery.PageSize != 5 {
		t.Fatalf("pagination mismatch: %+v", store.lastQuery)
	}

	var got mongostate.CircuitBreakerDeletionQueryResult
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if got.Total != 1 || len(got.Items) != 1 {
		t.Fatalf("result mismatch: %+v", got)
	}
}
