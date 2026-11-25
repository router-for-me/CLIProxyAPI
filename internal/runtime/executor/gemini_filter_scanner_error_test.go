package executor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const geminiIssue7956FilterText = "This request was blocked by Gemini's filters. Please review https://policies.google.com/terms/generative-ai/use-policy"

func TestGeminiExecuteStreamDoesNotFlushPendingFilterOnScannerError(t *testing.T) {
	for _, tc := range issue7956TruncationCases() {
		t.Run(tc.name, func(t *testing.T) {
			server := newIssue7956FilterTruncationServer(t, tc.trailing)
			defer server.Close()

			exec := NewGeminiExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{
				ID:       t.Name(),
				Provider: "gemini",
				Attributes: map[string]string{
					"api_key":  "test-key",
					"base_url": server.URL,
				},
			}

			assertIssue7956FilterFrameSuppressed(t, exec, context.Background(), auth)
		})
	}
}

func TestGeminiVertexExecuteStreamDoesNotFlushPendingFilterOnScannerError(t *testing.T) {
	for _, authMode := range []string{"api_key", "service_account"} {
		for _, tc := range issue7956TruncationCases() {
			t.Run(authMode+"/"+tc.name, func(t *testing.T) {
				server := newIssue7956FilterTruncationServer(t, tc.trailing)
				defer server.Close()

				ctx := context.Background()
				auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "vertex"}
				if authMode == "api_key" {
					auth.Attributes = map[string]string{
						"api_key":  "test-key",
						"base_url": server.URL,
					}
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
					target, errParse := url.Parse(server.URL)
					if errParse != nil {
						t.Fatal(errParse)
					}
					transport := http.DefaultTransport.(*http.Transport).Clone()
					transport.Proxy = nil
					t.Cleanup(transport.CloseIdleConnections)
					ctx = context.WithValue(ctx, "cliproxy.roundtripper", issue6258RedirectTransport{target: target, base: transport})
				}

				assertIssue7956FilterFrameSuppressed(t, NewGeminiVertexExecutor(&config.Config{}), ctx, auth)
			})
		}
	}
}

func issue7956TruncationCases() []struct {
	name     string
	trailing string
} {
	return []struct {
		name     string
		trailing string
	}{
		{name: "immediate_eof"},
		{name: "truncated_next_frame", trailing: "data: {"},
	}
}

func newIssue7956FilterTruncationServer(t *testing.T, trailing string) *httptest.Server {
	t.Helper()
	filterFrame := `{"candidates":[{"content":{"parts":[{"text":"` + geminiIssue7956FilterText + `"}]}}]}`
	sse := []byte("data: " + filterFrame + "\n\n" + trailing)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"local-token","token_type":"Bearer","expires_in":3600}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Length", "1000")
		_, _ = w.Write(sse)
	}))
}

func assertIssue7956FilterFrameSuppressed(t *testing.T, exec interface {
	ExecuteStream(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error)
}, ctx context.Context, auth *cliproxyauth.Auth) {
	t.Helper()
	result, errExecute := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "gemini-2.5-flash",
		Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{}}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatGemini,
		ResponseFormat: sdktranslator.FormatGemini,
		Stream:         true,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}

	var output bytes.Buffer
	var streamErr error
	for chunk := range result.Chunks {
		output.Write(chunk.Payload)
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
	}
	if bytes.Contains(output.Bytes(), []byte(geminiIssue7956FilterText)) {
		t.Fatalf("ExecuteStream() emitted buffered filter frame after scanner error: %s", output.Bytes())
	}
	if !errors.Is(streamErr, io.ErrUnexpectedEOF) {
		t.Fatalf("ExecuteStream() stream error = %v, want unexpected EOF", streamErr)
	}
}
