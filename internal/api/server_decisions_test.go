package api

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func newDecisionsTestServer(t *testing.T, cfg *config.Config, model string, credentials ...*auth.Auth) *Server {
	t.Helper()
	cfg.APIKeys = []string{"test-key"}
	server := newTestServerWithConfig(t, cfg)
	manager := server.handlers.AuthManager
	manager.SetConfig(cfg)
	manager.RegisterExecutor(executor.NewCodexAutoExecutor(cfg))
	for _, credential := range credentials {
		if _, errRegister := manager.Register(t.Context(), credential); errRegister != nil {
			t.Fatal(errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient(credential.ID, credential.Provider, []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(credential.ID) })
	}
	return server
}

func TestDecisionsPassthrough(t *testing.T) {
	const payload = `{"model":"vendor/decision-model","input":[{"role":"user","content":[{"type":"input_json","data":{"paid":false}}]}],"questions":[{"type":"predicate","name":"paid","instructions":"Is it paid?"}],"prompt_cache_key":"caller-cache"}`
	const expected = `{"model":"gpt-6-luna","input":[{"role":"user","content":[{"type":"input_json","data":{"paid":false}}]}],"questions":[{"type":"predicate","name":"paid","instructions":"Is it paid?"}],"prompt_cache_key":"caller-cache"}`
	const response = "{\n  \"model\":\"gpt-6-luna\",\"answers\":[{\"type\":\"predicate\",\"name\":\"paid\",\"probability\":0.02}],\"usage\":{\"input_tokens\":42,\"output_tokens\":3,\"total_tokens\":45}\n}\n"
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Error(errRead)
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/decisions" {
			t.Errorf("request = %s %s, want POST /v1/decisions", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer upstream-key" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("OpenAI-Project"); got != "project-id" {
			t.Errorf("OpenAI-Project = %q", got)
		}
		if got := r.Header.Get("Content-Encoding"); got != "" {
			t.Errorf("plaintext request carries Content-Encoding %q", got)
		}
		if got := r.Header.Get("Accept-Encoding"); got != "gzip" {
			t.Errorf("Accept-Encoding = %q, want configured gzip", got)
		}
		if string(body) != expected {
			t.Errorf("upstream body = %s, want %s", body, expected)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", "decision-request")
		w.Header().Set("Content-Encoding", "gzip")
		compressed := gzip.NewWriter(w)
		if _, errWrite := io.WriteString(compressed, response); errWrite != nil {
			t.Error(errWrite)
		}
		if errClose := compressed.Close(); errClose != nil {
			t.Error(errClose)
		}
	}))
	t.Cleanup(upstream.Close)
	cfg := &config.Config{
		SDKConfig: sdkconfig.SDKConfig{PassthroughHeaders: true},
		CodexKey:  []config.CodexKey{{APIKey: "upstream-key", BaseURL: upstream.URL + "/v1/", Prefix: "vendor", Models: []config.CodexModel{{Name: "gpt-6-luna", Alias: "decision-model"}}}},
	}
	server := newDecisionsTestServer(t, cfg, "vendor/decision-model",
		&auth.Auth{ID: t.Name() + "-oauth", Provider: "codex", Prefix: "vendor", Status: auth.StatusActive, Attributes: map[string]string{"priority": "100"}, Metadata: map[string]any{"access_token": "must-not-use", "account_id": "oauth-account"}},
		&auth.Auth{ID: t.Name() + "-key", Provider: "codex", Prefix: "vendor", Status: auth.StatusActive, Attributes: map[string]string{"api_key": "upstream-key", "base_url": upstream.URL + "/v1/", "header:OpenAI-Project": "$X-Project", "header:Accept-Encoding": "gzip", "header:Content-Encoding": "gzip"}},
	)
	gateway := httptest.NewServer(server.engine)
	t.Cleanup(gateway.Close)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, gateway.URL+"/v1/decisions", strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer test-key")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Project", "project-id")
	resp, err := gateway.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || string(body) != response {
		t.Fatalf("response = %d %s, want 200 %s", resp.StatusCode, body, response)
	}
	if got := resp.Header.Get("X-Request-Id"); got != "decision-request" {
		t.Fatalf("X-Request-Id = %q", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls = %d, want 1", calls.Load())
	}
}

func TestDecisionsPayloadRulesAreFinal(t *testing.T) {
	var received []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		_, _ = io.WriteString(w, `{"model":"configured-model","answers":[],"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer upstream.Close()
	cfg := &config.Config{Payload: config.PayloadConfig{
		Default: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "gpt-6-luna", Protocol: "openai-decisions"}}, Params: map[string]any{"input": "must-not-replace-caller", "prompt_cache_options.ttl": "30m"}}},
		Override: []config.PayloadRule{
			// This earlier rule matches only if the finalizer runs again.
			{Models: []config.PayloadModelRule{{Name: "gpt-6-luna", Protocol: "openai-decisions", Match: []map[string]any{{"prompt_cache_key": "once"}}}}, Params: map[string]any{"prompt_cache_key": "twice"}},
			{Models: []config.PayloadModelRule{{Name: "gpt-6-luna", Protocol: "openai-decisions", FromProtocol: "openai-decisions", Match: []map[string]any{{"model": "gpt-6-luna"}}}}, Params: map[string]any{"model": "configured-model", "questions.0.instructions": "Configured question", "prompt_cache_key": "once"}},
		},
		Filter: []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "gpt-6-luna", Protocol: "openai-decisions"}}, Params: []string{"safety_identifier"}}},
	}}
	server := newDecisionsTestServer(t, cfg, "gpt-6-luna", &auth.Auth{
		ID: t.Name(), Provider: "codex", Status: auth.StatusActive,
		Attributes: map[string]string{"api_key": "upstream-key", "base_url": upstream.URL},
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/decisions", strings.NewReader(`{"model":"gpt-6-luna(high)","input":"caller input","questions":[{"type":"predicate","instructions":"Caller question"}],"safety_identifier":"remove-me"}`))
	request.Header.Set("Authorization", "Bearer test-key")
	recorder := httptest.NewRecorder()
	server.engine.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	root := ojson.ParseBytes(received)
	for path, want := range map[string]string{"model": "configured-model", "input": "caller input", "questions.0.instructions": "Configured question", "prompt_cache_key": "once", "prompt_cache_options.ttl": "30m"} {
		if got := root.Get(path).String(); got != want {
			t.Errorf("%s = %q, want %q; body=%s", path, got, want, received)
		}
	}
	if root.Get("safety_identifier").Exists() || root.Get("stream").Exists() || root.Get("instructions").Exists() || root.Get("tools").Exists() {
		t.Fatalf("filtered field restored or Responses fields injected: %s", received)
	}
}

func TestDecisionsRejectsOAuthAndRequiresClientKey(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"answers":[]}`)
	}))
	defer upstream.Close()
	server := newDecisionsTestServer(t, &config.Config{}, "gpt-6-luna", &auth.Auth{
		ID: t.Name(), Provider: "codex", Status: auth.StatusActive,
		Attributes: map[string]string{"base_url": upstream.URL}, Metadata: map[string]any{"access_token": "oauth-token"},
	})
	for _, tc := range []struct {
		name, key string
		status    int
	}{
		{"no caller key", "", http.StatusUnauthorized},
		{"OAuth-only pool", "test-key", http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/decisions", strings.NewReader(`{"model":"gpt-6-luna","input":"test","questions":[{"type":"predicate","instructions":"Test?"}]}`))
			if tc.key != "" {
				req.Header.Set("Authorization", "Bearer "+tc.key)
			}
			rr := httptest.NewRecorder()
			server.engine.ServeHTTP(rr, req)
			if rr.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", rr.Code, tc.status, rr.Body.String())
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("ineligible requests reached upstream %d times", calls.Load())
	}
}

func TestDecisionsRetriesAPIKeysAndPreservesFinalError(t *testing.T) {
	for _, recover := range []bool{true, false} {
		t.Run(map[bool]string{true: "failover", false: "final upstream error"}[recover], func(t *testing.T) {
			const errorBody = "{\n \"error\": {\"message\":\"rate limited\",\"type\":\"rate_limit_error\",\"code\":\"rate_limit_exceeded\",\"extra\":true}\n}\n"
			var keys []string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				keys = append(keys, r.Header.Get("Authorization"))
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Request-Id", "decision-limit")
				if recover && len(keys) == 2 {
					_, _ = io.WriteString(w, `{"model":"gpt-6-luna","answers":[],"usage":{"input_tokens":1,"output_tokens":1}}`)
					return
				}
				w.Header().Set("Retry-After", "60")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = io.WriteString(w, errorBody)
			}))
			defer upstream.Close()
			cfg := &config.Config{SDKConfig: sdkconfig.SDKConfig{PassthroughHeaders: true}}
			server := newDecisionsTestServer(t, cfg, "gpt-6-luna",
				&auth.Auth{ID: t.Name() + "-oauth", Provider: "codex", Status: auth.StatusActive, Attributes: map[string]string{"priority": "100", "base_url": upstream.URL}, Metadata: map[string]any{"access_token": "must-not-use"}},
				&auth.Auth{ID: t.Name() + "-first", Provider: "codex", Status: auth.StatusActive, Attributes: map[string]string{"priority": "10", "api_key": "first-key", "base_url": upstream.URL}},
				&auth.Auth{ID: t.Name() + "-second", Provider: "codex", Status: auth.StatusActive, Attributes: map[string]string{"api_key": "second-key", "base_url": upstream.URL}},
			)
			server.handlers.AuthManager.SetRetryConfig(0, 0, 2)
			req := httptest.NewRequest(http.MethodPost, "/v1/decisions", strings.NewReader(`{"model":"gpt-6-luna","input":"test","questions":[{"type":"predicate","instructions":"Test?"}]}`))
			req.Header.Set("Authorization", "Bearer test-key")
			rr := httptest.NewRecorder()
			server.engine.ServeHTTP(rr, req)
			if want := []string{"Bearer first-key", "Bearer second-key"}; !reflect.DeepEqual(keys, want) {
				t.Fatalf("selected keys = %v, want %v", keys, want)
			}
			if recover {
				if rr.Code != http.StatusOK {
					t.Fatalf("failover status = %d: %s", rr.Code, rr.Body.String())
				}
			} else {
				if rr.Code != http.StatusTooManyRequests || rr.Body.String() != errorBody {
					t.Fatalf("response = %d %q, want 429 %q", rr.Code, rr.Body.String(), errorBody)
				}
				if rr.Header().Get("Retry-After") == "" || rr.Header().Get("X-Request-Id") != "decision-limit" {
					t.Fatalf("missing upstream headers: %v", rr.Header())
				}
			}
		})
	}
}

func TestDecisionsPreservesNonJSONUpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("X-Request-Id", "not-opted-in")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, "upstream unavailable\n")
	}))
	defer upstream.Close()
	server := newDecisionsTestServer(t, &config.Config{}, "gpt-6-luna", &auth.Auth{
		ID: t.Name(), Provider: "codex", Status: auth.StatusActive,
		Attributes: map[string]string{"api_key": "upstream-key", "base_url": upstream.URL},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/decisions", strings.NewReader(`{"model":"gpt-6-luna","input":"test","questions":[{"type":"predicate","instructions":"Test?"}]}`))
	req.Header.Set("Authorization", "Bearer test-key")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway || rr.Body.String() != "upstream unavailable\n" || rr.Header().Get("Content-Type") != "text/plain" {
		t.Fatalf("response = %d %q (%s)", rr.Code, rr.Body.String(), rr.Header().Get("Content-Type"))
	}
	if rr.Header().Get("X-Request-Id") != "" {
		t.Fatal("upstream headers forwarded without passthrough opt-in")
	}
}
