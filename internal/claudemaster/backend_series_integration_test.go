package claudemaster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type backendSeriesQuotaError struct{}

func (backendSeriesQuotaError) Error() string            { return "private quota response" }
func (backendSeriesQuotaError) StatusCode() int          { return http.StatusTooManyRequests }
func (backendSeriesQuotaError) IsCredentialScoped() bool { return true }

type backendSeriesPayloadQuotaError struct {
	backendSeriesQuotaError
}

func (backendSeriesPayloadQuotaError) StreamErrorPayloadEncoded() bool { return true }

type backendSeriesModelRateLimitError struct{}

func (backendSeriesModelRateLimitError) Error() string   { return "private model rate limit" }
func (backendSeriesModelRateLimitError) StatusCode() int { return http.StatusTooManyRequests }
func (backendSeriesModelRateLimitError) RetryAfter() *time.Duration {
	retryAfter := time.Hour
	return &retryAfter
}

type backendSeriesCapture struct {
	mu         sync.Mutex
	calls      []string
	models     []string
	bodyModels []string
	pinned     []any
	drained    string
	failModel  string
	drainAll   bool
	failure    error
	streamMode string
}

type backendSeriesBlockingHook struct {
	coreauth.NoopHook
	authID  string
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (h *backendSeriesBlockingHook) OnResult(_ context.Context, result coreauth.Result) {
	if result.AuthID != h.authID || result.Success || result.Error == nil || result.Error.HTTPStatus != http.StatusTooManyRequests {
		return
	}
	h.once.Do(func() { close(h.entered) })
	<-h.release
}

func (e *backendSeriesCapture) Identifier() string { return "claude" }

func (e *backendSeriesCapture) record(auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var body struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(req.Payload, &body)
	e.calls = append(e.calls, auth.ID)
	e.models = append(e.models, req.Model)
	e.bodyModels = append(e.bodyModels, body.Model)
	e.pinned = append(e.pinned, opts.Metadata[coreexecutor.PinnedAuthMetadataKey])
}

func (e *backendSeriesCapture) selectedFailure() error {
	if e.failure != nil {
		return e.failure
	}
	return backendSeriesQuotaError{}
}

func (e *backendSeriesCapture) shouldFail(auth *coreauth.Auth, model string) bool {
	return (e.failModel == "" || e.failModel == model) && (e.drainAll || auth.ID == e.drained)
}

func (e *backendSeriesCapture) Execute(_ context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	e.record(auth, req, opts)
	if e.shouldFail(auth, req.Model) {
		return coreexecutor.Response{}, e.selectedFailure()
	}
	return coreexecutor.Response{Payload: []byte(`{"type":"message","content":[{"type":"text","text":"ok"}]}`)}, nil
}

func TestBackendSeriesDoesNotFallThroughOnModelScopedRateLimit(t *testing.T) {
	model := t.Name() + "-model"
	otherModel := t.Name() + "-other-model"
	first, second := t.Name()+"-first", t.Name()+"-second"
	selector := &backendSeriesSelector{authIDs: []string{first, second}, provider: "claude"}
	capture := &backendSeriesCapture{drained: first, failModel: model, failure: backendSeriesModelRateLimitError{}}
	manager := coreauth.NewManager(nil, selector, nil)
	manager.SetRetryConfig(0, 0, 2)
	setBackendSeriesResultPolicy(manager, selector)
	manager.RegisterExecutor(capture)
	for _, authID := range []string{first, second} {
		registry.GetGlobalRegistry().RegisterClient(authID, "claude", []*registry.ModelInfo{{ID: backendAuthSelectionModel}})
		if _, err := manager.Register(t.Context(), &coreauth.Auth{ID: authID, Provider: "claude", Status: coreauth.StatusActive, Metadata: map[string]any{"disable_cooling": false}}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	}
	handler := newBackendHandler(t.Context(), BackendOptions{Provider: "claude", UseRequestModel: true}, handlers.NewBaseAPIHandlers(&config.SDKConfig{}, manager))
	request := func(requestModel string) *httptest.ResponseRecorder {
		writer := httptest.NewRecorder()
		handler.ServeHTTP(writer, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}]}`, requestModel))))
		return writer
	}
	firstResponse := request(model)
	secondResponse := request(model)
	otherResponse := request(otherModel)

	capture.mu.Lock()
	defer capture.mu.Unlock()
	if got := strings.Join(capture.calls, ","); got != first+","+first {
		t.Fatalf("model-scoped rate limit fell through: calls=%v", capture.calls)
	}
	if got := strings.Join(capture.models, ","); got != model+","+otherModel {
		t.Fatalf("model cooldown used the selection marker instead of requested models: models=%v", capture.models)
	}
	if selector.active != 0 {
		t.Fatalf("model-scoped rate limit advanced cursor to %d", selector.active)
	}
	for i, response := range []*httptest.ResponseRecorder{firstResponse, secondResponse} {
		if response.Code == http.StatusOK || strings.Contains(response.Body.String(), "private model rate limit") {
			t.Fatalf("model-limited response %d: status=%d body=%s", i+1, response.Code, response.Body.String())
		}
	}
	if otherResponse.Code != http.StatusOK {
		t.Fatalf("other model did not remain on the active profile: status=%d body=%s", otherResponse.Code, otherResponse.Body.String())
	}
}

func TestBackendSeriesAdvancesBeforePublishingCredentialCooldown(t *testing.T) {
	model := t.Name() + "-model"
	first, second := t.Name()+"-first", t.Name()+"-second"
	selector := &backendSeriesSelector{authIDs: []string{first, second}, provider: "claude"}
	hook := &backendSeriesBlockingHook{authID: first, entered: make(chan struct{}), release: make(chan struct{})}
	capture := &backendSeriesCapture{}
	manager := coreauth.NewManager(nil, selector, hook)
	manager.SetRetryConfig(0, 0, 2)
	setBackendSeriesResultPolicy(manager, selector)
	manager.RegisterExecutor(capture)
	for _, authID := range []string{first, second} {
		registry.GetGlobalRegistry().RegisterClient(authID, "claude", []*registry.ModelInfo{{ID: backendAuthSelectionModel}})
		if _, err := manager.Register(t.Context(), &coreauth.Auth{ID: authID, Provider: "claude", Status: coreauth.StatusActive, Metadata: map[string]any{"disable_cooling": false}}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	}

	done := make(chan struct{})
	result := backendSeriesQuotaResult(first)
	result.Model = model
	go func() {
		manager.MarkResult(t.Context(), result)
		close(done)
	}()
	<-hook.entered
	if _, err := manager.Execute(t.Context(), []string{"claude"}, coreexecutor.Request{Model: backendAuthSelectionModel}, coreexecutor.Options{}); err != nil {
		close(hook.release)
		<-done
		t.Fatalf("concurrent request did not select the next profile: %v", err)
	}
	close(hook.release)
	<-done

	capture.mu.Lock()
	defer capture.mu.Unlock()
	if len(capture.calls) != 1 || capture.calls[0] != second {
		t.Fatalf("concurrent handoff calls=%v, want only %s", capture.calls, second)
	}
}

func (e *backendSeriesCapture) ExecuteStream(_ context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.record(auth, req, opts)
	if e.shouldFail(auth, req.Model) && e.streamMode == "direct" {
		return nil, e.selectedFailure()
	}
	headers := http.Header{"Anthropic-Ratelimit-Requests-Limit": {"2"}}
	chunks := make(chan coreexecutor.StreamChunk, 2)
	if e.shouldFail(auth, req.Model) {
		headers.Set("Anthropic-Ratelimit-Requests-Limit", "1")
		if e.streamMode == "sse-late" {
			chunks <- coreexecutor.StreamChunk{Payload: []byte("event: message_start\ndata: {\"type\":\"message_start\"}\n\n")}
		}
		if e.streamMode == "sse-first" || e.streamMode == "sse-late" {
			chunks <- coreexecutor.StreamChunk{
				Payload: []byte("event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"message\":\"subscription exhausted\"}}\n\n"),
				Err:     backendSeriesPayloadQuotaError{},
			}
			close(chunks)
			return &coreexecutor.StreamResult{Headers: headers, Chunks: chunks}, nil
		}
		if e.streamMode == "late" {
			chunks <- coreexecutor.StreamChunk{Payload: []byte("event: message_start\ndata: {\"type\":\"message_start\"}\n\n")}
		}
		chunks <- coreexecutor.StreamChunk{Err: e.selectedFailure()}
	} else {
		chunks <- coreexecutor.StreamChunk{Payload: []byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")}
	}
	close(chunks)
	return &coreexecutor.StreamResult{Headers: headers, Chunks: chunks}, nil
}

func (e *backendSeriesCapture) CountTokens(_ context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	e.record(auth, req, opts)
	if e.shouldFail(auth, req.Model) {
		return coreexecutor.Response{}, e.selectedFailure()
	}
	return coreexecutor.Response{Payload: []byte(`{"input_tokens":9}`)}, nil
}

func (e *backendSeriesCapture) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *backendSeriesCapture) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("unused")
}

func registerBackendSeriesTestAuths(t *testing.T, manager *coreauth.Manager, _ string, authIDs ...string) {
	t.Helper()
	for _, authID := range authIDs {
		registry.GetGlobalRegistry().RegisterClient(authID, "claude", []*registry.ModelInfo{{ID: backendAuthSelectionModel}})
		if _, err := manager.Register(t.Context(), &coreauth.Auth{ID: authID, Provider: "claude", Status: coreauth.StatusActive, Metadata: map[string]any{"disable_cooling": false}}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	}
}

func TestBackendPreservesNativeModelAcrossResponseModes(t *testing.T) {
	models := []string{t.Name() + "-primary", t.Name() + "-fallback"}
	authID := t.Name() + "-auth"
	capture := &backendSeriesCapture{}
	manager := coreauth.NewManager(nil, &backendSeriesSelector{authIDs: []string{authID}, provider: "claude"}, nil)
	manager.SetRetryConfig(0, 0, 1)
	manager.RegisterExecutor(capture)
	registry.GetGlobalRegistry().RegisterClient(authID, "claude", []*registry.ModelInfo{{ID: backendAuthSelectionModel}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	if _, err := manager.Register(t.Context(), &coreauth.Auth{ID: authID, Provider: "claude", Status: coreauth.StatusActive, Metadata: map[string]any{"disable_cooling": false}}); err != nil {
		t.Fatal(err)
	}
	handler := newBackendHandler(t.Context(), BackendOptions{Provider: "claude", UseRequestModel: true}, handlers.NewBaseAPIHandlers(&config.SDKConfig{}, manager))

	requests := []struct {
		path   string
		model  string
		stream bool
	}{
		{path: "/v1/messages", model: models[0]},
		{path: "/v1/messages", model: models[1], stream: true},
		{path: "/v1/messages/count_tokens", model: models[0]},
	}
	for _, request := range requests {
		writer := httptest.NewRecorder()
		body := fmt.Sprintf(`{"model":%q,"stream":%t,"messages":[{"role":"user","content":"hello"}]}`, request.model, request.stream)
		handler.ServeHTTP(writer, httptest.NewRequest(http.MethodPost, request.path, strings.NewReader(body)))
		if writer.Code != http.StatusOK {
			t.Fatalf("%s stream=%t status=%d body=%s", request.path, request.stream, writer.Code, writer.Body.String())
		}
	}

	capture.mu.Lock()
	defer capture.mu.Unlock()
	want := []string{models[0], models[1], models[0]}
	if strings.Join(capture.models, ",") != strings.Join(want, ",") || strings.Join(capture.bodyModels, ",") != strings.Join(want, ",") {
		t.Fatalf("native models changed: execution=%v body=%v want=%v", capture.models, capture.bodyModels, want)
	}
}

func TestBackendSeriesRetriesStreamBeforeOutput(t *testing.T) {
	for _, mode := range []string{"direct", "bootstrap"} {
		t.Run(mode, func(t *testing.T) {
			model := t.Name() + "-model"
			first, second := t.Name()+"-first", t.Name()+"-second"
			selector := &backendSeriesSelector{authIDs: []string{first, second}, provider: "claude"}
			capture := &backendSeriesCapture{drained: first, streamMode: mode}
			manager := coreauth.NewManager(nil, selector, nil)
			manager.SetRetryConfig(0, 0, 2)
			setBackendSeriesResultPolicy(manager, selector)
			manager.RegisterExecutor(capture)
			registerBackendSeriesTestAuths(t, manager, model, first, second)
			handler := newBackendHandler(t.Context(), BackendOptions{Provider: "claude", UseRequestModel: true}, handlers.NewBaseAPIHandlers(&config.SDKConfig{PassthroughHeaders: true}, manager))

			writer := httptest.NewRecorder()
			handler.ServeHTTP(writer, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(fmt.Sprintf(`{"model":%q,"stream":true,"messages":[{"role":"user","content":"hello"}]}`, model))))
			if writer.Code != http.StatusOK || !strings.Contains(writer.Body.String(), "message_stop") || strings.Contains(writer.Body.String(), "private quota response") || writer.Header().Get("Anthropic-Ratelimit-Requests-Limit") != "2" {
				t.Fatalf("stream failover status=%d body=%s", writer.Code, writer.Body.String())
			}
			capture.mu.Lock()
			defer capture.mu.Unlock()
			if got := strings.Join(capture.calls, ","); got != first+","+second {
				t.Fatalf("stream calls=%q, want pre-output failover", got)
			}
		})
	}
}

func TestBackendSeriesDoesNotReplayStartedStream(t *testing.T) {
	model := t.Name() + "-model"
	first, second := t.Name()+"-first", t.Name()+"-second"
	selector := &backendSeriesSelector{authIDs: []string{first, second}, provider: "claude"}
	capture := &backendSeriesCapture{drained: first, streamMode: "late"}
	manager := coreauth.NewManager(nil, selector, nil)
	manager.SetRetryConfig(0, 0, 2)
	setBackendSeriesResultPolicy(manager, selector)
	manager.RegisterExecutor(capture)
	registerBackendSeriesTestAuths(t, manager, model, first, second)
	handler := newBackendHandler(t.Context(), BackendOptions{Provider: "claude", UseRequestModel: true}, handlers.NewBaseAPIHandlers(&config.SDKConfig{}, manager))
	request := func() *httptest.ResponseRecorder {
		writer := httptest.NewRecorder()
		handler.ServeHTTP(writer, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(fmt.Sprintf(`{"model":%q,"stream":true,"messages":[{"role":"user","content":"hello"}]}`, model))))
		return writer
	}

	firstResponse := request()
	if firstResponse.Code != http.StatusOK || !strings.Contains(firstResponse.Body.String(), "message_start") || strings.Contains(firstResponse.Body.String(), "message_stop") || strings.Contains(firstResponse.Body.String(), "private quota response") {
		t.Fatalf("started stream was replayed or leaked: status=%d body=%s", firstResponse.Code, firstResponse.Body.String())
	}
	secondResponse := request()
	if secondResponse.Code != http.StatusOK || !strings.Contains(secondResponse.Body.String(), "message_stop") {
		t.Fatalf("next request did not use the next profile: status=%d body=%s", secondResponse.Code, secondResponse.Body.String())
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if got := strings.Join(capture.calls, ","); got != first+","+second {
		t.Fatalf("started stream calls=%q, want no replay then next-profile use", got)
	}
}

func TestBackendSeriesHTTP200SSEQuotaErrorBeforeOutputRetriesWithoutLeakingFrame(t *testing.T) {
	model := t.Name() + "-model"
	first, second := t.Name()+"-first", t.Name()+"-second"
	selector := &backendSeriesSelector{authIDs: []string{first, second}, provider: "claude"}
	capture := &backendSeriesCapture{drained: first, streamMode: "sse-first"}
	manager := coreauth.NewManager(nil, selector, nil)
	manager.SetRetryConfig(0, 0, 2)
	setBackendSeriesResultPolicy(manager, selector)
	manager.RegisterExecutor(capture)
	registerBackendSeriesTestAuths(t, manager, model, first, second)
	handler := newBackendHandler(t.Context(), BackendOptions{Provider: "claude", UseRequestModel: true}, handlers.NewBaseAPIHandlers(&config.SDKConfig{}, manager))

	writer := httptest.NewRecorder()
	handler.ServeHTTP(writer, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(fmt.Sprintf(`{"model":%q,"stream":true,"messages":[{"role":"user","content":"hello"}]}`, model))))
	if writer.Code != http.StatusOK || !strings.Contains(writer.Body.String(), "message_stop") || strings.Contains(writer.Body.String(), "subscription exhausted") {
		t.Fatalf("pre-output SSE failover status=%d body=%q", writer.Code, writer.Body.String())
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if got := strings.Join(capture.calls, ","); got != first+","+second {
		t.Fatalf("pre-output SSE calls=%q, want same-request failover", got)
	}
}

func TestBackendSeriesHTTP200SSEQuotaErrorAfterOutputIsRawAndAdvancesNextRequest(t *testing.T) {
	model := t.Name() + "-model"
	first, second := t.Name()+"-first", t.Name()+"-second"
	selector := &backendSeriesSelector{authIDs: []string{first, second}, provider: "claude"}
	capture := &backendSeriesCapture{drained: first, streamMode: "sse-late"}
	manager := coreauth.NewManager(nil, selector, nil)
	manager.SetRetryConfig(0, 0, 2)
	setBackendSeriesResultPolicy(manager, selector)
	manager.RegisterExecutor(capture)
	registerBackendSeriesTestAuths(t, manager, model, first, second)
	handler := newBackendHandler(t.Context(), BackendOptions{Provider: "claude", UseRequestModel: true}, handlers.NewBaseAPIHandlers(&config.SDKConfig{}, manager))
	request := func() *httptest.ResponseRecorder {
		writer := httptest.NewRecorder()
		handler.ServeHTTP(writer, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(fmt.Sprintf(`{"model":%q,"stream":true,"messages":[{"role":"user","content":"hello"}]}`, model))))
		return writer
	}

	const wantFirst = "event: message_start\ndata: {\"type\":\"message_start\"}\n\n" +
		"event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"message\":\"subscription exhausted\"}}\n\n"
	firstResponse := request()
	if firstResponse.Code != http.StatusOK || firstResponse.Body.String() != wantFirst {
		t.Fatalf("started SSE was changed or replayed: status=%d body=%q", firstResponse.Code, firstResponse.Body.String())
	}
	secondResponse := request()
	if secondResponse.Code != http.StatusOK || !strings.Contains(secondResponse.Body.String(), "message_stop") {
		t.Fatalf("next request did not use next profile: status=%d body=%q", secondResponse.Code, secondResponse.Body.String())
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if got := strings.Join(capture.calls, ","); got != first+","+second {
		t.Fatalf("started SSE calls=%q, want no replay then next-profile use", got)
	}
}

func TestBackendSeriesTokenCountAdvancesConfirmedDrain(t *testing.T) {
	model := t.Name() + "-model"
	first, second := t.Name()+"-first", t.Name()+"-second"
	selector := &backendSeriesSelector{authIDs: []string{first, second}, provider: "claude"}
	capture := &backendSeriesCapture{drained: first}
	manager := coreauth.NewManager(nil, selector, nil)
	manager.SetRetryConfig(0, 0, 2)
	setBackendSeriesResultPolicy(manager, selector)
	manager.RegisterExecutor(capture)
	registerBackendSeriesTestAuths(t, manager, model, first, second)
	handler := newBackendHandler(t.Context(), BackendOptions{Provider: "claude", UseRequestModel: true}, handlers.NewBaseAPIHandlers(&config.SDKConfig{}, manager))

	countWriter := httptest.NewRecorder()
	handler.ServeHTTP(countWriter, httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}]}`, model))))
	if countWriter.Code != http.StatusOK {
		t.Fatalf("token-count failover status=%d body=%s", countWriter.Code, countWriter.Body.String())
	}
	messageWriter := httptest.NewRecorder()
	handler.ServeHTTP(messageWriter, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}]}`, model))))
	if messageWriter.Code != http.StatusOK {
		t.Fatalf("generation failover status=%d body=%s", messageWriter.Code, messageWriter.Body.String())
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if got := strings.Join(capture.calls, ","); got != first+","+second+","+second {
		t.Fatalf("token-count drain did not advance once: calls=%q", got)
	}
}

func TestBackendSeriesFinalExhaustionNeverWrapsOrUsesNativeMaster(t *testing.T) {
	model := t.Name() + "-model"
	first, second := t.Name()+"-first", t.Name()+"-second"
	selector := &backendSeriesSelector{authIDs: []string{first, second}, provider: "claude"}
	capture := &backendSeriesCapture{drainAll: true}
	manager := coreauth.NewManager(nil, selector, nil)
	manager.SetRetryConfig(0, 0, 2)
	setBackendSeriesResultPolicy(manager, selector)
	manager.RegisterExecutor(capture)
	registerBackendSeriesTestAuths(t, manager, model, first, second)
	handler := newBackendHandler(t.Context(), BackendOptions{Provider: "claude", UseRequestModel: true}, handlers.NewBaseAPIHandlers(&config.SDKConfig{}, manager))
	request := func() *httptest.ResponseRecorder {
		writer := httptest.NewRecorder()
		handler.ServeHTTP(writer, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}]}`, model))))
		return writer
	}

	firstResponse := request()
	if firstResponse.Code == http.StatusOK || strings.Contains(firstResponse.Body.String(), "private quota response") || !strings.Contains(firstResponse.Body.String(), "no fallback to the native master") {
		t.Fatalf("unsafe final exhaustion response: status=%d body=%s", firstResponse.Code, firstResponse.Body.String())
	}
	_ = request()
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if got := strings.Join(capture.calls, ","); got != first+","+second {
		t.Fatalf("final exhaustion wrapped or retried an executor: calls=%q", got)
	}
}

func TestBackendSeriesRetriesConfirmedDrainThenStaysOnNextProfile(t *testing.T) {
	model := t.Name() + "-model"
	first, second := t.Name()+"-first", t.Name()+"-second"
	selector := &backendSeriesSelector{authIDs: []string{first, second}, provider: "claude"}
	capture := &backendSeriesCapture{drained: first}
	manager := coreauth.NewManager(nil, selector, nil)
	manager.SetRetryConfig(0, 0, 2)
	setBackendSeriesResultPolicy(manager, selector)
	manager.RegisterExecutor(capture)
	for _, authID := range []string{first, second} {
		registry.GetGlobalRegistry().RegisterClient(authID, "claude", []*registry.ModelInfo{{ID: backendAuthSelectionModel}})
		if _, err := manager.Register(t.Context(), &coreauth.Auth{ID: authID, Provider: "claude", Status: coreauth.StatusActive, Metadata: map[string]any{"disable_cooling": false}}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	}
	handler := newBackendHandler(t.Context(), BackendOptions{Provider: "claude", UseRequestModel: true}, handlers.NewBaseAPIHandlers(&config.SDKConfig{}, manager))
	request := func() *httptest.ResponseRecorder {
		writer := httptest.NewRecorder()
		handler.ServeHTTP(writer, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}]}`, model))))
		return writer
	}

	if writer := request(); writer.Code != http.StatusOK {
		t.Fatalf("first request status=%d body=%s", writer.Code, writer.Body.String())
	}
	if writer := request(); writer.Code != http.StatusOK {
		t.Fatalf("second request status=%d body=%s", writer.Code, writer.Body.String())
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if got := strings.Join(capture.calls, ","); got != first+","+second+","+second {
		t.Fatalf("calls=%q, want first drain, same-request failover, then second", got)
	}
	for _, pinned := range capture.pinned {
		if pinned != nil {
			t.Fatalf("series request was hard-pinned: %v", capture.pinned)
		}
	}
}
