package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

func TestCodexExecutorDoesNotReplayTurnScopedHeadersAcrossRequests(t *testing.T) {
	var requestCount atomic.Int32
	seenTurnState := make([]string, 0, 2)
	seenTurnMetadata := make([]string, 0, 2)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index := requestCount.Add(1) - 1
		seenTurnState = append(seenTurnState, r.Header.Get(codexHeaderTurnState))
		seenTurnMetadata = append(seenTurnMetadata, r.Header.Get(codexHeaderTurnMetadata))

		w.Header().Set("Content-Type", "text/event-stream")
		if index == 0 {
			w.Header().Set(codexHeaderTurnState, "turn-state-1")
			w.Header().Set(codexHeaderTurnMetadata, `{"turn_id":"upstream-turn-1","thread_source":"user","sandbox":"none"}`)
		}
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID: "auth-1",
		Attributes: map[string]string{
			"base_url": server.URL,
			"api_key":  "test",
		},
	}
	request := cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","prompt_cache_key":"cache-key","input":"hello"}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
	}

	if _, err := executor.Execute(context.Background(), auth, request, opts); err != nil {
		t.Fatalf("first Execute() error = %v", err)
	}
	if _, err := executor.Execute(context.Background(), auth, request, opts); err != nil {
		t.Fatalf("second Execute() error = %v", err)
	}

	if got := requestCount.Load(); got != 2 {
		t.Fatalf("request count = %d, want 2", got)
	}
	if len(seenTurnState) != 2 || len(seenTurnMetadata) != 2 {
		t.Fatalf("unexpected captured headers: turn_state=%v turn_metadata=%v", seenTurnState, seenTurnMetadata)
	}
	if seenTurnState[0] != "" {
		t.Fatalf("first request turn state = %q, want empty", seenTurnState[0])
	}
	if seenTurnState[1] != "" {
		t.Fatalf("second request turn state = %q, want empty for a new request", seenTurnState[1])
	}
	if seenTurnMetadata[0] == "" {
		t.Fatal("first request should carry generated turn metadata")
	}
	if seenTurnMetadata[1] == "" {
		t.Fatal("second request should carry generated turn metadata")
	}
	if seenTurnMetadata[1] == `{"turn_id":"upstream-turn-1","thread_source":"user","sandbox":"none"}` {
		t.Fatalf("second request should not replay upstream turn metadata: %q", seenTurnMetadata[1])
	}
}
