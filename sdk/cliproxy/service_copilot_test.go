package cliproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type copilotCatalogTransport func(*http.Request) (*http.Response, error)

func (f copilotCatalogTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func newCopilotCatalogTestRefresher(t *testing.T, manager *coreauth.Manager, cfg *config.Config) *copilotCatalogRefresher {
	t.Helper()
	r := &copilotCatalogRefresher{
		service: &Service{cfg: cfg, coreManager: manager}, wake: make(chan struct{}, 1),
		jobs: make(chan *copilotCatalogJob, 16), states: make(map[string]*copilotCatalogState), inFlight: make(map[string]*copilotCatalogJob),
	}
	t.Cleanup(manager.AddHook(r))
	t.Cleanup(func() {
		for _, job := range r.inFlight {
			job.cancel()
		}
	})
	return r
}

func registerCopilotCatalogTestAuth(t *testing.T, manager *coreauth.Manager, fetched time.Time) *coreauth.Auth {
	useCopilotTestModels(t)
	t.Helper()
	snapshot := copilot.NewCatalog([]copilot.Model{{ID: "copilot-catalog-test-model", Endpoints: []string{copilot.Chat}}}, fetched)
	a, err := manager.Register(context.Background(), &coreauth.Auth{
		ID: t.Name(), Provider: copilot.Provider, Status: coreauth.StatusActive,
		CopilotCatalog: snapshot,
		Metadata:       map[string]any{"type": copilot.Provider, "access_token": "old-token", "refresh_token": "grant", "base_url": "https://api.individual.githubcopilot.com", "expired": time.Now().Add(24 * time.Hour).Format(time.RFC3339)},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(a.ID) })
	return a
}

func TestCopilotCatalogObserverSkipsOtherProviders(t *testing.T) {
	m := coreauth.NewManager(nil, nil, nil)
	r := newCopilotCatalogTestRefresher(t, m, &config.Config{})
	ctx := context.Background()
	base, err := m.Register(ctx, &coreauth.Auth{ID: t.Name(), Provider: "other", Status: coreauth.StatusActive})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.UpdateRefreshedAuth(ctx, base, base.Clone()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.wake:
		t.Fatal("unrelated credential woke Copilot discovery")
	default:
	}
	r.reconcile(ctx, time.Now())
	if len(r.states) != 0 || len(r.jobs) != 0 {
		t.Fatal("unrelated credential entered the Copilot queue")
	}
}

func TestCopilotAccountCatalogRepublishesOnAuthUpdate(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{OAuthModelAlias: map[string][]config.OAuthModelAlias{copilot.Provider: {{Name: "copilot/copilot-test-new", Alias: "copilot-test-alias"}}}}
	manager := coreauth.NewManager(nil, nil, nil)
	r := newCopilotCatalogTestRefresher(t, manager, cfg)
	base := registerCopilotCatalogTestAuth(t, manager, time.Now())
	r.reconcile(ctx, time.Now())
	updated := base.Clone()
	updated.CopilotCatalog = copilot.NewCatalog([]copilot.Model{{ID: "copilot-test-new", Endpoints: []string{copilot.Messages}}}, time.Now())
	current, err := manager.Update(ctx, updated)
	if err != nil {
		t.Fatal(err)
	}
	r.reconcile(ctx, time.Now())
	if models := GlobalModelRegistry().GetModelsForClient(base.ID); len(models) != 1 || models[0].ID != "copilot-test-alias" {
		t.Fatalf("update did not publish the account catalog with its alias: %+v", models)
	}
	updated = current.Clone()
	updated.CopilotCatalog = copilot.NewCatalog(nil, time.Now())
	if _, err = manager.Update(ctx, updated); err != nil {
		t.Fatal(err)
	}
	r.reconcile(ctx, time.Now())
	if models := GlobalModelRegistry().GetModelsForClient(base.ID); len(models) != 0 {
		t.Fatal("a successful empty catalog did not revoke the model")
	}
}

func TestCopilotPublicModelRegistrationAndRouting(t *testing.T) {
	for _, tc := range []struct {
		name, prefix, alias string
		forcePrefix         bool
	}{
		{"team-prefix", "team", "routing-chat", false},
		{"copilot-prefix", "copilot", "routing-chat", false},
		{"qualified-alias", "copilot", "copilot/routing-chat", false},
		{"alias-equals-prefix", "copilot", "copilot", false},
		{"forced-prefix", "copilot", "copilot/routing-chat", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testCopilotPublicModelRegistrationAndRouting(t, tc.prefix, tc.alias, tc.forcePrefix)
		})
	}
}

func testCopilotPublicModelRegistrationAndRouting(t *testing.T, prefix, alias string, forcePrefix bool) {
	useCopilotTestModels(t)
	t.Helper()
	const native = "copilot-routing-chat"
	const public = "copilot/copilot-routing-chat"
	cfg, err := config.ParseConfigBytes(fmt.Appendf(nil, `
routing:
  force-model-prefix: %t
oauth:
  model-alias:
    copilot:
      - name: copilot/copilot-routing-chat
        alias: %q
        fork: true
      - name: copilot/copilot-routing-hidden
        alias: routing-hidden
  excluded-models:
    copilot:
      - "copilot/*-hidden"
  request-scoped-errors:
    copilot:
      - status: 429
        match: [copilot_test_request_limit]
        action: stop
`, forcePrefix, alias))
	if err != nil {
		t.Fatal(err)
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.SetOAuthModelAlias(cfg.OAuthModelAlias)
	service := &Service{cfg: cfg, coreManager: manager}
	model := copilot.Model{ID: native, Name: "Routing Chat", Endpoints: []string{copilot.Chat}}
	vision := true
	model.Capabilities.Supports.Vision = &vision
	model.Capabilities.Supports.Reasoning = json.RawMessage(`["low","high"]`)
	model.Capabilities.Limits.Context = 32768
	models := []copilot.Model{model, {ID: "copilot-routing-hidden", Endpoints: []string{copilot.Chat}}}
	snapshot := copilot.NewCatalog(models, time.Now())
	auth, err := manager.Register(t.Context(), &coreauth.Auth{
		ID: t.Name(), Provider: copilot.Provider, Prefix: prefix, Status: coreauth.StatusActive,
		CopilotCatalog: snapshot,
		Metadata:       map[string]any{"access_token": "test-token", "base_url": "https://api.individual.githubcopilot.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	reg := GlobalModelRegistry()
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	if !service.refreshModelRegistrationForAuth(auth) {
		t.Fatal("failed to publish Copilot catalog")
	}
	registered := reg.GetModelsForClient(auth.ID)
	routedAlias := prefix + "/" + alias
	publicIDs := []string{prefix + "/" + public, routedAlias}
	if !forcePrefix {
		publicIDs = append(publicIDs, public, alias)
		routedAlias = alias
	}
	if len(registered) != len(publicIDs) {
		t.Fatalf("registered %d models, want routes %v", len(registered), publicIDs)
	}
	for _, id := range publicIDs {
		idx := slices.IndexFunc(registered, func(info *ModelInfo) bool { return info.ID == id })
		if idx < 0 {
			t.Fatalf("missing model %s in Copilot registration", id)
		}
		info := registered[idx]
		if info.DisplayName != "Routing Chat (Copilot)" || info.MetadataModelID != native ||
			info.ContextLength != 32768 || !slices.Contains(info.SupportedInputModalities, "IMAGE") ||
			info.Thinking == nil || !slices.Equal(info.Thinking.Levels, []string{"low", "high"}) {
			t.Fatalf("unexpected catalog metadata for %s: %+v", id, info)
		}
	}
	base := handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)
	catalog := openai.CodexClientModelsResponseForClient(openai.NewOpenAIAPIHandler(base).Models(), "0.153.4", false)
	entries := catalog["models"].([]map[string]any)
	for _, id := range publicIDs {
		if !slices.ContainsFunc(entries, func(entry map[string]any) bool {
			return entry["slug"] == id && entry["display_name"] == "Routing Chat (Copilot)"
		}) {
			t.Fatalf("missing Copilot model %s in Codex client catalog", id)
		}
	}

	calls := 0
	returnLimit := false
	ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", copilotCatalogTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Path != copilot.Chat {
			t.Fatalf("upstream path = %s, want %s", req.URL.Path, copilot.Chat)
		}
		body, errRead := io.ReadAll(req.Body)
		if errRead != nil {
			t.Fatal(errRead)
		}
		if wire := ojson.ParseBytes(body); wire.Get("model").String() != native || wire.Get("reasoning_effort").String() != "high" {
			t.Fatalf("unexpected upstream request: %s", body)
		}
		status := http.StatusOK
		response := `{"model":"copilot-routing-chat","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`
		if returnLimit {
			status = http.StatusTooManyRequests
			response = `{"error":{"message":"copilot_test_request_limit"}}`
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response))}, nil
	}))
	request := func(id string) int {
		id += "(high)"
		payload := []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}]}`, id))
		_, _, errMsg := base.ExecuteWithAuthManager(ctx, "openai", id, payload, "")
		if errMsg != nil {
			t.Logf("request for %s: %v", id, errMsg.Error)
			return errMsg.StatusCode
		}
		return http.StatusOK
	}
	for _, id := range publicIDs {
		if status := request(id); status != http.StatusOK {
			t.Fatalf("request for %s returned %d", id, status)
		}
	}
	for _, id := range []string{native, "copilot/copilot-routing-hidden", "routing-hidden"} {
		if status := request(id); status != http.StatusBadRequest {
			t.Fatalf("unregistered model %s returned %d", id, status)
		}
	}
	returnLimit = true
	if status := request(routedAlias); status != http.StatusTooManyRequests {
		t.Fatalf("request-scoped failure returned %d, want 429", status)
	}
	returnLimit = false
	if status := request(routedAlias); status != http.StatusOK {
		t.Fatalf("request-scoped failure cooled down the model: next status = %d", status)
	}
	if calls != len(publicIDs)+2 {
		t.Fatalf("upstream calls = %d, want %d", calls, len(publicIDs)+2)
	}
}

func TestCopilotExplicitSuffixUsesRegisteredAuthPrefix(t *testing.T) {
	ctx := t.Context()
	cfg := &config.Config{OAuthModelAlias: map[string][]config.OAuthModelAlias{copilot.Provider: {
		{Name: "copilot/alpha", Alias: "plain", Fork: true},
		{Name: "copilot/beta", Alias: "alpha(high)"},
		{Name: "copilot/beta", Alias: "copilot/plain(high)"},
	}}}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.SetOAuthModelAlias(cfg.OAuthModelAlias)
	service := &Service{cfg: cfg, coreManager: manager}
	auth := registerCopilotRoutingTestAuth(t, manager, copilot.Provider, "beta", "alpha")
	if !service.refreshModelRegistrationForAuth(auth) {
		t.Fatal("failed to publish Copilot catalog")
	}
	base := handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)
	for _, tc := range []struct{ route, native string }{
		{"copilot/alpha", "alpha"},
		{"copilot/alpha(high)", "beta"},
		{"copilot/plain(low)", "alpha"},
		{"copilot/plain(high)", "beta"},
	} {
		t.Run(tc.route, func(t *testing.T) {
			var upstreamModel string
			requestCtx := context.WithValue(ctx, "cliproxy.roundtripper", copilotCatalogTransport(func(req *http.Request) (*http.Response, error) {
				body, errRead := io.ReadAll(req.Body)
				if errRead != nil {
					return nil, errRead
				}
				upstreamModel = ojson.GetBytesNoCopy(body, "model").String()
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))}, nil
			}))
			payload := fmt.Appendf(nil, `{"model":%q,"messages":[{"role":"user","content":"hello"}]}`, tc.route)
			if _, _, errMsg := base.ExecuteWithAuthManager(requestCtx, "openai", tc.route, payload, ""); errMsg != nil {
				t.Fatalf("request for %s failed: %v", tc.route, errMsg.Error)
			}
			if upstreamModel != tc.native {
				t.Fatalf("upstream model = %q, want %q", upstreamModel, tc.native)
			}
		})
	}
}

func TestCopilotRoutesOnlyPublishedModels(t *testing.T) {
	for _, tc := range []struct {
		name, prefix, target, alias, excluded, native, responseModel string
		published, requests, rejected                                []string
	}{
		{
			name: "excluded-alias-target", target: "copilot/alpha", alias: "copilot/beta", excluded: "copilot/alpha",
			native: "beta", responseModel: "beta", published: []string{"copilot/beta"},
			requests: []string{"copilot/beta", "copilot/beta(high)"}, rejected: []string{"copilot/alpha", "copilot/beta(unknown)"},
		},
		{
			name: "suffix-only-alias", target: "copilot/alpha", alias: "alpha(high)",
			native: "alpha", responseModel: "alpha(high)", published: []string{"alpha(high)"},
			requests: []string{"alpha(high)"}, rejected: []string{"alpha", "alpha(low)", "copilot/alpha"},
		},
		{
			name: "prefix-overlaps-alias", prefix: "team", target: "copilot/beta", alias: "team/chat",
			native: "beta", responseModel: "team/chat", published: []string{"team/chat", "team/team/chat"},
			requests: []string{"team/chat", "team/team/chat", "team/chat(high)", "team/team/chat(high)"}, rejected: []string{"chat", "copilot/beta"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{OAuthModelAlias: map[string][]config.OAuthModelAlias{copilot.Provider: {
				{Name: tc.target, Alias: tc.alias, ForceMapping: true},
			}}}
			if tc.excluded != "" {
				cfg.OAuthExcludedModels = map[string][]string{copilot.Provider: {tc.excluded}}
			}
			if tc.prefix != "" {
				cfg.Payload.Override = []config.PayloadRule{{
					Models: []config.PayloadModelRule{{Name: "team/*"}},
					Params: map[string]any{"temperature": 0.25},
				}}
			}
			manager := coreauth.NewManager(nil, nil, nil)
			manager.SetConfig(cfg)
			manager.SetOAuthModelAlias(cfg.OAuthModelAlias)
			service := &Service{cfg: cfg, coreManager: manager}
			modelIDs := []string{tc.native}
			if tc.excluded != "" {
				modelIDs = append(modelIDs, "alpha")
			}
			auth := registerCopilotRoutingTestAuth(t, manager, tc.prefix, modelIDs...)
			if !service.refreshModelRegistrationForAuth(auth) {
				t.Fatal("failed to publish Copilot catalog")
			}
			registered := GlobalModelRegistry().GetModelsForClient(auth.ID)
			if len(registered) != len(tc.published) {
				t.Fatalf("registered %d models, want %v", len(registered), tc.published)
			}
			for _, id := range tc.published {
				if !slices.ContainsFunc(registered, func(info *ModelInfo) bool {
					return info.ID == id && info.MetadataModelID == tc.native
				}) {
					t.Fatalf("missing route %q to %q", id, tc.native)
				}
			}
			var calls int
			ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", copilotCatalogTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				body, errRead := io.ReadAll(req.Body)
				if errRead != nil {
					return nil, errRead
				}
				wire := ojson.ParseBytes(body)
				if model := wire.Get("model").String(); model != tc.native {
					t.Fatalf("upstream model = %q, want %q", model, tc.native)
				}
				if tc.prefix != "" && wire.Get("temperature").Float() != 0.25 {
					t.Fatalf("payload rule for the requested route did not apply: %s", body)
				}
				response := fmt.Sprintf(`{"model":%q,"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`, tc.native)
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response))}, nil
			}))
			base := handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)
			request := func(id string) ([]byte, *interfaces.ErrorMessage) {
				payload := fmt.Appendf(nil, `{"model":%q,"messages":[{"role":"user","content":"hello"}]}`, id)
				body, _, errMsg := base.ExecuteWithAuthManager(ctx, "openai", id, payload, "")
				return body, errMsg
			}
			for _, id := range tc.requests {
				body, errMsg := request(id)
				if errMsg != nil {
					t.Fatalf("request for %q failed: %v", id, errMsg.Error)
				}
				if model := ojson.GetBytesNoCopy(body, "model").String(); model != tc.responseModel {
					t.Fatalf("response to %q has model %q, want %q", id, model, tc.responseModel)
				}
			}
			for _, id := range tc.rejected {
				if _, errMsg := request(id); errMsg == nil {
					t.Fatalf("unpublished request for %q succeeded", id)
				}
			}
			if calls != len(tc.requests) {
				t.Fatalf("upstream calls = %d, want %d", calls, len(tc.requests))
			}
			if tc.prefix != "" {
				// Updating config before republication must not retarget the alias or force its response model.
				manager.SetOAuthModelAlias(map[string][]config.OAuthModelAlias{copilot.Provider: {
					{Name: "copilot/alpha", Alias: tc.alias, ForceMapping: true},
				}})
				body, errMsg := request(tc.requests[0])
				if errMsg != nil {
					t.Fatalf("request after alias update failed: %v", errMsg.Error)
				}
				if model := ojson.GetBytesNoCopy(body, "model").String(); model != tc.native {
					t.Fatalf("response after alias update has model %q, want %q", model, tc.native)
				}
			}
		})
	}
}

func registerCopilotRoutingTestAuth(t *testing.T, manager *coreauth.Manager, prefix string, modelIDs ...string) *coreauth.Auth {
	useCopilotTestModels(t)
	t.Helper()
	models := make([]copilot.Model, 0, len(modelIDs))
	for _, id := range modelIDs {
		models = append(models, copilot.Model{ID: id, Endpoints: []string{copilot.Chat}})
	}
	snapshot := copilot.NewCatalog(models, time.Now())
	// The registry keeps registration epochs even after a client is removed.
	auth, err := manager.Register(t.Context(), &coreauth.Auth{
		ID: t.Name() + "/" + uuid.NewString(), Provider: copilot.Provider, Prefix: prefix, Status: coreauth.StatusActive,
		CopilotCatalog: snapshot,
		Metadata:       map[string]any{"access_token": "test-token", "base_url": "https://api.individual.githubcopilot.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })
	return auth
}

func TestCopilotPublicModelsShareModelCooldown(t *testing.T) {
	useCopilotTestModels(t)
	const native = "copilot-shared-cooldown"
	const public = copilot.ModelPrefix + native
	const prefixed = copilot.ModelPrefix + public
	for _, ids := range [][2]string{{public, prefixed}, {prefixed, public}} {
		t.Run(ids[0], func(t *testing.T) {
			ctx := t.Context()
			cfg := &config.Config{}
			manager := coreauth.NewManager(nil, nil, nil)
			manager.SetConfig(cfg)
			service := &Service{cfg: cfg, coreManager: manager}
			snapshot := copilot.NewCatalog([]copilot.Model{{ID: native, Endpoints: []string{copilot.Chat}}}, time.Now())
			auth, err := manager.Register(ctx, &coreauth.Auth{
				ID: t.Name(), Provider: copilot.Provider, Prefix: copilot.Provider, Status: coreauth.StatusActive,
				CopilotCatalog: snapshot,
				Metadata:       map[string]any{"access_token": "test-token", "base_url": "https://api.individual.githubcopilot.com"},
			})
			if err != nil {
				t.Fatal(err)
			}
			reg := registry.GetGlobalRegistry()
			t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
			if !service.refreshModelRegistrationForAuth(auth) {
				t.Fatal("failed to publish Copilot catalog")
			}
			for _, id := range ids {
				if !reg.ClientSupportsModel(auth.ID, id) {
					t.Fatalf("missing published route %s", id)
				}
			}

			calls := 0
			ctx = context.WithValue(ctx, "cliproxy.roundtripper", copilotCatalogTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				body, errRead := io.ReadAll(req.Body)
				if errRead != nil {
					t.Fatal(errRead)
				}
				if model := ojson.GetBytesNoCopy(body, "model").String(); model != native {
					t.Fatalf("upstream model = %q, want %q", model, native)
				}
				return &http.Response{
					StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {"60"}},
					Body: io.NopCloser(strings.NewReader(`{"error":{"message":"rate limited"}}`)),
				}, nil
			}))
			base := handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)
			request := func(id string) *interfaces.ErrorMessage {
				payload := fmt.Appendf(nil, `{"model":%q,"messages":[{"role":"user","content":"hello"}]}`, id)
				_, _, errMsg := base.ExecuteWithAuthManager(ctx, "openai", id, payload, "")
				return errMsg
			}
			if errMsg := request(ids[0]); errMsg == nil || errMsg.StatusCode != http.StatusTooManyRequests || calls != 1 {
				t.Fatalf("first request for %s: error = %+v, upstream calls = %d", ids[0], errMsg, calls)
			}
			if errMsg := request(ids[1]); errMsg == nil || calls != 1 {
				t.Fatalf("second request for %s bypassed cooldown: error = %+v, upstream calls = %d", ids[1], errMsg, calls)
			}

			checkCooldown := func() *coreauth.Auth {
				t.Helper()
				current, ok := manager.GetByID(auth.ID)
				if !ok || len(current.ModelStates) != 1 || current.ModelStates[public] == nil {
					t.Fatalf("want only model state %q, got %+v", public, current)
				}
				if state := current.ModelStates[public]; !state.Unavailable || !state.Quota.Exceeded || state.Quota.Reason != "quota" || !state.NextRetryAfter.After(time.Now()) {
					t.Fatalf("expected active model-level cooldown, got %+v", state)
				}
				for _, id := range ids {
					if !reg.IsModelSuspendedForClient(auth.ID, id) || !reg.IsModelQuotaExceededForClient(auth.ID, id) {
						t.Fatalf("registry did not project cooldown onto %s", id)
					}
				}
				return current
			}
			if !service.refreshModelRegistrationForAuth(checkCooldown()) {
				t.Fatal("failed to refresh Copilot catalog")
			}
			checkCooldown()

			reg.UnregisterClient(auth.ID)
			manager.SetOAuthModelAlias(nil)
			current, _ := manager.GetByID(auth.ID)
			if len(current.ModelStates) != 1 || current.ModelStates[public] == nil {
				t.Fatalf("unpublished catalog changed canonical model state: %+v", current.ModelStates)
			}
			manager.ReconcileRegistryModelStates(ctx, auth.ID)
			current, _ = manager.GetByID(auth.ID)
			if len(current.ModelStates) != 0 {
				t.Fatalf("removed catalog retained model states: %+v", current.ModelStates)
			}
			for _, id := range ids {
				if reg.ClientSupportsModel(auth.ID, id) || reg.IsModelSuspendedForClient(auth.ID, id) || reg.IsModelQuotaExceededForClient(auth.ID, id) {
					t.Fatalf("registry retained removed route %s", id)
				}
			}

			manager.MarkResult(ctx, coreauth.Result{AuthID: auth.ID, Provider: copilot.Provider, Model: native, Error: &coreauth.Error{HTTPStatus: http.StatusTooManyRequests}})
			current, _ = manager.GetByID(auth.ID)
			if !service.refreshModelRegistrationForAuth(current) {
				t.Fatal("failed to republish Copilot catalog with legacy state")
			}
			checkCooldown()
		})
	}
}

func TestCopilotRemovedNativeCooldownDoesNotFollowReusedAlias(t *testing.T) {
	const alpha = "copilot/alpha"
	const beta = "copilot/beta"
	cfg := &config.Config{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	service := &Service{cfg: cfg, coreManager: manager, pluginHost: pluginhost.New()}
	t.Cleanup(func() {
		sdkAuth.RegisterPluginAuthParser(nil)
		sdktranslator.SetPluginHooks(nil)
	})
	auth := registerCopilotRoutingTestAuth(t, manager, "", "alpha", "beta")
	if !service.refreshModelRegistrationForAuth(auth) {
		t.Fatal("failed to publish Copilot catalog")
	}

	var calls []string
	ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", copilotCatalogTransport(func(req *http.Request) (*http.Response, error) {
		body, errRead := io.ReadAll(req.Body)
		if errRead != nil {
			return nil, errRead
		}
		model := ojson.GetBytesNoCopy(body, "model").String()
		calls = append(calls, model)
		status, header := http.StatusOK, make(http.Header)
		response := `{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`
		if len(calls) == 1 {
			status, response = http.StatusTooManyRequests, `{"error":{"message":"rate limited"}}`
			header.Set("Retry-After", "60")
		}
		return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(response))}, nil
	}))
	base := handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)
	request := func(model string) *interfaces.ErrorMessage {
		payload := fmt.Appendf(nil, `{"model":%q,"messages":[{"role":"user","content":"hello"}]}`, model)
		_, _, errMsg := base.ExecuteWithAuthManager(ctx, "openai", model, payload, "")
		return errMsg
	}
	if errMsg := request(alpha); errMsg == nil || errMsg.StatusCode != http.StatusTooManyRequests || !slices.Equal(calls, []string{"alpha"}) {
		t.Fatalf("alpha request = %+v, upstream calls = %v, want a 429 from alpha", errMsg, calls)
	}
	current, _ := manager.GetByID(auth.ID)
	if state := current.ModelStates[alpha]; len(current.ModelStates) != 1 || state == nil || !state.Unavailable || !state.Quota.Exceeded {
		t.Fatalf("want native alpha in cooldown, got %+v", current.ModelStates)
	}

	newCfg := &config.Config{
		OAuthExcludedModels: map[string][]string{copilot.Provider: {alpha}},
		OAuthModelAlias:     map[string][]config.OAuthModelAlias{copilot.Provider: {{Name: beta, Alias: alpha, Fork: true}}},
	}
	if !service.applyConfigUpdateWithAuthSynthesis(t.Context(), newCfg, false) {
		t.Fatal("config refresh failed")
	}
	current, _ = manager.GetByID(auth.ID)
	if len(current.ModelStates) != 0 || current.Unavailable || current.Quota.Exceeded {
		t.Fatalf("config refresh retained or migrated alpha's cooldown: states = %+v, quota = %+v", current.ModelStates, current.Quota)
	}
	reg := registry.GetGlobalRegistry()
	for _, id := range []string{beta, alpha} {
		native, _ := reg.ClientModelRoute(auth.ID, id, "")
		if native != "beta" || reg.IsModelSuspendedForClient(auth.ID, id) || reg.IsModelQuotaExceededForClient(auth.ID, id) {
			t.Fatalf("route %q was not published as available beta: native = %q", id, native)
		}
		if errMsg := request(id); errMsg != nil {
			t.Fatalf("route %q failed after refresh: %v", id, errMsg.Error)
		}
	}
	if !slices.Equal(calls, []string{"alpha", "beta", "beta"}) {
		t.Fatalf("upstream calls = %v, want [alpha beta beta]", calls)
	}
	current, _ = manager.GetByID(auth.ID)
	if _, hasAlpha := current.ModelStates[alpha]; hasAlpha {
		t.Fatal("successful requests through the reused alias recreated alpha's state")
	}
	if state := current.ModelStates[beta]; state != nil && (state.Unavailable || state.Quota.Exceeded) {
		t.Fatalf("beta inherited cooldown state: %+v", state)
	}
}

func TestCopilotRestoresModelCooldown(t *testing.T) {
	for _, tc := range []struct {
		name, prefix, alias, aliasTarget, storedModel, wantModel string
		alphaFirst, betaOnly, aliasAfterRestore, emptyCatalog    bool
	}{
		{name: "no-prefix", wantModel: "copilot/alpha"},
		{name: "other-prefix", prefix: "team", wantModel: "copilot/alpha"},
		{name: "published-alias", alias: "alpha", wantModel: "copilot/beta"},
		{name: "published-alias-without-native", alias: "alpha", betaOnly: true, wantModel: "copilot/beta"},
		{name: "published-suffixed-alias-beta-first", alias: "alpha(high)", wantModel: "copilot/alpha"},
		{name: "published-suffixed-alias-alpha-first", alias: "alpha(high)", wantModel: "copilot/alpha", alphaFirst: true},
		{name: "published-suffixed-alias-without-native", alias: "alpha(high)", betaOnly: true},
		{name: "qualified-state-with-namespaced-suffixed-alias", alias: "copilot/alpha(high)", storedModel: "copilot/alpha", wantModel: "copilot/alpha"},
		{name: "qualified-state-with-namespaced-suffixed-alias-without-native", alias: "copilot/alpha(high)", storedModel: "copilot/alpha", betaOnly: true},
		{name: "qualified-state-with-namespaced-suffixed-alias-set-after-restore", alias: "copilot/alpha(high)", storedModel: "copilot/alpha", wantModel: "copilot/alpha", aliasAfterRestore: true},
		{name: "qualified-state-with-namespaced-suffixed-alias-set-after-restore-without-native", alias: "copilot/alpha(high)", storedModel: "copilot/alpha", betaOnly: true, aliasAfterRestore: true},
		{name: "exact-namespaced-suffixed-alias", alias: "copilot/alpha(high)", storedModel: "Copilot/Alpha(HIGH)", wantModel: "copilot/beta"},
		{name: "exact-namespaced-suffixed-alias-set-after-restore", alias: "copilot/alpha(high)", storedModel: "Copilot/Alpha(HIGH)", wantModel: "copilot/beta", aliasAfterRestore: true},
		{name: "qualified-state-is-target-of-unpublished-alias", alias: "copilot/beta", aliasTarget: "copilot/alpha", storedModel: "copilot/alpha", betaOnly: true},
		{name: "alias-set-after-restore", alias: "alpha", wantModel: "copilot/beta", aliasAfterRestore: true},
		{name: "suffixed-alias-set-after-restore", alias: "alpha(high)", wantModel: "copilot/alpha", aliasAfterRestore: true},
		{name: "explicit-empty-registration", alias: "alpha(high)", aliasAfterRestore: true, emptyCatalog: true},
		{name: "qualified-state-explicit-empty-registration", storedModel: "copilot/alpha", emptyCatalog: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			cfg := &config.Config{AuthDir: t.TempDir(), SaveCooldownStatus: true}
			if tc.alias != "" {
				target := tc.aliasTarget
				if target == "" {
					target = "copilot/beta"
				}
				cfg.OAuthModelAlias = map[string][]config.OAuthModelAlias{copilot.Provider: {{Name: target, Alias: tc.alias, Fork: true}}}
			}
			manager := coreauth.NewManager(nil, nil, nil)
			manager.SetConfig(cfg)
			if !tc.aliasAfterRestore {
				manager.SetOAuthModelAlias(cfg.OAuthModelAlias)
			}
			service := &Service{cfg: cfg, coreManager: manager}
			modelIDs := []string{"beta", "alpha"}
			if tc.betaOnly {
				modelIDs = modelIDs[:1]
			} else if tc.alphaFirst {
				slices.Reverse(modelIDs)
			}
			auth := registerCopilotRoutingTestAuth(t, manager, tc.prefix, modelIDs...)
			store := service.resolveCooldownStateStore(cfg)
			service.configureCooldownStateStore(cfg)
			now := time.Now().UTC()
			retryAt := now.Add(time.Hour)
			storedModel := tc.storedModel
			if storedModel == "" {
				storedModel = "alpha"
			}
			if errSave := store.Save(ctx, []coreauth.CooldownStateRecord{{
				AuthID: auth.ID, Provider: copilot.Provider, Model: storedModel, NextRetryAfter: retryAt, UpdatedAt: now,
				LastError: &coreauth.Error{HTTPStatus: http.StatusTooManyRequests, Message: "rate limited"},
				Quota:     coreauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: retryAt},
			}}); errSave != nil {
				t.Fatal(errSave)
			}
			if errRestore := manager.RestoreCooldownStates(ctx); errRestore != nil {
				t.Fatal(errRestore)
			}
			loadModelCooldowns := func() []coreauth.CooldownStateRecord {
				t.Helper()
				records, errLoad := store.Load(ctx)
				if errLoad != nil {
					t.Fatal(errLoad)
				}
				return slices.DeleteFunc(records, func(record coreauth.CooldownStateRecord) bool { return record.Model == "" })
			}
			checkCooldown := func(wantModel string) *coreauth.Auth {
				t.Helper()
				records := loadModelCooldowns()
				current, _ := manager.GetByID(auth.ID)
				if wantModel == "" {
					if len(current.ModelStates) != 0 || len(records) != 0 {
						t.Fatalf("expected no cooldowns: states = %+v, records = %+v", current.ModelStates, records)
					}
					return current
				}
				if len(records) != 1 || records[0].Model != wantModel || !records[0].NextRetryAfter.Equal(retryAt) {
					t.Fatalf("want persisted cooldown for %s until %s, got %+v", wantModel, retryAt, records)
				}
				state := current.ModelStates[wantModel]
				if len(current.ModelStates) != 1 || state == nil || !state.Unavailable || !state.NextRetryAfter.Equal(retryAt) || !state.Quota.NextRecoverAt.Equal(retryAt) {
					t.Fatalf("want active cooldown for %s until %s, got %+v", wantModel, retryAt, current.ModelStates)
				}
				return current
			}
			checkCooldown(storedModel)
			reg := registry.GetGlobalRegistry()
			if models, epoch := reg.GetModelsAndEpochForClient(auth.ID); len(models) != 0 || epoch != 0 {
				t.Fatalf("expected unpublished catalog, got epoch %d and models %+v", epoch, models)
			}
			if tc.aliasAfterRestore {
				manager.SetOAuthModelAlias(cfg.OAuthModelAlias)
				checkCooldown(storedModel)
			}
			manager.ReconcileRegistryModelStates(ctx, auth.ID)
			current := checkCooldown(storedModel)
			if tc.emptyCatalog {
				reg.RegisterClient(auth.ID, copilot.Provider, nil)
				manager.ReconcileRegistryModelStates(ctx, auth.ID)
				checkCooldown("")
				return
			}
			if tc.aliasAfterRestore {
				if !service.registerModelsForAuthWithCache(ctx, current, nil) || !reg.ClientSupportsModel(auth.ID, tc.alias) {
					t.Fatal("failed to publish Copilot alias")
				}
				manager.SetOAuthModelAlias(cfg.OAuthModelAlias)
				wantModel := tc.wantModel
				if wantModel == "" {
					// Publication allows migration, but only reconciliation prunes unreachable keys.
					wantModel = storedModel
				}
				current = checkCooldown(wantModel)
			}
			if !service.refreshModelRegistrationForAuth(current) {
				t.Fatal("failed to refresh Copilot catalog")
			}
			checkCooldown(tc.wantModel)
			checkRoute := func(id string, wantCooldown bool) {
				t.Helper()
				if !reg.ClientSupportsModel(auth.ID, id) || reg.IsModelSuspendedForClient(auth.ID, id) != wantCooldown || reg.IsModelQuotaExceededForClient(auth.ID, id) != wantCooldown {
					t.Fatalf("expected published route %s with cooldown = %t", id, wantCooldown)
				}
			}
			checkRoute("copilot/beta", tc.wantModel == "copilot/beta")
			if !tc.betaOnly {
				checkRoute("copilot/alpha", tc.wantModel == "copilot/alpha")
			}
			if tc.alias != "" {
				checkRoute(tc.alias, tc.wantModel == "copilot/beta")
			}
			if tc.alias == "alpha(high)" && reg.ClientSupportsModel(auth.ID, "alpha") {
				t.Fatal("expected only the suffixed alias alpha(high) to be published")
			}
			if tc.betaOnly && reg.ClientSupportsModel(auth.ID, "copilot/alpha") {
				t.Fatal("expected copilot/alpha to be unpublished")
			}
		})
	}
}

func TestCopilotCatalogFetchOutcomesAndCredentialPreservation(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusTooManyRequests, http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			m := coreauth.NewManager(nil, nil, nil)
			r := newCopilotCatalogTestRefresher(t, m, &config.Config{})
			a := registerCopilotCatalogTestAuth(t, m, time.Now().Add(-copilot.CatalogMaxAge*3/4))
			ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", copilotCatalogTransport(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path != "/models" || req.Header.Get("Authorization") != "Bearer old-token" {
					t.Fatal("incorrect discovery request")
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[]}`)), Request: req}, nil
			}))
			r.reconcile(ctx, time.Now())
			job := <-r.jobs
			err := r.fetch(job)
			if (err != nil) != (status == http.StatusTooManyRequests) {
				t.Fatalf("unexpected discovery result: %v", err)
			}
			current, _ := m.GetByID(a.ID)
			if current.Metadata["access_token"] != "old-token" || current.Metadata["expired"] != a.Metadata["expired"] {
				t.Fatal("catalog fetch changed credential fields")
			}
			r.finish(job, time.Now())
			job.cancel()
			r.reconcile(ctx, time.Now())
			if len(r.jobs) != 0 {
				t.Fatal("discovery ignored its retry backoff")
			}
			models := GlobalModelRegistry().GetModelsForClient(a.ID)
			if (len(models) != 0) != (status == http.StatusTooManyRequests) {
				t.Fatalf("wrong availability after HTTP %d: %+v", status, models)
			}
			if status == http.StatusTooManyRequests && current.CopilotCatalog != a.CopilotCatalog {
				t.Fatal("failure extended the catalog TTL")
			}
		})
	}
}

func TestCopilotCatalogClocksAndExpiryAreIndependent(t *testing.T) {
	m := coreauth.NewManager(nil, nil, nil)
	r := newCopilotCatalogTestRefresher(t, m, &config.Config{})
	now := time.Now()
	a := registerCopilotCatalogTestAuth(t, m, now)
	r.reconcile(t.Context(), now)
	if len(r.jobs) != 0 {
		t.Fatal("fresh directory was fetched again")
	}
	due := now.Add(copilot.CatalogMaxAge / 2)
	r.reconcile(t.Context(), due)
	if len(r.jobs) != 1 {
		t.Fatal("long-lived credential did not schedule independent discovery")
	}
	job := <-r.jobs
	r.finish(job, due)
	job.cancel()
	r.reconcile(t.Context(), due)
	if len(r.jobs) != 0 {
		t.Fatal("failed discovery did not back off")
	}
	r.reconcile(t.Context(), due.Add(copilotCatalogRetryDelay))
	if len(r.jobs) != 1 {
		t.Fatal("directory retry waited for token expiration")
	}
	// Explicitly expire the runtime snapshot while a discovery job is pending.
	_, _, err := m.Modify(t.Context(), a.ID, a.RegistrationEpoch, func(current *coreauth.Auth) error {
		current.CopilotCatalog = copilot.NewCatalog([]copilot.Model{{ID: "copilot-catalog-test-model", Endpoints: []string{copilot.Chat}}}, time.Now().Add(-copilot.CatalogMaxAge-time.Minute))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	r.reconcile(t.Context(), time.Now())
	if models := GlobalModelRegistry().GetModelsForClient(a.ID); len(models) != 0 {
		t.Fatal("blocked discovery prevented hard-TTL model revocation")
	}
}

func TestCopilotCatalogRejectsStaleDiscoveryResults(t *testing.T) {
	for _, change := range []string{"token", "account", "origin", "registration", "catalog", "disabled", "removed"} {
		t.Run(change, func(t *testing.T) {
			m := coreauth.NewManager(nil, nil, nil)
			r := newCopilotCatalogTestRefresher(t, m, &config.Config{})
			a := registerCopilotCatalogTestAuth(t, m, time.Now().Add(-copilot.CatalogMaxAge*3/4))
			newer := copilot.NewCatalog(nil, time.Now())
			ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", copilotCatalogTransport(func(req *http.Request) (*http.Response, error) {
				if change == "removed" {
					m.Remove(t.Context(), a.ID)
				} else if change == "registration" {
					m.Remove(t.Context(), a.ID)
					if _, err := m.Register(t.Context(), a.Clone()); err != nil {
						t.Fatal(err)
					}
				} else {
					_, _, err := m.Modify(t.Context(), a.ID, a.RegistrationEpoch, func(current *coreauth.Auth) error {
						switch change {
						case "token":
							current.Metadata["access_token"] = "rotated"
						case "account":
							current.Metadata["account_id"] = "replacement"
						case "origin":
							current.Metadata["base_url"] = "https://api.business.githubcopilot.com"
						case "catalog":
							current.CopilotCatalog = newer
						case "disabled":
							current.Disabled = true
						}
						return nil
					})
					if err != nil {
						t.Fatal(err)
					}
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[]}`)), Request: req}, nil
			}))
			r.reconcile(ctx, time.Now())
			job := <-r.jobs
			err := r.fetch(job)
			current, exists := m.GetByID(a.ID)
			if change == "removed" {
				if exists {
					t.Fatal("discovery resurrected removed credential")
				}
				return
			}
			if err == nil {
				t.Fatal("stale directory was accepted")
			}
			want := a.CopilotCatalog
			if change == "catalog" {
				want = newer
			} else if change == "registration" {
				want = nil
			} else if change == "account" || change == "origin" || change == "disabled" {
				want = copilot.UnavailableCatalog
			}
			if !exists || current.CopilotCatalog != want {
				t.Fatal("stale directory overwrote current state")
			}
		})
	}
}

type copilotOtherRefreshExecutor struct {
	serviceTestPluginExecutor
	refreshed chan struct{}
}

func (*copilotOtherRefreshExecutor) Identifier() string { return "other" }
func (e *copilotOtherRefreshExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	updated := auth.Clone()
	updated.Metadata["access_token"] = "new-other"
	select {
	case e.refreshed <- struct{}{}:
	default:
	}
	return updated, nil
}

type copilotOtherRefreshEvaluator struct{}

func (copilotOtherRefreshEvaluator) ShouldRefresh(_ time.Time, auth *coreauth.Auth) bool {
	return auth.Metadata["access_token"] == "old-other"
}

func TestCopilotBlockedCatalogDoesNotBlockSharedRefreshAndStops(t *testing.T) {
	cfg := &config.Config{AuthAutoRefreshWorkers: 1}
	m := coreauth.NewManager(nil, nil, nil)
	m.SetConfig(cfg)
	other := &copilotOtherRefreshExecutor{refreshed: make(chan struct{}, 2)}
	m.RegisterExecutor(other)
	m.RegisterExecutor(runtimeexecutor.NewCopilotExecutor(cfg))
	a := registerCopilotCatalogTestAuth(t, m, time.Now().Add(-copilot.CatalogMaxAge*3/4))
	if _, err := m.Register(t.Context(), &coreauth.Auth{ID: "other-" + t.Name(), Provider: "other", Status: coreauth.StatusActive, Runtime: copilotOtherRefreshEvaluator{}, Metadata: map[string]any{"access_token": "old-other", "refresh_token": "grant"}}); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 8)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx = context.WithValue(ctx, "cliproxy.roundtripper", copilotCatalogTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/models" {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-req.Context().Done()
			return nil, req.Context().Err()
		}
		if req.URL.Path != "/copilot_internal/v2/token" {
			return nil, errors.New("unexpected request")
		}
		payload := fmt.Sprintf(`{"token":"new-copilot","expires_at":%d,"endpoints":{"api":"https://api.individual.githubcopilot.com"}}`, time.Now().Add(time.Hour).Unix())
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload)), Request: req}, nil
	}))
	s := &Service{cfg: cfg, coreManager: m}
	r := s.startCopilotCatalogRefresh(ctx)
	defer func() { r.cancel(); <-r.done }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("directory request did not start")
	}
	m.StartAutoRefresh(ctx, time.Millisecond)
	defer m.StopAutoRefresh()
	select {
	case <-other.refreshed:
	case <-time.After(5 * time.Second):
		t.Fatal("Copilot discovery blocked another provider's automatic refresh")
	}
	finished := make(chan []coreauth.ForceRefreshResult, 1)
	go func() { finished <- m.ForceRefreshAll(ctx) }()
	select {
	case results := <-finished:
		if len(results) != 2 {
			t.Fatalf("wrong forced refresh results: %+v", results)
		}
		for _, result := range results {
			if !result.Success {
				t.Fatalf("forced refresh failed: %+v", result)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Copilot discovery blocked the shared refresh-all worker")
	}
	current, _ := m.GetByID(a.ID)
	if current.Metadata["access_token"] != "new-copilot" {
		t.Fatal("blocked discovery delayed token publication")
	}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.done:
	default:
		t.Fatal("shutdown did not drain discovery")
	}
	for len(r.wake) > 0 {
		<-r.wake
	}
	if _, err := r.service.coreManager.UpdateRefreshedAuth(t.Context(), current, current.Clone()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.wake:
		t.Fatal("shutdown retained Copilot's hook subscription")
	default:
	}
}
