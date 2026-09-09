package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	exec "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

type copilotRoundTripper func(*http.Request) (*http.Response, error)

func (f copilotRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func newTestCopilot() (*CopilotExecutor, *coreauth.Auth) {
	e := NewCopilotExecutor(&config.Config{})
	e.tokenSource = func(context.Context, *coreauth.Auth, bool) (copilot.Token, error) {
		return copilot.Token{Token: "short-copilot-token", ExpiresAt: time.Now().Add(time.Hour).Unix()}, nil
	}
	auth := &coreauth.Auth{ID: "copilot-test", Provider: copilot.Provider, Metadata: map[string]any{"access_token": "persistent-github-token", "email": "octocat", "auth_kind": "oauth"}}
	return e, auth
}

func TestCopilotProtocolRoutingAndCredentialIsolation(t *testing.T) {
	for _, tc := range []struct {
		endpoint          string
		format            translator.Format
		payload, response string
	}{
		{"/chat/completions", translator.FormatOpenAI, `{"model":"test-model","messages":[{"role":"user","content":"hello"}]}`, `{"id":"chat-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`},
		{"/responses", translator.FormatOpenAIResponse, `{"model":"test-model","input":"hello"}`, `{"id":"resp-1","object":"response","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`},
		{"/v1/messages", translator.FormatClaude, `{"model":"test-model","max_tokens":20,"messages":[{"role":"user","content":"hello"}]}`, `{"id":"msg-1","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1}}`},
	} {
		t.Run(tc.endpoint, func(t *testing.T) {
			e, auth := newTestCopilot()
			registry.GetGlobalRegistry().RegisterClient(auth.ID, copilot.Provider, []*registry.ModelInfo{{ID: "test-model", UpstreamEndpoint: tc.endpoint}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			otherEndpoint := "/chat/completions"
			if tc.endpoint == otherEndpoint {
				otherEndpoint = "/responses"
			}
			otherID := auth.ID + "-other"
			registry.GetGlobalRegistry().RegisterClient(otherID, copilot.Provider, []*registry.ModelInfo{{ID: "test-model", UpstreamEndpoint: otherEndpoint}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(otherID) })
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", copilotRoundTripper(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "api.githubcopilot.com" || r.URL.Path != tc.endpoint {
					t.Errorf("wrong endpoint: %s", r.URL)
				}
				if r.Header.Get("Authorization") != "Bearer short-copilot-token" {
					t.Errorf("wrong inference credential: %q", r.Header.Get("Authorization"))
				}
				if r.Header.Get("Copilot-Integration-Id") != "vscode-chat" {
					t.Error("missing Copilot headers")
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(body), "persistent-github-token") {
					t.Error("leaked GitHub credential")
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(tc.response))}, nil
			}))
			req := exec.Request{Model: "test-model", Payload: []byte(tc.payload)}
			opts := exec.Options{SourceFormat: tc.format, OriginalRequest: req.Payload, Metadata: map[string]any{exec.SelectedAuthMetadataKey: auth.ID}}
			if got := e.RequestToFormat(req, opts); got != tc.format {
				t.Errorf("selected account format = %q, want %q", got, tc.format)
			}
			if got := e.delegate(auth, req, opts).Identifier(); got != copilot.Provider {
				t.Errorf("usage attributed to %s", got)
			}
			resp, err := e.Execute(ctx, auth, req, opts)
			if err != nil {
				t.Fatal(err)
			}
			if !gjson.ValidBytes(resp.Payload) {
				t.Fatalf("invalid response: %s", resp.Payload)
			}
			if auth.Attributes["api_key"] != "" || auth.Metadata["access_token"] != "persistent-github-token" {
				t.Fatal("request mutated persisted credentials")
			}
		})
	}
}

func TestCopilotResponsesStreamTerminalAndUsage(t *testing.T) {
	for _, terminal := range []string{"completed", "incomplete", ""} {
		t.Run(terminal, func(t *testing.T) {
			complete := terminal != ""
			e, auth := newTestCopilot()
			registry.GetGlobalRegistry().RegisterClient(auth.ID, copilot.Provider, []*registry.ModelInfo{{ID: "test-response", UpstreamEndpoint: "/responses"}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			otherID := auth.ID + "-other"
			registry.GetGlobalRegistry().RegisterClient(otherID, copilot.Provider, []*registry.ModelInfo{{ID: "test-response", UpstreamEndpoint: "/chat/completions"}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(otherID) })
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", copilotRoundTripper(func(r *http.Request) (*http.Response, error) {
				body, _ := io.ReadAll(r.Body)
				if r.URL.Path != "/responses" || gjson.GetBytes(body, "stream_options").Exists() {
					t.Errorf("bad Responses request: %s %s", r.URL, body)
				}
				sse := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\",\"object\":\"response\",\"status\":\"in_progress\",\"output\":[]}}\n\n"
				if complete {
					sse += "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":2,\"output_tokens\":1,\"total_tokens\":3}}}\n\n"
					sse = strings.ReplaceAll(sse, "completed", terminal)
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(sse))}, nil
			}))
			req := exec.Request{Model: "test-response", Payload: []byte(`{"model":"test-response","input":"hi","stream":true}`)}
			stream, err := e.ExecuteStream(ctx, auth, req, exec.Options{SourceFormat: translator.FormatOpenAIResponse, OriginalRequest: req.Payload, Stream: true})
			if err != nil {
				t.Fatal(err)
			}
			failed := false
			var output strings.Builder
			for chunk := range stream.Chunks {
				if chunk.Err != nil {
					failed = true
				}
				output.Write(chunk.Payload)
			}
			if failed == complete {
				t.Fatalf("complete=%v failed=%v", complete, failed)
			}
			if complete && !strings.Contains(output.String(), "response."+terminal) {
				t.Fatal("missing terminal event")
			}
		})
	}
}

func TestCopilotQuotaFailureUsesAccountFailover(t *testing.T) {
	e, auth := newTestCopilot()
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", copilotRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 402, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"message":"quota exhausted"}}`))}, nil
	}))
	_, err := e.Execute(ctx, auth, exec.Request{Model: "test", Payload: []byte(`{"model":"test","messages":[]}`)}, exec.Options{SourceFormat: translator.FormatOpenAI})
	status, ok := err.(interface {
		StatusCode() int
		IsCredentialScoped() bool
	})
	if !ok || status.StatusCode() != 429 || !status.IsCredentialScoped() {
		t.Fatalf("quota error=%v", err)
	}
}

func TestCopilotFormatUsesSelectedAccountAlias(t *testing.T) {
	e, auth := newTestCopilot()
	r := registry.GetGlobalRegistry()
	r.RegisterClient(auth.ID, copilot.Provider, []*registry.ModelInfo{{ID: "team/public-model", UpstreamEndpoint: "/responses"}})
	otherID := auth.ID + "-other"
	r.RegisterClient(otherID, copilot.Provider, []*registry.ModelInfo{{ID: "team/public-model", UpstreamEndpoint: "/chat/completions"}})
	t.Cleanup(func() { r.UnregisterClient(auth.ID); r.UnregisterClient(otherID) })
	req := exec.Request{Model: "upstream-model(high)"}
	opts := exec.Options{Metadata: map[string]any{
		exec.SelectedAuthMetadataKey:   auth.ID,
		exec.RequestedModelMetadataKey: "team/public-model(high)",
	}}
	if got := e.RequestToFormat(req, opts); got != translator.FormatOpenAIResponse {
		t.Fatalf("aliased account format = %q, want Responses", got)
	}
	opts.Metadata[exec.SelectedAuthMetadataKey] = otherID
	if got := e.RequestToFormat(req, opts); got != translator.FormatOpenAI {
		t.Fatalf("other account format = %q, want Chat Completions", got)
	}
}
