package auth_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

type kimiCountTokensRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f kimiCountTokensRoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type staticRoundTripperProvider struct {
	roundTripper http.RoundTripper
}

func (p staticRoundTripperProvider) RoundTripperFor(*cliproxyauth.Auth) http.RoundTripper {
	return p.roundTripper
}

func TestManagerExecuteCountReportsKimiClaudeWireFormat(t *testing.T) {
	var upstreamRequest *http.Request
	var upstreamBody []byte
	roundTripper := kimiCountTokensRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		upstreamRequest = req.Clone(req.Context())
		var errRead error
		upstreamBody, errRead = io.ReadAll(req.Body)
		if errRead != nil {
			return nil, errRead
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"input_tokens":42}`)),
		}, nil
	})

	const authID = "kimi-test-auth"
	registry.GetGlobalRegistry().RegisterClient(authID, "kimi", []*registry.ModelInfo{{ID: "kimi-k3"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })

	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor.NewKimiExecutor(&config.Config{}))
	manager.SetRoundTripperProvider(staticRoundTripperProvider{roundTripper: roundTripper})
	if _, errRegister := manager.Register(context.Background(), &cliproxyauth.Auth{
		ID:         authID,
		Provider:   "kimi",
		Attributes: map[string]string{},
		Metadata:   map[string]any{"access_token": "test-token"},
	}); errRegister != nil {
		t.Fatalf("register Kimi auth: %v", errRegister)
	}

	var interceptedFormat sdktranslator.Format
	payload := []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)
	_, errCount := manager.ExecuteCount(context.Background(), []string{"kimi"}, cliproxyexecutor.Request{
		Model:   "kimi-k3",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatGemini,
		OriginalRequest: payload,
		RequestAfterAuthInterceptor: func(_ context.Context, req cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
			interceptedFormat = req.ToFormat
			return cliproxyexecutor.RequestAfterAuthInterceptResponse{}
		},
	})
	if errCount != nil {
		t.Fatalf("ExecuteCount() error = %v", errCount)
	}
	if interceptedFormat != sdktranslator.FormatClaude {
		t.Fatalf("interceptor ToFormat = %q, want %q", interceptedFormat, sdktranslator.FormatClaude)
	}
	if upstreamRequest == nil {
		t.Fatal("Kimi count-token request was not captured")
	}
	if got := upstreamRequest.URL.String(); got != "https://api.kimi.com/coding/v1/messages/count_tokens?beta=true" {
		t.Fatalf("upstream URL = %q, want Kimi Claude count-token endpoint", got)
	}
	if got := gjson.GetBytes(upstreamBody, "model").String(); got != "k3" {
		t.Fatalf("upstream model = %q, want k3; body=%s", got, upstreamBody)
	}
	if got := gjson.GetBytes(upstreamBody, "messages.0.role").String(); got != "user" {
		t.Fatalf("upstream body is not Claude Messages format; body=%s", upstreamBody)
	}
}
