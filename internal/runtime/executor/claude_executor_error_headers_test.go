package executor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestClassifyClaudeUpstreamError_PreservesHeadersAndClassification(t *testing.T) {
	for _, tc := range []struct {
		name, message                string
		status                       int
		unified, credential, request bool
	}{
		{name: "bad_request", status: http.StatusBadRequest, message: "invalid request"},
		{name: "unauthorized", status: http.StatusUnauthorized, message: "unauthorized"},
		{name: "model_rate_limit", status: http.StatusTooManyRequests, message: "model usage window rejected"},
		{name: "credential_rate_limit", status: http.StatusTooManyRequests, message: "shared usage window rejected", unified: true, credential: true},
		{name: "entitlement", status: http.StatusTooManyRequests, message: "Usage credits are required for fast mode.", request: true},
		{name: "server_error", status: http.StatusInternalServerError, message: "server error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers := http.Header{
				"Request-Id": {"synthetic-request-id"}, "Retry-After": {"120"},
				"X-Should-Retry": {"false"}, "aNtHrOpIc-RaTeLiMiT-Test": {"one", "two"},
			}
			if tc.unified {
				headers.Set("Anthropic-Ratelimit-Unified-5h-Status", "rejected")
			}
			wantHeaders := headers.Clone()
			body := []byte(fmt.Sprintf(`{"type":"error","error":{"type":"synthetic_error","message":%q}}`, tc.message))
			err := classifyClaudeUpstreamError(tc.status, headers, body)
			// Match the SDK's direct assertion, not just errors.As traversal.
			carrier, ok := err.(interface{ Headers() http.Header })
			if !ok {
				t.Fatalf("%T does not directly expose error response headers", err)
			}
			var status cliproxyexecutor.StatusError
			if !errors.As(err, &status) || status.StatusCode() != tc.status || err.Error() != string(body) {
				t.Fatalf("status/body changed: %v", err)
			}
			var concrete statusErr
			if !errors.As(err, &concrete) || concrete.StatusCode() != tc.status {
				t.Fatal("header wrapper hid concrete status used by replay cleanup")
			}
			var scoped interface{ IsCredentialScoped() bool }
			if !errors.As(err, &scoped) || scoped.IsCredentialScoped() != tc.credential {
				t.Fatalf("credential scope changed for %T", err)
			}
			requestScoped, hasRequestScope := err.(cliproxyexecutor.RequestScopedError)
			if (hasRequestScope && requestScoped.IsRequestScoped()) != tc.request {
				t.Fatalf("request scope changed for %T", err)
			}
			var retry interface{ RetryAfter() *time.Duration }
			if !errors.As(err, &retry) || retry.RetryAfter() == nil || *retry.RetryAfter() < 120*time.Second || *retry.RetryAfter() > 150*time.Second {
				t.Fatalf("retry duration was lost: %v", err)
			}
			// Neither later transport mutations nor a caller changing a returned
			// Header map may mutate the retained error snapshot.
			headers.Set("Request-Id", "mutated-source")
			headers["aNtHrOpIc-RaTeLiMiT-Test"][0] = "mutated-source-slice"
			first := carrier.Headers()
			if !reflect.DeepEqual(first, wantHeaders) {
				t.Fatalf("upstream header snapshot changed: got %v want %v", first, wantHeaders)
			}
			first.Set("Request-Id", "mutated-return")
			first["aNtHrOpIc-RaTeLiMiT-Test"][0] = "mutated-return-slice"
			if got := carrier.Headers(); !reflect.DeepEqual(got, wantHeaders) {
				t.Fatalf("returned headers alias retained snapshot: got %v want %v", got, wantHeaders)
			}
		})
	}
}

func TestClaudeExecutor_UpstreamErrorHeaders_AllInferencePaths(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusTooManyRequests} {
		for _, operation := range []string{"messages", "stream", "count_tokens"} {
			t.Run(fmt.Sprintf("%s_%d", operation, status), func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					wantPath := "/v1/messages"
					if operation == "count_tokens" {
						wantPath += "/count_tokens"
					}
					if r.Method != http.MethodPost || r.URL.Path != wantPath {
						t.Errorf("unexpected synthetic request: %s %s", r.Method, r.URL.Path)
					}
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("Request-Id", "synthetic-executor-request")
					w.Header().Set("Retry-After", "120")
					w.Header().Set("X-Should-Retry", "false")
					w.Header().Set("Anthropic-Ratelimit-Unified-Status", "allowed")
					w.WriteHeader(status)
					_, _ = w.Write([]byte(`{"type":"error","error":{"type":"synthetic_error","message":"synthetic upstream refusal"}}`))
				}))
				defer server.Close()
				executor := NewClaudeExecutor(&config.Config{})
				auth := &cliproxyauth.Auth{ID: "error-headers-" + operation, Provider: "claude", Attributes: map[string]string{"api_key": "synthetic-not-a-key", "base_url": server.URL}}
				request := cliproxyexecutor.Request{Model: "claude-sonnet-4-5", Payload: []byte(`{"model":"claude-sonnet-4-5","max_tokens":32,"messages":[{"role":"user","content":"synthetic request"}]}`)}
				options := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
				var err error
				switch operation {
				case "messages":
					_, err = executor.Execute(context.Background(), auth, request, options)
				case "stream":
					_, err = executor.ExecuteStream(context.Background(), auth, request, options)
				case "count_tokens":
					// Invoke the upstream path directly: a custom base URL otherwise
					// intentionally selects the local estimator before making HTTP.
					_, err = executor.countTokensUpstream(context.Background(), auth, request, options)
				}
				if err == nil {
					t.Fatal("expected synthetic upstream refusal")
				}
				carrier, ok := err.(interface{ Headers() http.Header })
				if !ok {
					t.Fatalf("%s lost response headers on %T", operation, err)
				}
				for key, want := range map[string]string{"Request-Id": "synthetic-executor-request", "Retry-After": "120", "X-Should-Retry": "false", "Anthropic-Ratelimit-Unified-Status": "allowed"} {
					if got := carrier.Headers().Get(key); got != want {
						t.Errorf("%s = %q, want %q", key, got, want)
					}
				}
				if requests.Load() != 1 {
					t.Fatalf("unexpected retry/fallback: requests=%d", requests.Load())
				}
			})
		}
	}
}
