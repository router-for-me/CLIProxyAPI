package cliproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	codexmodels "github.com/router-for-me/CLIProxyAPI/v8/internal/client/codex/models"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	internalregistry "github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
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

func waitForRegisteredModel(t *testing.T, authID, modelID string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if registeredModel(t, authID, modelID) != nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("model %s was not registered for %s", modelID, authID)
}

// forgetLiveModelAdditions drops whatever was read for the credential, under
// any of its live model keys.
func forgetLiveModelAdditions(authID string) {
	liveModelAdditions.mu.Lock()
	defer liveModelAdditions.mu.Unlock()
	for key := range liveModelAdditions.additions {
		if strings.HasPrefix(key, authID+"\x00") {
			delete(liveModelAdditions.additions, key)
		}
	}
}

// syncLiveModelsAndWait runs one check and waits for the reads it started.
func syncLiveModelsAndWait(ctx context.Context, service *Service, state *liveModelSyncState, now time.Time) {
	service.syncLiveModels(ctx, state, now)
	state.reads.Wait()
}

func registerLiveTestAuths(t *testing.T, ctx context.Context, manager *coreauth.Manager, auths ...*coreauth.Auth) {
	t.Helper()
	for _, auth := range auths {
		auth.Status = coreauth.StatusActive
		if _, errRegister := manager.Register(coreauth.WithSkipPersist(ctx), auth); errRegister != nil {
			t.Fatalf("Register(%s): %v", auth.ID, errRegister)
		}
		authID := auth.ID
		t.Cleanup(func() {
			GlobalModelRegistry().UnregisterClient(authID)
			forgetLiveModelAdditions(authID)
		})
	}
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
		{"id":"muse-no-output-9","metadata":{"muse-code":{"name":"No output list","modalities":{"input":["text"]}}}},
		{"id":"muse-no-modalities-9","metadata":{"muse-code":{"name":"No modalities"}}}},
		{"id":"muse-output-string-9","metadata":{"muse-code":{"modalities":{"output":"text"}}}},
		{"id":"gpt-9-live","metadata":{"muse-code":{"modalities":{"output":["text"]}}}},
		{"id":"muse-bare-9"},
		{"id":""}
	]}`))
	if len(models) != 2 || models[0].id != "muse-live-9" || models[1].id != "muse-plain-9" {
		t.Fatalf("meta models = %+v, want only the two visible Muse models whose output list has text", models)
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
	if info := liveModelInfo("meta", plain, internalregistry.GetMetaModels()); info.Thinking != nil || !info.ExplicitThinking {
		t.Fatalf("model without reasoning: thinking %+v, explicit %v; want no thinking, marked explicit", info.Thinking, info.ExplicitThinking)
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
			forgetLiveModelAdditions(authID)
		})
	}

	cfg := &internalconfig.Config{}
	cfg.Claude.LiveModels = true
	cfg.Codex.LiveModels = true
	service := &Service{cfg: cfg, coreManager: manager}
	state := newLiveModelSyncState()
	now := time.Now()

	syncLiveModelsAndWait(ctx, service, state, now)

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
	syncLiveModelsAndWait(ctx, service, state, now.Add(liveModelSyncTick))
	mu.Lock()
	claudeRequests, codexRequests := requests["claude"], requests["codex"]
	mu.Unlock()
	if claudeRequests != 1 || codexRequests != 1 {
		t.Fatalf("requests after a second tick = claude %d, codex %d; want 1 and 1", claudeRequests, codexRequests)
	}

	// Turning discovery off for a provider removes its additions.
	cfg.Claude.LiveModels = false
	syncLiveModelsAndWait(ctx, service, state, now.Add(2*liveModelSyncTick))
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
			forgetLiveModelAdditions(authID)
		})
	}
	service := &Service{cfg: cfg, coreManager: manager}

	syncLiveModelsAndWait(ctx, service, newLiveModelSyncState(), time.Now())

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
		forgetLiveModelAdditions(authID)
	})
	service := &Service{cfg: cfg, coreManager: manager}
	state := newLiveModelSyncState()
	now := time.Now()

	syncLiveModelsAndWait(ctx, service, state, now)
	syncLiveModelsAndWait(ctx, service, state, now.Add(liveModelSyncInterval+time.Second))

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
		forgetLiveModelAdditions(authID)
	})
	service := &Service{cfg: cfg, coreManager: manager}
	state := newLiveModelSyncState()
	now := time.Now()

	syncLiveModelsAndWait(ctx, service, state, now)
	if registeredModel(t, authID, "muse-kept-test") == nil {
		t.Fatal("the discovered Meta model was not registered")
	}
	mu.Lock()
	answer = `{"error":"maintenance"}`
	mu.Unlock()
	syncLiveModelsAndWait(ctx, service, state, now.Add(liveModelSyncInterval+time.Second))
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
	state := newLiveModelSyncState()
	now := time.Now()

	syncLiveModelsAndWait(ctx, service, state, now)
	syncLiveModelsAndWait(ctx, service, state, now.Add(liveModelSyncTick))
	syncLiveModelsAndWait(ctx, service, state, now.Add(liveModelSyncRetryBackoff+time.Second))

	mu.Lock()
	defer mu.Unlock()
	if requests != 2 {
		t.Fatalf("requests = %d, want one attempt and one retry after the backoff", requests)
	}
}

func TestLiveModelSyncStateSkipsACredentialWhileItsReadRuns(t *testing.T) {
	state := newLiveModelSyncState()
	now := time.Now()

	if !state.begin("a", now) {
		t.Fatal("a credential that was never read is not due")
	}
	if state.begin("a", now.Add(2*liveModelSyncInterval)) {
		t.Fatal("a second read started while the first is still running")
	}
	if !state.begin("b", now) {
		t.Fatal("a running read held up another credential")
	}
	state.finish("a", now.Add(liveModelSyncInterval))
	if state.begin("a", now.Add(liveModelSyncTick)) {
		t.Fatal("a credential was read again before it was due")
	}
	if !state.begin("a", now.Add(liveModelSyncInterval)) {
		t.Fatal("a credential is not read again once it is due")
	}
}

func TestSyncLiveModelsDoesNotWaitForAStalledRead(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseStalled := func() { releaseOnce.Do(func() { close(release) }) }
	var mu sync.Mutex
	requests := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		requests[token]++
		mu.Unlock()
		if token == "stalled-token" {
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-` + strings.TrimSuffix(token, "-token") + `-live-test"}]}`))
	}))
	defer server.Close()
	defer releaseStalled()
	originalClaudeURL := claudeModelsURL
	claudeModelsURL = server.URL
	t.Cleanup(func() { claudeModelsURL = originalClaudeURL })

	ctx := context.Background()
	manager := coreauth.NewManager(nil, nil, nil)
	stalledID, fastID := "live-sync-stalled", "live-sync-fast"
	registerLiveTestAuths(t, ctx, manager,
		&coreauth.Auth{ID: stalledID, Provider: "claude", Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"access_token": "stalled-token"}},
		&coreauth.Auth{ID: fastID, Provider: "claude", Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"access_token": "fast-token"}},
	)
	cfg := &internalconfig.Config{}
	cfg.Claude.LiveModels = true
	service := &Service{cfg: cfg, coreManager: manager}
	state := newLiveModelSyncState()
	now := time.Now()

	returned := make(chan struct{})
	go func() {
		service.syncLiveModels(ctx, state, now)
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(10 * time.Second):
		t.Fatal("the check waited for a read that does not answer")
	}
	waitForRegisteredModel(t, fastID, "claude-fast-live-test")

	// The next check does not start a second read of the stalled credential.
	service.syncLiveModels(ctx, state, now.Add(liveModelSyncInterval+time.Second))
	releaseStalled()
	state.reads.Wait()

	mu.Lock()
	stalledReads := requests["stalled-token"]
	mu.Unlock()
	if stalledReads != 1 {
		t.Fatalf("the stalled credential was read %d times, want once", stalledReads)
	}
	if registeredModel(t, stalledID, "claude-stalled-live-test") == nil {
		t.Fatal("the stalled read was not registered once it answered")
	}
}

func TestSyncLiveModelsKeepsAdditionsWhenAnAnswerHasNoList(t *testing.T) {
	var mu sync.Mutex
	requests := map[string]int{}
	answers := map[string]string{
		"claude": `{"data":[{"id":"claude-kept-live-test"}]}`,
		"codex":  `{"models":[{"slug":"gpt-kept-live-test"}]}`,
	}
	serve := func(provider string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			requests[provider]++
			_, _ = w.Write([]byte(answers[provider]))
		}))
	}
	claudeServer, codexServer := serve("claude"), serve("codex")
	defer claudeServer.Close()
	defer codexServer.Close()
	originalClaudeURL, originalCodexURL := claudeModelsURL, codexModelsURL
	claudeModelsURL, codexModelsURL = claudeServer.URL, codexServer.URL
	t.Cleanup(func() { claudeModelsURL, codexModelsURL = originalClaudeURL, originalCodexURL })

	ctx := context.Background()
	manager := coreauth.NewManager(nil, nil, nil)
	claudeID, codexID := "live-sync-claude-kept", "live-sync-codex-kept"
	registerLiveTestAuths(t, ctx, manager,
		&coreauth.Auth{ID: claudeID, Provider: "claude", Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"access_token": "claude-token"}},
		&coreauth.Auth{ID: codexID, Provider: "codex", Attributes: map[string]string{"auth_kind": "oauth", "plan_type": "pro"}, Metadata: map[string]any{"access_token": "codex-token"}},
	)
	cfg := &internalconfig.Config{}
	cfg.Claude.LiveModels = true
	cfg.Codex.LiveModels = true
	service := &Service{cfg: cfg, coreManager: manager}
	state := newLiveModelSyncState()
	now := time.Now()

	syncLiveModelsAndWait(ctx, service, state, now)
	if registeredModel(t, claudeID, "claude-kept-live-test") == nil || registeredModel(t, codexID, "gpt-kept-live-test") == nil {
		t.Fatal("the discovered models were not registered")
	}

	// A successful answer without the list keeps the models found before ...
	mu.Lock()
	answers["claude"], answers["codex"] = `{"type":"error","error":{"type":"overloaded_error"}}`, `{"detail":"maintenance"}`
	mu.Unlock()
	failedAt := now.Add(liveModelSyncInterval + time.Second)
	syncLiveModelsAndWait(ctx, service, state, failedAt)
	if registeredModel(t, claudeID, "claude-kept-live-test") == nil {
		t.Fatal("a Claude answer without its data list dropped the models found before")
	}
	if registeredModel(t, codexID, "gpt-kept-live-test") == nil {
		t.Fatal("a Codex answer without its models list dropped the models found before")
	}

	// ... and is retried after the backoff rather than an hour later.
	syncLiveModelsAndWait(ctx, service, state, failedAt.Add(liveModelSyncRetryBackoff))
	mu.Lock()
	defer mu.Unlock()
	if requests["claude"] != 3 || requests["codex"] != 3 {
		t.Fatalf("reads = claude %d, codex %d; want a retry of each after the backoff (3 and 3)", requests["claude"], requests["codex"])
	}
}

// liveCodexClientCatalogAnswer is a Codex model catalog answer with one valid
// client entry, built from the first entry of the served catalog.
func liveCodexClientCatalogAnswer(t *testing.T, slug string) string {
	t.Helper()
	var catalog struct {
		Models []map[string]any `json:"models"`
	}
	if errDecode := json.Unmarshal(internalregistry.GetCodexClientModelsJSON(), &catalog); errDecode != nil || len(catalog.Models) == 0 {
		t.Fatalf("decode served Codex client catalog: %v", errDecode)
	}
	entry := make(map[string]any, len(catalog.Models[0]))
	for key, value := range catalog.Models[0] {
		entry[key] = value
	}
	entry["slug"], entry["display_name"] = slug, "Live "+slug
	answer, errEncode := json.Marshal(map[string]any{"models": []any{entry}})
	if errEncode != nil {
		t.Fatalf("encode Codex catalog answer: %v", errEncode)
	}
	return string(answer)
}

func servesCodexClientSlug(slug string) bool {
	return gjson.GetBytes(internalregistry.GetCodexClientModelsJSON(), `models.#(slug=="`+slug+`")`).Exists()
}

func TestSyncLiveModelsDropsCodexClientEntriesWithTheirCredentials(t *testing.T) {
	t.Cleanup(func() { internalregistry.RetainLiveCodexClientModels(nil) })
	base := internalregistry.GetCodexClientModelsJSON()
	answer := liveCodexClientCatalogAnswer(t, "gpt-overlay-live-test")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(answer))
	}))
	defer server.Close()
	originalCodexURL := codexModelsURL
	codexModelsURL = server.URL
	t.Cleanup(func() { codexModelsURL = originalCodexURL })

	ctx := context.Background()
	manager := coreauth.NewManager(nil, nil, nil)
	codexID := "live-sync-codex-overlay"
	registerLiveTestAuths(t, ctx, manager,
		&coreauth.Auth{ID: codexID, Provider: "codex", Attributes: map[string]string{"auth_kind": "oauth", "plan_type": "pro"}, Metadata: map[string]any{"access_token": "codex-token"}},
	)
	cfg := &internalconfig.Config{}
	cfg.Codex.LiveModels = true
	service := &Service{cfg: cfg, coreManager: manager}
	state := newLiveModelSyncState()
	now := time.Now()

	syncLiveModelsAndWait(ctx, service, state, now)
	if !servesCodexClientSlug("gpt-overlay-live-test") {
		t.Fatal("the Codex client catalog does not serve the live entry")
	}

	// Turning discovery off returns the Codex client catalog to the catalog alone.
	cfg.Codex.LiveModels = false
	syncLiveModelsAndWait(ctx, service, state, now.Add(liveModelSyncTick))
	if !bytes.Equal(internalregistry.GetCodexClientModelsJSON(), base) {
		t.Fatal("the Codex client catalog kept live entries after discovery was turned off")
	}

	// Turned on again, the credential is read at the next check.
	cfg.Codex.LiveModels = true
	syncLiveModelsAndWait(ctx, service, state, now.Add(2*liveModelSyncTick))
	if !servesCodexClientSlug("gpt-overlay-live-test") {
		t.Fatal("the live entry did not come back when discovery was turned on again")
	}

	// Removing the last Codex login does the same.
	manager.Remove(ctx, codexID)
	syncLiveModelsAndWait(ctx, service, state, now.Add(3*liveModelSyncTick))
	if !bytes.Equal(internalregistry.GetCodexClientModelsJSON(), base) {
		t.Fatal("the Codex client catalog kept live entries after the last Codex login was removed")
	}
}

func TestSyncLiveModelsMetaModelWithoutReasoningShowsNoReasoningLevels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[
			{"id":"muse-plain-live-test","metadata":{"muse-code":{"reasoning":false,"modalities":{"output":["text"]},"variants":{"high":{"reasoningEffort":"high"}}}}},
			{"id":"muse-reasoning-live-test","metadata":{"muse-code":{"modalities":{"output":["text"]},"variants":{"high":{"reasoningEffort":"high"},"low":{"reasoningEffort":"low"}}}}}
		]}`))
	}))
	defer server.Close()

	ctx := context.Background()
	cfg := &internalconfig.Config{}
	cfg.Meta.LiveModels = true
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(runtimeexecutor.NewMetaExecutor(cfg))
	metaID := "live-sync-meta-reasoning"
	registerLiveTestAuths(t, ctx, manager,
		&coreauth.Auth{ID: metaID, Provider: "meta", Attributes: map[string]string{"base_url": server.URL + "/v1", "api_key": "meta-key", "source_backend": "file", "source": "/auths/meta-reasoning.json"}},
	)
	service := &Service{cfg: cfg, coreManager: manager}
	syncLiveModelsAndWait(ctx, service, newLiveModelSyncState(), time.Now())
	if registeredModel(t, metaID, "muse-plain-live-test") == nil || registeredModel(t, metaID, "muse-reasoning-live-test") == nil {
		t.Fatal("the discovered Meta models were not registered")
	}

	// The Codex client catalog lists no reasoning levels for the model Meta
	// reports as not reasoning, and the reported levels for the other one.
	response := codexmodels.BuildResponseForClient(internalregistry.GetGlobalRegistry().GetAvailableModels("openai"), nil, false, codexLiveClientVersion)
	catalog, errEncode := json.Marshal(response)
	if errEncode != nil {
		t.Fatalf("encode Codex client catalog: %v", errEncode)
	}
	plain := gjson.GetBytes(catalog, `models.#(slug=="muse-plain-live-test")`)
	if !plain.Exists() || plain.Get("supported_reasoning_levels").Raw != "[]" || plain.Get("default_reasoning_level").Exists() {
		t.Fatalf("Codex client entry of the model without reasoning = %s, want supported_reasoning_levels [] and no default", plain.Raw)
	}
	var efforts []string
	gjson.GetBytes(catalog, `models.#(slug=="muse-reasoning-live-test").supported_reasoning_levels.#.effort`).ForEach(func(_, effort gjson.Result) bool {
		efforts = append(efforts, effort.String())
		return true
	})
	if strings.Join(efforts, ",") != "low,high" {
		t.Fatalf("Codex client reasoning levels of the reasoning model = %v, want low,high", efforts)
	}

	// A request to it has its reasoning settings removed rather than sent to Meta.
	info := internalregistry.LookupModelInfo("muse-plain-live-test", "meta")
	request := []byte(`{"model":"muse-plain-live-test","input":"hi","reasoning":{"effort":"high"}}`)
	prepared, errThinking := thinking.ApplyThinkingWithModelInfo(request, request, "muse-plain-live-test", "codex", "codex", "meta", info)
	if errThinking != nil || gjson.GetBytes(prepared, "reasoning").Exists() {
		t.Fatalf("request to the model without reasoning = %s, %v; want the reasoning settings removed", prepared, errThinking)
	}
}

func TestSyncLiveModelsReregistersChangedModelMetadata(t *testing.T) {
	var mu sync.Mutex
	contextLength := 100000
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_, _ = fmt.Fprintf(w, `{"data":[{"id":"claude-metadata-live-test","display_name":"Metadata %d","max_input_tokens":%d}]}`, contextLength, contextLength)
	}))
	defer server.Close()
	originalClaudeURL := claudeModelsURL
	claudeModelsURL = server.URL
	t.Cleanup(func() { claudeModelsURL = originalClaudeURL })

	ctx := context.Background()
	manager := coreauth.NewManager(nil, nil, nil)
	authID := "live-sync-metadata"
	registerLiveTestAuths(t, ctx, manager,
		&coreauth.Auth{ID: authID, Provider: "claude", Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"access_token": "token"}},
	)
	cfg := &internalconfig.Config{}
	cfg.Claude.LiveModels = true
	service := &Service{cfg: cfg, coreManager: manager}
	state := newLiveModelSyncState()
	now := time.Now()

	syncLiveModelsAndWait(ctx, service, state, now)
	if model := registeredModel(t, authID, "claude-metadata-live-test"); model == nil || model.ContextLength != 100000 {
		t.Fatalf("discovered model = %+v, want a 100000 context window", model)
	}

	// The provider keeps the model but changes what it reports about it.
	mu.Lock()
	contextLength = 200000
	mu.Unlock()
	syncLiveModelsAndWait(ctx, service, state, now.Add(liveModelSyncInterval+time.Second))
	model := registeredModel(t, authID, "claude-metadata-live-test")
	if model == nil || model.ContextLength != 200000 || model.DisplayName != "Metadata 200000" {
		t.Fatalf("model after the provider changed it = %+v, want the new context window and name", model)
	}
}

func TestMetaModelsGETRefusesACredentialReplacedByAnotherProvider(t *testing.T) {
	var mu sync.Mutex
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.URL.Path+" "+r.Header.Get("Authorization"))
		mu.Unlock()
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	ctx := context.Background()
	cfg := &internalconfig.Config{}
	cfg.Meta.LiveModels = true
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(runtimeexecutor.NewMetaExecutor(cfg))
	manager.RegisterExecutor(runtimeexecutor.NewCodexExecutor(cfg))
	authID := "live-sync-meta-now-codex"
	// The credential file now holds a Codex login under the same ID.
	registerLiveTestAuths(t, ctx, manager, &coreauth.Auth{ID: authID, Provider: "codex",
		Attributes: map[string]string{"auth_kind": "oauth", "base_url": server.URL + "/v1"},
		Metadata:   map[string]any{"access_token": "codex-secret-token"},
	})
	// The read began while it was a Meta login that holds only a DCA token.
	snapshot := &coreauth.Auth{ID: authID, Provider: "meta",
		Attributes: map[string]string{"base_url": server.URL + "/v1", "auth_kind": "oauth", "source_backend": "file", "source": "/auths/meta.json"},
		Metadata:   map[string]any{"type": "meta", "access_token": "dca:replaced", "dca_token": "dca:replaced"},
	}
	service := &Service{cfg: cfg, coreManager: manager}

	if _, errGet := service.metaModelsGET(ctx, snapshot); errGet == nil {
		t.Fatal("the Muse Code catalog was read with a credential that is no longer a Meta login")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 0 {
		t.Fatalf("requests = %q, want none; the Codex token must not reach the Muse Code catalog", requests)
	}
}

func TestLiveModelsStayWithTheCredentialTheyWereReadFor(t *testing.T) {
	claudeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		account := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer token-")
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-account-` + account + `-live-test"}]}`))
	}))
	defer claudeServer.Close()
	codexServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-switched-live-test"}]}`))
	}))
	defer codexServer.Close()
	originalClaudeURL, originalCodexURL := claudeModelsURL, codexModelsURL
	claudeModelsURL, codexModelsURL = claudeServer.URL, codexServer.URL
	t.Cleanup(func() { claudeModelsURL, codexModelsURL = originalClaudeURL, originalCodexURL })

	ctx := context.Background()
	manager := coreauth.NewManager(nil, nil, nil)
	authID := "live-sync-replaced"
	registerLiveTestAuths(t, ctx, manager, &coreauth.Auth{ID: authID, Provider: "claude",
		Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"access_token": "token-a", "email": "a@example.com"}})
	cfg := &internalconfig.Config{}
	cfg.Claude.LiveModels = true
	cfg.Codex.LiveModels = true
	service := &Service{cfg: cfg, coreManager: manager}
	state := newLiveModelSyncState()
	now := time.Now()
	replace := func(auth *coreauth.Auth) {
		t.Helper()
		auth.Status = coreauth.StatusActive
		if _, errUpdate := manager.Update(coreauth.WithSkipPersist(ctx), auth); errUpdate != nil {
			t.Fatalf("Update: %v", errUpdate)
		}
		current, _ := manager.GetByID(auth.ID)
		service.refreshModelRegistrationForAuth(current)
	}

	syncLiveModelsAndWait(ctx, service, state, now)
	if registeredModel(t, authID, "claude-account-a-live-test") == nil {
		t.Fatal("the first account's model was not registered")
	}

	// Another account's login replaces the file under the same ID.
	replace(&coreauth.Auth{ID: authID, Provider: "claude",
		Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"access_token": "token-b", "email": "b@example.com"}})
	if registeredModel(t, authID, "claude-account-a-live-test") != nil {
		t.Fatal("the previous account's model was registered for the account that replaced it")
	}
	// It is read at the next check, although the previous read is not an hour old.
	syncLiveModelsAndWait(ctx, service, state, now.Add(liveModelSyncTick))
	if registeredModel(t, authID, "claude-account-b-live-test") == nil {
		t.Fatal("the new account was not read at the next check")
	}

	// A Codex login of the same person replaces it: no Claude model may be routed to it.
	replace(&coreauth.Auth{ID: authID, Provider: "codex",
		Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"access_token": "codex-token", "email": "b@example.com"}})
	if registeredModel(t, authID, "claude-account-b-live-test") != nil {
		t.Fatal("a Claude model was registered for the Codex login that replaced the Claude one")
	}
	syncLiveModelsAndWait(ctx, service, state, now.Add(2*liveModelSyncTick))
	if registeredModel(t, authID, "gpt-switched-live-test") == nil {
		t.Fatal("the Codex login was not read at the next check")
	}
}

func TestSyncLiveModelsDropsAReadWhoseCredentialWasReplaced(t *testing.T) {
	t.Cleanup(func() { internalregistry.RetainLiveCodexClientModels(nil) })
	base := internalregistry.GetCodexClientModelsJSON()
	answer := liveCodexClientCatalogAnswer(t, "gpt-replaced-live-test")
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseRead := func() { releaseOnce.Do(func() { close(release) }) }
	reading := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case reading <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte(answer))
	}))
	defer server.Close()
	defer releaseRead()
	originalCodexURL := codexModelsURL
	codexModelsURL = server.URL
	t.Cleanup(func() { codexModelsURL = originalCodexURL })

	ctx := context.Background()
	manager := coreauth.NewManager(nil, nil, nil)
	authID := "live-sync-replaced-mid-read"
	registerLiveTestAuths(t, ctx, manager, &coreauth.Auth{ID: authID, Provider: "codex",
		Attributes: map[string]string{"auth_kind": "oauth", "plan_type": "pro"},
		Metadata:   map[string]any{"access_token": "codex-token-a", "account_id": "acct-a", "email": "a@example.com"}})
	cfg := &internalconfig.Config{}
	cfg.Codex.LiveModels = true
	service := &Service{cfg: cfg, coreManager: manager}
	state := newLiveModelSyncState()

	service.syncLiveModels(ctx, state, time.Now())
	select {
	case <-reading:
	case <-time.After(10 * time.Second):
		t.Fatal("the read did not start")
	}
	// Another account's login replaces the file while the read is running.
	if _, errUpdate := manager.Update(coreauth.WithSkipPersist(ctx), &coreauth.Auth{ID: authID, Provider: "codex", Status: coreauth.StatusActive,
		Attributes: map[string]string{"auth_kind": "oauth", "plan_type": "pro"},
		Metadata:   map[string]any{"access_token": "codex-token-b", "account_id": "acct-b", "email": "b@example.com"}}); errUpdate != nil {
		t.Fatalf("Update: %v", errUpdate)
	}
	releaseRead()
	state.reads.Wait()

	if !bytes.Equal(internalregistry.GetCodexClientModelsJSON(), base) {
		t.Fatal("the Codex client catalog serves what was read for the login that was replaced")
	}
	if registeredModel(t, authID, "gpt-replaced-live-test") != nil {
		t.Fatal("a model read for the replaced login was registered for its replacement")
	}
}
