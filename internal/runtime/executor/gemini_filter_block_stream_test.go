package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestGeminiExecuteStreamFilterBlockWithPromptFeedbackReturnsBadRequest(t *testing.T) {
	for _, blockReason := range []string{"SAFETY", "PROHIBITED_CONTENT"} {
		t.Run(blockReason, func(t *testing.T) {
			server := newGeminiFilterBlockStreamServer(t, blockReason)
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

			assertGeminiFilterBlockReturns400(t, exec, context.Background(), auth)
		})
	}
}

func TestGeminiVertexExecuteStreamFilterBlockWithPromptFeedbackReturnsBadRequest(t *testing.T) {
	for _, authMode := range []string{"api_key", "service_account"} {
		for _, blockReason := range []string{"SAFETY", "PROHIBITED_CONTENT"} {
			t.Run(authMode+"/"+blockReason, func(t *testing.T) {
				server := newGeminiFilterBlockStreamServer(t, blockReason)
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

				assertGeminiFilterBlockReturns400(t, NewGeminiVertexExecutor(&config.Config{}), ctx, auth)
			})
		}
	}
}

func newGeminiFilterBlockStreamServer(t *testing.T, blockReason string) *httptest.Server {
	t.Helper()
	filterFrame := `{"candidates":[{"content":{"parts":[{"text":"` + geminiIssue7956FilterText + `"}]}}],"promptFeedback":{"blockReason":"` + blockReason + `"}}`
	sse := []byte("data: " + filterFrame + "\n\n")
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"local-token","token_type":"Bearer","expires_in":3600}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write(sse)
	}))
}

func assertGeminiFilterBlockReturns400(t *testing.T, exec interface {
	ExecuteStream(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error)
}, ctx context.Context, auth *cliproxyauth.Auth) {
	t.Helper()
	result, errExecute := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "gemini-2.5-flash",
		Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{}}`),
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatGemini,
		OriginalRequest: []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`),
		Stream:          true,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() unexpected setup error: %v", errExecute)
	}

	var gotErr error
	var payloads [][]byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			gotErr = chunk.Err
		}
		if len(chunk.Payload) > 0 {
			payloads = append(payloads, chunk.Payload)
		}
	}

	if gotErr == nil {
		t.Fatalf("expected HTTP 400 filter-block error, got nil (emitted %d payload chunks)", len(payloads))
	}
	var sErr statusErr
	if !errors.As(gotErr, &sErr) {
		t.Fatalf("expected statusErr, got %T (%v)", gotErr, gotErr)
	}
	if sErr.code != http.StatusBadRequest {
		t.Fatalf("expected status code %d, got %d (msg=%q)", http.StatusBadRequest, sErr.code, sErr.msg)
	}
	if !strings.Contains(sErr.msg, "blocked by Gemini's filters") {
		t.Fatalf("expected filter-block message, got %q", sErr.msg)
	}
	if len(payloads) > 0 {
		t.Fatalf("expected filter-blocked frame not to be emitted as payload, got %d chunks", len(payloads))
	}
}
