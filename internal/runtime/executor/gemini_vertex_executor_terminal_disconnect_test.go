package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type vertexTerminalDisconnectUsageCapture struct {
	authID  string
	records chan usage.Record
}

func (c *vertexTerminalDisconnectUsageCapture) HandleUsage(_ context.Context, record usage.Record) {
	if c == nil || record.Provider != "vertex" || record.AuthID != c.authID {
		return
	}
	select {
	case c.records <- record:
	default:
	}
}

type vertexTerminalDisconnectUsageNoop struct{}

func (vertexTerminalDisconnectUsageNoop) HandleUsage(context.Context, usage.Record) {}

func TestGeminiVertexStream_ClientDisconnectAfterTerminalEventIsNotFailed(t *testing.T) {
	for _, terminal := range []struct {
		name         string
		finishReason string
		event        string
	}{
		{name: "completed", finishReason: "STOP", event: "response.completed"},
		{name: "incomplete", finishReason: "MAX_TOKENS", event: "response.incomplete"},
	} {
		t.Run(terminal.name, func(t *testing.T) {
			upstreamSSE := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hello\"}]},\"finishReason\":\"" + terminal.finishReason + "\"}],\"usageMetadata\":{\"promptTokenCount\":10,\"candidatesTokenCount\":5,\"totalTokenCount\":15},\"modelVersion\":\"gemini-3.7-flash\",\"responseId\":\"vertex-terminal-disconnect\"}\n\n"
			for _, provider := range []string{"api_key", "service_account"} {
				t.Run(provider, func(t *testing.T) {
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
						if request.URL.Path == "/token" {
							w.Header().Set("Content-Type", "application/json")
							_, _ = io.WriteString(w, `{"access_token":"local-token","token_type":"Bearer","expires_in":3600}`)
							return
						}
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, upstreamSSE)
						if flusher, ok := w.(http.Flusher); ok {
							flusher.Flush()
						}
						<-request.Context().Done()
					}))
					defer server.Close()

					ctx := context.Background()
					auth := &cliproxyauth.Auth{ID: t.Name()}
					if provider == "api_key" {
						auth.Attributes = map[string]string{"api_key": "test-key", "base_url": server.URL}
					} else {
						var serviceAccount map[string]any
						if errUnmarshal := json.Unmarshal(testVertexServiceAccountJSON(t, server.URL+"/token"), &serviceAccount); errUnmarshal != nil {
							t.Fatal(errUnmarshal)
						}
						auth.Metadata = map[string]any{
							"project_id":      "proxy-test",
							"location":        "global",
							"service_account": serviceAccount,
						}
						target, errParse := parseTestURL(t, server.URL)
						if errParse != nil {
							t.Fatal(errParse)
						}
						transport := http.DefaultTransport.(*http.Transport).Clone()
						transport.Proxy = nil
						t.Cleanup(transport.CloseIdleConnections)
						ctx = context.WithValue(ctx, "cliproxy.roundtripper", issue6258RedirectTransport{target: target, base: transport})
					}

					capture := &vertexTerminalDisconnectUsageCapture{authID: auth.ID, records: make(chan usage.Record, 1)}
					usage.RegisterNamedPlugin(t.Name(), capture)
					t.Cleanup(func() {
						usage.RegisterNamedPlugin(t.Name(), vertexTerminalDisconnectUsageNoop{})
					})

					ctx, cancel := context.WithCancel(ctx)
					defer cancel()
					result, errExecute := NewGeminiVertexExecutor(&config.Config{}).ExecuteStream(ctx, auth, cliproxyexecutor.Request{
						Model:   "gemini-3.7-flash",
						Payload: []byte(`{"input":"hello","stream":true}`),
					}, cliproxyexecutor.Options{
						SourceFormat:   sdktranslator.FormatOpenAIResponse,
						ResponseFormat: sdktranslator.FormatOpenAIResponse,
						Stream:         true,
					})
					if errExecute != nil {
						t.Fatalf("ExecuteStream() error = %v", errExecute)
					}

					terminalReceived := false
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatalf("unexpected stream error: %v", chunk.Err)
						}
						if strings.Contains(string(chunk.Payload), terminal.event) {
							terminalReceived = true
							cancel()
							break
						}
					}
					if !terminalReceived {
						t.Fatalf("expected %s before client cancellation", terminal.event)
					}

					select {
					case record := <-capture.records:
						if record.Failed {
							t.Fatalf("usage record marked failed: status=%d, body=%q", record.Fail.StatusCode, record.Fail.Body)
						}
						if record.Detail.InputTokens != 10 || record.Detail.OutputTokens != 5 || record.Detail.TotalTokens != 15 {
							t.Fatalf("reported usage = %+v, want input 10 output 5 total 15", record.Detail)
						}
					case <-time.After(5 * time.Second):
						t.Fatal("timed out waiting for usage record")
					}
				})
			}
		})
	}
}

func parseTestURL(t *testing.T, rawURL string) (*url.URL, error) {
	t.Helper()
	return url.Parse(rawURL)
}
