package auth

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

type httpRequestTestExecutor struct {
	id string

	mu            sync.Mutex
	authIDs       []string
	requestModels []string
	statusByModel map[string]int
}

func (e *httpRequestTestExecutor) Identifier() string { return e.id }

func (e *httpRequestTestExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "Execute not implemented"}
}

func (e *httpRequestTestExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "ExecuteStream not implemented"}
}

func (e *httpRequestTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *httpRequestTestExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "CountTokens not implemented"}
}

func (e *httpRequestTestExecutor) HttpRequest(_ context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	model := req.Header.Get("X-Model")

	e.mu.Lock()
	if auth != nil {
		e.authIDs = append(e.authIDs, auth.ID)
	}
	e.requestModels = append(e.requestModels, model)
	status := http.StatusOK
	if e.statusByModel != nil {
		if candidate, ok := e.statusByModel[model]; ok {
			status = candidate
		}
	}
	e.mu.Unlock()

	return &http.Response{
		StatusCode: status,
		Header: http.Header{
			"Content-Type": []string{"text/plain; charset=utf-8"},
			"X-Model":      []string{model},
		},
		Body: io.NopCloser(strings.NewReader(model)),
	}, nil
}

func (e *httpRequestTestExecutor) AuthIDs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.authIDs))
	copy(out, e.authIDs)
	return out
}

func (e *httpRequestTestExecutor) RequestModels() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.requestModels))
	copy(out, e.requestModels)
	return out
}

func TestManagerExecuteHTTPRequest_AppliesOpenAICompatModelPool(t *testing.T) {
	const alias = "claude-opus-4.66"

	cfg := &internalconfig.Config{
		OpenAICompatibility: []internalconfig.OpenAICompatibility{{
			Name: "pool",
			Models: []internalconfig.OpenAICompatibilityModel{
				{Name: "qwen3.5-plus", Alias: alias},
				{Name: "glm-5", Alias: alias},
			},
		}},
	}

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(cfg)

	executor := &httpRequestTestExecutor{
		id: "pool",
		statusByModel: map[string]int{
			"qwen3.5-plus": http.StatusInternalServerError,
			"glm-5":        http.StatusOK,
		},
	}
	manager.RegisterExecutor(executor)

	auth := &Auth{
		ID:       "pool-auth",
		Provider: "pool",
		Status:   StatusActive,
		Attributes: map[string]string{
			"api_key":      "test-key",
			"compat_name":  "pool",
			"provider_key": "pool",
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "pool", []*registry.ModelInfo{{ID: alias}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	var builtModels []string
	resp, selectedAuth, err := manager.ExecuteHTTPRequest(context.Background(), []string{"pool"}, alias, alias, cliproxyexecutor.Options{}, func(ctx context.Context, auth *Auth, upstreamModel string) (*http.Request, error) {
		builtModels = append(builtModels, upstreamModel)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.com/http", nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("X-Model", upstreamModel)
		return req, nil
	})
	if err != nil {
		t.Fatalf("ExecuteHTTPRequest() error = %v", err)
	}
	if selectedAuth == nil || selectedAuth.ID != auth.ID {
		t.Fatalf("selected auth = %#v, want %q", selectedAuth, auth.ID)
	}
	if got := builtModels; len(got) != 2 || got[0] != "qwen3.5-plus" || got[1] != "glm-5" {
		t.Fatalf("built models = %v, want [qwen3.5-plus glm-5]", got)
	}
	if got := executor.RequestModels(); len(got) != 2 || got[0] != "qwen3.5-plus" || got[1] != "glm-5" {
		t.Fatalf("request models = %v, want [qwen3.5-plus glm-5]", got)
	}
	if resp == nil {
		t.Fatalf("expected non-nil response")
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll(response body): %v", err)
	}
	_ = resp.Body.Close()
	if string(body) != "glm-5" {
		t.Fatalf("response body = %q, want %q", string(body), "glm-5")
	}
}

func TestManagerExecuteHTTPRequest_RetriesAcrossAuthsOnBuildFailure(t *testing.T) {
	manager := NewManager(nil, nil, nil)

	executor := &httpRequestTestExecutor{id: "pool"}
	manager.RegisterExecutor(executor)

	auth1 := &Auth{ID: "auth1", Provider: "pool", Status: StatusActive}
	auth2 := &Auth{ID: "auth2", Provider: "pool", Status: StatusActive}
	if _, err := manager.Register(context.Background(), auth1); err != nil {
		t.Fatalf("register auth1: %v", err)
	}
	if _, err := manager.Register(context.Background(), auth2); err != nil {
		t.Fatalf("register auth2: %v", err)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth1.ID, "pool", []*registry.ModelInfo{{ID: "test-model"}})
	reg.RegisterClient(auth2.ID, "pool", []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth1.ID)
		reg.UnregisterClient(auth2.ID)
	})

	var buildAuthIDs []string
	resp, selectedAuth, err := manager.ExecuteHTTPRequest(context.Background(), []string{"pool"}, "test-model", "test-model", cliproxyexecutor.Options{}, func(ctx context.Context, auth *Auth, upstreamModel string) (*http.Request, error) {
		buildAuthIDs = append(buildAuthIDs, auth.ID)
		if auth.ID == "auth1" {
			return nil, &Error{HTTPStatus: http.StatusBadGateway, Message: "build failed for auth1"}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.com/http", nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("X-Model", upstreamModel)
		return req, nil
	})
	if err != nil {
		t.Fatalf("ExecuteHTTPRequest() error = %v", err)
	}
	if selectedAuth == nil || selectedAuth.ID != "auth2" {
		t.Fatalf("selected auth = %#v, want auth2", selectedAuth)
	}
	if got := buildAuthIDs; len(got) != 2 || got[0] != "auth1" || got[1] != "auth2" {
		t.Fatalf("build auth IDs = %v, want [auth1 auth2]", got)
	}
	if got := executor.AuthIDs(); len(got) != 1 || got[0] != "auth2" {
		t.Fatalf("executor auth IDs = %v, want [auth2]", got)
	}
	if resp == nil {
		t.Fatalf("expected non-nil response")
	}
	_ = resp.Body.Close()
}
