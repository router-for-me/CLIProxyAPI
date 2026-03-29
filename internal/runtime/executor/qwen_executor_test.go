package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

type authScopedUsagePlugin struct {
	authID  string
	records chan usage.Record
}

func (p *authScopedUsagePlugin) HandleUsage(_ context.Context, record usage.Record) {
	if p == nil || record.AuthID != p.authID {
		return
	}
	select {
	case p.records <- record:
	default:
	}
}

func waitForUsageRecord(t *testing.T, records <-chan usage.Record) usage.Record {
	t.Helper()
	select {
	case record := <-records:
		return record
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for usage record")
		return usage.Record{}
	}
}

var (
	qwenPassthroughUsagePluginOnce sync.Once
	qwenPassthroughUsagePlugin     = &authScopedUsagePlugin{
		authID:  "qwen-passthrough-no-usage",
		records: make(chan usage.Record, 8),
	}
)

func TestQwenExecutorParseSuffix(t *testing.T) {
	tests := []struct {
		name      string
		model     string
		wantBase  string
		wantLevel string
	}{
		{"no suffix", "qwen-max", "qwen-max", ""},
		{"with level suffix", "qwen-max(high)", "qwen-max", "high"},
		{"with budget suffix", "qwen-max(16384)", "qwen-max", "16384"},
		{"complex model name", "qwen-plus-latest(medium)", "qwen-plus-latest", "medium"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := thinking.ParseSuffix(tt.model)
			if result.ModelName != tt.wantBase {
				t.Errorf("ParseSuffix(%q).ModelName = %q, want %q", tt.model, result.ModelName, tt.wantBase)
			}
		})
	}
}

func TestQwenExecutorExecuteStream_PublishesFallbackUsageWithoutUsageChunk(t *testing.T) {
	qwenPassthroughUsagePluginOnce.Do(func() {
		usage.RegisterPlugin(qwenPassthroughUsagePlugin)
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	executor := NewQwenExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID: "qwen-passthrough-no-usage",
		Attributes: map[string]string{
			"api_key":  "token-123",
			"base_url": server.URL,
		},
	}

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "qwen-max",
		Payload: []byte(`{"model":"qwen-max","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream chunk error: %v", chunk.Err)
		}
	}

	record := waitForUsageRecord(t, qwenPassthroughUsagePlugin.records)
	if record.AuthID != auth.ID {
		t.Fatalf("usage record auth_id = %q, want %q", record.AuthID, auth.ID)
	}
	if record.Provider != "qwen" {
		t.Fatalf("usage record provider = %q, want %q", record.Provider, "qwen")
	}
	if record.Failed {
		t.Fatal("usage fallback should mark request as successful")
	}
	if record.Detail != (usage.Detail{}) {
		t.Fatalf("usage fallback detail = %+v, want zero-value detail", record.Detail)
	}
}
