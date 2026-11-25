package cliproxy

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func antigravityBaseNameTestModels() []*ModelInfo {
	levels := func(values ...string) *registry.ThinkingSupport {
		return &registry.ThinkingSupport{Levels: values}
	}
	return []*ModelInfo{
		{ID: "gemini-4-neon-xhigh", DisplayName: "Gemini 4 Neon", Thinking: levels("low", "medium", "high", "xhigh")},
		{ID: "gemini-4-argon-high", DisplayName: "Gemini 4 Argon", Thinking: levels("low", "medium", "high")},
		{ID: "gemini-3.8-flash-high", DisplayName: "Gemini 3.8 Flash", Thinking: levels("low", "medium", "high")},
		{ID: "gemini-3.8-flash-tiered", Thinking: levels("low", "medium", "high")},
		{ID: "gemini-3-flash", Thinking: levels("minimal", "low", "medium", "high")},
		{ID: "gemini-3.1-flash-image", Thinking: levels("minimal", "high")},
		{ID: "claude-opus-5-5-high", Thinking: levels("low", "medium", "high")},
		{ID: "gpt-oss-120b-medium"},
	}
}

func TestAntigravityBaseModelAliases_DerivesGeminiBaseNames(t *testing.T) {
	got := antigravityBaseModelAliases(antigravityBaseNameTestModels())

	want := map[string]string{
		"gemini-4-neon-xhigh":   "gemini-4-neon",
		"gemini-4-argon-high":   "gemini-4-argon",
		"gemini-3.8-flash-high": "gemini-3.8-flash",
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d aliases, got %d: %+v", len(want), len(got), got)
	}
	for _, entry := range got {
		if want[entry.Name] != entry.Alias {
			t.Fatalf("unexpected alias %q -> %q", entry.Name, entry.Alias)
		}
		if entry.Fork {
			t.Fatalf("alias %q must replace the catalog ID, not fork it", entry.Alias)
		}
		if !entry.ForceMapping {
			t.Fatalf("alias %q must rewrite the response model back to the base name", entry.Alias)
		}
	}
}

func TestAntigravityBaseModelAliases_SkipsBaseNameAlreadyInCatalog(t *testing.T) {
	models := []*ModelInfo{
		{ID: "gemini-4-neon", Thinking: &registry.ThinkingSupport{Levels: []string{"low", "high"}}},
		{ID: "gemini-4-neon-high", Thinking: &registry.ThinkingSupport{Levels: []string{"low", "high"}}},
		{ID: "gemini-4-argon-low", Thinking: &registry.ThinkingSupport{Levels: []string{"low", "high"}}},
		{ID: "gemini-4-argon-high", Thinking: &registry.ThinkingSupport{Levels: []string{"low", "high"}}},
	}

	got := antigravityBaseModelAliases(models)
	if len(got) != 1 {
		t.Fatalf("expected 1 alias, got %d: %+v", len(got), got)
	}
	if got[0].Name != "gemini-4-argon-low" || got[0].Alias != "gemini-4-argon" {
		t.Fatalf("expected first argon variant to own the base name, got %+v", got[0])
	}
}

func TestApplyOAuthModelAliasEntries_AntigravityBaseNamesReplaceCatalogIDs(t *testing.T) {
	models := antigravityBaseNameTestModels()
	out := applyOAuthModelAliasEntries(antigravityBaseModelAliases(models), models)

	ids := make(map[string]*ModelInfo, len(out))
	for _, model := range out {
		ids[model.ID] = model
	}
	if len(out) != len(models) {
		t.Fatalf("expected %d models (no duplicates added), got %d", len(models), len(out))
	}
	for _, removed := range []string{"gemini-4-neon-xhigh", "gemini-4-argon-high", "gemini-3.8-flash-high"} {
		if _, exists := ids[removed]; exists {
			t.Fatalf("catalog ID %q should be replaced by its base name", removed)
		}
	}
	neon := ids["gemini-4-neon"]
	if neon == nil {
		t.Fatalf("expected gemini-4-neon in %v", ids)
	}
	if neon.OAuthAliasSource != "gemini-4-neon-xhigh" {
		t.Fatalf("expected alias source gemini-4-neon-xhigh, got %q", neon.OAuthAliasSource)
	}
	if neon.Thinking == nil || strings.Join(neon.Thinking.Levels, ",") != "low,medium,high,xhigh" {
		t.Fatalf("base name must keep the catalog thinking levels, got %+v", neon.Thinking)
	}
	for _, untouched := range []string{"gemini-3.8-flash-tiered", "gemini-3-flash", "gemini-3.1-flash-image", "claude-opus-5-5-high", "gpt-oss-120b-medium"} {
		if _, exists := ids[untouched]; !exists {
			t.Fatalf("expected %q to stay listed", untouched)
		}
	}
}

func TestEffectiveOAuthModelAlias_DisabledReturnsConfiguredMap(t *testing.T) {
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"antigravity": {{Name: "gemini-pro-agent", Alias: "gemini-3.1-pro-preview"}},
		},
	}
	disabled := false
	cfg.Antigravity.BaseModelNames = &disabled

	got := effectiveOAuthModelAlias(cfg)
	if len(got) != 1 || len(got["antigravity"]) != 1 {
		t.Fatalf("expected configured aliases unchanged, got %+v", got)
	}
	if aliases := oauthModelAliasesForAuth(cfg, "antigravity", nil); len(aliases) != 1 {
		t.Fatalf("expected 1 listing alias, got %+v", aliases)
	}
}

func TestEffectiveOAuthModelAlias_DefaultAddsCatalogBaseNames(t *testing.T) {
	derived := antigravityBaseModelAliases(registry.GetAntigravityModels())
	if len(derived) < 2 {
		t.Skip("embedded Antigravity catalog has fewer than two level-suffixed Gemini models")
	}
	overridden, kept := derived[0], derived[1]

	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"antigravity": {{Name: overridden.Name, Alias: "my-custom-name"}},
			"codex":       {{Name: "gpt-5", Alias: "g5"}},
		},
	}

	got := effectiveOAuthModelAlias(cfg)
	if len(cfg.OAuthModelAlias["antigravity"]) != 1 {
		t.Fatalf("configured aliases must not be mutated, got %+v", cfg.OAuthModelAlias["antigravity"])
	}
	if len(got["codex"]) != 1 {
		t.Fatalf("other channels must be unchanged, got %+v", got["codex"])
	}
	byAlias := make(map[string]config.OAuthModelAlias)
	for _, entry := range got["antigravity"] {
		byAlias[entry.Alias] = entry
	}
	if entry, exists := byAlias["my-custom-name"]; !exists || entry.Name != overridden.Name {
		t.Fatalf("explicit alias must be kept, got %+v", got["antigravity"])
	}
	if _, exists := byAlias[overridden.Alias]; exists {
		t.Fatalf("derived alias %q must yield to the explicit alias for %q", overridden.Alias, overridden.Name)
	}
	if entry, exists := byAlias[kept.Alias]; !exists || entry.Name != kept.Name {
		t.Fatalf("expected derived alias %q -> %q, got %+v", kept.Name, kept.Alias, got["antigravity"])
	}

	listing := oauthModelAliasesForAuth(cfg, "antigravity", nil)
	if len(listing) != len(got["antigravity"]) {
		t.Fatalf("listing and routing aliases must match: %d vs %d", len(listing), len(got["antigravity"]))
	}
	if other := oauthModelAliasesForAuth(cfg, "codex", nil); len(other) != 1 {
		t.Fatalf("derived aliases must stay on the antigravity channel, got %+v", other)
	}
}

type antigravityBaseNameRecordingExecutor struct {
	models *[]string
}

func (e antigravityBaseNameRecordingExecutor) Identifier() string { return "antigravity" }

func (e antigravityBaseNameRecordingExecutor) Execute(_ context.Context, _ *coreauth.Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	*e.models = append(*e.models, req.Model)
	return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (e antigravityBaseNameRecordingExecutor) ExecuteStream(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e antigravityBaseNameRecordingExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e antigravityBaseNameRecordingExecutor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e antigravityBaseNameRecordingExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestAntigravityBaseModelNames_ListsAndRoutesBaseName(t *testing.T) {
	derived := antigravityBaseModelAliases(registry.GetAntigravityModels())
	if len(derived) == 0 {
		t.Skip("embedded Antigravity catalog has no level-suffixed Gemini model")
	}
	target := derived[0]

	cfg := &config.Config{}

	ctx := coreauth.WithSkipPersist(t.Context())
	manager := coreauth.NewManager(nil, nil, nil)
	var upstreamModels []string
	manager.RegisterExecutor(antigravityBaseNameRecordingExecutor{models: &upstreamModels})
	service := &Service{cfg: cfg, coreManager: manager}
	if !service.applyManagerConfig(ctx, configCommit{cfg: cfg, sequence: 1}) {
		t.Fatal("applyManagerConfig failed")
	}

	auth := &coreauth.Auth{
		ID:         "antigravity-base-model-names-auth",
		Provider:   "antigravity",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"auth_kind": "oauth"},
	}
	models := service.antigravityModelsForHintsWithConfig(auth, antigravityModelCapabilityHints{}, cfg)
	listed := make(map[string]struct{}, len(models))
	for _, model := range models {
		listed[model.ID] = struct{}{}
	}
	if _, exists := listed[target.Alias]; !exists {
		t.Fatalf("expected base name %q to be listed", target.Alias)
	}
	if _, exists := listed[target.Name]; exists {
		t.Fatalf("catalog ID %q must not be listed next to its base name", target.Name)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "antigravity", models)
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	manager.RefreshSchedulerEntry(auth.ID)

	for _, tc := range []struct {
		requested string
		upstream  string
	}{
		{requested: target.Alias, upstream: target.Name},
		{requested: target.Alias + "(low)", upstream: target.Name + "(low)"},
	} {
		upstreamModels = nil
		if _, errExecute := manager.Execute(ctx, []string{"antigravity"}, cliproxyexecutor.Request{Model: tc.requested}, cliproxyexecutor.Options{}); errExecute != nil {
			t.Fatalf("execute %q: %v", tc.requested, errExecute)
		}
		if len(upstreamModels) != 1 || upstreamModels[0] != tc.upstream {
			t.Fatalf("request %q reached upstream as %v, want %q", tc.requested, upstreamModels, tc.upstream)
		}
	}
}
