package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	cpaauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	cpaexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestHomeV8PromptCacheKey(t *testing.T) {
	auth := &cpaauth.Auth{Provider: "compat", Attributes: map[string]string{"compat_name": "compat", "provider_key": "compat", "api_key": "fixture", "base_url": "https://example.test", "support_prompt_cache_key": "true"}, Metadata: map[string]any{"credential_options": map[string]any{"support-prompt-cache-key": true}}}
	for _, home := range []bool{false, true} {
		cfg := &config.Config{}
		cfg.Home.Enabled = home
		if !home {
			cfg.OpenAICompatibility = []config.OpenAICompatibility{{Name: "compat", SupportPromptCacheKey: true}}
		}
		e := NewOpenAICompatExecutor("compat", cfg)
		out, err := e.applyPromptCacheKey(context.Background(), auth, translator.FromString("openai"), "review-model", cpaexecutor.Request{Payload: []byte(`{"model":"review-model","prompt_cache_key":"fixture-cache-key"}`)}, cpaexecutor.Options{}, []byte(`{"model":"review-model"}`))
		if err != nil {
			t.Fatal(err)
		}
		got := gjson.GetBytes(out, "prompt_cache_key").String()
		t.Logf("home=%v prompt_cache_key=%q", home, got)
		if got != "fixture-cache-key" {
			t.Errorf("home=%v: enabled prompt cache option is not consumed", home)
		}
	}
}

func TestHomeV8CompatOptionsReachUpstream(t *testing.T) {
	bodies := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		bodies <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"fixture","choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()
	cfg := &config.Config{}
	cfg.Home.Enabled = true
	e := NewOpenAICompatExecutor("home-compat", cfg)
	for _, enabled := range []bool{true, false, true} {
		auth := &cpaauth.Auth{Provider: "home-compat", Attributes: map[string]string{"compat_name": "home-compat", "api_key": "fixture", "base_url": server.URL + "/v1"}, Metadata: map[string]any{"credential_options": map[string]any{
			"support-prompt-cache-key": enabled,
			"models":                   []any{map[string]any{"name": "upstream", "use-max-completion-tokens": enabled}},
		}}}
		payload := []byte(`{"model":"upstream","input":[{"role":"user","content":"hi"}],"max_output_tokens":512,"prompt_cache_key":"fixture-cache"}`)
		_, err := e.Execute(context.Background(), auth, cpaexecutor.Request{Model: "upstream", Payload: payload}, cpaexecutor.Options{SourceFormat: translator.FromString("openai-response")})
		if err != nil {
			t.Fatal(err)
		}
		body := <-bodies
		field := "max_tokens"
		if enabled {
			field = "max_completion_tokens"
		}
		if gjson.GetBytes(body, field).Int() != 512 {
			t.Fatalf("enabled=%v missing %s in %s", enabled, field, body)
		}
		if enabled && gjson.GetBytes(body, "prompt_cache_key").String() != "fixture-cache" {
			t.Fatalf("prompt cache missing: %s", body)
		}
	}
}

func TestHomeV8PromptCacheOverrideAndFallback(t *testing.T) {
	for _, tc := range []struct {
		name         string
		home         bool
		attrs        map[string]string
		metadata     map[string]any
		global, want bool
	}{
		{name: "explicit false", home: true, attrs: map[string]string{"support_prompt_cache_key": "false"}, metadata: map[string]any{"credential_options": map[string]any{"support-prompt-cache-key": true}}, global: true, want: false},
		{name: "older Home fallback", home: true, global: true, want: true},
		{name: "standalone ignores Home options", home: false, metadata: map[string]any{"credential_options": map[string]any{"support-prompt-cache-key": true}}, want: false},
		{name: "metadata only", home: true, metadata: map[string]any{"credential_options": map[string]any{"support-prompt-cache-key": true}}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{Name: "compat", SupportPromptCacheKey: tc.global}}}
			cfg.Home.Enabled = tc.home
			if tc.attrs == nil {
				tc.attrs = map[string]string{}
			}
			tc.attrs["compat_name"] = "compat"
			e := NewOpenAICompatExecutor("compat", cfg)
			auth := &cpaauth.Auth{Provider: "compat", Attributes: tc.attrs, Metadata: tc.metadata}
			result, err := e.applyPromptCacheKey(context.Background(), auth, translator.FromString("openai"), "model", cpaexecutor.Request{Payload: []byte(`{"prompt_cache_key":"cache"}`)}, cpaexecutor.Options{}, []byte(`{"model":"model"}`))
			if err != nil {
				t.Fatal(err)
			}
			if got := gjson.GetBytes(result, "prompt_cache_key").Exists(); got != tc.want {
				t.Fatalf("cache key present=%v, want=%v", got, tc.want)
			}
		})
	}
}

type compatOptionsHomeDispatcher []byte

func (compatOptionsHomeDispatcher) HeartbeatOK() bool       { return true }
func (compatOptionsHomeDispatcher) AbortAmbiguousDispatch() {}
func (d compatOptionsHomeDispatcher) RPopAuth(context.Context, string, string, http.Header, int) ([]byte, error) {
	return d, nil
}

type compatOptionsModelRouter string

func (compatOptionsModelRouter) HasModelRouters() bool { return true }
func (r compatOptionsModelRouter) RouteModel(context.Context, pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
	return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetProvider, Target: "home-compat", TargetModel: string(r)}, true
}

func TestHomeCompatOptionsUseSelectedRoute(t *testing.T) {
	for _, tc := range []struct {
		name, client, route, upstream string
		withoutModelInfo              bool
	}{
		{name: "alias", client: "alias-b", route: "alias-b", upstream: "upstream"},
		{name: "plugin reroute", client: "alias-a", route: "alias-b", upstream: "upstream"},
		{name: "prefix and suffix", client: "alias-a", route: "tenant/alias-b(high)", upstream: "upstream(high)"},
		{name: "without model info", client: "alias-a", route: "alias-b", upstream: "upstream", withoutModelInfo: true},
	} {
		for _, enabled := range []bool{true, false} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/enabled=%v/stream=%v", tc.name, enabled, stream), func(t *testing.T) {
					bodies := make(chan []byte, 1)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						body, errRead := io.ReadAll(r.Body)
						if errRead != nil {
							t.Error(errRead)
						}
						bodies <- body
						if stream {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = io.WriteString(w, "data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n")
							return
						}
						_, _ = io.WriteString(w, `{"id":"fixture","choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
					}))
					defer server.Close()
					modalities, otherModalities := []string{"text"}, []string{"text", "image"}
					if !enabled {
						modalities, otherModalities = otherModalities, modalities
					}
					auth := &cpaauth.Auth{ID: "fixture", Provider: "home-compat", Prefix: "tenant", Attributes: map[string]string{
						"compat_name": "home-compat", "provider_key": "home-compat", "api_key": "fixture", "base_url": server.URL + "/v1", "auth_kind": "apikey",
					}, Metadata: map[string]any{"credential_options": map[string]any{"models": []config.OpenAICompatibilityModel{
						{Name: "upstream", Alias: "alias-a", UseMaxCompletionTokens: !enabled, InputModalities: otherModalities},
						{Name: "upstream", Alias: "alias-b", UseMaxCompletionTokens: enabled, InputModalities: modalities},
					}}}}
					dispatch := map[string]any{"model": tc.upstream, "auth": auth}
					if !tc.withoutModelInfo {
						dispatch["model_info"] = map[string]any{"id": "upstream", "user_defined": true, "thinking": map[string]any{"levels": []string{"high"}}}
					}
					wire, errMarshal := json.Marshal(dispatch)
					if errMarshal != nil {
						t.Fatal(errMarshal)
					}
					cfg := &config.Config{Home: config.HomeConfig{Enabled: true}}
					manager := cpaauth.NewManager(nil, nil, nil)
					manager.SetConfig(cfg)
					manager.PublishHomeDispatch(compatOptionsHomeDispatcher(wire), executionregistry.New(), 1)
					manager.RegisterExecutor(NewOpenAICompatExecutor("openai-compatibility", cfg))
					handler := handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)
					handler.SetModelRouterHost(compatOptionsModelRouter(tc.route))
					payload := []byte(fmt.Sprintf(`{"model":%q,"max_tokens":64,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call","name":"read","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call","content":[{"type":"text","text":"result"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA=="}}]}]}]}`, tc.client))
					if stream {
						chunks, _, errs := handler.ExecuteStreamWithAuthManager(t.Context(), "claude", tc.client, payload, "")
						for range chunks {
						}
						for errStream := range errs {
							if errStream != nil {
								t.Fatal(errStream)
							}
						}
					} else if _, _, errExecute := handler.ExecuteWithAuthManager(t.Context(), "claude", tc.client, payload, ""); errExecute != nil {
						t.Fatal(errExecute)
					}
					body := <-bodies
					field, otherField := "max_tokens", "max_completion_tokens"
					if enabled {
						field, otherField = otherField, field
					}
					if gjson.GetBytes(body, field).Int() != 64 || gjson.GetBytes(body, otherField).Exists() {
						t.Errorf("expected only %s=64 in %s", field, body)
					}
					if hasImage := strings.Contains(string(body), "image_url"); hasImage == enabled {
						t.Errorf("image present=%v, want %v in %s", hasImage, !enabled, body)
					}
				})
			}
		}
	}
}
