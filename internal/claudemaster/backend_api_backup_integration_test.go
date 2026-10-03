package claudemaster

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

const backendAPIBackupSyntheticKey = "sk-ant-api03-synthetic-final-backup-only"

type backendAPIBackupWireRequest struct {
	authID, authKind string
	method, target   string
	header           http.Header
	body             []byte
}

// No request ever delegates to a network transport, even for unexpected URLs.
type backendAPIBackupIntegrationTransport struct {
	mu       sync.Mutex
	requests []backendAPIBackupWireRequest
	path     string
	response string
	chunks   [][]byte
}

type backendAPIBackupIntegrationRoundTripper struct {
	transport        *backendAPIBackupIntegrationTransport
	authID, authKind string
}

func (rt *backendAPIBackupIntegrationTransport) RoundTripperFor(auth *coreauth.Auth) http.RoundTripper {
	return backendAPIBackupIntegrationRoundTripper{transport: rt, authID: auth.ID, authKind: auth.AuthKind()}
}

func (rt backendAPIBackupIntegrationRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	var body []byte
	if request.Body != nil {
		var err error
		body, err = io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		if err := request.Body.Close(); err != nil {
			return nil, err
		}
	}
	rt.transport.mu.Lock()
	rt.transport.requests = append(rt.transport.requests, backendAPIBackupWireRequest{
		authID: rt.authID, authKind: rt.authKind, method: request.Method,
		target: request.URL.String(), header: request.Header.Clone(), body: bytes.Clone(body),
	})
	rt.transport.mu.Unlock()

	headers := http.Header{"Content-Type": {"application/json"}}
	status := http.StatusOK
	response := rt.transport.response
	var reader io.Reader
	switch {
	case request.Method == http.MethodGet && request.URL.String() == ClaudeOAuthUsageEndpoint:
		if rt.authKind != coreauth.AuthKindOAuth {
			return nil, errors.New("synthetic usage endpoint rejects non-OAuth credentials")
		}
		response = `{"seven_day":{"utilization":0,"resets_at":"2099-01-01T00:00:00Z"}}`
	case request.Method == http.MethodPost && request.URL.String() == "https://api.anthropic.com"+rt.transport.path+"?beta=true":
		if rt.authKind == coreauth.AuthKindOAuth {
			status = http.StatusTooManyRequests
			headers.Set("Anthropic-Ratelimit-Unified-Status", "rejected")
			headers.Set("Anthropic-Ratelimit-Unified-7d-Status", "rejected")
			headers.Set("Anthropic-Ratelimit-Unified-7d-Utilization", "1")
			headers.Set("Anthropic-Ratelimit-Unified-7d-Reset", "4070908800")
			headers.Set("Retry-After", "60")
			response = `{"type":"error","error":{"type":"rate_limit_error","message":"synthetic subscription quota exhausted"}}`
		} else if rt.authKind == coreauth.AuthKindAPIKey {
			headers["X-Anthropic-Future-Header"] = []string{"one", "two"}
			if rt.transport.chunks != nil {
				headers.Set("Content-Type", "text/event-stream")
				reader = &backendAPIBackupChunkReader{chunks: rt.transport.chunks}
			}
		} else {
			return nil, errors.New("synthetic transport rejects unexpected credential kind")
		}
	default:
		return nil, errors.New("synthetic transport rejects unexpected upstream route")
	}
	if reader == nil {
		reader = strings.NewReader(response)
	}
	return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(reader), Request: request}, nil
}

// Split inside SSE fields and preserve CRLF to catch event reconstruction or
// early termination on either an unknown event or a late provider error.
type backendAPIBackupChunkReader struct {
	chunks [][]byte
	index  int
	offset int
}

func (r *backendAPIBackupChunkReader) Read(dst []byte) (int, error) {
	if r.index == len(r.chunks) {
		return 0, io.EOF
	}
	count := copy(dst, r.chunks[r.index][r.offset:])
	r.offset += count
	if r.offset == len(r.chunks[r.index]) {
		r.index++
		r.offset = 0
	}
	return count, nil
}

type backendAPIBackupChunkRecorder struct {
	*httptest.ResponseRecorder
	writes [][]byte
}

func (w *backendAPIBackupChunkRecorder) Write(payload []byte) (int, error) {
	w.writes = append(w.writes, bytes.Clone(payload))
	return w.ResponseRecorder.Write(payload)
}

func TestBackendAPIBackupRealExecutorQuotaMappingAndStreaming(t *testing.T) {
	for _, tc := range []struct {
		name, path, response string
		stream               bool
	}{
		{name: "messages", path: "/v1/messages", response: `{"type":"message","content":[{"type":"text","text":"synthetic API answer"}]}`},
		{name: "stream", path: "/v1/messages", stream: true},
		{name: "count_tokens", path: "/v1/messages/count_tokens", response: `{"input_tokens":17}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const incomingModel, targetModel = "claude-synthetic-incoming", "claude-synthetic-target"
			credentials := make([]BackendCredential, 0, 2)
			tokens, accounts := make([]string, 0, 2), make([]string, 0, 2)
			for index := 1; index <= 2; index++ {
				token := fmt.Sprintf("sk-ant-oat01-synthetic-profile-%d", index)
				account := fmt.Sprintf("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaa%d", index)
				opts := writeSyntheticBackendCredential(t, canonicalTestTempDir(t), "selected.json", map[string]any{
					"type": "claude", "access_token": token, "account_uuid": account,
					"claude_device_ids": []string{integrationClaudeDevice},
				})
				credentials = append(credentials, BackendCredential{AuthDir: opts.AuthDir, Provider: "claude", AuthID: opts.AuthID})
				tokens, accounts = append(tokens, token), append(accounts, account)
			}
			var quotaMu sync.Mutex
			var quotaIDs []string
			badQuotaCall := false
			backend, err := NewBackendSeries(t.Context(), BackendSeriesOptions{
				Credentials: credentials, BackupAPIKey: backendAPIBackupSyntheticKey,
				ModelMap: map[string]string{incomingModel: targetModel, targetModel: "must-not-chain"},
				QuotaRequest: func(_ context.Context, auth *coreauth.Auth, request *http.Request) (*http.Response, error) {
					quotaMu.Lock()
					quotaIDs = append(quotaIDs, auth.ID)
					badQuotaCall = badQuotaCall || auth.AuthKind() != coreauth.AuthKindOAuth || request.Method != http.MethodGet || request.URL.String() != ClaudeOAuthUsageEndpoint
					quotaMu.Unlock()
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"seven_day":{"utilization":0,"resets_at":"2099-01-01T00:00:00Z"}}`))}, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = backend.Close() })
			if len(backend.authIDs) != 3 {
				t.Fatal("constructor did not register subscriptions plus one final backup")
			}
			quotaMu.Lock()
			validStartupQuota := !badQuotaCall && len(quotaIDs) == 2 && quotaIDs[0] != quotaIDs[1]
			quotaMu.Unlock()
			if !validStartupQuota {
				t.Fatal("startup usage queried something other than each distinct OAuth subscription")
			}

			backup, ok := backend.manager.GetByID(backend.authIDs[2])
			if !ok || backup.AuthKind() != coreauth.AuthKindAPIKey || backup.Attributes[coreauth.AttributeAPIKey] != backendAPIBackupSyntheticKey || backup.Attributes[coreauth.AttributeRuntimeOnly] != "true" || backup.FileName != "" {
				t.Fatal("constructor did not keep the API key exclusively in a runtime-only credential")
			}
			if _, err := backend.store.Save(t.Context(), backup); err == nil {
				t.Fatal("API-key backup can be saved into an OAuth profile")
			}

			cfg := backendConfig(credentials[0].AuthDir)
			// Production forces direct egress; this fixture permits only the
			// injected no-network transport for the actual native executor.
			cfg.ProxyURL, cfg.MaxRetryCredentials = "", len(backend.authIDs)
			executor := runtimeexecutor.NewClaudeExecutor(cfg)
			for _, authID := range backend.authIDs {
				auth, ok := backend.manager.GetByID(authID)
				if !ok || executor.ShouldPrepareRequestAuth(auth) {
					t.Fatal("fixture would need live OAuth identity preparation")
				}
			}
			transport := &backendAPIBackupIntegrationTransport{path: tc.path, response: tc.response}
			if tc.stream {
				transport.chunks = [][]byte{
					[]byte(": native keepalive\r\n\r\nevent: message_st"),
					[]byte("art\r\ndata: {\"type\":\"message_start\"}\r\n\r\n"),
					[]byte("event: future_native_event\ndata: {\"opaque\":true}\n\n"),
					[]byte("event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\"}}\n\n"),
					[]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n: bytes after error\n\n"),
				}
			}
			backend.manager.SetConfig(cfg)
			backend.manager.SetRoundTripperProvider(transport)
			backend.manager.RegisterExecutor(executor)
			// Even an overbroad caller supplying all runtime IDs must never query
			// the subscription usage endpoint with the API credential.
			loadBackendWeeklyQuotas(t.Context(), backend.manager, backend.seriesSelector, backend.authIDs, func(ctx context.Context, auth *coreauth.Auth, request *http.Request) (*http.Response, error) {
				// Manager.HttpRequest delegates directly to the executor rather
				// than injecting its transport provider as inference does.
				ctx = context.WithValue(ctx, "cliproxy.roundtripper", transport.RoundTripperFor(auth))
				return backend.manager.HttpRequest(ctx, auth, request)
			})
			body := fmt.Sprintf(`{"model":%q,"max_tokens":64,"stream":%t,"metadata":{"user_id":%q,"keep":"native-value"},"messages":[{"role":"user","content":"synthetic hello"}],"tools":[{"name":"Bash","input_schema":%s}]}`, incomingModel, tc.stream, integrationMasterCanary, integrationToolSchema)
			request := httptest.NewRequest(http.MethodPost, "https://api.anthropic.com"+tc.path, strings.NewReader(body))
			request.Header.Set("Authorization", integrationMasterCanary)
			request.Header.Set("X-Api-Key", integrationMasterCanary)
			request.Header.Set("X-Account-Id", integrationMasterCanary)
			request.Header.Set("X-Claude-Code-Session-Id", integrationClaudeSession)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Anthropic-Version", "2023-06-01")
			request.Header["Anthropic-Beta"] = []string{"oauth-2025-04-20,native-feature-beta", "future-beta"}
			request = request.WithContext(coreexecutor.WithNativeClaudeProtocolHeaders(request.Context(), request.Header))
			response := &backendAPIBackupChunkRecorder{ResponseRecorder: httptest.NewRecorder()}
			backend.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("quota fallback did not reach API backup: status=%d body=%s", response.Code, response.Body.String())
			}
			if tc.stream {
				if !response.Flushed || !reflect.DeepEqual(response.writes, transport.chunks) || !bytes.Equal(response.Body.Bytes(), bytes.Join(transport.chunks, nil)) {
					t.Fatal("API backup streaming altered chunks, reconstructed SSE, or stopped at a late error")
				}
			} else if response.Body.String() != tc.response {
				t.Fatal("API backup response was not forwarded verbatim")
			}
			if !reflect.DeepEqual(response.Header().Values("X-Anthropic-Future-Header"), []string{"one", "two"}) {
				t.Fatal("API backup lost duplicate end-to-end headers")
			}

			transport.mu.Lock()
			requests := append([]backendAPIBackupWireRequest(nil), transport.requests...)
			transport.mu.Unlock()
			var inference []backendAPIBackupWireRequest
			usageCount := 0
			for _, upstream := range requests {
				if upstream.method == http.MethodGet {
					usageCount++
					if upstream.target != ClaudeOAuthUsageEndpoint || upstream.authKind != coreauth.AuthKindOAuth || !strings.HasPrefix(upstream.header.Get("Authorization"), "Bearer sk-ant-oat01-synthetic-profile-") || upstream.header.Get("X-Api-Key") != "" {
						t.Fatal("usage endpoint received an API key or incorrect credential")
					}
				} else {
					inference = append(inference, upstream)
				}
			}
			if usageCount != 2 || len(inference) != 3 {
				t.Fatalf("unexpected usage or inference retry count: usage=%d inference=%d", usageCount, len(inference))
			}
			for index, upstream := range inference {
				if upstream.authID != backend.authIDs[index] || gjson.GetBytes(upstream.body, "model").String() != targetModel || upstream.header.Get("X-Claude-Code-Session-Id") != integrationClaudeSession || gjson.GetBytes(upstream.body, "metadata.keep").String() != "native-value" || gjson.GetBytes(upstream.body, "tools.0.name").String() != "Bash" {
					t.Fatal("quota retry changed the chosen model, order, conversation, or native content")
				}
				if strings.Contains(string(upstream.body), integrationMasterCanary) || upstream.header.Get("X-Account-Id") != "" {
					t.Fatal("master account identity leaked into a retried request")
				}
				if index < 2 {
					if upstream.authKind != coreauth.AuthKindOAuth || upstream.header.Get("Authorization") != "Bearer "+tokens[index] || upstream.header.Get("X-Api-Key") != "" {
						t.Fatal("subscription retry did not use only its selected OAuth bearer")
					}
					if tc.path != "/v1/messages/count_tokens" && gjson.Get(gjson.GetBytes(upstream.body, "metadata.user_id").String(), "account_uuid").String() != accounts[index] {
						t.Fatal("subscription retry retained another account's metadata identity")
					}
					if !reflect.DeepEqual(upstream.header.Values("Anthropic-Beta"), []string{"oauth-2025-04-20,native-feature-beta", "future-beta"}) {
						t.Fatal("subscription retry lost native beta values")
					}
				} else if upstream.authKind != coreauth.AuthKindAPIKey || upstream.header.Get("X-Api-Key") != backendAPIBackupSyntheticKey || upstream.header.Get("Authorization") != "" || gjson.GetBytes(upstream.body, "metadata.user_id").Exists() || !reflect.DeepEqual(upstream.header.Values("Anthropic-Beta"), []string{"native-feature-beta", "future-beta"}) {
					t.Fatal("API backup retained OAuth authorization, account identity, or OAuth-only beta")
				}
			}
			if err := backend.Close(); err != nil {
				t.Fatal(err)
			}
			for _, credential := range credentials {
				entries, err := os.ReadDir(credential.AuthDir)
				if err != nil || len(entries) != 1 || entries[0].Name() != credential.AuthID {
					t.Fatal("API-key backup created an authentication file in a subscription profile")
				}
				data, err := os.ReadFile(filepath.Join(credential.AuthDir, credential.AuthID))
				if err != nil || bytes.Contains(data, []byte(backendAPIBackupSyntheticKey)) || bytes.Contains(data, []byte(backup.ID)) {
					t.Fatal("runtime API key or identity was persisted in a subscription credential")
				}
			}
		})
	}
}
