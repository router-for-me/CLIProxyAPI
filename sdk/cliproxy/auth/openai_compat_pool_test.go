package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

type openAICompatPoolExecutor struct {
	id string

	mu                sync.Mutex
	executeModels     []string
	countModels       []string
	streamModels      []string
	executeErrors     map[string]error
	countErrors       map[string]error
	streamFirstErrors map[string]error
	streamPayloads    map[string][]cliproxyexecutor.StreamChunk
}

func (e *openAICompatPoolExecutor) Identifier() string { return e.id }

func (e *openAICompatPoolExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	_ = ctx
	_ = auth
	_ = opts
	e.mu.Lock()
	e.executeModels = append(e.executeModels, req.Model)
	err := e.executeErrors[req.Model]
	e.mu.Unlock()
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: []byte(req.Model)}, nil
}

func (e *openAICompatPoolExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	_ = ctx
	_ = auth
	_ = opts
	e.mu.Lock()
	e.streamModels = append(e.streamModels, req.Model)
	err := e.streamFirstErrors[req.Model]
	payloadChunks, hasCustomChunks := e.streamPayloads[req.Model]
	chunks := append([]cliproxyexecutor.StreamChunk(nil), payloadChunks...)
	e.mu.Unlock()
	ch := make(chan cliproxyexecutor.StreamChunk, max(1, len(chunks)))
	if err != nil {
		ch <- cliproxyexecutor.StreamChunk{Err: err}
		close(ch)
		return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Model": {req.Model}}, Chunks: ch}, nil
	}
	if !hasCustomChunks {
		ch <- cliproxyexecutor.StreamChunk{Payload: []byte(req.Model)}
	} else {
		for _, chunk := range chunks {
			ch <- chunk
		}
	}
	close(ch)
	return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Model": {req.Model}}, Chunks: ch}, nil
}

func (e *openAICompatPoolExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *openAICompatPoolExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	_ = ctx
	_ = auth
	_ = opts
	e.mu.Lock()
	e.countModels = append(e.countModels, req.Model)
	err := e.countErrors[req.Model]
	e.mu.Unlock()
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: []byte(req.Model)}, nil
}

func (e *openAICompatPoolExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	_ = ctx
	_ = auth
	_ = req
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "HttpRequest not implemented"}
}

func (e *openAICompatPoolExecutor) ExecuteModels() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.executeModels))
	copy(out, e.executeModels)
	return out
}

func (e *openAICompatPoolExecutor) CountModels() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.countModels))
	copy(out, e.countModels)
	return out
}

func (e *openAICompatPoolExecutor) StreamModels() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.streamModels))
	copy(out, e.streamModels)
	return out
}

func newOpenAICompatPoolTestManager(t *testing.T, alias string, models []internalconfig.OpenAICompatibilityModel, executor *openAICompatPoolExecutor) *Manager {
	t.Helper()
	cfg := &internalconfig.Config{
		OpenAICompatibility: []internalconfig.OpenAICompatibility{{
			Name:   "pool",
			Models: models,
		}},
	}
	m := NewManager(nil, nil, nil)
	m.SetConfig(cfg)
	if executor == nil {
		executor = &openAICompatPoolExecutor{id: "pool"}
	}
	m.RegisterExecutor(executor)

	auth := &Auth{
		ID:       "pool-auth-" + t.Name(),
		Provider: "pool",
		Status:   StatusActive,
		Attributes: map[string]string{
			"api_key":      "test-key",
			"compat_name":  "pool",
			"provider_key": "pool",
		},
	}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "pool", []*registry.ModelInfo{{ID: alias}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})
	return m
}

func TestManagerExecuteCount_OpenAICompatAliasPoolStopsOnInvalidRequest(t *testing.T) {
	alias := "claude-opus-4.66"
	invalidErr := &Error{HTTPStatus: http.StatusUnprocessableEntity, Message: "unprocessable entity"}
	executor := &openAICompatPoolExecutor{
		id:          "pool",
		countErrors: map[string]error{"qwen3.5-plus": invalidErr},
	}
	m := newOpenAICompatPoolTestManager(t, alias, []internalconfig.OpenAICompatibilityModel{
		{Name: "qwen3.5-plus", Alias: alias},
		{Name: "glm-5", Alias: alias},
	}, executor)

	_, err := m.ExecuteCount(context.Background(), []string{"pool"}, cliproxyexecutor.Request{Model: alias}, cliproxyexecutor.Options{})
	if err == nil || err.Error() != invalidErr.Error() {
		t.Fatalf("execute count error = %v, want %v", err, invalidErr)
	}
	got := executor.CountModels()
	if len(got) != 1 || got[0] != "qwen3.5-plus" {
		t.Fatalf("count calls = %v, want only first invalid model", got)
	}
}
func TestResolveModelAliasPoolFromConfigModels(t *testing.T) {
	models := []modelAliasEntry{
		internalconfig.OpenAICompatibilityModel{Name: "qwen3.5-plus", Alias: "claude-opus-4.66"},
		internalconfig.OpenAICompatibilityModel{Name: "glm-5", Alias: "claude-opus-4.66"},
		internalconfig.OpenAICompatibilityModel{Name: "kimi-k2.5", Alias: "claude-opus-4.66"},
	}
	got := resolveModelAliasPoolFromConfigModels("claude-opus-4.66(8192)", models)
	want := []string{"qwen3.5-plus(8192)", "glm-5(8192)", "kimi-k2.5(8192)"}
	if len(got) != len(want) {
		t.Fatalf("pool len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pool[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestManagerExecute_OpenAICompatAliasPoolRotatesWithinAuth(t *testing.T) {
	alias := "claude-opus-4.66"
	executor := &openAICompatPoolExecutor{id: "pool"}
	m := newOpenAICompatPoolTestManager(t, alias, []internalconfig.OpenAICompatibilityModel{
		{Name: "qwen3.5-plus", Alias: alias},
		{Name: "glm-5", Alias: alias},
	}, executor)

	for i := 0; i < 3; i++ {
		resp, err := m.Execute(context.Background(), []string{"pool"}, cliproxyexecutor.Request{Model: alias}, cliproxyexecutor.Options{})
		if err != nil {
			t.Fatalf("execute %d: %v", i, err)
		}
		if len(resp.Payload) == 0 {
			t.Fatalf("execute %d returned empty payload", i)
		}
	}

	got := executor.ExecuteModels()
	want := []string{"qwen3.5-plus", "glm-5", "qwen3.5-plus"}
	if len(got) != len(want) {
		t.Fatalf("execute calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("execute call %d model = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestManagerExecutionModelCandidates_OpenAICompatAliasPoolDoesNotRotate(t *testing.T) {
	alias := "claude-opus-4.66"
	executor := &openAICompatPoolExecutor{id: "pool"}
	m := newOpenAICompatPoolTestManager(t, alias, []internalconfig.OpenAICompatibilityModel{
		{Name: "qwen3.5-plus", Alias: alias},
		{Name: "glm-5", Alias: alias},
	}, executor)

	auth, ok := m.GetByID("pool-auth-" + t.Name())
	if !ok {
		t.Fatal("expected registered auth")
	}

	firstPreview := m.ExecutionModelCandidates(auth, alias)
	secondPreview := m.ExecutionModelCandidates(auth, alias)
	wantPreview := []string{"qwen3.5-plus", "glm-5"}
	if len(firstPreview) != len(wantPreview) || len(secondPreview) != len(wantPreview) {
		t.Fatalf("preview lengths = %v / %v, want %v", firstPreview, secondPreview, wantPreview)
	}
	for i := range wantPreview {
		if firstPreview[i] != wantPreview[i] {
			t.Fatalf("first preview[%d] = %q, want %q", i, firstPreview[i], wantPreview[i])
		}
		if secondPreview[i] != wantPreview[i] {
			t.Fatalf("second preview[%d] = %q, want %q", i, secondPreview[i], wantPreview[i])
		}
	}

	resp, err := m.Execute(context.Background(), []string{"pool"}, cliproxyexecutor.Request{Model: alias}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if string(resp.Payload) != "qwen3.5-plus" {
		t.Fatalf("payload = %q, want %q", string(resp.Payload), "qwen3.5-plus")
	}
	got := executor.ExecuteModels()
	if len(got) != 1 || got[0] != "qwen3.5-plus" {
		t.Fatalf("execute calls = %v, want [qwen3.5-plus]", got)
	}
}

func TestManagerExecute_OpenAICompatAliasPoolStopsOnBadRequest(t *testing.T) {
	alias := "claude-opus-4.66"
	invalidErr := &Error{HTTPStatus: http.StatusBadRequest, Message: "invalid_request_error: malformed payload"}
	executor := &openAICompatPoolExecutor{
		id:            "pool",
		executeErrors: map[string]error{"qwen3.5-plus": invalidErr},
	}
	m := newOpenAICompatPoolTestManager(t, alias, []internalconfig.OpenAICompatibilityModel{
		{Name: "qwen3.5-plus", Alias: alias},
		{Name: "glm-5", Alias: alias},
	}, executor)

	_, err := m.Execute(context.Background(), []string{"pool"}, cliproxyexecutor.Request{Model: alias}, cliproxyexecutor.Options{})
	if err == nil || err.Error() != invalidErr.Error() {
		t.Fatalf("execute error = %v, want %v", err, invalidErr)
	}
	got := executor.ExecuteModels()
	if len(got) != 1 || got[0] != "qwen3.5-plus" {
		t.Fatalf("execute calls = %v, want only first invalid model", got)
	}
}
func TestManagerExecute_OpenAICompatAliasPoolFallsBackWithinSameAuth(t *testing.T) {
	alias := "claude-opus-4.66"
	executor := &openAICompatPoolExecutor{
		id:            "pool",
		executeErrors: map[string]error{"qwen3.5-plus": &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"}},
	}
	m := newOpenAICompatPoolTestManager(t, alias, []internalconfig.OpenAICompatibilityModel{
		{Name: "qwen3.5-plus", Alias: alias},
		{Name: "glm-5", Alias: alias},
	}, executor)

	resp, err := m.Execute(context.Background(), []string{"pool"}, cliproxyexecutor.Request{Model: alias}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if string(resp.Payload) != "glm-5" {
		t.Fatalf("payload = %q, want %q", string(resp.Payload), "glm-5")
	}
	got := executor.ExecuteModels()
	want := []string{"qwen3.5-plus", "glm-5"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("execute call %d model = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestManagerExecuteStream_OpenAICompatAliasPoolRetriesOnEmptyBootstrap(t *testing.T) {
	alias := "claude-opus-4.66"
	executor := &openAICompatPoolExecutor{
		id: "pool",
		streamPayloads: map[string][]cliproxyexecutor.StreamChunk{
			"qwen3.5-plus": {},
		},
	}
	m := newOpenAICompatPoolTestManager(t, alias, []internalconfig.OpenAICompatibilityModel{
		{Name: "qwen3.5-plus", Alias: alias},
		{Name: "glm-5", Alias: alias},
	}, executor)

	streamResult, err := m.ExecuteStream(context.Background(), []string{"pool"}, cliproxyexecutor.Request{Model: alias}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("execute stream: %v", err)
	}
	var payload []byte
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
		payload = append(payload, chunk.Payload...)
	}
	if string(payload) != "glm-5" {
		t.Fatalf("payload = %q, want %q", string(payload), "glm-5")
	}
	got := executor.StreamModels()
	want := []string{"qwen3.5-plus", "glm-5"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stream call %d model = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestManagerExecuteStream_OpenAICompatAliasPoolFallsBackBeforeFirstByte(t *testing.T) {
	alias := "claude-opus-4.66"
	executor := &openAICompatPoolExecutor{
		id:                "pool",
		streamFirstErrors: map[string]error{"qwen3.5-plus": &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"}},
	}
	m := newOpenAICompatPoolTestManager(t, alias, []internalconfig.OpenAICompatibilityModel{
		{Name: "qwen3.5-plus", Alias: alias},
		{Name: "glm-5", Alias: alias},
	}, executor)

	streamResult, err := m.ExecuteStream(context.Background(), []string{"pool"}, cliproxyexecutor.Request{Model: alias}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("execute stream: %v", err)
	}
	var payload []byte
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
		payload = append(payload, chunk.Payload...)
	}
	if string(payload) != "glm-5" {
		t.Fatalf("payload = %q, want %q", string(payload), "glm-5")
	}
	got := executor.StreamModels()
	want := []string{"qwen3.5-plus", "glm-5"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stream call %d model = %q, want %q", i, got[i], want[i])
		}
	}
	if gotHeader := streamResult.Headers.Get("X-Model"); gotHeader != "glm-5" {
		t.Fatalf("header X-Model = %q, want %q", gotHeader, "glm-5")
	}
}

func TestManagerExecuteStream_OpenAICompatAliasPoolStopsOnInvalidRequest(t *testing.T) {
	alias := "claude-opus-4.66"
	invalidErr := &Error{HTTPStatus: http.StatusUnprocessableEntity, Message: "unprocessable entity"}
	executor := &openAICompatPoolExecutor{
		id:                "pool",
		streamFirstErrors: map[string]error{"qwen3.5-plus": invalidErr},
	}
	m := newOpenAICompatPoolTestManager(t, alias, []internalconfig.OpenAICompatibilityModel{
		{Name: "qwen3.5-plus", Alias: alias},
		{Name: "glm-5", Alias: alias},
	}, executor)

	_, err := m.ExecuteStream(context.Background(), []string{"pool"}, cliproxyexecutor.Request{Model: alias}, cliproxyexecutor.Options{})
	if err == nil || err.Error() != invalidErr.Error() {
		t.Fatalf("execute stream error = %v, want %v", err, invalidErr)
	}
	got := executor.StreamModels()
	if len(got) != 1 || got[0] != "qwen3.5-plus" {
		t.Fatalf("stream calls = %v, want only first invalid model", got)
	}
}
func TestManagerExecuteCount_OpenAICompatAliasPoolRotatesWithinAuth(t *testing.T) {
	alias := "claude-opus-4.66"
	executor := &openAICompatPoolExecutor{id: "pool"}
	m := newOpenAICompatPoolTestManager(t, alias, []internalconfig.OpenAICompatibilityModel{
		{Name: "qwen3.5-plus", Alias: alias},
		{Name: "glm-5", Alias: alias},
	}, executor)

	for i := 0; i < 2; i++ {
		resp, err := m.ExecuteCount(context.Background(), []string{"pool"}, cliproxyexecutor.Request{Model: alias}, cliproxyexecutor.Options{})
		if err != nil {
			t.Fatalf("execute count %d: %v", i, err)
		}
		if len(resp.Payload) == 0 {
			t.Fatalf("execute count %d returned empty payload", i)
		}
	}

	got := executor.CountModels()
	want := []string{"qwen3.5-plus", "glm-5"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("count call %d model = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestManagerExecuteStream_OpenAICompatAliasPoolStopsOnInvalidBootstrap(t *testing.T) {
	alias := "claude-opus-4.66"
	invalidErr := &Error{HTTPStatus: http.StatusBadRequest, Message: "invalid_request_error: malformed payload"}
	executor := &openAICompatPoolExecutor{
		id:                "pool",
		streamFirstErrors: map[string]error{"qwen3.5-plus": invalidErr},
	}
	m := newOpenAICompatPoolTestManager(t, alias, []internalconfig.OpenAICompatibilityModel{
		{Name: "qwen3.5-plus", Alias: alias},
		{Name: "glm-5", Alias: alias},
	}, executor)

	streamResult, err := m.ExecuteStream(context.Background(), []string{"pool"}, cliproxyexecutor.Request{Model: alias}, cliproxyexecutor.Options{})
	if err == nil {
		t.Fatal("expected invalid request error")
	}
	if err != invalidErr {
		t.Fatalf("error = %v, want %v", err, invalidErr)
	}
	if streamResult != nil {
		t.Fatalf("streamResult = %#v, want nil on invalid bootstrap", streamResult)
	}
	if got := executor.StreamModels(); len(got) != 1 || got[0] != "qwen3.5-plus" {
		t.Fatalf("stream calls = %v, want only first upstream model", got)
	}
}
