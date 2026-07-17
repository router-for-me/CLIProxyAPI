package cliproxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	codexauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestFetchCodexModelCatalogUsesAuthProxyAndHeaders(t *testing.T) {
	var gotURL string
	var gotAccountID string
	var gotUserAgent string
	var gotCustomHeader string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		gotAccountID = r.Header.Get("Chatgpt-Account-Id")
		gotUserAgent = r.Header.Get("User-Agent")
		gotCustomHeader = r.Header.Get("X-Test")
		_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-5.6-sol"}]}`))
	}))
	defer proxy.Close()

	service := &Service{cfg: &config.Config{}}
	service.cfg.CodexHeaderDefaults.UserAgent = "configured-agent"
	auth := testCodexDiscoveryAuth("codex-proxy")
	auth.ProxyURL = proxy.URL
	auth.Attributes["base_url"] = "http://upstream.invalid/backend-api/codex"
	auth.Attributes["header:X-Test"] = "custom-value"

	models, errFetch := service.fetchCodexModelCatalog(context.Background(), auth)
	if errFetch != nil {
		t.Fatalf("fetchCodexModelCatalog() error = %v", errFetch)
	}
	if len(models) != 1 || models[0].Slug != "gpt-5.6-sol" {
		t.Fatalf("models = %#v", models)
	}
	if gotURL != "http://upstream.invalid/backend-api/codex/models?client_version=0.144.1" {
		t.Fatalf("proxy request URL = %q", gotURL)
	}
	if gotAccountID != "test-account" {
		t.Fatalf("Chatgpt-Account-Id = %q", gotAccountID)
	}
	if gotUserAgent != "configured-agent" {
		t.Fatalf("User-Agent = %q", gotUserAgent)
	}
	if gotCustomHeader != "custom-value" {
		t.Fatalf("X-Test = %q", gotCustomHeader)
	}
}

func TestCodexModelDiscoveryReplacesStaleFreeCatalog(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	service := &Service{cfg: &config.Config{}, coreManager: manager}
	auth := testCodexDiscoveryAuth("codex-stale-free")
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	service.codexModelsFetch = func(ctx context.Context, _ *coreauth.Auth) ([]codexauth.ModelCatalogEntry, error) {
		close(started)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
		}
		return []codexauth.ModelCatalogEntry{{Slug: "gpt-5.6-sol"}}, nil
	}

	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.UnregisterClient(auth.ID)
	t.Cleanup(func() { modelRegistry.UnregisterClient(auth.ID) })
	service.registerModelsForAuth(context.Background(), auth)
	waitSignal(t, started, "model discovery start")

	if clientHasModel(modelRegistry, auth.ID, "gpt-5.6-sol") {
		t.Fatal("free fallback unexpectedly registered gpt-5.6-sol")
	}
	if !clientHasModel(modelRegistry, auth.ID, "gpt-5.6-luna") {
		t.Fatal("free fallback did not register gpt-5.6-luna")
	}

	close(release)
	waitFor(t, "dynamic model registration", func() bool {
		return clientHasModel(modelRegistry, auth.ID, "gpt-5.6-sol")
	})
	if clientHasModel(modelRegistry, auth.ID, "gpt-5.6-luna") {
		t.Fatal("authoritative catalog retained model absent upstream")
	}
	if !clientHasModel(modelRegistry, auth.ID, "gpt-image-2") {
		t.Fatal("authoritative catalog omitted local Codex image built-in")
	}
}

func TestCodexModelDiscoveryFailureKeepsLastGoodCatalog(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	auth := testCodexDiscoveryAuth("codex-last-good")
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	service := &Service{
		cfg:                   &config.Config{},
		coreManager:           manager,
		codexModelsGeneration: 1,
		codexModels: map[string]*codexModelDiscoveryEntry{
			auth.ID: {
				generation: 1,
				revision:   1,
				identity:   codexModelDiscoveryIdentity(auth),
				models:     []codexauth.ModelCatalogEntry{{Slug: "gpt-5.6-sol"}},
				ready:      true,
				attempted:  true,
			},
		},
	}
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.UnregisterClient(auth.ID)
	t.Cleanup(func() { modelRegistry.UnregisterClient(auth.ID) })
	service.registerModelsForAuth(context.Background(), auth)

	failedDone := make(chan struct{})
	retryDone := make(chan struct{})
	var calls atomic.Int32
	service.codexModelsFetch = func(context.Context, *coreauth.Auth) ([]codexauth.ModelCatalogEntry, error) {
		switch calls.Add(1) {
		case 1:
			close(failedDone)
			return nil, errors.New("upstream unavailable")
		case 2:
			close(retryDone)
			return []codexauth.ModelCatalogEntry{{Slug: "gpt-5.6-luna"}}, nil
		default:
			return nil, errors.New("unexpected model discovery fetch")
		}
	}
	service.invalidateCodexModelDiscovery(auth)
	service.registerModelsForAuth(context.Background(), auth)
	waitSignal(t, failedDone, "failed model discovery")
	waitFor(t, "failed discovery to become retryable", func() bool {
		return codexModelDiscoveryRetryable(service, auth.ID)
	})
	if !clientHasModel(modelRegistry, auth.ID, "gpt-5.6-sol") {
		t.Fatal("failed refresh removed last-good model catalog")
	}

	service.registerModelsForAuth(context.Background(), auth)
	waitSignal(t, retryDone, "model discovery retry")
	waitFor(t, "retried model registration", func() bool {
		return clientHasModel(modelRegistry, auth.ID, "gpt-5.6-luna")
	})
	if clientHasModel(modelRegistry, auth.ID, "gpt-5.6-sol") {
		t.Fatal("successful retry retained the stale last-good model catalog")
	}
}

func TestCodexModelDiscoveryFirstFailureKeepsPlanFallbackAndRetries(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	service := &Service{cfg: &config.Config{}, coreManager: manager}
	auth := testCodexDiscoveryAuth("codex-first-failure")
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	done := make(chan struct{})
	retryDone := make(chan struct{})
	var calls atomic.Int32
	service.codexModelsFetch = func(context.Context, *coreauth.Auth) ([]codexauth.ModelCatalogEntry, error) {
		switch calls.Add(1) {
		case 1:
			close(done)
			return nil, errors.New("upstream unavailable")
		case 2:
			close(retryDone)
			return []codexauth.ModelCatalogEntry{{Slug: "gpt-5.6-sol"}}, nil
		default:
			return nil, errors.New("unexpected model discovery fetch")
		}
	}
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.UnregisterClient(auth.ID)
	t.Cleanup(func() { modelRegistry.UnregisterClient(auth.ID) })
	service.registerModelsForAuth(context.Background(), auth)
	waitSignal(t, done, "failed first model discovery")
	waitFor(t, "failed discovery to become retryable", func() bool {
		return codexModelDiscoveryRetryable(service, auth.ID)
	})

	if clientHasModel(modelRegistry, auth.ID, "gpt-5.6-sol") {
		t.Fatal("failed first discovery registered paid-tier model for free fallback")
	}
	if !clientHasModel(modelRegistry, auth.ID, "gpt-5.6-luna") {
		t.Fatal("failed first discovery removed free plan fallback")
	}

	service.registerModelsForAuth(context.Background(), auth)
	waitSignal(t, retryDone, "model discovery retry")
	waitFor(t, "retried model registration", func() bool {
		return clientHasModel(modelRegistry, auth.ID, "gpt-5.6-sol")
	})
	if clientHasModel(modelRegistry, auth.ID, "gpt-5.6-luna") {
		t.Fatal("successful retry retained the plan fallback catalog")
	}
}

func TestCodexModelDiscoveryDeduplicatesAndRejectsRemovedAuth(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	service := &Service{cfg: &config.Config{}, coreManager: manager}
	auth := testCodexDiscoveryAuth("codex-removed")
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	service.codexModelsFetch = func(ctx context.Context, _ *coreauth.Auth) ([]codexauth.ModelCatalogEntry, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return []codexauth.ModelCatalogEntry{{Slug: "gpt-5.6-sol"}}, nil
		}
	}

	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.UnregisterClient(auth.ID)
	t.Cleanup(func() { modelRegistry.UnregisterClient(auth.ID) })
	for range 5 {
		service.registerModelsForAuth(context.Background(), auth)
	}
	waitSignal(t, started, "deduplicated model discovery")
	if got := calls.Load(); got != 1 {
		t.Fatalf("fetch calls = %d, want 1", got)
	}

	service.removeCodexModelDiscovery(auth.ID)
	manager.Remove(context.Background(), auth.ID)
	modelRegistry.UnregisterClient(auth.ID)
	close(release)
	time.Sleep(20 * time.Millisecond)
	if clientHasModel(modelRegistry, auth.ID, "gpt-5.6-sol") {
		t.Fatal("late discovery result registered a removed auth")
	}
}

func TestCodexModelDiscoveryDisabledForLocalModeAndAPIKeys(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config.Config
		auth *coreauth.Auth
	}{
		{
			name: "local mode",
			cfg:  &config.Config{DisableCodexModelDiscovery: true},
			auth: testCodexDiscoveryAuth("codex-local"),
		},
		{
			name: "api key",
			cfg:  &config.Config{},
			auth: &coreauth.Auth{
				ID:       "codex-api-key",
				Provider: "codex",
				Attributes: map[string]string{
					coreauth.AttributeAuthKind: coreauth.AuthKindAPIKey,
					coreauth.AttributeAPIKey:   "key",
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &Service{cfg: tt.cfg}
			var calls atomic.Int32
			service.codexModelsFetch = func(context.Context, *coreauth.Auth) ([]codexauth.ModelCatalogEntry, error) {
				calls.Add(1)
				return []codexauth.ModelCatalogEntry{{Slug: "gpt-5.6-sol"}}, nil
			}
			service.registerModelsForAuth(context.Background(), tt.auth)
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(tt.auth.ID) })
			time.Sleep(10 * time.Millisecond)
			if got := calls.Load(); got != 0 {
				t.Fatalf("fetch calls = %d, want 0", got)
			}
		})
	}
}

func TestCodexModelDiscoveryStopsWithServiceContext(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	service := &Service{cfg: &config.Config{}, coreManager: manager}
	auth := testCodexDiscoveryAuth("codex-context-cancel")
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	started := make(chan struct{})
	stopped := make(chan struct{})
	service.codexModelsFetch = func(ctx context.Context, _ *coreauth.Auth) ([]codexauth.ModelCatalogEntry, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return nil, ctx.Err()
	}
	serviceCtx, cancel := context.WithCancel(context.Background())
	service.startCodexModelDiscovery(serviceCtx)
	t.Cleanup(service.stopCodexModelDiscovery)
	service.registerModelsForAuth(context.Background(), auth)
	waitSignal(t, started, "model discovery start")
	cancel()
	waitSignal(t, stopped, "model discovery cancellation")
}

func TestCodexCatalogModelInfosSynthesizesUnknownModels(t *testing.T) {
	models := codexCatalogModelInfos([]codexauth.ModelCatalogEntry{{
		Slug:             "gpt-future",
		DisplayName:      "GPT Future",
		Description:      "Future model",
		ContextWindow:    123456,
		MaxContextWindow: 234567,
		SupportedReasoningLevels: []codexauth.ModelCatalogReasoningLevel{
			{Effort: "low"},
			{Effort: " HIGH "},
			{Effort: "high"},
		},
	}})
	var future *ModelInfo
	for _, model := range models {
		if model != nil && model.ID == "gpt-future" {
			future = model
			break
		}
	}
	if future == nil {
		t.Fatal("unknown upstream model was not synthesized")
	}
	if future.DisplayName != "GPT Future" || future.ContextLength != 123456 {
		t.Fatalf("future model metadata = %+v", future)
	}
	if future.Thinking == nil || len(future.Thinking.Levels) != 2 || future.Thinking.Levels[1] != "high" {
		t.Fatalf("future model thinking = %+v", future.Thinking)
	}
}

func TestCodexDiscoveredModelsApplyExclusionsAliasesAndPrefixes(t *testing.T) {
	auth := testCodexDiscoveryAuth("codex-dynamic-transforms")
	auth.Prefix = "account"
	auth.Attributes["excluded_models"] = "gpt-5.6-luna"
	service := &Service{
		cfg: &config.Config{
			SDKConfig: config.SDKConfig{ForceModelPrefix: true},
			OAuthModelAlias: map[string][]config.OAuthModelAlias{
				"codex": {{Name: "gpt-5.6-sol", Alias: "sol-live"}},
			},
		},
		codexModels: map[string]*codexModelDiscoveryEntry{
			auth.ID: {
				identity:  codexModelDiscoveryIdentity(auth),
				models:    []codexauth.ModelCatalogEntry{{Slug: "gpt-5.6-sol"}, {Slug: "gpt-5.6-luna"}},
				ready:     true,
				attempted: true,
			},
		},
	}
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.UnregisterClient(auth.ID)
	t.Cleanup(func() { modelRegistry.UnregisterClient(auth.ID) })
	service.registerModelsForAuth(context.Background(), auth)

	if !clientHasModel(modelRegistry, auth.ID, "account/sol-live") {
		t.Fatal("dynamic model alias or prefix was not applied")
	}
	for _, forbidden := range []string{"gpt-5.6-sol", "sol-live", "gpt-5.6-luna", "account/gpt-5.6-luna"} {
		if clientHasModel(modelRegistry, auth.ID, forbidden) {
			t.Fatalf("unexpected registered model %q", forbidden)
		}
	}
}

func TestCodexModelDiscoveryIdentityFallsBackToTokenFingerprint(t *testing.T) {
	first := &coreauth.Auth{
		ID:       "imported-codex",
		Provider: "codex",
		Metadata: map[string]any{"access_token": "token-one"},
	}
	second := first.Clone()
	second.Metadata["access_token"] = "token-two"

	firstIdentity := codexModelDiscoveryIdentity(first)
	secondIdentity := codexModelDiscoveryIdentity(second)
	if firstIdentity == secondIdentity {
		t.Fatalf("token identities matched: %q", firstIdentity)
	}
	if strings.Contains(firstIdentity, "token-one") || strings.Contains(secondIdentity, "token-two") {
		t.Fatal("model discovery identity leaked an access token")
	}
}

func testCodexDiscoveryAuth(id string) *coreauth.Auth {
	return &coreauth.Auth{
		ID:       id,
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			coreauth.AttributeAuthKind: coreauth.AuthKindOAuth,
			"plan_type":                "free",
		},
		Metadata: map[string]any{
			"access_token": "test-token",
			"account_id":   "test-account",
		},
	}
}

func clientHasModel(modelRegistry *registry.ModelRegistry, authID, modelID string) bool {
	for _, model := range modelRegistry.GetModelsForClient(authID) {
		if model != nil && model.ID == modelID {
			return true
		}
	}
	return false
}

func codexModelDiscoveryRetryable(service *Service, authID string) bool {
	service.codexModelsMu.Lock()
	defer service.codexModelsMu.Unlock()
	entry := service.codexModels[authID]
	return entry != nil && !entry.fetching && !entry.attempted
}

func waitSignal(t *testing.T, signal <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func waitFor(t *testing.T, label string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", label)
}
