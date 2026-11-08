package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func codexSearchDualPartitionAuth() *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       "codex-dual-partition",
		Provider: "codex",
		Metadata: map[string]any{
			"type":          "codex",
			"access_token":  "original-oauth-token",
			"refresh_token": "original-refresh-token",
			"account_id":    "original-account",
			"use_siwc":      true,
			"siwc": map[string]any{
				"access_token":  "nested-siwc-token",
				"refresh_token": "nested-siwc-refresh-token",
				"client_id":     "oaiapp_test",
			},
		},
	}
}

func TestCodexAlphaSearchUsesOriginalOAuthAtTransport(t *testing.T) {
	auth := codexSearchDualPartitionAuth()
	auth.EnsureIndex()
	before := auth.Clone()
	t.Cleanup(func() {
		if !reflect.DeepEqual(auth, before) {
			t.Error("Search or Responses mutated the shared auth")
		}
	})
	executor := NewCodexExecutor(&config.Config{})
	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	var requests []*http.Request
	ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		requests = append(requests, req.Clone(req.Context()))
		contentType, body := "application/json", `{"results":[]}`
		if req.URL.Path == "/v1/responses" {
			contentType = "text/event-stream"
			body = "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_siwc\",\"status\":\"completed\",\"model\":\"gpt-6.1-sol\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {contentType}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	}))

	req, errNewHttpRequest := manager.NewHttpRequest(ctx, auth, http.MethodPost,
		"https://chatgpt.com/backend-api/codex/alpha/search", []byte(`{"commands":{"search_query":[{"q":"OpenAI"}]}}`), nil)
	if errNewHttpRequest != nil {
		t.Fatalf("NewHttpRequest: %v", errNewHttpRequest)
	}
	resp, errHttpRequest := manager.HttpRequest(ctx, auth, req)
	if errHttpRequest != nil {
		t.Fatalf("HttpRequest: %v", errHttpRequest)
	}
	if errClose := resp.Body.Close(); errClose != nil {
		t.Errorf("close search response body: %v", errClose)
	}
	if len(requests) != 1 {
		t.Fatalf("Search transport calls = %d, want 1", len(requests))
	}
	if got := requests[0].Header.Get("Authorization"); got != "Bearer original-oauth-token" {
		t.Errorf("final transport Authorization = %q, want Bearer original-oauth-token", got)
	}

	_, errExecute := executor.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "gpt-6.1-sol",
		Payload: []byte(`{"model":"gpt-6.1-sol","input":[]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
	if errExecute != nil {
		t.Fatalf("Responses Execute after Search: %v", errExecute)
	}
	if len(requests) != 2 {
		t.Fatalf("Search plus Responses transport calls = %d, want 2", len(requests))
	}
	if got := requests[1].URL.String(); got != "https://api.openai.com/v1/responses" {
		t.Errorf("Responses URL = %q, want https://api.openai.com/v1/responses", got)
	}
	if got := requests[1].Header.Get("Authorization"); got != "Bearer nested-siwc-token" {
		t.Errorf("Responses Authorization = %q, want Bearer nested-siwc-token", got)
	}
	if got := requests[1].Header.Get("Chatgpt-Account-Id"); got != "" {
		t.Errorf("SIWC Responses Chatgpt-Account-Id = %q, want empty", got)
	}
}

func TestCodexAlphaSearchCredentialPartitionBoundaries(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		configure func(*cliproxyauth.Auth)
		wantError bool
	}{
		{
			name: "missing_original_oauth",
			configure: func(auth *cliproxyauth.Auth) {
				delete(auth.Metadata, "access_token")
			},
			wantError: true,
		},
		{
			name: "blank_original_oauth",
			configure: func(auth *cliproxyauth.Auth) {
				auth.Metadata["access_token"] = " \t "
			},
			wantError: true,
		},
		{
			name: "missing_nested_siwc",
			configure: func(auth *cliproxyauth.Auth) {
				delete(auth.Metadata, "siwc")
			},
		},
		{
			name: "missing_nested_siwc_access_token",
			configure: func(auth *cliproxyauth.Auth) {
				delete(auth.Metadata["siwc"].(map[string]any), "access_token")
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			auth := codexSearchDualPartitionAuth()
			testCase.configure(auth)
			before := auth.Clone()
			t.Cleanup(func() {
				if !reflect.DeepEqual(auth, before) {
					t.Error("Search mutated the shared auth")
				}
			})
			manager := cliproxyauth.NewManager(nil, nil, nil)
			manager.RegisterExecutor(NewCodexExecutor(&config.Config{}))
			var requests []*http.Request
			ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				requests = append(requests, req.Clone(req.Context()))
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"results":[]}`)),
					Request:    req,
				}, nil
			}))

			req, errRequest := manager.NewHttpRequest(ctx, auth, http.MethodPost,
				"https://chatgpt.com/backend-api/codex/alpha/search", []byte(`{}`), nil)
			if errRequest == nil {
				var resp *http.Response
				resp, errRequest = manager.HttpRequest(ctx, auth, req)
				if resp != nil {
					if errClose := resp.Body.Close(); errClose != nil {
						t.Errorf("close response body: %v", errClose)
					}
				}
			}
			if testCase.wantError {
				if errRequest == nil {
					t.Error("Search without original OAuth succeeded, want credential error")
				} else {
					var status interface{ StatusCode() int }
					if !errors.As(errRequest, &status) || status.StatusCode() != http.StatusUnauthorized {
						t.Errorf("credential error = %v, want HTTP 401", errRequest)
					}
					if !strings.Contains(errRequest.Error(), "original OAuth") {
						t.Errorf("credential error = %v, want an explicit original OAuth error", errRequest)
					}
				}
				if len(requests) != 0 {
					t.Errorf("Search without original OAuth reached transport with Authorization = %q", requests[0].Header.Get("Authorization"))
				}
				return
			}
			if errRequest != nil {
				t.Fatalf("Search with original OAuth must not require nested SIWC: %v", errRequest)
			}
			if len(requests) != 1 {
				t.Fatalf("Search transport calls = %d, want 1", len(requests))
			}
			if got := requests[0].Header.Get("Authorization"); got != "Bearer original-oauth-token" {
				t.Errorf("final transport Authorization = %q, want Bearer original-oauth-token", got)
			}
		})
	}
}

func TestCodexAlphaSearchCredentialSelectionScope(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		targetURL   string
		disableSIWC bool
		omitSIWC    bool
		attributes  map[string]string
		wantBearer  string
	}{
		{
			name:       "other_scheme",
			targetURL:  "http://chatgpt.com/backend-api/codex/alpha/search",
			wantBearer: "Bearer nested-siwc-token",
		},
		{
			name:       "other_host",
			targetURL:  "https://example.com/backend-api/codex/alpha/search",
			wantBearer: "Bearer nested-siwc-token",
		},
		{
			name:       "other_path",
			targetURL:  "https://chatgpt.com/backend-api/codex/responses",
			wantBearer: "Bearer nested-siwc-token",
		},
		{
			name:        "ordinary_oauth",
			disableSIWC: true,
			wantBearer:  "Bearer original-oauth-token",
		},
		{
			name:        "api_key",
			disableSIWC: true,
			attributes:  map[string]string{cliproxyauth.AttributeAPIKey: "api-key-token"},
			wantBearer:  "Bearer api-key-token",
		},
		{
			name:       "custom_authorization",
			attributes: map[string]string{"header:Authorization": "Bearer custom-token"},
			wantBearer: "Bearer custom-token",
		},
		{
			name:       "custom_authorization_without_nested_siwc",
			omitSIWC:   true,
			attributes: map[string]string{"header:Authorization": "Bearer custom-token"},
			wantBearer: "Bearer custom-token",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			auth := codexSearchDualPartitionAuth()
			auth.Attributes = testCase.attributes
			if testCase.disableSIWC {
				auth.Metadata["use_siwc"] = false
			}
			if testCase.omitSIWC {
				delete(auth.Metadata, "siwc")
			}
			before := auth.Clone()
			t.Cleanup(func() {
				if !reflect.DeepEqual(auth, before) {
					t.Error("request preparation mutated the shared auth")
				}
			})
			targetURL := testCase.targetURL
			if targetURL == "" {
				targetURL = "https://chatgpt.com/backend-api/codex/alpha/search"
			}
			manager := cliproxyauth.NewManager(nil, nil, nil)
			manager.RegisterExecutor(NewCodexExecutor(&config.Config{}))
			var requests []*http.Request
			ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				requests = append(requests, req.Clone(req.Context()))
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{}`)),
					Request:    req,
				}, nil
			}))
			req, errNewHttpRequest := manager.NewHttpRequest(ctx, auth, http.MethodPost, targetURL, []byte(`{}`), nil)
			if errNewHttpRequest != nil {
				t.Fatalf("NewHttpRequest: %v", errNewHttpRequest)
			}
			resp, errHttpRequest := manager.HttpRequest(ctx, auth, req)
			if errHttpRequest != nil {
				t.Fatalf("HttpRequest: %v", errHttpRequest)
			}
			if errClose := resp.Body.Close(); errClose != nil {
				t.Errorf("close response body: %v", errClose)
			}
			if len(requests) != 1 {
				t.Fatalf("transport calls = %d, want 1", len(requests))
			}
			if got := requests[0].Header.Get("Authorization"); got != testCase.wantBearer {
				t.Errorf("final transport Authorization = %q, want %q", got, testCase.wantBearer)
			}
		})
	}
}
