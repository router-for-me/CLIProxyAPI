package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestGetUsageAggregatedReturnsLeaderboardShape(t *testing.T) {
	gin.SetMode(gin.TestMode)

	withAggregatedUsageStore(t, func() {
		now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
		ingestAggregatedRecord(usage.Record{
			Provider:    "codex",
			Model:       "gpt-5",
			Alias:       "gpt-5",
			APIKey:      "sk-cli-test1",
			AuthType:    "oauth",
			Source:      "openai",
			RequestedAt: now,
			Latency:     1500 * time.Millisecond,
			Detail: usage.Detail{
				InputTokens:  1000,
				OutputTokens: 200,
				CachedTokens: 100,
				TotalTokens:  1200,
			},
		})
		ingestAggregatedRecord(usage.Record{
			Provider:    "codex",
			Model:       "gpt-5",
			Alias:       "gpt-5",
			APIKey:      "sk-cli-test1",
			AuthType:    "oauth",
			Source:      "openai",
			RequestedAt: now.Add(2 * time.Minute),
			Latency:     800 * time.Millisecond,
			Detail: usage.Detail{
				InputTokens:  500,
				OutputTokens: 50,
				TotalTokens:  550,
			},
		})
		ingestAggregatedRecord(usage.Record{
			Provider:    "claude",
			Model:       "claude-sonnet-4",
			Alias:       "claude-sonnet-4",
			APIKey:      "sk-cli-test2",
			AuthType:    "oauth",
			Source:      "anthropic",
			RequestedAt: now,
			Latency:     1000 * time.Millisecond,
			Detail: usage.Detail{
				InputTokens:  2000,
				OutputTokens: 400,
				TotalTokens:  2400,
			},
		})

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage", nil)

		h := &Handler{}
		h.GetUsageAggregated(ginCtx)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
		}

		var resp struct {
			Usage struct {
				Apis map[string]struct {
					TotalRequests int64 `json:"total_requests"`
					TotalTokens   int64 `json:"total_tokens"`
					Models        map[string]struct {
						Details []struct {
							Timestamp string `json:"timestamp"`
							Tokens    struct {
								InputTokens  int64 `json:"input_tokens"`
								OutputTokens int64 `json:"output_tokens"`
								CachedTokens int64 `json:"cached_tokens"`
								TotalTokens  int64 `json:"total_tokens"`
							} `json:"tokens"`
						} `json:"details"`
					} `json:"models"`
				} `json:"apis"`
			} `json:"usage"`
		}
		if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &resp); errUnmarshal != nil {
			t.Fatalf("unmarshal response: %v body=%s", errUnmarshal, rec.Body.String())
		}

		key1 := resp.Usage.Apis["sk-cli-test1"]
		if key1.TotalRequests != 2 {
			t.Fatalf("sk-cli-test1 total_requests = %d, want 2", key1.TotalRequests)
		}
		if key1.TotalTokens != 1750 {
			t.Fatalf("sk-cli-test1 total_tokens = %d, want 1750", key1.TotalTokens)
		}
		gpt5 := key1.Models["gpt-5"]
		if len(gpt5.Details) != 2 {
			t.Fatalf("sk-cli-test1.gpt-5.details count = %d, want 2", len(gpt5.Details))
		}
		if gpt5.Details[0].Tokens.InputTokens != 1000 {
			t.Fatalf("first detail input_tokens = %d, want 1000", gpt5.Details[0].Tokens.InputTokens)
		}
		if gpt5.Details[0].Tokens.CachedTokens != 100 {
			t.Fatalf("first detail cached_tokens = %d, want 100", gpt5.Details[0].Tokens.CachedTokens)
		}

		key2 := resp.Usage.Apis["sk-cli-test2"]
		if key2.TotalRequests != 1 {
			t.Fatalf("sk-cli-test2 total_requests = %d, want 1", key2.TotalRequests)
		}
		if key2.TotalTokens != 2400 {
			t.Fatalf("sk-cli-test2 total_tokens = %d, want 2400", key2.TotalTokens)
		}
	})
}

func TestGetUsageAggregatedEmptyStateReturnsEmptyApis(t *testing.T) {
	gin.SetMode(gin.TestMode)

	withAggregatedUsageStore(t, func() {
		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage", nil)

		h := &Handler{}
		h.GetUsageAggregated(ginCtx)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}

		var resp struct {
			Usage struct {
				Apis map[string]json.RawMessage `json:"apis"`
			} `json:"usage"`
		}
		if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &resp); errUnmarshal != nil {
			t.Fatalf("unmarshal: %v", errUnmarshal)
		}
		if len(resp.Usage.Apis) != 0 {
			t.Fatalf("apis count = %d, want 0 on empty store", len(resp.Usage.Apis))
		}
	})
}

func ingestAggregatedRecord(r usage.Record) {
	aggregatedUsageStore.HandleUsage(context.Background(), r)
}

func withAggregatedUsageStore(t *testing.T, fn func()) {
	t.Helper()
	aggregatedUsageStore.reset()
	defer aggregatedUsageStore.reset()
	fn()
}
