package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
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
		if tc.status == 200 && ojson.GetBytesNoCopy(rr.Body.Bytes(), "id").String() != tc.id {
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
	if rr := get("home/visible", true); rr.Code != 200 || ojson.GetBytesNoCopy(rr.Body.Bytes(), "id").String() != "home/visible" {
		t.Fatalf("Home detail: %d %s", rr.Code, rr.Body.String())
	}
	if rr := get("local/only", true); rr.Code != 404 {
		t.Fatalf("local registry leaked into Home: %d %s", rr.Code, rr.Body.String())
	}
}

func TestGeminiModelDetailUsesFilteredCatalog(t *testing.T) {
	for _, useHome := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "home"}[useHome], func(t *testing.T) {
			server := newTestServer(t)
			modelRegistry := registry.GetGlobalRegistry()
			modelRegistry.RegisterClient(t.Name(), "gemini", []*registry.ModelInfo{{ID: "visible", Name: "visible", DisplayName: "Visible"}, {ID: "hidden", Name: "hidden"}})
			t.Cleanup(func() { modelRegistry.UnregisterClient(t.Name()) })
			engine := server.engine
			if useHome {
				server.cfg.Home.Enabled = true
				engine = gin.New()
				engine.GET("/v1beta/models", server.geminiModelsHandler(nil))
				engine.GET("/v1beta/models/*action", server.geminiGetHandler(nil))
				previousHome := home.Current()
				home.SetCurrent(newHomeCatalogClient(t, `{"gemini":[{"id":"visible","display_name":"Visible"},{"id":"hidden"}]}`))
				t.Cleanup(func() { home.SetCurrent(previousHome) })
			}
			get := func(path string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodGet, path, nil)
				req.Header.Set("Authorization", "Bearer test-key")
				rr := httptest.NewRecorder()
				engine.ServeHTTP(rr, req)
				return rr
			}
			before := get("/v1beta/models/visible")
			if before.Code != http.StatusOK {
				t.Fatalf("unfiltered detail: %d %s", before.Code, before.Body.String())
			}
			calls := 0
			server.handlers.SetPluginHost(&mockModelListInterceptorHost{interceptResponse: func(_ context.Context, req pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
				calls++
				var catalog struct {
					Models []map[string]any `json:"models"`
				}
				if err := json.Unmarshal(req.Body, &catalog); err != nil {
					t.Fatal(err)
				}
				filtered := make([]map[string]any, 0, len(catalog.Models))
				for _, model := range catalog.Models {
					if model["name"] != "models/hidden" {
						filtered = append(filtered, model)
					}
				}
				body, err := json.Marshal(gin.H{"models": filtered})
				if err != nil {
					t.Fatal(err)
				}
				return pluginapi.ResponseInterceptResponse{Body: body}
			}})
			after := get("/v1beta/models/models/visible")
			var beforeBody, afterBody map[string]any
			if err := json.Unmarshal(before.Body.Bytes(), &beforeBody); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(after.Body.Bytes(), &afterBody); err != nil {
				t.Fatal(err)
			}
			if after.Code != http.StatusOK || !reflect.DeepEqual(beforeBody, afterBody) {
				t.Fatalf("visible detail changed: %d %s; before %s", after.Code, after.Body.String(), before.Body.String())
			}
			if rr := get("/v1beta/models/hidden"); rr.Code != http.StatusNotFound {
				t.Fatalf("filtered detail: %d %s", rr.Code, rr.Body.String())
			}
			if rr := get("/v1beta/models"); rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), "models/hidden") {
				t.Fatalf("filtered list: %d %s", rr.Code, rr.Body.String())
			}
			if calls != 3 {
				t.Fatalf("interceptor calls = %d, want 3", calls)
			}
		})
	}
}
