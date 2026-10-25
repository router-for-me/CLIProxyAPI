package executor

import (
	"context"
	"errors"
	"fmt"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	execution "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	translator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDelegatedLaunchActualCodexErrorCategories(t *testing.T) {
	for _, tc := range []struct {
		name, body, code, scope string
		modelCooling            bool
	}{
		{"account", `{"error":{"type":"usage_limit_reached"}}`, "usage_limit", "account", false},
		{"quota-code", `{"error":{"code":"insufficient_quota"}}`, "usage_limit", "account", false},
		{"model-cooling-not-proof", `{"error":{"type":"usage_limit_reached"}}`, "quota_unverified", "unknown", true},
		{"throttle", `{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded"}}`, "throttled", "transient", false},
		{"capacity", `{"error":{"code":"model_at_capacity"}}`, "model_capacity", "model", false},
		{"overload", `{"error":{"code":"server_is_overloaded"}}`, "model_capacity", "model", false},
		{"prose-capacity", `{"error":{"message":"Selected model is at capacity. Please try a different model."}}`, "", "", false},
		{"unknown", `{"error":{"code":"future_429"}}`, "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := newCodexStatusErrWithCooling(http.StatusTooManyRequests, []byte(tc.body), tc.modelCooling)
			code, scope := err.DelegatedFailure()
			if code != tc.code || scope != tc.scope {
				t.Fatalf("classification=%s/%s want %s/%s", code, scope, tc.code, tc.scope)
			}
			// Exercise actual terminal event extraction, not a dummy manager error.
			event := []byte(`{"type":"response.failed","response":` + tc.body + `}`)
			terminal, _, ok := codexTerminalFailureErrWithCooling(event, tc.modelCooling)
			if !ok {
				t.Fatal("terminal error lost")
			}
			if c, s := terminal.DelegatedFailure(); c != tc.code || s != tc.scope {
				t.Fatalf("terminal=%s/%s", c, s)
			}
		})
	}
}

type delegatedCountingTransport struct{ calls *atomic.Int32 }

func (d delegatedCountingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	d.calls.Add(1)
	return nil, errors.New("unexpected outbound execution")
}
func TestDelegatedLaunchFinalHeaderPairing(t *testing.T) {
	for _, header := range []string{"Authorization", "Chatgpt-Account-Id"} {
		for _, path := range []string{"response", "stream", "compact"} {
			t.Run(header+"/"+path, func(t *testing.T) {
				model := "bound-header-" + strings.ToLower(header) + "-" + path
				id := t.Name()
				registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: model, Config: &registry.ModelConfig{OverrideHeader: map[string]string{header: "wrong-authority"}}}})
				defer registry.GetGlobalRegistry().UnregisterClient(id)
				ex := NewCodexAutoExecutor(&config.Config{})
				a := &auth.Auth{ID: id, Provider: "codex", Metadata: map[string]any{"type": "codex", "access_token": "synthetic-token", "account_id": "synthetic-account"}}
				var calls atomic.Int32
				ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", delegatedCountingTransport{&calls})
				opts := execution.Options{SourceFormat: translator.FormatOpenAIResponse, Stream: path == "stream"}
				if path == "compact" {
					opts.Alt = "responses/compact"
				}
				_, _, err := ex.ExecuteDelegatedLaunch(ctx, a, execution.Request{Model: model, Payload: []byte(fmt.Sprintf(`{"model":%q,"input":[]}`, model))}, opts)
				if !errors.Is(err, execution.ErrBoundAuthChanged) || calls.Load() != 0 {
					t.Fatalf("final auth override dispatched: error=%v outbound=%d", err, calls.Load())
				}
			})
		}
	}
}
