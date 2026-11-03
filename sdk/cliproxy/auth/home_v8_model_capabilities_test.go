package auth

import (
	"encoding/json"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestHomeV8ModelCapabilitiesAndLegacyPayload(t *testing.T) {
	for _, tc := range []struct {
		name, raw     string
		contextLength int
		compat        bool
	}{
		{"v8", `{"id":"upstream","context_length":32768,"thinking":{"levels":["high","none"],"zero_allowed":true},"user_defined":false}`, 32768, true},
		{"v8-with-redundant-fields", `{"id":"upstream","context_length":32768,"max_context_length":32768,"is_compat":false,"thinking":{"levels":["high","none"],"zero_allowed":true},"user_defined":false}`, 32768, true},
		{"legacy", `{"id":"upstream","context_length":16384,"user_defined":true}`, 16384, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var wire homeDispatchModelInfo
			if err := json.Unmarshal([]byte(tc.raw), &wire); err != nil {
				t.Fatal(err)
			}
			auth := &Auth{}
			if tc.compat {
				auth.Metadata = map[string]any{"credential_options": map[string]any{"models": []internalconfig.CodexModel{{Name: "upstream", Alias: "alias", IsCompat: true}}}}
			}
			req := attachResolvedHomeModelInfo(cliproxyexecutor.Request{Model: "alias"}, auth, "alias", wire.registryModelInfo(), wire.SupportConfigurationUpdate)
			info, ok := ResolvedModelInfo(req)
			if !ok || info.MaxContextLength != 0 || info.IsCompat != tc.compat || info.ContextLength != tc.contextLength || info.UserDefined != wire.UserDefined {
				t.Fatalf("resolved capabilities=%+v", info)
			}
			if tc.compat && (info.Thinking == nil || !info.Thinking.ZeroAllowed || len(info.Thinking.Levels) != 2) {
				t.Fatalf("thinking=%+v", info.Thinking)
			}
		})
	}
}

func TestHomeCodexCompatUsesSelectedCredentialOptions(t *testing.T) {
	cfg := &internalconfig.Config{}
	cfg.Home.Enabled = true
	auth := &Auth{Provider: "codex", Metadata: map[string]any{"credential_options": map[string]any{"models": []any{map[string]any{"name": "upstream", "alias": "alias", "is-compat": true}}}}}
	for _, model := range []string{"upstream", "alias", "upstream(high)"} {
		if !CodexAPIKeyModelIsCompat(cfg, auth, model) {
			t.Errorf("Home model %s lost compat flag", model)
		}
	}
	if CodexAPIKeyModelIsCompat(cfg, &Auth{Provider: "codex"}, "upstream") {
		t.Fatal("compat flag leaked to another credential")
	}
	cfg.Home.Enabled = false
	if CodexAPIKeyModelIsCompat(cfg, auth, "upstream") {
		t.Fatal("standalone began consuming Home options")
	}
}

func TestHomeCompatUsesCredentialModelOptions(t *testing.T) {
	for _, tc := range []struct {
		name, options, upstream, localModel, route string
		want                                       bool
	}{
		{name: "explicit true", options: `{"models":[{"name":"model","is-compat":true}]}`, want: true},
		{name: "explicit false", options: `{"models":[{"name":"model","is-compat":false}]}`},
		{name: "omitted flag defaults false", options: `{"models":[{"name":"model"}]}`},
		{name: "null models", options: `{"models":null}`},
		{name: "empty models", options: `{"models":[]}`},
		{name: "unlisted model", options: `{"models":[{"name":"other","is-compat":true}]}`},
		{name: "missing options default false"},
		{name: "unrelated options default false", options: `{"weight":2}`},
		{name: "legacy different model", localModel: "other"},
		{name: "invalid models default false", options: `{"models":"invalid"}`},
		{name: "upstream wins over another alias", options: `{"models":[{"name":"other","alias":"model","is-compat":true},{"name":"model","is-compat":false}]}`},
		{name: "alias fallback", options: `{"models":[{"name":"upstream","alias":"model","is-compat":true}]}`, want: true},
		{name: "suffix fallback", options: `{"models":[{"name":"model","is-compat":true}]}`, upstream: "model(high)", want: true},
		{name: "exact dispatched suffix", options: `{"models":[{"name":"model","is-compat":true},{"name":"model(high)","is-compat":false}]}`, upstream: "model(high)"},
		{name: "same upstream with different aliases", options: `{"models":[{"name":"model","alias":"other","is-compat":false},{"name":"model","alias":"chosen","is-compat":true}]}`, upstream: "model(high)", route: "tenant/chosen(high)", want: true},
		{name: "same alias with different upstreams", options: `{"models":[{"name":"other","alias":"chosen","is-compat":false},{"name":"model","alias":"chosen","is-compat":true}]}`, route: "tenant/chosen", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth := &Auth{Prefix: "tenant", Attributes: map[string]string{homeUpstreamModelAttributeKey: tc.upstream}}
			if tc.options != "" {
				auth.Metadata = map[string]any{"credential_options": json.RawMessage(tc.options)}
			}
			localModel := tc.localModel
			if localModel == "" {
				localModel = "model"
			}
			local := &registry.ModelInfo{ID: localModel, IsCompat: true}
			homeInfo := &registry.ModelInfo{ID: "model"}
			req := cliproxyexecutor.Request{Model: "alias", Metadata: map[string]any{resolvedAPIKeyModelInfoMetadataKey: local}}
			req = attachResolvedHomeModelInfo(req, auth, tc.route, homeInfo, nil)
			info, ok := ResolvedModelInfo(req)
			if !ok || info.IsCompat != tc.want {
				t.Fatalf("resolved %+v, want compat=%v", info, tc.want)
			}
			if !local.IsCompat || homeInfo.IsCompat {
				t.Fatal("resolution mutated the source model definitions")
			}
		})
	}
}

func TestHomeCompatCredentialOptionsExecutionPaths(t *testing.T) {
	for _, provider := range []string{"codex", "claude", "gemini", "custom-compat"} {
		for _, path := range []string{"execute", "stream", "count"} {
			t.Run(provider+"/"+path, func(t *testing.T) {
				manager := NewManager(nil, nil, nil)
				manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
				dispatcher := &configurationUpdateHomeDispatcher{}
				manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
				executor := &selectedCapabilityCaptureExecutor{provider: provider}
				manager.RegisterExecutor(executor)
				auth := configuredCapabilityTestAuth("home-compat-"+provider, "fixture-key")
				auth.Provider = provider
				for _, enabled := range []bool{true, false} {
					auth.Metadata = map[string]any{"credential_options": map[string]any{"models": []internalconfig.CodexModel{
						{Name: "upstream", Alias: "other", IsCompat: !enabled},
						{Name: "upstream", Alias: "alias", IsCompat: enabled},
					}}}
					payload, errMarshal := json.Marshal(homeAuthDispatchResponse{
						Model: "upstream(high)", Auth: *auth, ModelInfo: &homeDispatchModelInfo{ID: "upstream"},
					})
					if errMarshal != nil {
						t.Fatal(errMarshal)
					}
					dispatcher.payload = payload
					req := cliproxyexecutor.Request{Model: "tenant/alias(high)"}
					opts := cliproxyexecutor.Options{}
					switch path {
					case "execute":
						if _, errExecute := manager.Execute(t.Context(), []string{provider}, req, opts); errExecute != nil {
							t.Fatal(errExecute)
						}
					case "stream":
						stream, errStream := manager.ExecuteStream(t.Context(), []string{provider}, req, opts)
						if errStream != nil {
							t.Fatal(errStream)
						}
						for chunk := range stream.Chunks {
							if chunk.Err != nil {
								t.Fatal(chunk.Err)
							}
						}
					case "count":
						if _, errCount := manager.ExecuteCount(t.Context(), []string{provider}, req, opts); errCount != nil {
							t.Fatal(errCount)
						}
					}
					if len(executor.requests) == 0 {
						t.Fatal("executor received no request")
					}
					info, ok := ResolvedModelInfo(executor.requests[len(executor.requests)-1])
					if !ok || info.IsCompat != enabled {
						t.Fatalf("resolved capabilities=%+v, want compat=%v", info, enabled)
					}
				}
			})
		}
	}
}

func TestHomeCompatDoesNotInheritLocalModelExecutionPaths(t *testing.T) {
	for _, tc := range []struct {
		name, options string
	}{
		{name: "missing options"},
		{name: "unrelated options", options: `{"weight":2}`},
		{name: "invalid models", options: `{"models":"invalid"}`},
	} {
		for _, path := range []string{"execute", "stream", "count"} {
			t.Run(tc.name+"/"+path, func(t *testing.T) {
				manager := NewManager(nil, nil, nil)
				manager.SetConfig(&internalconfig.Config{
					Home: internalconfig.HomeConfig{Enabled: true},
					CodexKey: []internalconfig.CodexKey{{APIKey: "fixture-key", Prefix: "tenant", Models: []internalconfig.CodexModel{
						{Name: "upstream", Alias: "alias", IsCompat: true},
					}}},
				})
				auth := configuredCapabilityTestAuth("home-no-local-compat", "fixture-key")
				auth.Provider = "codex"
				auth.Attributes[AttributeSource] = "config:codex[0]"
				if tc.options != "" {
					auth.Metadata = map[string]any{"credential_options": json.RawMessage(tc.options)}
				}
				payload, errMarshal := json.Marshal(homeAuthDispatchResponse{
					Model: "upstream(high)", Auth: *auth, ModelInfo: &homeDispatchModelInfo{ID: "upstream"},
				})
				if errMarshal != nil {
					t.Fatal(errMarshal)
				}
				manager.PublishHomeDispatch(configurationUpdateHomeDispatcher{payload: payload}, executionregistry.New(), 1)
				executor := &selectedCapabilityCaptureExecutor{provider: "codex"}
				manager.RegisterExecutor(executor)
				req := cliproxyexecutor.Request{Model: "tenant/alias(high)"}
				opts := cliproxyexecutor.Options{}
				switch path {
				case "execute":
					if _, errExecute := manager.Execute(t.Context(), []string{"codex"}, req, opts); errExecute != nil {
						t.Fatal(errExecute)
					}
				case "stream":
					stream, errStream := manager.ExecuteStream(t.Context(), []string{"codex"}, req, opts)
					if errStream != nil {
						t.Fatal(errStream)
					}
					for chunk := range stream.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				case "count":
					if _, errCount := manager.ExecuteCount(t.Context(), []string{"codex"}, req, opts); errCount != nil {
						t.Fatal(errCount)
					}
				}
				if len(executor.requests) != 1 {
					t.Fatalf("executor received %d requests, want 1", len(executor.requests))
				}
				received := executor.requests[0]
				local, okLocal := ResolvedAPIKeyModelInfo(received)
				if !okLocal || !local.IsCompat {
					t.Fatal("fixture did not bind the conflicting local compatibility setting")
				}
				info, ok := ResolvedModelInfo(received)
				if !ok || info.IsCompat {
					t.Fatalf("Home capabilities must not inherit local is-compat: %+v", info)
				}
			})
		}
	}
}
