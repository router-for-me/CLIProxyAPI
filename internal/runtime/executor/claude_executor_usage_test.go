package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
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

var (
	claudePassthroughUsagePluginOnce sync.Once
	claudePassthroughUsagePlugin     = &authScopedUsagePlugin{
		authID:  "claude-passthrough-no-usage",
		records: make(chan usage.Record, 8),
	}
)

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

func TestClaudeExecutorExecuteStream_PassthroughPublishesFallbackUsageWithoutUsageChunk(t *testing.T) {
	claudePassthroughUsagePluginOnce.Do(func() {
		usage.RegisterPlugin(claudePassthroughUsagePlugin)
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID: "claude-passthrough-no-usage",
		Attributes: map[string]string{
			"api_key":  "key-123",
			"base_url": server.URL,
		},
	}
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
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

	record := waitForUsageRecord(t, claudePassthroughUsagePlugin.records)
	if record.AuthID != auth.ID {
		t.Fatalf("usage record auth_id = %q, want %q", record.AuthID, auth.ID)
	}
	if record.Provider != "claude" {
		t.Fatalf("usage record provider = %q, want %q", record.Provider, "claude")
	}
	if record.Failed {
		t.Fatal("usage fallback should mark request as successful")
	}
	if record.Detail != (usage.Detail{}) {
		t.Fatalf("usage fallback detail = %+v, want zero-value detail", record.Detail)
	}
}
