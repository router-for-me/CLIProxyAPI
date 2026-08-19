package test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

func TestGeminiExecutorRecordsSuccessfulZeroUsageInQueue(t *testing.T) {
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

	model, _ := executeGeminiZeroUsage(t, "queue")
	waitForQueuedUsageModelTotalTokens(t, "gemini", model, 0)
}

func TestGeminiExecutorRecordsSuccessfulZeroUsageInStatistics(t *testing.T) {
	prevStatsEnabled := internalusage.StatisticsEnabled()
	internalusage.SetStatisticsEnabled(true)
	t.Cleanup(func() {
		internalusage.SetStatisticsEnabled(prevStatsEnabled)
	})

	model, source := executeGeminiZeroUsage(t, "stats")
	detail := waitForStatisticsDetail(t, "gemini", model)
	if detail.Source == source {
		t.Fatalf("detail source leaked raw account identifier")
	}
	if detail.Failed {
		t.Fatalf("detail failed = true, want false")
	}
	if detail.Tokens.TotalTokens != 0 {
		t.Fatalf("total tokens = %d, want 0", detail.Tokens.TotalTokens)
	}
}

func executeGeminiZeroUsage(t *testing.T, suffix string) (string, string) {
	t.Helper()

	model := fmt.Sprintf("gemini-2.5-flash-zero-usage-%s-%d", suffix, time.Now().UnixNano())
	source := fmt.Sprintf("zero-usage-%s-%d@example.com", suffix, time.Now().UnixNano())

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

	return model, source
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

func waitForStatisticsDetail(t *testing.T, apiName, model string) internalusage.RequestDetail {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snapshot := internalusage.GetRequestStatistics().Snapshot()
		apiSnapshot, ok := snapshot.APIs[apiName]
		if !ok {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		modelSnapshot, ok := apiSnapshot.Models[model]
		if !ok {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		for _, detail := range modelSnapshot.Details {
			if detail.Source != "" {
				return detail
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for statistics detail for api=%q model=%q", apiName, model)
	return internalusage.RequestDetail{}
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
