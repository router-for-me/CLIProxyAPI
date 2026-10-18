package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type metaTerminalDisconnectUsageCapture struct {
	authID  string
	records chan usage.Record
}

func (c *metaTerminalDisconnectUsageCapture) HandleUsage(_ context.Context, record usage.Record) {
	if c == nil || record.Provider != "meta" || record.AuthID != c.authID {
		return
	}
	select {
	case c.records <- record:
	default:
	}
}

type metaTerminalDisconnectUsageNoop struct{}

func (metaTerminalDisconnectUsageNoop) HandleUsage(context.Context, usage.Record) {}

const (
	metaTerminalDisconnectDelta     = "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"output_index\":0,\"content_index\":0,\"delta\":\"hello\"}\n\n"
	metaTerminalDisconnectCompleted = "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"muse-wonder-1.0\",\"output\":[{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}],\"usage\":{\"input_tokens\":10,\"output_tokens\":5,\"total_tokens\":15}}}\n\n"
)

// The upstream keeps the body open after the written events, as Meta does
// while it still has to send response.subscription_usage. A Claude client
// cancels once it sees cancelAfter.
func runMetaClaudeDisconnect(t *testing.T, upstreamSSE, cancelAfter string) usage.Record {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, upstreamSSE)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	auth := &cliproxyauth.Auth{
		ID:       t.Name(),
		Provider: "meta",
		Attributes: map[string]string{
			"api_key":  "meta-token",
			"base_url": server.URL,
		},
	}
	capture := &metaTerminalDisconnectUsageCapture{authID: auth.ID, records: make(chan usage.Record, 1)}
	usage.RegisterNamedPlugin(t.Name(), capture)
	t.Cleanup(func() {
		usage.RegisterNamedPlugin(t.Name(), metaTerminalDisconnectUsageNoop{})
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result, errExecute := NewMetaExecutor(&config.Config{}).ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "muse-wonder-1.0",
		Payload: []byte(`{"model":"muse-wonder-1.0","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
		Stream:       true,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}

	canceled := false
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error before cancellation: %v", chunk.Err)
		}
		if strings.Contains(string(chunk.Payload), cancelAfter) {
			canceled = true
			cancel()
			break
		}
	}
	if !canceled {
		t.Fatalf("stream ended before %q", cancelAfter)
	}

	select {
	case record := <-capture.records:
		return record
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for usage record")
		return usage.Record{}
	}
}

func TestMetaStream_ClientDisconnectAfterTerminalEventIsNotFailed(t *testing.T) {
	record := runMetaClaudeDisconnect(t, metaTerminalDisconnectDelta+metaTerminalDisconnectCompleted, "message_stop")
	if record.Failed {
		t.Fatalf("usage record marked failed: status=%d", record.Fail.StatusCode)
	}
	if record.Detail.InputTokens != 10 || record.Detail.OutputTokens != 5 {
		t.Fatalf("reported usage = %+v, want input 10 output 5", record.Detail)
	}
}

func TestMetaStream_ClientDisconnectBeforeTerminalEventIsFailed(t *testing.T) {
	record := runMetaClaudeDisconnect(t, metaTerminalDisconnectDelta, "content_block_delta")
	if !record.Failed {
		t.Fatal("usage record not marked failed")
	}
	if record.Fail.StatusCode != clienterror.StatusClientClosedRequest {
		t.Fatalf("Fail.StatusCode = %d, want %d", record.Fail.StatusCode, clienterror.StatusClientClosedRequest)
	}
}
