package cliproxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type copilotPluginCatalog struct {
	models       []pluginapi.ModelInfo
	authProvider string
}

func (c copilotPluginCatalog) RegisterModels(context.Context, pluginapi.ModelRegistrationRequest) (pluginapi.ModelRegistrationResponse, error) {
	return pluginapi.ModelRegistrationResponse{Provider: copilot.Provider, Models: c.models}, nil
}

func (c copilotPluginCatalog) StaticModels(context.Context, pluginapi.StaticModelRequest) (pluginapi.ModelResponse, error) {
	return pluginapi.ModelResponse{Provider: copilot.Provider, Models: c.models}, nil
}

func (c copilotPluginCatalog) ModelsForAuth(context.Context, pluginapi.AuthModelRequest) (pluginapi.ModelResponse, error) {
	return pluginapi.ModelResponse{Provider: copilot.Provider, Models: c.models, AuthUpdate: pluginapi.AuthData{Provider: c.authProvider}}, nil
}

func TestCopilotPluginCatalogPublishesExecutableRoutes(t *testing.T) {
	for _, tc := range []struct {
		name, alphaSuffix, betaSuffix string
		perAuth, forceMapping         bool
	}{
		{name: "per-auth", perAuth: true},
		// These exact routes are not present in the built-in catalog, but their
		// native IDs are entitled, so they can exercise the actual executor.
		{name: "static append", alphaSuffix: "(high)", betaSuffix: "(low)"},
		{name: "static append force mapping", alphaSuffix: "(high)", betaSuffix: "(low)", forceMapping: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			alpha, beta, hidden := "alpha"+tc.alphaSuffix, "beta"+tc.betaSuffix, "hidden"+tc.alphaSuffix
			alias := "plugin-copilot-chat"
			if !tc.forceMapping {
				alias += tc.alphaSuffix
			}
			cfg := &config.Config{
				OAuthExcludedModels: map[string][]string{copilot.Provider: {"copilot/hidden", "copilot/" + hidden, alpha, alias}},
				OAuthModelAlias: map[string][]config.OAuthModelAlias{copilot.Provider: {
					{Name: "copilot/" + alpha, Alias: alias, ForceMapping: tc.forceMapping},
					{Name: "copilot/" + hidden, Alias: "excluded-copilot-plugin-alias"},
				}},
			}
			cfg.ForceModelPrefix = true
			catalog := copilotPluginCatalog{models: []pluginapi.ModelInfo{
				{ID: alpha, DisplayName: "Plugin Alpha"},
				{ID: "copilot/" + beta, DisplayName: "Plugin Beta (Copilot)"},
				{ID: hidden},
			}}
			var caps pluginapi.Capabilities
			if tc.perAuth {
				caps.ModelProvider = catalog
			} else {
				caps.ModelRegistrar = catalog
			}
			ctx := t.Context()
			manager := coreauth.NewManager(nil, nil, nil)
			manager.SetConfig(cfg)
			manager.SetOAuthModelAlias(cfg.OAuthModelAlias)
			host := pluginhost.New()
			host.RegisterPluginForTest(t.Name(), pluginapi.Plugin{Capabilities: caps})
			reg := registry.GetGlobalRegistry()
			host.RegisterModels(ctx, reg)
			host.RegisterExecutors(manager, reg)
			service := &Service{cfg: cfg, coreManager: manager, pluginHost: host}
			auth := registerCopilotRoutingTestAuth(t, manager, "team", "alpha", "beta", "hidden")
			var entries []copilot.Model
			for _, id := range []string{"alpha", "beta", "hidden"} {
				entry := copilot.Model{ID: id, Endpoints: []string{copilot.Chat}}
				entry.Capabilities.Supports.Reasoning = json.RawMessage(`["low","high"]`)
				entries = append(entries, entry)
			}
			auth.CopilotCatalog = copilot.NewCatalog(entries, time.Now())
			auth, errUpdate := manager.Update(ctx, auth)
			if errUpdate != nil {
				t.Fatal(errUpdate)
			}
			if !service.refreshModelRegistrationForAuth(auth) {
				t.Fatal("failed to publish plugin catalog")
			}

			registered := reg.GetModelsForClient(auth.ID)
			for _, unexpected := range []string{alpha, alias, beta, "team/" + alpha, "team/copilot/" + alpha, "team/copilot/copilot/" + beta, "team/copilot/" + hidden, "team/excluded-copilot-plugin-alias"} {
				if slices.ContainsFunc(registered, func(info *ModelInfo) bool { return info.ID == unexpected }) {
					t.Errorf("unexpected published model %q", unexpected)
				}
			}

			base := handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)
			request := func(id, native, effort, responseModel string) {
				t.Helper()
				upstreamModel, upstreamEffort := "", ""
				requestCtx := context.WithValue(ctx, "cliproxy.roundtripper", copilotCatalogTransport(func(req *http.Request) (*http.Response, error) {
					body, errRead := io.ReadAll(req.Body)
					if errRead != nil {
						return nil, errRead
					}
					if req.URL.Path != copilot.Chat {
						t.Fatalf("unexpected upstream endpoint %q", req.URL.Path)
					}
					wire := ojson.ParseBytes(body)
					upstreamModel = wire.Get("model").String()
					upstreamEffort = wire.Get("reasoning_effort").String()
					response := fmt.Sprintf(`{"model":%q,"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`, upstreamModel)
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response))}, nil
				}))
				payload := fmt.Appendf(nil, `{"model":%q,"messages":[{"role":"user","content":"hello"}]}`, id)
				body, _, errMsg := base.ExecuteWithAuthManager(requestCtx, "openai", id, payload, "")
				if errMsg != nil {
					t.Fatalf("request for %s failed: %v", id, errMsg.Error)
				}
				if upstreamModel != native || upstreamEffort != effort {
					t.Errorf("route %q sent model %q with effort %q upstream, want %q with effort %q", id, upstreamModel, upstreamEffort, native, effort)
				}
				if got := ojson.GetBytesNoCopy(body, "model").String(); got != responseModel {
					t.Errorf("route %q returned response model %q, want %q", id, got, responseModel)
				}
			}
			alphaResponse := "alpha"
			if tc.forceMapping {
				alphaResponse = alias
			}
			for _, want := range []struct{ id, native, displayName, effort, responseModel string }{
				{"team/" + alias, "alpha", "Plugin Alpha (Copilot)", strings.Trim(tc.alphaSuffix, "()"), alphaResponse},
				{"team/copilot/" + beta, "beta", "Plugin Beta (Copilot)", strings.Trim(tc.betaSuffix, "()"), "beta"},
			} {
				if !slices.ContainsFunc(registered, func(info *ModelInfo) bool {
					return info.ID == want.id && info.SourceModelID == copilot.ModelPrefix+want.native && info.MetadataModelID == want.native && info.DisplayName == want.displayName
				}) {
					t.Fatalf("missing published route %+v", want)
				}
				request(want.id, want.native, want.effort, want.responseModel)
			}
			if tc.forceMapping {
				request("team/"+alias+"(low)", "alpha", "high", alias)

				// A different configured source suffix must not change the published route or its thinking.
				manager.SetOAuthModelAlias(map[string][]config.OAuthModelAlias{copilot.Provider: {
					{Name: "copilot/alpha(low)", Alias: alias, ForceMapping: true},
				}})
				request("team/"+alias, "alpha", "high", "alpha")
			}
		})
	}
}

func TestCopilotPluginCatalogPreservesExplicitAuthProvider(t *testing.T) {
	const channel = "plugin-custom-copilot"
	cfg := &config.Config{
		OAuthExcludedModels: map[string][]string{channel: {"hidden"}},
		OAuthModelAlias:     map[string][]config.OAuthModelAlias{channel: {{Name: "alpha", Alias: "custom-alpha"}}},
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	host := pluginhost.New()
	catalog := copilotPluginCatalog{authProvider: channel, models: []pluginapi.ModelInfo{{ID: "alpha", DisplayName: "Custom Alpha"}, {ID: "hidden"}}}
	host.RegisterPluginForTest(t.Name(), pluginapi.Plugin{Capabilities: pluginapi.Capabilities{ModelProvider: catalog}})
	reg := registry.GetGlobalRegistry()
	host.RegisterModels(t.Context(), reg)
	service := &Service{cfg: cfg, coreManager: manager, pluginHost: host}
	auth := registerCopilotRoutingTestAuth(t, manager, "", "alpha", "hidden")
	if !service.registerModelsForAuth(t.Context(), auth) {
		t.Fatal("failed to publish plugin catalog")
	}
	models := reg.GetModelsForClient(auth.ID)
	if len(models) != 1 || models[0].ID != "custom-alpha" || models[0].DisplayName != "Custom Alpha" || !slices.Equal(reg.GetModelProviders("custom-alpha"), []string{channel}) {
		t.Fatalf("explicit channel should publish its own aliased model, got %+v", models)
	}
}
