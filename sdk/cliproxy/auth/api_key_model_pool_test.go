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

type apiKeyPoolExecutor struct {
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

func (e *apiKeyPoolExecutor) Identifier() string { return e.id }

func (e *apiKeyPoolExecutor) Execute(_ context.Context, _ *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.executeModels = append(e.executeModels, req.Model)
	err := e.executeErrors[req.Model]
	e.mu.Unlock()
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: []byte(req.Model)}, nil
}

func (e *apiKeyPoolExecutor) ExecuteStream(_ context.Context, _ *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
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

func (e *apiKeyPoolExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *apiKeyPoolExecutor) CountTokens(_ context.Context, _ *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.countModels = append(e.countModels, req.Model)
	err := e.countErrors[req.Model]
	e.mu.Unlock()
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: []byte(req.Model)}, nil
}

func (e *apiKeyPoolExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "HttpRequest not implemented"}
}

func (e *apiKeyPoolExecutor) ExecuteModels() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.executeModels))
	copy(out, e.executeModels)
	return out
}

func (e *apiKeyPoolExecutor) CountModels() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.countModels))
	copy(out, e.countModels)
	return out
}

func (e *apiKeyPoolExecutor) StreamModels() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.streamModels))
	copy(out, e.streamModels)
	return out
}

func newClaudeAPIKeyPoolTestManager(t *testing.T, alias string, models []internalconfig.ClaudeModel, executor *apiKeyPoolExecutor) *Manager {
	t.Helper()
	cfg := &internalconfig.Config{
		ClaudeKey: []internalconfig.ClaudeKey{{
			APIKey: "test-key",
			Models: models,
		}},
	}
	return newAPIKeyPoolTestManager(t, "claude", alias, cfg, executor)
}

func newCodexAPIKeyPoolTestManager(t *testing.T, alias string, models []internalconfig.CodexModel, executor *apiKeyPoolExecutor) *Manager {
	t.Helper()
	cfg := &internalconfig.Config{
		CodexKey: []internalconfig.CodexKey{{
			APIKey: "test-key",
			Models: models,
		}},
	}
	return newAPIKeyPoolTestManager(t, "codex", alias, cfg, executor)
}

func newAPIKeyPoolTestManager(t *testing.T, provider, alias string, cfg *internalconfig.Config, executor *apiKeyPoolExecutor) *Manager {
	t.Helper()
	m := NewManager(nil, nil, nil)
	m.SetConfig(cfg)
	if executor == nil {
		executor = &apiKeyPoolExecutor{id: provider}
	}
	m.RegisterExecutor(executor)

	auth := &Auth{
		ID:       provider + "-pool-auth-" + t.Name(),
		Provider: provider,
		Status:   StatusActive,
		Attributes: map[string]string{
			"api_key": "test-key",
		},
	}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: alias}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})
	return m
}

func TestManagerExecute_ClaudeAPIKeyAliasPoolRotatesWithinAuth(t *testing.T) {
	alias := "claude-sonnet"
	executor := &apiKeyPoolExecutor{id: "claude"}
	m := newClaudeAPIKeyPoolTestManager(t, alias, []internalconfig.ClaudeModel{
		{Name: "claude-sonnet-4-20250514", Alias: alias},
		{Name: "claude-sonnet-4-5-20250929", Alias: alias},
	}, executor)

	for i := 0; i < 3; i++ {
		resp, err := m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: alias}, cliproxyexecutor.Options{})
		if err != nil {
			t.Fatalf("execute %d: %v", i, err)
		}
		if len(resp.Payload) == 0 {
			t.Fatalf("execute %d returned empty payload", i)
		}
	}

	got := executor.ExecuteModels()
	want := []string{"claude-sonnet-4-20250514", "claude-sonnet-4-5-20250929", "claude-sonnet-4-20250514"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("execute call %d model = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestManagerExecuteStream_ClaudeAPIKeyAliasPoolFallsBackBeforeFirstByte(t *testing.T) {
	alias := "claude-sonnet"
	executor := &apiKeyPoolExecutor{
		id:                "claude",
		streamFirstErrors: map[string]error{"claude-sonnet-4-20250514": &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"}},
	}
	m := newClaudeAPIKeyPoolTestManager(t, alias, []internalconfig.ClaudeModel{
		{Name: "claude-sonnet-4-20250514", Alias: alias},
		{Name: "claude-sonnet-4-5-20250929", Alias: alias},
	}, executor)

	streamResult, err := m.ExecuteStream(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: alias}, cliproxyexecutor.Options{})
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

	if string(payload) != "claude-sonnet-4-5-20250929" {
		t.Fatalf("payload = %q, want %q", string(payload), "claude-sonnet-4-5-20250929")
	}
	got := executor.StreamModels()
	want := []string{"claude-sonnet-4-20250514", "claude-sonnet-4-5-20250929"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stream call %d model = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestManagerExecuteCount_ClaudeAPIKeyAliasPoolStopsOnInvalidRequest(t *testing.T) {
	alias := "claude-sonnet"
	invalidErr := &Error{HTTPStatus: http.StatusUnprocessableEntity, Message: "unprocessable entity"}
	executor := &apiKeyPoolExecutor{
		id:          "claude",
		countErrors: map[string]error{"claude-sonnet-4-20250514": invalidErr},
	}
	m := newClaudeAPIKeyPoolTestManager(t, alias, []internalconfig.ClaudeModel{
		{Name: "claude-sonnet-4-20250514", Alias: alias},
		{Name: "claude-sonnet-4-5-20250929", Alias: alias},
	}, executor)

	_, err := m.ExecuteCount(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: alias}, cliproxyexecutor.Options{})
	if err == nil || err.Error() != invalidErr.Error() {
		t.Fatalf("execute count error = %v, want %v", err, invalidErr)
	}
	got := executor.CountModels()
	if len(got) != 1 || got[0] != "claude-sonnet-4-20250514" {
		t.Fatalf("count calls = %v, want only first invalid model", got)
	}
}

func TestManagerExecute_CodexAPIKeyAliasPoolRotatesWithinAuth(t *testing.T) {
	alias := "gpt-latest"
	executor := &apiKeyPoolExecutor{id: "codex"}
	m := newCodexAPIKeyPoolTestManager(t, alias, []internalconfig.CodexModel{
		{Name: "gpt-5.2", Alias: alias},
		{Name: "gpt-5.3-codex", Alias: alias},
	}, executor)

	for i := 0; i < 3; i++ {
		resp, err := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: alias}, cliproxyexecutor.Options{})
		if err != nil {
			t.Fatalf("execute %d: %v", i, err)
		}
		if len(resp.Payload) == 0 {
			t.Fatalf("execute %d returned empty payload", i)
		}
	}

	got := executor.ExecuteModels()
	want := []string{"gpt-5.2", "gpt-5.3-codex", "gpt-5.2"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("execute call %d model = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestManagerExecuteStream_CodexAPIKeyAliasPoolFallsBackBeforeFirstByte(t *testing.T) {
	alias := "gpt-latest"
	executor := &apiKeyPoolExecutor{
		id:                "codex",
		streamFirstErrors: map[string]error{"gpt-5.2": &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"}},
	}
	m := newCodexAPIKeyPoolTestManager(t, alias, []internalconfig.CodexModel{
		{Name: "gpt-5.2", Alias: alias},
		{Name: "gpt-5.3-codex", Alias: alias},
	}, executor)

	streamResult, err := m.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: alias}, cliproxyexecutor.Options{})
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

	if string(payload) != "gpt-5.3-codex" {
		t.Fatalf("payload = %q, want %q", string(payload), "gpt-5.3-codex")
	}
	got := executor.StreamModels()
	want := []string{"gpt-5.2", "gpt-5.3-codex"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stream call %d model = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestManagerExecuteCount_CodexAPIKeyAliasPoolStopsOnInvalidRequest(t *testing.T) {
	alias := "gpt-latest"
	invalidErr := &Error{HTTPStatus: http.StatusUnprocessableEntity, Message: "unprocessable entity"}
	executor := &apiKeyPoolExecutor{
		id:          "codex",
		countErrors: map[string]error{"gpt-5.2": invalidErr},
	}
	m := newCodexAPIKeyPoolTestManager(t, alias, []internalconfig.CodexModel{
		{Name: "gpt-5.2", Alias: alias},
		{Name: "gpt-5.3-codex", Alias: alias},
	}, executor)

	_, err := m.ExecuteCount(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: alias}, cliproxyexecutor.Options{})
	if err == nil || err.Error() != invalidErr.Error() {
		t.Fatalf("execute count error = %v, want %v", err, invalidErr)
	}
	got := executor.CountModels()
	if len(got) != 1 || got[0] != "gpt-5.2" {
		t.Fatalf("count calls = %v, want only first invalid model", got)
	}
}
