package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestOpenAICompatExecutorMissingDoneKeepsCredentialsAvailable(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprintf("partial=%t", partial), func(t *testing.T) {
			var calls atomic.Int32
			transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				calls.Add(1)
				response := ""
				if partial {
					response = `data: {"id":"chatcmpl_test","object":"chat.completion.chunk","created":1,"model":"compat-test-model","choices":[{"index":0,"delta":{"role":"assistant","content":"partial"},"finish_reason":null}]}` + "\n\n"
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(response)), Request: req}, nil
			})
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
			provider := "compat-missing-done"
			model := "compat-test-model"
			manager := cliproxyauth.NewManager(nil, nil, nil)
			manager.SetRetryConfig(0, 0, 0)
			manager.RegisterExecutor(NewOpenAICompatExecutor(provider, &config.Config{}))
			reg := registry.GetGlobalRegistry()
			var authIDs []string
			for i := range 3 {
				auth := &cliproxyauth.Auth{
					ID:       fmt.Sprintf("compat-missing-done-%t-%d", partial, i),
					Provider: provider,
					Attributes: map[string]string{
						"base_url": "https://compat.example/v1",
						"api_key":  fmt.Sprintf("test-key-%d", i),
					},
				}
				if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
					t.Fatal(errRegister)
				}
				reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
				authIDs = append(authIDs, auth.ID)
			}

			// More requests than credentials reproduce the transition to auth_unavailable
			// if each protocol failure incorrectly cools down one otherwise valid key.
			for attempt := range 4 {
				payload := []byte(`{"model":"compat-test-model","input":"hi","stream":true}`)
				result, errStream := manager.ExecuteStream(ctx, []string{provider}, cliproxyexecutor.Request{Model: model, Payload: payload}, cliproxyexecutor.Options{
					SourceFormat:    sdktranslator.FormatOpenAIResponse,
					ResponseFormat:  sdktranslator.FormatOpenAIResponse,
					OriginalRequest: payload,
					Stream:          true,
				})
				var output strings.Builder
				if result != nil {
					for chunk := range result.Chunks {
						output.Write(chunk.Payload)
						if chunk.Err != nil {
							errStream = chunk.Err
						}
					}
				}
				var status interface{ StatusCode() int }
				if !errors.As(errStream, &status) || status.StatusCode() != http.StatusBadGateway {
					t.Errorf("request %d: error = %v, want the original 502 protocol error", attempt, errStream)
				}
				if errStream == nil || !strings.Contains(errStream.Error(), "closed before [DONE]") {
					t.Errorf("request %d: lost the missing [DONE] diagnostic: %v", attempt, errStream)
				}
				if strings.Contains(output.String(), "response.completed") {
					t.Errorf("request %d: incomplete stream was reported as completed", attempt)
				}
				if partial && !strings.Contains(output.String(), "response.output_text.delta") {
					t.Errorf("request %d: partial output was lost", attempt)
				}
				if got := calls.Load(); got != int32(attempt+1) {
					t.Errorf("request %d: upstream calls = %d, want %d without credential fallback", attempt, got, attempt+1)
				}
			}
			var failed int64
			for _, id := range authIDs {
				auth, ok := manager.GetByID(id)
				if !ok {
					t.Fatalf("credential %s disappeared", id)
				}
				failed += auth.Failed
				if auth.Unavailable || !auth.NextRetryAfter.IsZero() || auth.ModelStates[model] != nil {
					t.Errorf("credential %s was cooled down for a missing stream terminator", id)
				}
				if reg.IsModelSuspendedForClient(id, model) {
					t.Errorf("model was suspended for credential %s", id)
				}
			}
			if failed != 4 {
				t.Errorf("recorded failures = %d, want 4", failed)
			}
		})
	}
}
