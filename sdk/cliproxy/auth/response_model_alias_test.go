package auth

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

type responseAliasExecutor struct {
	id string

	mu             sync.Mutex
	executeModels  []string
	streamModels   []string
	executePayload map[string][]byte
	streamPayload  map[string][]cliproxyexecutor.StreamChunk
}

func (e *responseAliasExecutor) Identifier() string { return e.id }

func (e *responseAliasExecutor) Execute(_ context.Context, _ *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.executeModels = append(e.executeModels, req.Model)
	payload := append([]byte(nil), e.executePayload[req.Model]...)
	e.mu.Unlock()
	return cliproxyexecutor.Response{Payload: payload}, nil
}

func (e *responseAliasExecutor) ExecuteStream(_ context.Context, _ *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.mu.Lock()
	e.streamModels = append(e.streamModels, req.Model)
	chunks := append([]cliproxyexecutor.StreamChunk(nil), e.streamPayload[req.Model]...)
	e.mu.Unlock()
	ch := make(chan cliproxyexecutor.StreamChunk, max(1, len(chunks)))
	for _, chunk := range chunks {
		ch <- chunk
	}
	close(ch)
	return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Model": {req.Model}}, Chunks: ch}, nil
}

func (e *responseAliasExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *responseAliasExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "CountTokens not implemented"}
}

func (e *responseAliasExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "HttpRequest not implemented"}
}

func (e *responseAliasExecutor) ExecuteModels() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.executeModels))
	copy(out, e.executeModels)
	return out
}

func (e *responseAliasExecutor) StreamModels() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.streamModels))
	copy(out, e.streamModels)
	return out
}

func newOpenAICompatAliasResponseTestManager(t *testing.T, alias string, models []internalconfig.OpenAICompatibilityModel, executor *responseAliasExecutor) *Manager {
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
		executor = &responseAliasExecutor{id: "pool"}
	}
	m.RegisterExecutor(executor)

	auth := &Auth{
		ID:       "alias-auth-" + t.Name(),
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

func TestManagerExecute_OpenAICompatExplicitAliasRewritesResponseModel(t *testing.T) {
	const (
		alias    = "glm-4.7"
		upstream = "MiniMax-M2.7-highspeed"
	)
	executor := &responseAliasExecutor{
		id: "pool",
		executePayload: map[string][]byte{
			upstream: []byte(`{"id":"resp_1","model":"MiniMax-M2.7-highspeed","choices":[]}`),
		},
	}
	manager := newOpenAICompatAliasResponseTestManager(t, alias, []internalconfig.OpenAICompatibilityModel{
		{Name: upstream, Alias: alias},
	}, executor)

	resp, err := manager.Execute(context.Background(), []string{"pool"}, cliproxyexecutor.Request{Model: alias}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := gjson.GetBytes(resp.Payload, "model").String(); got != alias {
		t.Fatalf("response model = %q, want %q", got, alias)
	}
	gotModels := executor.ExecuteModels()
	if len(gotModels) != 1 || gotModels[0] != upstream {
		t.Fatalf("execute models = %v, want [%s]", gotModels, upstream)
	}
}

func TestManagerExecuteStream_OpenAICompatExplicitAliasRewritesSSEModel(t *testing.T) {
	const (
		alias    = "glm-4.7"
		upstream = "MiniMax-M2.7-highspeed"
	)
	executor := &responseAliasExecutor{
		id: "pool",
		streamPayload: map[string][]cliproxyexecutor.StreamChunk{
			upstream: {
				{Payload: []byte("data: {\"id\":\"resp_1\",\"model\":\"MiniMax-M2.7-highspeed\",\"choices\":[]}\n\n")},
				{Payload: []byte("data: [DONE]\n\n")},
			},
		},
	}
	manager := newOpenAICompatAliasResponseTestManager(t, alias, []internalconfig.OpenAICompatibilityModel{
		{Name: upstream, Alias: alias},
	}, executor)

	result, err := manager.ExecuteStream(context.Background(), []string{"pool"}, cliproxyexecutor.Request{Model: alias}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("execute stream: %v", err)
	}
	var payload strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		payload.Write(chunk.Payload)
	}
	got := payload.String()
	if !strings.Contains(got, `"model":"glm-4.7"`) {
		t.Fatalf("stream payload = %q, want rewritten alias model", got)
	}
	if !strings.Contains(got, "data: [DONE]") {
		t.Fatalf("stream payload = %q, want DONE marker preserved", got)
	}
	gotModels := executor.StreamModels()
	if len(gotModels) != 1 || gotModels[0] != upstream {
		t.Fatalf("stream models = %v, want [%s]", gotModels, upstream)
	}
}

func TestManagerRequestedResponseModelAlias_RequiresExplicitAlias(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{})
	auth := &Auth{
		ID:       "plain-auth",
		Provider: "pool",
		Status:   StatusActive,
		Attributes: map[string]string{
			"api_key":      "test-key",
			"compat_name":  "pool",
			"provider_key": "pool",
		},
	}
	opts := cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.RequestedModelMetadataKey: "glm-4.7",
		},
	}
	if got := manager.requestedResponseModelAlias(auth, opts, "glm-4.7", "MiniMax-M2.7-highspeed"); got != "" {
		t.Fatalf("requested response alias = %q, want empty", got)
	}
}
