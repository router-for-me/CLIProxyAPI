package cliproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	internalregistry "github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func registeredModel(t *testing.T, authID, modelID string) *ModelInfo {
	t.Helper()
	for _, model := range GlobalModelRegistry().GetModelsForClient(authID) {
		if model != nil && model.ID == modelID {
			return model
		}
	}
	return nil
}

func TestParseClaudeAndCodexLiveModels(t *testing.T) {
	claude := parseClaudeLiveModels([]byte(`{"data":[
		{"type":"model","id":"claude-opus-9-9","display_name":"Claude Opus 9.9","created_at":"2027-01-02T03:04:05Z","max_input_tokens":500000,"max_tokens":64000},
		{"type":"model","id":"not-a-claude-model"},
		{"type":"model","id":""}
	],"has_more":false}`))
	if len(claude) != 1 {
		t.Fatalf("claude models = %+v, want one claude model", claude)
	}
	wantCreated := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC).Unix()
	if got := claude[0]; got.id != "claude-opus-9-9" || got.displayName != "Claude Opus 9.9" || got.created != wantCreated || got.contextLength != 500000 || got.maxCompletionTokens != 64000 {
		t.Fatalf("claude model = %+v", got)
	}

	codex := parseCodexLiveModels([]byte(`{"models":[
		{"slug":"gpt-9-live","display_name":"GPT-9 Live","description":"Live model","context_window":400000,
		 "supported_reasoning_levels":[{"effort":"low"},{"effort":"high"}],"input_modalities":["text","image"],"available_in_plans":["Pro","plus"]},
		{"display_name":"no slug"}
	]}`))
	if len(codex) != 1 {
		t.Fatalf("codex models = %+v, want one model", codex)
	}
	got := codex[0]
	if got.id != "gpt-9-live" || got.displayName != "GPT-9 Live" || got.description != "Live model" || got.contextLength != 400000 {
		t.Fatalf("codex model = %+v", got)
	}
	if len(got.thinkingLevels) != 2 || got.thinkingLevels[1] != "high" || len(got.inputModalities) != 2 || len(got.plans) != 2 || got.plans[0] != "pro" {
		t.Fatalf("codex model lists = %+v", got)
	}
}

func TestParseMetaLiveModels(t *testing.T) {
	models := parseMetaLiveModels([]byte(`{"object":"list","data":[
		{"id":"muse-live-9","object":"model","created":1791662408,"owned_by":"meta","metadata":{"muse-code":{
		 "name":"Muse Live 9","description":"Live model","is_hidden":false,"reasoning":true,
		 "modalities":{"input":["text","image"],"output":["text"]},"limit":{"context":1007997,"output":128000},
		 "variants":{"max":{"reasoningEffort":"max"},"fast":{"reasoningEffort":"minimal"},"high":{},"turbo":{"reasoningEffort":"turbo"}}}}},
		{"id":"muse-plain-9","metadata":{"muse-code":{"reasoning":false,"modalities":{"output":["text"]},"variants":{"low":{"reasoningEffort":"low"}}}}},
		{"id":"muse-hidden-9","metadata":{"muse-code":{"is_hidden":true,"modalities":{"output":["text"]}}}},
		{"id":"muse-image-9","metadata":{"muse-code":{"modalities":{"input":["text"],"output":["image"]}}}},
		{"id":"gpt-9-live","metadata":{"muse-code":{"modalities":{"output":["text"]}}}},
		{"id":"muse-bare-9"},
		{"id":""}
	]}`))
	if len(models) != 2 || models[0].id != "muse-live-9" || models[1].id != "muse-plain-9" {
		t.Fatalf("meta models = %+v, want the two visible Muse text models", models)
	}
	got := models[0]
	if got.displayName != "Muse Live 9" || got.description != "Live model" || got.created != 1791662408 || got.contextLength != 1007997 || got.maxCompletionTokens != 128000 {
		t.Fatalf("meta model = %+v", got)
	}
	// Variants are read by their reasoningEffort (or name), in the standard order,
	// and names the thinking levels do not know are dropped.
	if levels := strings.Join(got.thinkingLevels, ","); levels != "minimal,high,max" || len(got.inputModalities) != 2 || got.noThinking {
		t.Fatalf("meta model levels = %q, modalities = %v, noThinking = %v", levels, got.inputModalities, got.noThinking)
	}
	plain := models[1]
	if !plain.noThinking || len(plain.thinkingLevels) != 0 {
		t.Fatalf("model without reasoning = %+v, want no reasoning levels", plain)
	}
	if info := liveModelInfo("meta", plain, internalregistry.GetMetaModels()); info.Thinking != nil {
		t.Fatalf("a model without reasoning inherited reasoning levels %+v", info.Thinking)
	}
}

func TestLiveModelProviderTakesMetaLoginsOnly(t *testing.T) {
	for _, tc := range []struct {
		name string
		auth *coreauth.Auth
		want string
	}{
		{"meta login with a minted key", &coreauth.Auth{ID: "meta-file", Provider: "meta", Attributes: map[string]string{"api_key": "minted", "source_backend": "file", "source": "/auths/meta.json"}}, "meta"},
		{"meta login before minting", &coreauth.Auth{ID: "meta-dca", Provider: "meta", Metadata: map[string]any{"access_token": "dca:token"}}, "meta"},
		{"meta-api-key entry", &coreauth.Auth{ID: "meta-config", Provider: "meta", Attributes: map[string]string{"api_key": "sk-meta", "source": "config:meta[0]"}}, ""},
		{"disabled meta login", &coreauth.Auth{ID: "meta-off", Provider: "meta", Disabled: true}, ""},
		{"claude login", &coreauth.Auth{ID: "claude-oauth", Provider: "claude", Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"access_token": "token"}}, "claude"},
		{"xai login", &coreauth.Auth{ID: "xai-oauth", Provider: "xai", Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"access_token": "token"}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := liveModelProvider(tc.auth)
			if got != tc.want || ok != (tc.want != "") {
				t.Fatalf("liveModelProvider = %q, %v; want %q", got, ok, tc.want)
			}
		})
	}
}

func TestMetaModelsURLFollowsTheCredentialOrigin(t *testing.T) {
	for _, tc := range []struct {
		name string
		auth *coreauth.Auth
		want string
	}{
		{"base_url attribute", &coreauth.Auth{
			Attributes: map[string]string{"base_url": "https://meta.example.com:8443/v1"},
			Metadata:   map[string]any{"base_url": "https://ignored.example.com/v1"},
		}, "https://meta.example.com:8443/muse-code/models"},
		{"base_url metadata", &coreauth.Auth{Metadata: map[string]any{"base_url": "https://meta.example.com/v1"}}, "https://meta.example.com/muse-code/models"},
		{"api_base_url metadata", &coreauth.Auth{Metadata: map[string]any{"api_base_url": "http://127.0.0.1:9000/v1/"}}, "http://127.0.0.1:9000/muse-code/models"},
		{"default origin", &coreauth.Auth{}, "https://api.meta.ai/muse-code/models"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, errURL := metaModelsURL(tc.auth)
			if errURL != nil || got != tc.want {
				t.Fatalf("metaModelsURL = %q, %v; want %q", got, errURL, tc.want)
			}
		})
	}
	if got, errURL := metaModelsURL(&coreauth.Auth{Attributes: map[string]string{"base_url": "not a url"}}); errURL == nil {
		t.Fatalf("metaModelsURL accepted an invalid base URL: %q", got)
	}
}

func TestLiveModelAdditionsForBuildsFromClosestCatalogModel(t *testing.T) {
	catalog := internalregistry.GetClaudeModels()
	if len(catalog) == 0 {
		t.Fatal("claude catalog is empty")
	}
	var sibling *ModelInfo
	for _, model := range catalog {
		if model.ID == "claude-opus-5-5" {
			sibling = model
		}
	}
	if sibling == nil {
		t.Skip("claude-opus-5-5 is no longer in the catalog")
	}

	auth := &coreauth.Auth{ID: "live-additions-claude", Provider: "claude"}
	additions := liveModelAdditionsFor("claude", auth, []liveModel{
		{id: sibling.ID, displayName: "already known"},
		{id: "claude-opus-5-6", displayName: "Claude Opus 5.6", created: 1_800_000_000, contextLength: 777000},
	})

	if len(additions) != 1 {
		t.Fatalf("additions = %d, want only the model the catalog does not list", len(additions))
	}
	addition := additions[0]
	if addition.ID != "claude-opus-5-6" || addition.DisplayName != "Claude Opus 5.6" || addition.Created != 1_800_000_000 || addition.ContextLength != 777000 {
		t.Fatalf("addition = %+v", addition)
	}
	if addition.OwnedBy != sibling.OwnedBy || addition.Type != sibling.Type || addition.MaxCompletionTokens != sibling.MaxCompletionTokens {
		t.Fatalf("addition did not inherit from %s: %+v", sibling.ID, addition)
	}
	if (addition.Thinking == nil) != (sibling.Thinking == nil) {
		t.Fatalf("addition thinking = %+v, want it inherited from %s", addition.Thinking, sibling.ID)
	}
	if addition.Thinking != nil && addition.Thinking == sibling.Thinking {
		t.Fatal("addition shares the thinking definition of the catalog model")
	}
}

func TestLiveModelAdditionsForCodexHonoursPlan(t *testing.T) {
	discovered := []liveModel{
		{id: "gpt-9-live", plans: []string{"pro", "plus"}, thinkingLevels: []string{"low", "high"}},
		{id: "gpt-9-team-only", plans: []string{"team"}},
		{id: "gpt-9-everyone"},
	}
	auth := &coreauth.Auth{ID: "live-additions-codex", Provider: "codex", Attributes: map[string]string{"plan_type": "pro"}}

	additions := liveModelAdditionsFor("codex", auth, discovered)

	if len(additions) != 2 || additions[0].ID != "gpt-9-everyone" || additions[1].ID != "gpt-9-live" {
		t.Fatalf("additions = %+v, want the two models available on the pro plan", additions)
	}
	if levels := additions[1].Thinking; levels == nil || len(levels.Levels) != 2 || levels.Levels[1] != "high" {
		t.Fatalf("thinking levels = %+v, want the levels the provider reported", levels)
	}
}

func TestSyncLiveModelsRegistersDiscoveredModels(t *testing.T) {
	knownClaude := internalregistry.GetClaudeModels()[0].ID
	knownCodex := internalregistry.GetCodexProModels()[0].ID

	var mu sync.Mutex
	requests := map[string]int{}
	var codexQuery, codexAccount string
	claudeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests["claude"]++
		mu.Unlock()
		_, _ = w.Write([]byte(`{"data":[{"id":"` + knownClaude + `"},{"id":"claude-live-sync-test","display_name":"Claude Live Sync","max_input_tokens":123456}]}`))
	}))
	defer claudeServer.Close()
	codexServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests["codex"]++
		codexQuery = r.URL.Query().Get("client_version")
		codexAccount = r.Header.Get("Chatgpt-Account-Id")
		mu.Unlock()
		_, _ = w.Write([]byte(`{"models":[{"slug":"` + knownCodex + `"},{"slug":"gpt-live-sync-test","display_name":"GPT Live Sync","context_window":654321}]}`))
	}))
	defer codexServer.Close()

	originalClaudeURL, originalCodexURL := claudeModelsURL, codexModelsURL
	claudeModelsURL, codexModelsURL = claudeServer.URL+"/v1/models?limit=1000", codexServer.URL+"/models"
	t.Cleanup(func() { claudeModelsURL, codexModelsURL = originalClaudeURL, originalCodexURL })

	ctx := context.Background()
	manager := coreauth.NewManager(nil, nil, nil)
	claudeID, codexID, apiKeyID := "live-sync-claude", "live-sync-codex", "live-sync-claude-api-key"
	for _, auth := range []*coreauth.Auth{
		{ID: claudeID, Provider: "claude", Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"access_token": "claude-token"}},
		{ID: codexID, Provider: "codex", Attributes: map[string]string{"auth_kind": "oauth", "plan_type": "pro"}, Metadata: map[string]any{"access_token": "codex-token", "account_id": "acct-7"}},
		{ID: apiKeyID, Provider: "claude", Attributes: map[string]string{"auth_kind": "apikey", "api_key": "sk-test"}},
	} {
		auth.Status = coreauth.StatusActive
		if _, errRegister := manager.Register(coreauth.WithSkipPersist(ctx), auth); errRegister != nil {
			t.Fatalf("Register(%s): %v", auth.ID, errRegister)
		}
		authID := auth.ID
		t.Cleanup(func() {
			GlobalModelRegistry().UnregisterClient(authID)
			liveModelAdditions.set(authID, nil)
		})
	}

	cfg := &internalconfig.Config{}
	cfg.Claude.LiveModels = true
	cfg.Codex.LiveModels = true
	service := &Service{cfg: cfg, coreManager: manager}
	nextSync := make(map[string]time.Time)
	now := time.Now()

	service.syncLiveModels(ctx, nextSync, now)

	claudeModel := registeredModel(t, claudeID, "claude-live-sync-test")
	if claudeModel == nil {
		t.Fatal("the discovered Claude model was not registered")
	}
	if claudeModel.DisplayName != "Claude Live Sync" || claudeModel.ContextLength != 123456 {
		t.Fatalf("discovered Claude model = %+v", claudeModel)
	}
	if registeredModel(t, claudeID, knownClaude) == nil {
		t.Fatalf("catalog model %s was dropped", knownClaude)
	}
	codexModel := registeredModel(t, codexID, "gpt-live-sync-test")
	if codexModel == nil || codexModel.ContextLength != 654321 {
		t.Fatalf("discovered Codex model = %+v", codexModel)
	}
	if registeredModel(t, apiKeyID, "claude-live-sync-test") != nil {
		t.Fatal("an API key credential received live models")
	}
	mu.Lock()
	gotQuery, gotAccount := codexQuery, codexAccount
	mu.Unlock()
	if gotQuery != codexLiveClientVersion || gotAccount != "acct-7" {
		t.Fatalf("codex request: client_version=%q account=%q", gotQuery, gotAccount)
	}

	// Within the sync interval nothing is read again.
	service.syncLiveModels(ctx, nextSync, now.Add(liveModelSyncTick))
	mu.Lock()
	claudeRequests, codexRequests := requests["claude"], requests["codex"]
	mu.Unlock()
	if claudeRequests != 1 || codexRequests != 1 {
		t.Fatalf("requests after a second tick = claude %d, codex %d; want 1 and 1", claudeRequests, codexRequests)
	}

	// Turning discovery off for a provider removes its additions.
	cfg.Claude.LiveModels = false
	service.syncLiveModels(ctx, nextSync, now.Add(2*liveModelSyncTick))
	if registeredModel(t, claudeID, "claude-live-sync-test") != nil {
		t.Fatal("the discovered Claude model stayed registered after discovery was turned off")
	}
	if registeredModel(t, claudeID, knownClaude) == nil {
		t.Fatalf("catalog model %s was dropped when discovery was turned off", knownClaude)
	}
	if registeredModel(t, codexID, "gpt-live-sync-test") == nil {
		t.Fatal("turning off Claude discovery removed a Codex addition")
	}
}

func TestSyncLiveModelsRegistersMetaModels(t *testing.T) {
	knownMeta := internalregistry.GetMetaModels()[0].ID

	var mu sync.Mutex
	var paths []string
	var authorization, clientID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		authorization, clientID = r.Header.Get("Authorization"), r.Header.Get("X-Client-Id")
		mu.Unlock()
		_, _ = w.Write([]byte(`{"object":"list","data":[
			{"id":"` + knownMeta + `","metadata":{"muse-code":{"modalities":{"output":["text"]}}}},
			{"id":"muse-live-sync-test","created":1791662408,"metadata":{"muse-code":{"name":"muse-live-sync-test",
			 "modalities":{"input":["text","image"],"output":["text"]},"limit":{"context":777777,"output":128000},
			 "variants":{"low":{"reasoningEffort":"low"},"max":{"reasoningEffort":"max"}}}}},
			{"id":"muse-hidden-sync-test","metadata":{"muse-code":{"is_hidden":true}}}
		]}`))
	}))
	defer server.Close()

	ctx := context.Background()
	cfg := &internalconfig.Config{}
	cfg.Meta.LiveModels = true
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(runtimeexecutor.NewMetaExecutor(cfg))
	metaID, configKeyID := "live-sync-meta", "live-sync-meta-config-key"
	for _, auth := range []*coreauth.Auth{
		{ID: metaID, Provider: "meta", Attributes: map[string]string{"base_url": server.URL + "/v1", "api_key": "meta-minted-key", "source_backend": "file", "source": "/auths/meta-test.json"}},
		{ID: configKeyID, Provider: "meta", Attributes: map[string]string{"base_url": server.URL + "/v1", "api_key": "sk-meta-config", "source": "config:meta[0]"}},
	} {
		auth.Status = coreauth.StatusActive
		if _, errRegister := manager.Register(coreauth.WithSkipPersist(ctx), auth); errRegister != nil {
			t.Fatalf("Register(%s): %v", auth.ID, errRegister)
		}
		authID := auth.ID
		t.Cleanup(func() {
			GlobalModelRegistry().UnregisterClient(authID)
			liveModelAdditions.set(authID, nil)
		})
	}
	service := &Service{cfg: cfg, coreManager: manager}

	service.syncLiveModels(ctx, make(map[string]time.Time), time.Now())

	model := registeredModel(t, metaID, "muse-live-sync-test")
	if model == nil {
		t.Fatal("the discovered Meta model was not registered")
	}
	if model.OwnedBy != "meta" || model.Created != 1791662408 || model.ContextLength != 777777 || model.MaxCompletionTokens != 128000 {
		t.Fatalf("discovered Meta model = %+v", model)
	}
	if model.Thinking == nil || len(model.Thinking.Levels) != 2 || model.Thinking.Levels[1] != "max" {
		t.Fatalf("thinking = %+v, want the reasoning variants Meta reported", model.Thinking)
	}
	if registeredModel(t, metaID, knownMeta) == nil {
		t.Fatalf("catalog model %s was dropped", knownMeta)
	}
	if registeredModel(t, metaID, "muse-hidden-sync-test") != nil {
		t.Fatal("a model Meta hides was registered")
	}
	if registeredModel(t, configKeyID, "muse-live-sync-test") != nil {
		t.Fatal("a meta-api-key entry received live models")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 1 || paths[0] != metaModelsPath {
		t.Fatalf("requests = %v, want one read of %s", paths, metaModelsPath)
	}
	if authorization != "Bearer meta-minted-key" || clientID != "tbh:tui" {
		t.Fatalf("catalog request carried Authorization %q and X-Client-Id %q", authorization, clientID)
	}
}

func TestSyncLiveModelsMintsMetaKeyOnce(t *testing.T) {
	var mu sync.Mutex
	mints := 0
	var catalogAuthorization []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/muse-code/key":
			mints++
			_, _ = w.Write([]byte(`{"api_key":"LLM|minted-for-sync"}`))
		case metaModelsPath:
			catalogAuthorization = append(catalogAuthorization, r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"data":[{"id":"muse-mint-sync-test","metadata":{"muse-code":{"modalities":{"output":["text"]}}}}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	t.Setenv("META_MINT_URL", server.URL+"/muse-code/key")

	ctx := context.Background()
	cfg := &internalconfig.Config{}
	cfg.Meta.LiveModels = true
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(runtimeexecutor.NewMetaExecutor(cfg))
	authID := "live-sync-meta-dca"
	if _, errRegister := manager.Register(coreauth.WithSkipPersist(ctx), &coreauth.Auth{
		ID: authID, Provider: "meta", Status: coreauth.StatusActive,
		Attributes: map[string]string{"base_url": server.URL + "/v1", "auth_kind": "oauth", "source_backend": "file", "source": "/auths/meta-dca.json"},
		Metadata:   map[string]any{"type": "meta", "access_token": "dca:sync-test", "dca_token": "dca:sync-test"},
	}); errRegister != nil {
		t.Fatalf("Register: %v", errRegister)
	}
	t.Cleanup(func() {
		GlobalModelRegistry().UnregisterClient(authID)
		liveModelAdditions.set(authID, nil)
	})
	service := &Service{cfg: cfg, coreManager: manager}
	nextSync := make(map[string]time.Time)
	now := time.Now()

	service.syncLiveModels(ctx, nextSync, now)
	service.syncLiveModels(ctx, nextSync, now.Add(liveModelSyncInterval+time.Second))

	if registeredModel(t, authID, "muse-mint-sync-test") == nil {
		t.Fatal("the discovered Meta model was not registered")
	}
	stored, ok := manager.GetByID(authID)
	if !ok || stored.Attributes["api_key"] != "LLM|minted-for-sync" {
		t.Fatal("the minted key was not saved on the credential")
	}
	mu.Lock()
	defer mu.Unlock()
	if mints != 1 {
		t.Fatalf("mints = %d over two reads, want 1", mints)
	}
	if len(catalogAuthorization) != 2 || catalogAuthorization[0] != "Bearer LLM|minted-for-sync" || catalogAuthorization[1] != catalogAuthorization[0] {
		t.Fatalf("catalog reads carried %q, want the minted key both times", catalogAuthorization)
	}
}

func TestSyncLiveModelsKeepsMetaModelsWhenTheListIsMissing(t *testing.T) {
	var mu sync.Mutex
	answer := `{"data":[{"id":"muse-kept-test","metadata":{"muse-code":{"modalities":{"output":["text"]}}}}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_, _ = w.Write([]byte(answer))
	}))
	defer server.Close()

	ctx := context.Background()
	cfg := &internalconfig.Config{}
	cfg.Meta.LiveModels = true
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(runtimeexecutor.NewMetaExecutor(cfg))
	authID := "live-sync-meta-kept"
	if _, errRegister := manager.Register(coreauth.WithSkipPersist(ctx), &coreauth.Auth{
		ID: authID, Provider: "meta", Status: coreauth.StatusActive,
		Attributes: map[string]string{"base_url": server.URL + "/v1", "api_key": "meta-key", "auth_kind": "oauth", "source_backend": "file", "source": "/auths/meta-kept.json"},
	}); errRegister != nil {
		t.Fatalf("Register: %v", errRegister)
	}
	t.Cleanup(func() {
		GlobalModelRegistry().UnregisterClient(authID)
		liveModelAdditions.set(authID, nil)
	})
	service := &Service{cfg: cfg, coreManager: manager}
	nextSync := make(map[string]time.Time)
	now := time.Now()

	service.syncLiveModels(ctx, nextSync, now)
	if registeredModel(t, authID, "muse-kept-test") == nil {
		t.Fatal("the discovered Meta model was not registered")
	}
	mu.Lock()
	answer = `{"error":"maintenance"}`
	mu.Unlock()
	service.syncLiveModels(ctx, nextSync, now.Add(liveModelSyncInterval+time.Second))
	if registeredModel(t, authID, "muse-kept-test") == nil {
		t.Fatal("an answer without the model list dropped the models found before")
	}
}

func TestSyncLiveModelsBacksOffAfterFailure(t *testing.T) {
	requests := 0
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	originalClaudeURL := claudeModelsURL
	claudeModelsURL = server.URL
	t.Cleanup(func() { claudeModelsURL = originalClaudeURL })

	ctx := context.Background()
	manager := coreauth.NewManager(nil, nil, nil)
	authID := "live-sync-failing"
	if _, errRegister := manager.Register(coreauth.WithSkipPersist(ctx), &coreauth.Auth{
		ID: authID, Provider: "claude", Status: coreauth.StatusActive,
		Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"access_token": "token"},
	}); errRegister != nil {
		t.Fatalf("Register: %v", errRegister)
	}
	cfg := &internalconfig.Config{}
	cfg.Claude.LiveModels = true
	service := &Service{cfg: cfg, coreManager: manager}
	nextSync := make(map[string]time.Time)
	now := time.Now()

	service.syncLiveModels(ctx, nextSync, now)
	service.syncLiveModels(ctx, nextSync, now.Add(liveModelSyncTick))
	service.syncLiveModels(ctx, nextSync, now.Add(liveModelSyncRetryBackoff+time.Second))

	mu.Lock()
	defer mu.Unlock()
	if requests != 2 {
		t.Fatalf("requests = %d, want one attempt and one retry after the backoff", requests)
	}
}
