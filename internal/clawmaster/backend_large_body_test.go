package clawmaster

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type inferenceTestFill byte

func (fill inferenceTestFill) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(fill)
	}
	return len(p), nil
}

type largeClaudeTestTransport func(*http.Request) (*http.Response, error)

func (transport largeClaudeTestTransport) RoundTripperFor(*coreauth.Auth) http.RoundTripper {
	return transport
}

func (transport largeClaudeTestTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

// A subscription retry must start from the same original bytes. Exercise the
// real OAuth executor, including identity replacement and signing, without
// retaining a second body-sized copy in the test.
type immutableInferenceExecutor struct {
	*runtimeexecutor.ClaudeExecutor
}

func (e immutableInferenceExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	before := sha256.Sum256(req.Payload)
	response, err := e.ClaudeExecutor.Execute(ctx, auth, req, opts)
	if sha256.Sum256(req.Payload) != before {
		return coreexecutor.Response{}, errors.New("native execution changed the original request needed for fallback")
	}
	return response, err
}

func (e immutableInferenceExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	before := sha256.Sum256(req.Payload)
	response, err := e.ClaudeExecutor.ExecuteStream(ctx, auth, req, opts)
	if sha256.Sum256(req.Payload) != before {
		return nil, errors.New("native stream changed the original request needed for fallback")
	}
	return response, err
}

func (e immutableInferenceExecutor) CountTokens(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	before := sha256.Sum256(req.Payload)
	response, err := e.ClaudeExecutor.CountTokens(ctx, auth, req, opts)
	if sha256.Sum256(req.Payload) != before {
		return coreexecutor.Response{}, errors.New("native token count changed the original request")
	}
	return response, err
}

func TestBackendForwardsLargeInferenceBodies(t *testing.T) {
	for _, tt := range []struct {
		name, path string
		size       int
		chunked    bool
		stream     bool
	}{
		{name: "desktop", path: "/v1/messages", size: 34_232_216, stream: true},
		{name: "provider_limit", path: "/v1/messages", size: 128_000_000},
		{name: "chunked_messages", path: "/v1/messages", size: 128_000_000, chunked: true, stream: true},
		{name: "chunked_count", path: "/v1/messages/count_tokens", size: 128_000_000, chunked: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			model := "claude-opus-4-8"
			opts := writeSyntheticBackendCredential(t, canonicalTestTempDir(t), "large-test.json", map[string]any{
				"type": "claude", "access_token": integrationClaudeToken,
				"account_uuid": integrationClaudeAccount, "claude_device_ids": []string{integrationClaudeDevice},
			})
			opts.Provider, opts.Model = "claude", model
			store, credential, err := loadBackendCredential(t.Context(), opts)
			if err != nil {
				t.Fatal(err)
			}
			cfg := backendConfig(opts.AuthDir)
			cfg.ProxyURL = ""
			executor := runtimeexecutor.NewClaudeExecutor(cfg)
			if executor.ShouldPrepareRequestAuth(credential) {
				t.Fatal("test fixture would need a live OAuth profile")
			}
			count := tt.path == "/v1/messages/count_tokens"
			var sent bool
			transport := largeClaudeTestTransport(func(r *http.Request) (*http.Response, error) {
				if sent || r.URL.Host != "api.anthropic.com" || r.URL.Path != tt.path {
					return nil, errors.New("unexpected upstream request")
				}
				sent = true
				if r.Header.Get("Authorization") != "Bearer "+integrationClaudeToken {
					return nil, errors.New("selected credential was not used")
				}
				defer r.Body.Close()
				first := make([]byte, 512)
				if _, errRead := io.ReadFull(r.Body, first); errRead != nil {
					return nil, errRead
				}
				header := string(first)
				if !count && (strings.Contains(header, "cch=00000;") || !strings.Contains(header, integrationClaudeAccount)) {
					return nil, errors.New("native billing signature or selected OAuth identity not applied")
				}
				if count && strings.Contains(header, "user_id") {
					return nil, errors.New("count route retained credential identity")
				}
				n, errRead := io.Copy(io.Discard, r.Body)
				if errRead != nil || n+int64(len(first)) != r.ContentLength || n < int64(tt.size)-1024 {
					return nil, errors.New("large upstream body was truncated")
				}
				reply := integrationClaudeReply
				contentType := "application/json"
				if tt.stream {
					reply, contentType = integrationClaudeStream(), "text/event-stream"
				}
				if count {
					reply = `{"input_tokens":17}`
				}
				return &http.Response{
					StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(reply)),
					Header: http.Header{"Content-Type": {contentType}}, Request: r,
				}, nil
			})
			manager := coreauth.NewManager(store, &backendSelector{authID: opts.AuthID, provider: opts.Provider}, nil)
			manager.SetConfig(cfg)
			manager.SetRetryConfig(0, 0, 1)
			manager.SetRoundTripperProvider(transport)
			manager.RegisterExecutor(immutableInferenceExecutor{executor})
			registry.GetGlobalRegistry().RegisterClient(opts.AuthID, opts.Provider, []*registry.ModelInfo{{ID: backendAuthSelectionModel}})
			t.Cleanup(func() {
				manager.CloseExecutionSession(coreauth.CloseAllExecutionSessionsID)
				store.seal()
				registry.GetGlobalRegistry().UnregisterClient(opts.AuthID)
			})
			if _, errRegister := manager.Register(coreauth.WithSkipPersist(t.Context()), credential); errRegister != nil {
				t.Fatal(errRegister)
			}
			opts.UseRequestModel = true
			handler := newBackendHandler(t.Context(), opts, handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
			system := `"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.220.test; cc_entrypoint=sdk-cli; cch=00000;"}],`
			if count {
				system = ""
			}
			prefix := fmt.Sprintf(`{"model":%q,"max_tokens":64,"stream":%t,%s"metadata":{"user_id":%q},"messages":[{"role":"user","content":"`, model, tt.stream, system, integrationMasterCanary)
			suffix := `"}]}`
			body := io.MultiReader(strings.NewReader(prefix),
				io.LimitReader(inferenceTestFill('a'), int64(tt.size-len(prefix)-len(suffix))),
				strings.NewReader(suffix))
			request := httptest.NewRequest(http.MethodPost, tt.path, body)
			if !tt.chunked {
				request.ContentLength = int64(tt.size)
			}
			for key, values := range nativeProtocolFixture() {
				request.Header[key] = append([]string(nil), values...)
			}
			request.Header.Set("X-Claude-Code-Session-Id", integrationClaudeSession)
			request = request.WithContext(coreexecutor.WithNativeClaudeProtocolHeaders(request.Context(), request.Header))
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK || !sent || tt.stream && !strings.Contains(recorder.Body.String(), "message_stop") {
				t.Fatalf("request (bytes=%d, declared length=%d) status=%d, sent=%t: %s",
					tt.size, request.ContentLength, recorder.Code, sent, recorder.Body.String())
			}
		})
	}
}

type pausedInferenceBody struct {
	started, release chan struct{}
}

func (body pausedInferenceBody) Read([]byte) (int, error) {
	close(body.started)
	<-body.release
	return 0, io.EOF
}

type largeUploadRecorder struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
}

func (recorder *largeUploadRecorder) SetReadDeadline(deadline time.Time) error {
	recorder.deadlines = append(recorder.deadlines, deadline)
	return nil
}

func TestBackendLargeBodyAdmission(t *testing.T) {
	handler := newBackendHandler(t.Context(), BackendOptions{UseRequestModel: true}, nil)
	body := pausedInferenceBody{started: make(chan struct{}), release: make(chan struct{})}
	first := httptest.NewRequest(http.MethodPost, "/v1/messages", body)
	first.ContentLength = 34_232_216
	done := make(chan struct{})
	var released bool
	defer func() {
		if !released {
			close(body.release)
		}
		<-done
	}()
	firstResponse := &largeUploadRecorder{ResponseRecorder: httptest.NewRecorder()}
	go func() {
		defer close(done)
		handler.ServeHTTP(firstResponse, first)
	}()
	<-body.started

	for _, tt := range []struct {
		name, path   string
		size         int64
		chunkedBytes int64
		want         int
		errorType    string
	}{
		{name: "other_large", path: "/v1/messages/count_tokens", size: 34_232_216, want: http.StatusServiceUnavailable, errorType: "overloaded_error"},
		{name: "another_chunked_large", path: "/v1/messages", chunkedBytes: (32 << 20) + 1, want: http.StatusServiceUnavailable, errorType: "overloaded_error"},
		{name: "oversize_rejected_before_admission", path: "/v1/messages", size: 128_000_001, want: http.StatusRequestEntityTooLarge, errorType: "request_too_large"},
		{name: "small_still_reaches_validation", path: "/v1/messages", size: 1, want: http.StatusBadRequest, errorType: "api_error"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var input io.Reader = strings.NewReader("{")
			if tt.chunkedBytes > 0 {
				input = io.LimitReader(inferenceTestFill('a'), tt.chunkedBytes)
			}
			request := httptest.NewRequest(http.MethodPost, tt.path, input)
			if tt.size > 0 {
				request.ContentLength = tt.size
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tt.want || !strings.Contains(response.Body.String(), fmt.Sprintf(`"type":%q`, tt.errorType)) {
				t.Fatalf("status=%d response=%q, want %d %s", response.Code, response.Body.String(), tt.want, tt.errorType)
			}
			if tt.want != http.StatusBadRequest && response.Header().Get("Connection") != "close" {
				t.Fatal("HTTP/1.1 would drain the rejected request")
			}
		})
	}
	select {
	case <-done:
		t.Fatal("large request unexpectedly completed while body read was blocked")
	default:
	}
	close(body.release) // Incomplete client upload.
	released = true
	<-done
	if firstResponse.Code != http.StatusBadRequest || len(firstResponse.deadlines) != 2 ||
		firstResponse.deadlines[0].IsZero() || !firstResponse.deadlines[1].IsZero() {
		t.Fatalf("large upload did not set and clear a client read deadline: status=%d deadlines=%v", firstResponse.Code, firstResponse.deadlines)
	}

	second := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{"))
	second.ContentLength = 34_232_216
	secondResponse := &largeUploadRecorder{ResponseRecorder: httptest.NewRecorder()}
	handler.ServeHTTP(secondResponse, second)
	if secondResponse.Code != http.StatusBadRequest || len(secondResponse.deadlines) != 2 {
		t.Fatalf("incomplete upload retained the slot: next status=%d deadlines=%v", secondResponse.Code, secondResponse.deadlines)
	}
}

func TestBackendRejectsOversizedChunkedInference(t *testing.T) {
	handler := newBackendHandler(t.Context(), BackendOptions{UseRequestModel: true}, nil)
	request := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", io.LimitReader(inferenceTestFill('a'), 128_000_001))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge || !strings.Contains(response.Body.String(), `"type":"request_too_large"`) {
		t.Fatalf("status=%d response=%q, want 413 request_too_large", response.Code, response.Body.String())
	}
}
