package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

func TestLiteLLMOnTheFlyLogEndpoints(t *testing.T) {
	skipIfNoPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pg, err := store.NewPostgresStore(ctx, store.PostgresStoreConfig{DSN: pgTestDSN(), Schema: "test_mgmt_onthefly"})
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	defer pg.Close()
	if err := pg.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	onTheFly := store.NewOnTheFlyLogStore(pg)
	logStore := store.NewLiteLLMSyncStore(pg)

	h := &Handler{}
	h.SetLiteLLMOnTheFlyStore(onTheFly)
	h.SetLiteLLMSyncStore(logStore)

	if err := onTheFly.RecordOnTheFly(ctx, store.OnTheFlyLogEvent{
		Outcome: store.OnTheFlyOutcomeSynced, KeyPrefix: "sk-ab", KeyID: "h1", UserID: "u1",
	}); err != nil {
		t.Fatalf("RecordOnTheFly: %v", err)
	}

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/litellm/onthefly-log?outcome=synced", nil)
	h.ListLiteLLMOnTheFlyLog(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var listResp struct {
		Entries []store.OnTheFlyLogEvent `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listResp.Entries) != 1 || listResp.Entries[0].KeyID != "h1" {
		t.Fatalf("entries = %+v", listResp.Entries)
	}

	rec2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(rec2)
	c2.Request = httptest.NewRequest(http.MethodDelete, "/litellm/onthefly-log", nil)
	h.ClearLiteLLMOnTheFlyLog(c2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("clear status = %d; body=%s", rec2.Code, rec2.Body.String())
	}
	remaining, _ := onTheFly.ListOnTheFlyLog(ctx, store.OnTheFlyLogFilter{}, 10)
	if len(remaining) != 0 {
		t.Fatalf("remaining = %d; want 0", len(remaining))
	}
}
