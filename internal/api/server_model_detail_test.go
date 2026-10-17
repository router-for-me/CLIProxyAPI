package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

type issue5190LifecycleHost struct {
	mockModelListInterceptorHost
	completions []pluginapi.RequestCompletion
}

func (h *issue5190LifecycleHost) CompleteRequest(_ context.Context, completion pluginapi.RequestCompletion) {
	h.completions = append(h.completions, completion)
}

func TestIssue5190ModelDetailLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name   string
		body   string
		status int
	}{
		{"visible", `{"data":[{"id":"visible"}]}`, http.StatusOK},
		{"missing", `{"data":[]}`, http.StatusNotFound},
		{"invalid", `invalid`, http.StatusBadGateway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newTestServer(t)
			host := &issue5190LifecycleHost{}
			host.interceptResponse = func(_ context.Context, _ pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
				return pluginapi.ResponseInterceptResponse{Body: []byte(tc.body)}
			}
			server.handlers.SetPluginHost(host)
			req := httptest.NewRequest(http.MethodGet, "/v1/models/visible", nil)
			req.Header.Set("Authorization", "Bearer test-key")
			rr := httptest.NewRecorder()
			server.engine.ServeHTTP(rr, req)
			if rr.Code != tc.status || len(host.completions) != 1 {
				t.Fatalf("status=%d completions=%+v", rr.Code, host.completions)
			}
			completion := host.completions[0]
			wantOutcome := pluginapi.RequestCompletionSucceeded
			if tc.status != http.StatusOK {
				wantOutcome = pluginapi.RequestCompletionFailed
			}
			if completion.StatusCode != tc.status || completion.Outcome != wantOutcome {
				t.Fatalf("completion=%+v want status=%d outcome=%s", completion, tc.status, wantOutcome)
			}
		})
	}
}

func TestIssue5190ModelDetailUsesVisibleCatalog(t *testing.T) {
	server := newTestServer(t)
	calls := 0
	server.handlers.SetPluginHost(&mockModelListInterceptorHost{
		interceptResponse: func(_ context.Context, req pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
			calls++
			return pluginapi.ResponseInterceptResponse{Headers: http.Header{"Content-Length": []string{"98"}}, Body: []byte(`{"object":"list","data":[{"id":"vendor/visible","object":"model","owned_by":"plugin"}]}`)}
		},
	})
	for _, tc := range []struct {
		id     string
		status int
	}{{"vendor/visible", 200}, {"hidden", 404}} {
		req := httptest.NewRequest(http.MethodGet, "/v1/models/"+tc.id, nil)
		req.Header.Set("Authorization", "Bearer test-key")
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)
		if length := rr.Header().Get("Content-Length"); length != "" {
			t.Errorf("stale catalog content length: %s", length)
		}
		if rr.Code != tc.status {
			t.Errorf("%s: status=%d want=%d body=%s", tc.id, rr.Code, tc.status, rr.Body.String())
		}
		if tc.status == 200 && gjson.Get(rr.Body.String(), "id").String() != tc.id {
			t.Errorf("wrong model: %s", rr.Body.String())
		}
	}
	if calls != 2 {
		t.Errorf("catalog interceptor calls=%d want=2", calls)
	}
}

func TestIssue5190ModelDetailHomeVisibility(t *testing.T) {
	server := newTestServer(t)
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(t.Name(), "openai", []*registry.ModelInfo{{ID: "local/only", Object: "model"}})
	t.Cleanup(func() { modelRegistry.UnregisterClient(t.Name()) })
	engine := server.engine
	get := func(id string, authorized bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/v1/models/"+id, nil)
		if authorized {
			req.Header.Set("Authorization", "Bearer test-key")
		}
		rr := httptest.NewRecorder()
		engine.ServeHTTP(rr, req)
		return rr
	}
	if rr := get("local/only", true); rr.Code != 200 {
		t.Fatalf("local detail: %d %s", rr.Code, rr.Body.String())
	}
	if rr := get("local/only", false); rr.Code != 401 {
		t.Fatalf("unauthorized detail: %d", rr.Code)
	}
	server.cfg.Home.Enabled = true
	// The catalog fixture does not implement Home's readiness handshake.
	// Reuse the registered route without the unrelated readiness middleware.
	engine = gin.New()
	for _, route := range server.engine.Routes() {
		if route.Method == http.MethodGet && route.Path == "/v1/models/*model" {
			engine.GET(route.Path, route.HandlerFunc)
		}
	}
	previousHome := home.Current()
	home.SetCurrent(newHomeCatalogClient(t, `{"openai":[{"id":"home/visible"}]}`))
	t.Cleanup(func() { home.SetCurrent(previousHome) })
	if rr := get("home/visible", true); rr.Code != 200 || gjson.Get(rr.Body.String(), "id").String() != "home/visible" {
		t.Fatalf("Home detail: %d %s", rr.Code, rr.Body.String())
	}
	if rr := get("local/only", true); rr.Code != 404 {
		t.Fatalf("local registry leaked into Home: %d %s", rr.Code, rr.Body.String())
	}
}
