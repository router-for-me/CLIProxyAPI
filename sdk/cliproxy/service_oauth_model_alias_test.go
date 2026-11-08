package cliproxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/gemini"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestApplyOAuthModelAlias_Rename(t *testing.T) {
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"codex": {
				{Name: "gpt-5", Alias: "g5", DisplayName: "Configured GPT Five"},
			},
		},
	}
	models := []*ModelInfo{
		{ID: "gpt-5", Name: "models/gpt-5", DisplayName: "Upstream GPT Five"},
	}

	out := applyOAuthModelAlias(cfg, "codex", "oauth", models)
	if len(out) != 1 {
		t.Fatalf("expected 1 model, got %d", len(out))
	}
	if out[0].ID != "g5" {
		t.Fatalf("expected model id %q, got %q", "g5", out[0].ID)
	}
	if out[0].Name != "models/g5" {
		t.Fatalf("expected model name %q, got %q", "models/g5", out[0].Name)
	}
	if out[0].DisplayName != "Configured GPT Five" {
		t.Fatalf("expected display name %q, got %q", "Configured GPT Five", out[0].DisplayName)
	}
}

func TestApplyOAuthModelAlias_ForkAddsAlias(t *testing.T) {
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"codex": {
				{Name: "gpt-5", Alias: "g5", Fork: true, DisplayName: "Configured GPT Five"},
			},
		},
	}
	models := []*ModelInfo{
		{ID: "gpt-5", Name: "models/gpt-5", DisplayName: "Upstream GPT Five"},
	}

	out := applyOAuthModelAlias(cfg, "codex", "oauth", models)
	if len(out) != 2 {
		t.Fatalf("expected 2 models, got %d", len(out))
	}
	if out[0].ID != "gpt-5" {
		t.Fatalf("expected first model id %q, got %q", "gpt-5", out[0].ID)
	}
	if out[1].ID != "g5" {
		t.Fatalf("expected second model id %q, got %q", "g5", out[1].ID)
	}
	if out[1].Name != "models/g5" {
		t.Fatalf("expected forked model name %q, got %q", "models/g5", out[1].Name)
	}
	if out[0].DisplayName != "Upstream GPT Five" {
		t.Fatalf("expected original display name %q, got %q", "Upstream GPT Five", out[0].DisplayName)
	}
	if out[1].DisplayName != "Configured GPT Five" {
		t.Fatalf("expected alias display name %q, got %q", "Configured GPT Five", out[1].DisplayName)
	}
}

func TestApplyOAuthModelAlias_PreservesUpstreamDisplayNameByDefault(t *testing.T) {
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"codex": {
				{Name: "gpt-5", Alias: "g5"},
			},
		},
	}
	models := []*ModelInfo{
		{ID: "gpt-5", DisplayName: "Upstream GPT Five"},
	}

	out := applyOAuthModelAlias(cfg, "codex", "oauth", models)
	if len(out) != 1 {
		t.Fatalf("expected 1 model, got %d", len(out))
	}
	if out[0].DisplayName != "Upstream GPT Five" {
		t.Fatalf("expected upstream display name %q, got %q", "Upstream GPT Five", out[0].DisplayName)
	}
}

func TestApplyOAuthModelAlias_ForkAddsMultipleAliases(t *testing.T) {
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"codex": {
				{Name: "gpt-5", Alias: "g5", Fork: true},
				{Name: "gpt-5", Alias: "g5-2", Fork: true},
			},
		},
	}
	models := []*ModelInfo{
		{ID: "gpt-5", Name: "models/gpt-5"},
	}

	out := applyOAuthModelAlias(cfg, "codex", "oauth", models)
	if len(out) != 3 {
		t.Fatalf("expected 3 models, got %d", len(out))
	}
	if out[0].ID != "gpt-5" {
		t.Fatalf("expected first model id %q, got %q", "gpt-5", out[0].ID)
	}
	if out[1].ID != "g5" {
		t.Fatalf("expected second model id %q, got %q", "g5", out[1].ID)
	}
	if out[1].Name != "models/g5" {
		t.Fatalf("expected forked model name %q, got %q", "models/g5", out[1].Name)
	}
	if out[2].ID != "g5-2" {
		t.Fatalf("expected third model id %q, got %q", "g5-2", out[2].ID)
	}
	if out[2].Name != "models/g5-2" {
		t.Fatalf("expected forked model name %q, got %q", "models/g5-2", out[2].Name)
	}
}

func TestApplyOAuthModelAlias_Meta(t *testing.T) {
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"meta": {
				{Name: "muse-spark-1.3", Alias: "muse-latest", DisplayName: "Muse Latest"},
			},
		},
	}
	models := []*ModelInfo{
		{ID: "muse-spark-1.3", Name: "models/muse-spark-1.3", DisplayName: "Muse Spark 1.3"},
	}

	out := applyOAuthModelAlias(cfg, "meta", "oauth", models)
	if len(out) != 1 {
		t.Fatalf("expected 1 model, got %d", len(out))
	}
	if out[0].ID != "muse-latest" {
		t.Fatalf("expected model id %q, got %q", "muse-latest", out[0].ID)
	}
	if out[0].Name != "models/muse-latest" {
		t.Fatalf("expected model name %q, got %q", "models/muse-latest", out[0].Name)
	}
	if out[0].DisplayName != "Muse Latest" {
		t.Fatalf("expected display name %q, got %q", "Muse Latest", out[0].DisplayName)
	}

	apiKeyOut := applyOAuthModelAlias(cfg, "meta", "apikey", models)
	if len(apiKeyOut) != 1 || apiKeyOut[0].ID != "muse-spark-1.3" {
		t.Fatalf("expected meta-api-key models to remain unchanged, got %#v", apiKeyOut)
	}
}

func TestApplyOAuthModelAlias_PluginProvider(t *testing.T) {
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"sample-provider": {
				{Name: "sample-model-latest", Alias: "sample-latest"},
			},
		},
	}
	models := []*ModelInfo{
		{ID: "sample-model-latest", Name: "models/sample-model-latest"},
	}

	out := applyOAuthModelAlias(cfg, "sample-provider", "oauth", models)
	if len(out) != 1 {
		t.Fatalf("expected 1 model, got %d", len(out))
	}
	if out[0].ID != "sample-latest" {
		t.Fatalf("expected plugin alias id %q, got %q", "sample-latest", out[0].ID)
	}
	if out[0].Name != "models/sample-latest" {
		t.Fatalf("expected plugin alias name %q, got %q", "models/sample-latest", out[0].Name)
	}
}

func TestApplyOAuthModelAlias_PluginProviderSkipsAPIKey(t *testing.T) {
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"sample-provider": {
				{Name: "sample-model-latest", Alias: "sample-latest"},
			},
		},
	}
	models := []*ModelInfo{
		{ID: "sample-model-latest", Name: "models/sample-model-latest"},
	}

	out := applyOAuthModelAlias(cfg, "sample-provider", "api_key", models)
	if len(out) != 1 || out[0].ID != "sample-model-latest" {
		t.Fatalf("expected API key plugin model to remain unchanged, got %#v", out)
	}
}

func TestApplyOAuthModelAlias_PerAuthAlias(t *testing.T) {
	models := []*ModelInfo{
		{ID: "gpt-5.3-codex-spark", Name: "models/gpt-5.3-codex-spark"},
	}
	attributes := map[string]string{
		"model_aliases": `[{"name":"gpt-5.3-codex-spark","alias":"gpt-5.5","display-name":"Configured GPT Five"}]`,
	}

	out := applyOAuthModelAliasForAuth(nil, "codex", "oauth", attributes, models)
	if len(out) != 1 {
		t.Fatalf("expected 1 model, got %d", len(out))
	}
	if out[0].ID != "gpt-5.5" {
		t.Fatalf("expected per-auth alias id %q, got %q", "gpt-5.5", out[0].ID)
	}
	if out[0].Name != "models/gpt-5.5" {
		t.Fatalf("expected per-auth alias name %q, got %q", "models/gpt-5.5", out[0].Name)
	}
	if out[0].DisplayName != "Configured GPT Five" {
		t.Fatalf("expected per-auth display name %q, got %q", "Configured GPT Five", out[0].DisplayName)
	}
	if out[0].MetadataModelID != "gpt-5.3-codex-spark" {
		t.Fatalf("expected per-auth MetadataModelID %q, got %q", "gpt-5.3-codex-spark", out[0].MetadataModelID)
	}
}

func TestApplyOAuthModelAlias_PreservesMetadataModelID(t *testing.T) {
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"codex": {
				{Name: "gpt-6-astra", Alias: "codex-main", Fork: true},
				{Name: "gpt-5.6-luna", Alias: "codex-luna", Fork: false},
			},
		},
	}
	models := []*ModelInfo{
		{ID: "gpt-6-astra", Name: "models/gpt-6-astra"},
		{ID: "gpt-5.6-luna", Name: "models/gpt-5.6-luna"},
	}

	out := applyOAuthModelAlias(cfg, "codex", "oauth", models)
	if len(out) != 3 {
		t.Fatalf("expected 3 models (original astra + forked astra alias + renamed luna alias), got %d", len(out))
	}

	entryMap := make(map[string]*ModelInfo, len(out))
	for _, m := range out {
		entryMap[m.ID] = m
	}

	if astra := entryMap["gpt-6-astra"]; astra == nil {
		t.Fatal("missing original gpt-6-astra")
	}
	if codexMain := entryMap["codex-main"]; codexMain == nil {
		t.Fatal("missing alias codex-main")
	} else if codexMain.MetadataModelID != "gpt-6-astra" {
		t.Fatalf("codex-main MetadataModelID = %q, want gpt-6-astra", codexMain.MetadataModelID)
	}

	if codexLuna := entryMap["codex-luna"]; codexLuna == nil {
		t.Fatal("missing alias codex-luna")
	} else if codexLuna.MetadataModelID != "gpt-5.6-luna" {
		t.Fatalf("codex-luna MetadataModelID = %q, want gpt-5.6-luna", codexLuna.MetadataModelID)
	}
}

func TestApplyModelPrefixes_PreservesMetadataModelID(t *testing.T) {
	webSearch := true
	models := []*ModelInfo{
		{ID: "gpt-6-astra"},
		{ID: "codex-main", MetadataModelID: "gpt-6-astra", NativeCapabilities: &registry.NativeCapabilities{WebSearch: &webSearch}},
	}

	out := applyModelPrefixes(models, "1", false)
	if len(out) != 4 {
		t.Fatalf("expected 4 models (2 unprefixed + 2 prefixed), got %d", len(out))
	}

	entryMap := make(map[string]*ModelInfo, len(out))
	for _, m := range out {
		entryMap[m.ID] = m
	}

	if m := entryMap["1/gpt-6-astra"]; m == nil {
		t.Fatal("missing 1/gpt-6-astra")
	} else if m.MetadataModelID != "gpt-6-astra" {
		t.Fatalf("1/gpt-6-astra MetadataModelID = %q, want gpt-6-astra", m.MetadataModelID)
	}

	if m := entryMap["1/codex-main"]; m == nil {
		t.Fatal("missing 1/codex-main")
	} else if m.MetadataModelID != "gpt-6-astra" {
		t.Fatalf("1/codex-main MetadataModelID = %q, want gpt-6-astra", m.MetadataModelID)
	} else if m.NativeCapabilities == nil || m.NativeCapabilities.WebSearch == nil || !*m.NativeCapabilities.WebSearch {
		t.Fatalf("1/codex-main did not inherit native capabilities: %+v", m)
	} else {
		*m.NativeCapabilities.WebSearch = false
		if !*entryMap["codex-main"].NativeCapabilities.WebSearch {
			t.Fatal("prefixed capability metadata aliases the source model")
		}
	}
}

func TestApplyModelPrefixes_RewritesModelInfoNameForGeminiEndpoints(t *testing.T) {
	models := []*ModelInfo{
		{ID: "gemini-4-argon", Name: "models/gemini-4-argon", DisplayName: "Gemini 4 Argon"},
		{ID: "gemini-3.8-flash", Name: "gemini-3.8-flash", DisplayName: "Gemini 3.8 Flash"},
	}

	out := applyModelPrefixes(models, "gcp", false)
	if len(out) != 4 {
		t.Fatalf("expected 4 models (2 unprefixed + 2 prefixed), got %d", len(out))
	}

	entryMap := make(map[string]*ModelInfo, len(out))
	for _, m := range out {
		entryMap[m.ID] = m
	}

	if base := entryMap["gemini-4-argon"]; base == nil {
		t.Fatal("missing unprefixed gemini-4-argon")
	} else if base.Name != "models/gemini-4-argon" {
		t.Fatalf("unprefixed gemini-4-argon Name = %q, want models/gemini-4-argon", base.Name)
	}

	if prefixed := entryMap["gcp/gemini-4-argon"]; prefixed == nil {
		t.Fatal("missing gcp/gemini-4-argon")
	} else {
		if prefixed.Name != "models/gcp/gemini-4-argon" {
			t.Fatalf("gcp/gemini-4-argon Name = %q, want models/gcp/gemini-4-argon", prefixed.Name)
		}
		if prefixed.DisplayName != "Gemini 4 Argon" {
			t.Fatalf("gcp/gemini-4-argon DisplayName = %q, want Gemini 4 Argon", prefixed.DisplayName)
		}
		if prefixed.MetadataModelID != "gemini-4-argon" {
			t.Fatalf("gcp/gemini-4-argon MetadataModelID = %q, want gemini-4-argon", prefixed.MetadataModelID)
		}
	}

	if prefixedBare := entryMap["gcp/gemini-3.8-flash"]; prefixedBare == nil {
		t.Fatal("missing gcp/gemini-3.8-flash")
	} else if prefixedBare.Name != "gcp/gemini-3.8-flash" {
		t.Fatalf("gcp/gemini-3.8-flash Name = %q, want gcp/gemini-3.8-flash", prefixedBare.Name)
	}

	gin.SetMode(gin.TestMode)
	authID := t.Name() + "-vertex-gcp"
	svc := &Service{cfg: &config.Config{}}
	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "vertex",
		Prefix:   "gcp",
		Status:   coreauth.StatusActive,
	}
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(authID)
	})
	svc.registerModelsForAuth(context.Background(), auth)

	handler := gemini.NewGeminiAPIHandler(&handlers.BaseAPIHandler{})
	router := gin.New()
	router.GET("/v1beta/models", handler.GeminiModels)
	router.GET("/v1beta/models/*action", handler.GeminiGetHandler)

	listRec := httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet, "/v1beta/models", nil)
	router.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("GET /v1beta/models status = %d, want 200", listRec.Code)
	}
	var listResp struct {
		Models []struct {
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
		} `json:"models"`
	}
	if errUnmarshal := json.Unmarshal(listRec.Body.Bytes(), &listResp); errUnmarshal != nil {
		t.Fatalf("decode model list response: %v", errUnmarshal)
	}
	foundPrefixed := false
	for _, m := range listResp.Models {
		if m.Name == "models/gcp/gemini-4-argon" {
			foundPrefixed = true
			break
		}
	}
	if !foundPrefixed {
		t.Fatalf("expected GET /v1beta/models to include models/gcp/gemini-4-argon")
	}

	getRec := httptest.NewRecorder()
	getReq := httptest.NewRequest(http.MethodGet, "/v1beta/models/gcp/gemini-4-argon", nil)
	router.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET /v1beta/models/gcp/gemini-4-argon status = %d, want 200 (body: %s)", getRec.Code, getRec.Body.String())
	}
	var getResp struct {
		Name        string `json:"name"`
		DisplayName string `json:"displayName"`
	}
	if errUnmarshal := json.Unmarshal(getRec.Body.Bytes(), &getResp); errUnmarshal != nil {
		t.Fatalf("decode model detail response: %v", errUnmarshal)
	}
	if getResp.Name != "models/gcp/gemini-4-argon" {
		t.Fatalf("model detail name = %q, want models/gcp/gemini-4-argon", getResp.Name)
	}

	unprefixedRec := httptest.NewRecorder()
	unprefixedReq := httptest.NewRequest(http.MethodGet, "/v1beta/models/gemini-4-argon", nil)
	router.ServeHTTP(unprefixedRec, unprefixedReq)
	if unprefixedRec.Code != http.StatusOK {
		t.Fatalf("GET /v1beta/models/gemini-4-argon status = %d, want 200 when force-model-prefix is false", unprefixedRec.Code)
	}

	forcedOut := applyModelPrefixes(models, "gcp", true)
	if len(forcedOut) != 2 {
		t.Fatalf("expected 2 forced-prefix models, got %d", len(forcedOut))
	}
	if forcedOut[0].Name != "models/gcp/gemini-4-argon" {
		t.Fatalf("forced gcp/gemini-4-argon Name = %q, want models/gcp/gemini-4-argon", forcedOut[0].Name)
	}
}
