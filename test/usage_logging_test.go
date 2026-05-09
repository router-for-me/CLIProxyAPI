package test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	internalusage "github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestGeminiExecutorRecordsSuccessfulZeroUsageInStatistics(t *testing.T) {
	model := fmt.Sprintf("gemini-2.5-flash-zero-usage-%d", time.Now().UnixNano())
	source := fmt.Sprintf("zero-usage-%d@example.com", time.Now().UnixNano())

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/v1beta/models/" + model + ":generateContent"
		if r.URL.Path != wantPath {
			t.Fatalf("path = %q, want %q", r.URL.Path, wantPath)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":0,"candidatesTokenCount":0,"totalTokenCount":0}}`))
	}))
	defer server.Close()

	store, err := internalusage.NewSQLiteStore(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	restoreStore := internalusage.SetDefaultStoreForTest(store)
	t.Cleanup(restoreStore)

	executor := runtimeexecutor.NewGeminiExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "gemini",
		Attributes: map[string]string{
			"api_key":  "test-upstream-key",
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"email": source,
		},
	}

	prevStatsEnabled := internalusage.StatisticsEnabled()
	internalusage.SetStatisticsEnabled(true)
	t.Cleanup(func() {
		internalusage.SetStatisticsEnabled(prevStatsEnabled)
	})

	_, err = executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   model,
		Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatGemini,
		OriginalRequest: []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`),
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	detail := waitForStatisticsDetail(t, store, "gemini", model, source)
	if detail.ID == "" {
		t.Fatalf("detail ID is empty")
	}
	if detail.LatencyMs < 0 || detail.FirstByteLatencyMs < 0 || detail.GenerationMs < 0 {
		t.Fatalf("latency fields must be non-negative: latency=%d first_byte=%d generation=%d", detail.LatencyMs, detail.FirstByteLatencyMs, detail.GenerationMs)
	}
	if detail.Failed {
		t.Fatalf("detail failed = true, want false")
	}
	if detail.Tokens.TotalTokens != 0 {
		t.Fatalf("total tokens = %d, want 0", detail.Tokens.TotalTokens)
	}
}

func TestGeminiExecutorRecordsSuccessfulZeroUsageInQueue(t *testing.T) {
	model := fmt.Sprintf("gemini-2.5-flash-zero-usage-%d", time.Now().UnixNano())
	source := fmt.Sprintf("zero-usage-%d@example.com", time.Now().UnixNano())

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/v1beta/models/" + model + ":generateContent"
		if r.URL.Path != wantPath {
			t.Fatalf("path = %q, want %q", r.URL.Path, wantPath)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":0,"candidatesTokenCount":0,"totalTokenCount":0}}`))
	}))
	defer server.Close()

	executor := runtimeexecutor.NewGeminiExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "gemini",
		Attributes: map[string]string{
			"api_key":  "test-upstream-key",
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"email": source,
		},
	}

	prevQueueEnabled := redisqueue.Enabled()
	prevUsageEnabled := redisqueue.UsageStatisticsEnabled()
	redisqueue.SetEnabled(false)
	redisqueue.SetEnabled(true)
	redisqueue.SetUsageStatisticsEnabled(true)
	t.Cleanup(func() {
		redisqueue.SetEnabled(false)
		redisqueue.SetEnabled(prevQueueEnabled)
		redisqueue.SetUsageStatisticsEnabled(prevUsageEnabled)
	})

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   model,
		Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatGemini,
		OriginalRequest: []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`),
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	waitForQueuedUsageModelTotalTokens(t, "gemini", model, 0)
}

func waitForQueuedUsageModelTotalTokens(t *testing.T, wantProvider, wantModel string, wantTokens int64) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		items := redisqueue.PopOldest(10)
		for _, item := range items {
			got, ok := parseQueuedUsagePayload(t, item)
			if !ok {
				continue
			}
			if got.Provider != wantProvider || got.Model != wantModel {
				continue
			}
			if got.Failed {
				t.Fatalf("payload failed = true, want false")
			}
			if got.Tokens.TotalTokens != wantTokens {
				t.Fatalf("payload total tokens = %d, want %d", got.Tokens.TotalTokens, wantTokens)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for queued usage payload for provider=%q model=%q", wantProvider, wantModel)
}

type queuedUsagePayload struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Failed   bool   `json:"failed"`
	Tokens   struct {
		TotalTokens int64 `json:"total_tokens"`
	} `json:"tokens"`
}

func parseQueuedUsagePayload(t *testing.T, payload []byte) (queuedUsagePayload, bool) {
	t.Helper()

	var parsed queuedUsagePayload
	if len(payload) == 0 {
		return parsed, false
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return parsed, false
	}
	if parsed.Provider == "" || parsed.Model == "" {
		return parsed, false
	}
	return parsed, true
}

func waitForStatisticsDetail(t *testing.T, store internalusage.Store, apiName, model, source string) internalusage.RequestDetail {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		usageByAPI, err := store.Query(context.Background(), internalusage.QueryRange{})
		if err != nil {
			t.Fatalf("Query() error = %v", err)
		}
		for _, detail := range usageByAPI[apiName][model] {
			if detail.Source == source {
				return detail
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for statistics detail for api=%q model=%q source=%q", apiName, model, source)
	return internalusage.RequestDetail{}
}
