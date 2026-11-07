package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type terminalPoolBody struct {
	io.ReadCloser
	read      bool
	release   func()
	readError error
}

func (b *terminalPoolBody) Read(p []byte) (int, error) {
	if b.read {
		b.release()
		if b.readError != nil {
			return 0, b.readError
		}
	}
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.read = true
	}
	return n, err
}

// A client may cancel immediately on response.completed. The HTTP body must
// already be at EOF, otherwise cancellation destroys the reusable connection.
func TestAntigravityPooledCompletionFollowsEOF(t *testing.T) {
	for _, broken := range []bool{false, true} {
		t.Run(fmt.Sprint("broken=", broken), func(t *testing.T) { testAntigravityPooledCompletion(t, broken) })
	}
}

func testAntigravityPooledCompletion(t *testing.T, broken bool) {
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":1,\"candidatesTokenCount\":1,\"totalTokenCount\":2}}}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	tr := http.DefaultTransport.(*http.Transport).Clone()
	defer tr.CloseIdleConnections()
	var idle atomic.Bool
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{PutIdleConn: func(err error) {
		if err == nil {
			idle.Store(true)
		}
	}})
	ctx = context.WithValue(ctx, "cliproxy.roundtripper", roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		resp, err := tr.RoundTrip(r)
		if err == nil {
			body := &terminalPoolBody{ReadCloser: resp.Body, release: unblock}
			if broken {
				body.readError = io.ErrUnexpectedEOF
			}
			resp.Body = body
		}
		return resp, err
	}))
	enabled := true
	exec := NewAntigravityExecutor(&config.Config{Antigravity: config.AntigravityConfig{ConnectionPool: config.AntigravityConnectionPoolConfig{Enabled: &enabled}}})
	auth := &cliproxyauth.Auth{ID: "terminal-pool", Provider: "antigravity", Attributes: map[string]string{"base_url": server.URL}, Metadata: map[string]any{"access_token": "token", "project_id": "project", "expired": time.Now().Add(time.Hour).Format(time.RFC3339)}}
	payload := []byte(`{"model":"gemini-3.8-flash-high","input":"hi","stream":true}`)
	result, err := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{Model: "gemini-3.8-flash-high", Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse, Stream: true, OriginalRequest: payload})
	if err != nil {
		t.Fatal(err)
	}
	completed := false
	failed := false
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			failed = true
			if !broken {
				t.Fatal(chunk.Err)
			}
		}
		if strings.Contains(string(chunk.Payload), "response.completed") {
			completed = true
			wasIdle := idle.Load()
			cancel()
			unblock()
			if !wasIdle {
				t.Error("completion delivered before HTTP EOF returned connection to pool")
			}
		}
	}
	if broken {
		if completed || !failed {
			t.Fatalf("broken stream completed=%v failed=%v", completed, failed)
		}
	} else if !completed {
		t.Fatal("missing completion")
	}
}
