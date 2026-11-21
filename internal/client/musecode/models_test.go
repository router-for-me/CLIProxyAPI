package musecode

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func parseModelsByID(t *testing.T, raw []byte) (string, map[string]ojson.Result) {
	t.Helper()
	root := ojson.ParseBytes(raw)
	obj := root.Get("object").String()
	data := root.Get("data")
	if !data.IsArray() {
		t.Fatalf("expected data array in JSON, got %s", string(raw))
	}
	byID := make(map[string]ojson.Result)
	data.ForEach(func(_, item ojson.Result) bool {
		id := item.Get("id").String()
		byID[id] = item
		return true
	})
	return obj, byID
}

func stringSliceFromResult(res ojson.Result) []string {
	arr := res.Array()
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		out = append(out, item.String())
	}
	return out
}

func TestBuildModelsResponseSchemaAndVariants(t *testing.T) {
	createdTS := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC).Unix()
	inputs := []ModelInput{
		{
			ID:                        "muse-wonder-1.0",
			Created:                   createdTS,
			OwnedBy:                   "meta",
			Type:                      "meta",
			DisplayName:               "Muse Wonder 1.0",
			Description:               "Reasoning flagship <beta>",
			ContextLength:             1007997,
			MaxCompletionTokens:       128000,
			Thinking:                  &registry.ThinkingSupport{Levels: []string{"minimal", "low", "medium", "high", "xhigh", "max"}},
			SupportedInputModalities:  []string{"text", "image", "video"},
			SupportedOutputModalities: []string{"text"},
		},
		{
			ID:                        "muse-spark-1.3",
			Created:                   createdTS,
			OwnedBy:                   "meta",
			Type:                      "meta",
			DisplayName:               "Muse Spark 1.3",
			ContextLength:             1007997,
			MaxCompletionTokens:       128000,
			Thinking:                  &registry.ThinkingSupport{Levels: []string{"minimal", "low", "medium", "high", "xhigh", "max"}},
			SupportedInputModalities:  []string{"text", "image", "video"},
			SupportedOutputModalities: []string{"text"},
		},
		{
			ID:                        "gpt-6.1-sol",
			Created:                   createdTS,
			OwnedBy:                   "openai",
			Type:                      "codex",
			DisplayName:               "GPT 6.1 Sol",
			ContextLength:             500000,
			MaxCompletionTokens:       128000,
			Thinking:                  &registry.ThinkingSupport{Levels: []string{"none", "low", "medium", "high", "xhigh", "max", "ultra"}},
			SupportedInputModalities:  []string{"text", "image"},
			SupportedOutputModalities: []string{"text"},
		},
		{
			ID:                        "claude-sonnet-4-5-20250929",
			Created:                   createdTS,
			OwnedBy:                   "anthropic",
			Type:                      "claude",
			DisplayName:               "Claude 4.5 Sonnet",
			ContextLength:             200000,
			MaxCompletionTokens:       64000,
			Thinking:                  &registry.ThinkingSupport{Min: 1024, Max: 128000, ZeroAllowed: true},
			SupportedInputModalities:  []string{"text", "image"},
			SupportedOutputModalities: []string{"text"},
		},
		{
			ID:                        "gemini-2.5-flash",
			Created:                   createdTS,
			OwnedBy:                   "google",
			Type:                      "gemini",
			DisplayName:               "Gemini 2.5 Flash",
			ContextLength:             1048576,
			MaxCompletionTokens:       65536,
			Thinking:                  &registry.ThinkingSupport{Max: 24576, ZeroAllowed: true, DynamicAllowed: true},
			SupportedInputModalities:  []string{"text", "image", "audio", "video"},
			SupportedOutputModalities: []string{"text"},
		},
		{
			ID:                        "kimi-k2",
			Created:                   createdTS,
			OwnedBy:                   "moonshot",
			Type:                      "kimi",
			DisplayName:               "Kimi K2",
			ContextLength:             131072,
			MaxCompletionTokens:       32768,
			SupportedInputModalities:  []string{"text"},
			SupportedOutputModalities: []string{"text"},
		},
		{
			ID:          "custom-text-model",
			Created:     createdTS,
			OwnedBy:     "custom",
			DisplayName: "Custom Text Model",
		},
		{
			ID:                        "muse-image-1.0",
			Created:                   createdTS,
			OwnedBy:                   "meta",
			Type:                      "meta",
			DisplayName:               "Muse Image 1.0",
			SupportedInputModalities:  []string{"text", "image"},
			SupportedOutputModalities: []string{"image"},
		},
		{
			ID:      "gpt-image-2",
			Created: createdTS,
			OwnedBy: "openai",
			Type:    "openai-image",
		},
		// Duplicate and blank IDs must be normalized away.
		{ID: "  "},
		{ID: "kimi-k2", DisplayName: "Duplicate Should Be Ignored"},
	}

	resp := BuildModelsResponse(inputs)
	if resp.Object != "list" {
		t.Fatalf("Object = %q, want list", resp.Object)
	}
	if len(resp.Data) != 9 {
		t.Fatalf("len(Data) = %d, want 9", len(resp.Data))
	}

	raw, errMarshal := MarshalCompact(resp)
	if errMarshal != nil {
		t.Fatalf("MarshalCompact: %v", errMarshal)
	}
	if !ojson.ValidBytes(raw) {
		t.Fatalf("MarshalCompact produced invalid JSON: %s", string(raw))
	}
	if strings.Contains(string(raw), `\u003c`) {
		t.Fatalf("MarshalCompact escaped HTML unexpectedly: %s", string(raw))
	}

	obj, entries := parseModelsByID(t, raw)
	if obj != "list" {
		t.Fatalf("top-level object = %q, want list", obj)
	}
	for id, entry := range entries {
		if entry.Get("object").String() != "model" {
			t.Fatalf("model %s object = %q, want model", id, entry.Get("object").String())
		}
		if !entry.Get("metadata.muse-code").Exists() {
			t.Fatalf("model %s missing metadata.muse-code: %s", id, entry.Raw)
		}
	}

	// 1. Meta flagship chat model.
	wonder := entries["muse-wonder-1.0"].Get("metadata.muse-code")
	if wonder.Get("family").String() != "avocado" || wonder.Get("release_date").String() != "2026-11-08" {
		t.Fatalf("unexpected wonder family/release_date: %s", wonder.Raw)
	}
	if wonder.Get("is_hidden").Bool() || !wonder.Get("attachment").Bool() || !wonder.Get("reasoning").Bool() || wonder.Get("temperature").Bool() || !wonder.Get("tool_call").Bool() {
		t.Fatalf("unexpected wonder capability flags: %s", wonder.Raw)
	}
	if wonder.Get("background_agent_model").String() != "muse-spark-1.3" {
		t.Fatalf("background_agent_model = %q, want muse-spark-1.3", wonder.Get("background_agent_model").String())
	}
	wonderOpts := wonder.Get("options")
	if wonderOpts.Get("reasoningEffort").String() != "high" || !wonderOpts.Get("forceReasoning").Bool() ||
		wonderOpts.Get("temperature").Float() != 0.9 || wonderOpts.Get("top_p").Float() != 0.9 {
		t.Fatalf("unexpected wonder options: %s", wonderOpts.Raw)
	}

	// 2. Codex model filters "none", keeps "ultra", omits temperature/top_p in options.
	sol := entries["gpt-6.1-sol"].Get("metadata.muse-code")
	if sol.Get("family").String() != "openai" || !sol.Get("reasoning").Bool() || sol.Get("temperature").Bool() {
		t.Fatalf("unexpected sol metadata: %s", sol.Raw)
	}
	solVariants := sol.Get("variants")
	if solVariants.Get("none").Exists() {
		t.Fatalf("sol variants must exclude none: %s", solVariants.Raw)
	}
	for _, wantLevel := range []string{"low", "medium", "high", "xhigh", "max", "ultra"} {
		if !solVariants.Get(wantLevel).Exists() {
			t.Fatalf("sol variants missing %q: %s", wantLevel, solVariants.Raw)
		}
	}
	solOpts := sol.Get("options")
	if solOpts.Get("reasoningEffort").String() != "medium" || !solOpts.Get("forceReasoning").Bool() {
		t.Fatalf("unexpected sol options: %s", solOpts.Raw)
	}
	if solOpts.Get("temperature").Exists() {
		t.Fatalf("non-meta model should not inject options.temperature: %s", solOpts.Raw)
	}

	// 3. Budget-only Claude 4.5 & Gemini 2.5 Flash derive discrete variants.
	claude45 := entries["claude-sonnet-4-5-20250929"].Get("metadata.muse-code.variants")
	for _, wantLevel := range []string{"low", "medium", "high", "xhigh", "max"} {
		if !claude45.Get(wantLevel).Exists() {
			t.Fatalf("claude 4.5 missing derived variant %q: %s", wantLevel, claude45.Raw)
		}
	}
	if claude45.Get("minimal").Exists() {
		t.Fatalf("claude 4.5 (min=1024) should not expose redundant minimal tier: %s", claude45.Raw)
	}

	flashVariants := entries["gemini-2.5-flash"].Get("metadata.muse-code.variants")
	for _, wantLevel := range []string{"minimal", "low", "medium", "high"} {
		if !flashVariants.Get(wantLevel).Exists() {
			t.Fatalf("gemini 2.5 flash missing derived variant %q: %s", wantLevel, flashVariants.Raw)
		}
	}
	if flashVariants.Get("max").Exists() || flashVariants.Get("xhigh").Exists() {
		t.Fatalf("gemini 2.5 flash (max=24576) should not expose xhigh/max tiers: %s", flashVariants.Raw)
	}

	// 4. Non-reasoning text model (static description enriched) & custom model (description: null).
	kimi := entries["kimi-k2"].Get("metadata.muse-code")
	if kimi.Get("reasoning").Bool() || !kimi.Get("temperature").Bool() || kimi.Get("attachment").Bool() || !kimi.Get("tool_call").Bool() || kimi.Get("is_hidden").Bool() {
		t.Fatalf("unexpected kimi metadata: %s", kimi.Raw)
	}
	if kimi.Get("variants").Exists() {
		t.Fatalf("non-reasoning model must omit variants: %s", kimi.Raw)
	}
	if kimi.Get("description").Type != ojson.String {
		t.Fatalf("kimi-k2 should receive static description when input description is empty: %s", kimi.Raw)
	}
	if kimi.Get("options").Raw != "{}" {
		t.Fatalf("non-reasoning options must be {}, got %s", kimi.Get("options").Raw)
	}

	custom := entries["custom-text-model"].Get("metadata.muse-code")
	if !custom.Get("description").Exists() || custom.Get("description").Type != ojson.Null {
		t.Fatalf("empty description with no static match must serialize as null, got %s", custom.Raw)
	}

	// 5. Hidden image models.
	museImg := entries["muse-image-1.0"].Get("metadata.muse-code")
	if museImg.Get("family").String() != "muse-image" || !museImg.Get("is_hidden").Bool() || museImg.Get("tool_call").Bool() {
		t.Fatalf("unexpected muse-image-1.0 metadata: %s", museImg.Raw)
	}
	if museImg.Get("limit").Exists() {
		t.Fatalf("muse-image-1.0 with no limits must omit limit: %s", museImg.Raw)
	}

	gptImg := entries["gpt-image-2"].Get("metadata.muse-code")
	if !gptImg.Get("is_hidden").Bool() || gptImg.Get("tool_call").Bool() {
		t.Fatalf("unexpected gpt-image-2 metadata: %s", gptImg.Raw)
	}
	outMods := stringSliceFromResult(gptImg.Get("modalities.output"))
	if len(outMods) != 1 || outMods[0] != "image" {
		t.Fatalf("gpt-image-2 output modalities = %v, want [image]", outMods)
	}
}

func TestBuildModelsResponseAliasCollisionAndExplicitEmptyModalities(t *testing.T) {
	resp := BuildModelsResponse([]ModelInput{
		{
			// Aliased Claude model sharing public ID with built-in gpt-6.1-sol must use Claude metadata.
			ID:              "gpt-6.1-sol",
			MetadataModelID: "claude-opus-4-6",
		},
		{
			// Explicit alias to unknown upstream must NOT inherit built-in gpt-5.5 metadata.
			ID:              "gpt-5.5",
			MetadataModelID: "custom-unknown-upstream",
		},
		{
			// Public alias starting with gpt-image-* but explicitly returning text must NOT be hidden.
			ID:                        "gpt-image-chat",
			MetadataModelID:           "gpt-5.5",
			OwnedBy:                   "openai",
			SupportedOutputModalities: []string{"text"},
			HasOutputModalities:       true,
		},
		{
			// Explicit empty input modalities must NOT fall back to static image/video attachment.
			ID:                       "muse-wonder-1.0",
			OwnedBy:                  "meta",
			Thinking:                 nil,
			ExplicitThinking:         true,
			SupportedInputModalities: []string{},
			ExplicitInputModalities:  true,
		},
	})

	raw, errMarshal := MarshalCompact(resp)
	if errMarshal != nil {
		t.Fatalf("MarshalCompact: %v", errMarshal)
	}
	_, entries := parseModelsByID(t, raw)

	aliasedClaude := entries["gpt-6.1-sol"].Get("metadata.muse-code")
	if aliasedClaude.Get("family").String() != "anthropic" || entries["gpt-6.1-sol"].Get("owned_by").String() != "anthropic" {
		t.Fatalf("alias colliding with gpt-6.1-sol must use claude-opus-4-6 identity, got %s", entries["gpt-6.1-sol"].Raw)
	}
	if aliasedClaude.Get("variants.xhigh").Exists() || !aliasedClaude.Get("variants.max").Exists() {
		t.Fatalf("alias must inherit claude-opus-4-6 variants (low/medium/high/max), got %s", aliasedClaude.Get("variants").Raw)
	}

	unknownAlias := entries["gpt-5.5"].Get("metadata.muse-code")
	if unknownAlias.Get("limit").Exists() || unknownAlias.Get("reasoning").Bool() {
		t.Fatalf("unknown upstream alias must not fall back to colliding gpt-5.5 static limits/reasoning: %s", unknownAlias.Raw)
	}

	mediaNamedChat := entries["gpt-image-chat"].Get("metadata.muse-code")
	if mediaNamedChat.Get("is_hidden").Bool() || !mediaNamedChat.Get("tool_call").Bool() {
		t.Fatalf("chat model aliased as gpt-image-chat must remain visible with tool_call=true: %s", mediaNamedChat.Raw)
	}
	if gotOut := stringSliceFromResult(mediaNamedChat.Get("modalities.output")); len(gotOut) != 1 || gotOut[0] != "text" {
		t.Fatalf("gpt-image-chat output modalities = %v, want [text]", gotOut)
	}

	explicitEmpty := entries["muse-wonder-1.0"].Get("metadata.muse-code")
	if explicitEmpty.Get("reasoning").Bool() {
		t.Fatalf("ExplicitThinking=true with nil Thinking must keep reasoning=false")
	}
	if explicitEmpty.Get("attachment").Bool() {
		t.Fatalf("ExplicitInputModalities=true with empty slice must keep attachment=false")
	}
	if gotIn := stringSliceFromResult(explicitEmpty.Get("modalities.input")); len(gotIn) != 1 || gotIn[0] != "text" {
		t.Fatalf("explicit empty input modalities = %v, want [text]", gotIn)
	}
	if explicitEmpty.Get("background_agent_model").Exists() {
		t.Fatalf("background_agent_model must be omitted when muse-spark-1.3 is absent")
	}
}

func TestBuildModelsResponseBudgetClampingAndConservativeMultiRouteMerge(t *testing.T) {
	resp := BuildModelsResponse([]ModelInput{
		{
			ID:       "budget-pinned-32k",
			OwnedBy:  "anthropic",
			Thinking: &registry.ThinkingSupport{Min: 32768, Max: 32768},
		},
		{
			ID:       "budget-narrow-range",
			OwnedBy:  "anthropic",
			Thinking: &registry.ThinkingSupport{Min: 2000, Max: 4000},
		},
		{
			ID:       "budget-antigravity-64k",
			OwnedBy:  "anthropic",
			Thinking: &registry.ThinkingSupport{Min: 1024, Max: 64000},
		},
		{
			ID:       "dynamic-only-no-bounds",
			OwnedBy:  "google",
			Thinking: &registry.ThinkingSupport{DynamicAllowed: true},
		},
		{
			ID: "multi-route-conservative",
			Routes: []ModelRoute{
				{
					Provider:                  "codex",
					OwnedBy:                   "openai",
					ContextLength:             500000,
					MaxCompletionTokens:       128000,
					Thinking:                  &registry.ThinkingSupport{Levels: []string{"low", "medium", "high"}},
					SupportedInputModalities:  []string{"text", "image"},
					ExplicitInputModalities:   true,
					SupportedOutputModalities: []string{"text"},
					HasOutputModalities:       true,
				},
				{
					Provider:                  "compat",
					OwnedBy:                   "openai",
					ContextLength:             128000,
					MaxCompletionTokens:       32000,
					Thinking:                  &registry.ThinkingSupport{Levels: []string{"medium", "high", "max"}},
					SupportedInputModalities:  []string{"text"},
					ExplicitInputModalities:   true,
					SupportedOutputModalities: []string{"text"},
					HasOutputModalities:       true,
				},
			},
		},
		{
			ID: "multi-route-disjoint-media",
			Routes: []ModelRoute{
				{
					Provider:                  "openai-image",
					Type:                      "openai-image",
					SupportedOutputModalities: []string{"image"},
					HasOutputModalities:       true,
				},
				{
					Provider:                  "openai",
					Type:                      "openai",
					SupportedOutputModalities: []string{"text"},
					HasOutputModalities:       true,
				},
			},
		},
		{
			// Claude route aliases claude-opus-4-6 (1M context) as gpt-6.1-sol alongside
			// native Codex gpt-6.1-sol (272k context). Codex route must keep its own
			// identity and cap context conservatively at 272000.
			ID:              "gpt-6.1-sol",
			MetadataModelID: "claude-opus-4-6",
			Routes: []ModelRoute{
				{
					Provider:        "claude",
					MetadataModelID: "claude-opus-4-6",
				},
				{
					Provider:        "codex",
					MetadataModelID: "",
				},
			},
		},
	})

	raw, errMarshal := MarshalCompact(resp)
	if errMarshal != nil {
		t.Fatalf("MarshalCompact: %v", errMarshal)
	}
	_, entries := parseModelsByID(t, raw)

	// Min=32768, Max=32768 collapses to a single deduplicated tier ("xhigh").
	pinned := entries["budget-pinned-32k"].Get("metadata.muse-code.variants")
	if pinned.Get("low").Exists() || pinned.Get("high").Exists() || !pinned.Get("xhigh").Exists() || len(pinned.Map()) != 1 {
		t.Fatalf("budget-pinned-32k variants = %s, want only xhigh", pinned.Raw)
	}

	// Min=2000, Max=4000 produces low (clamped 2000) and medium (clamped 4000).
	narrow := entries["budget-narrow-range"].Get("metadata.muse-code.variants")
	if !narrow.Get("low").Exists() || !narrow.Get("medium").Exists() || len(narrow.Map()) != 2 {
		t.Fatalf("budget-narrow-range variants = %s, want [low, medium]", narrow.Raw)
	}

	// Min=1024, Max=64000 includes max clamped to 64000.
	ag64 := entries["budget-antigravity-64k"].Get("metadata.muse-code.variants")
	for _, want := range []string{"low", "medium", "high", "xhigh", "max"} {
		if !ag64.Get(want).Exists() {
			t.Fatalf("budget-antigravity-64k missing %q in %s", want, ag64.Raw)
		}
	}

	// DynamicAllowed with Min=0, Max=0 matches thinking.detectModelCapability -> CapabilityNone.
	dynOnly := entries["dynamic-only-no-bounds"].Get("metadata.muse-code")
	if dynOnly.Get("reasoning").Bool() || dynOnly.Get("variants").Exists() {
		t.Fatalf("dynamic-only-no-bounds must not advertise reasoning: %s", dynOnly.Raw)
	}

	// Multi-route conservative intersection: text-only input, medium+high variants, min limits 128000/32000.
	merged := entries["multi-route-conservative"].Get("metadata.muse-code")
	if merged.Get("attachment").Bool() {
		t.Fatalf("multi-route must intersect input modalities to text-only: %s", merged.Raw)
	}
	if merged.Get("variants.low").Exists() || merged.Get("variants.max").Exists() ||
		!merged.Get("variants.medium").Exists() || !merged.Get("variants.high").Exists() {
		t.Fatalf("multi-route variants = %s, want intersection [medium, high]", merged.Get("variants").Raw)
	}
	if merged.Get("limit.context").Int() != 128000 || merged.Get("limit.output").Int() != 32000 {
		t.Fatalf("multi-route limit = %s, want 128000/32000", merged.Get("limit").Raw)
	}

	// Disjoint output modalities remain hidden and consistent with non-text output.
	disjoint := entries["multi-route-disjoint-media"].Get("metadata.muse-code")
	if !disjoint.Get("is_hidden").Bool() || disjoint.Get("tool_call").Bool() {
		t.Fatalf("disjoint text/image routes must conservatively hide model: %s", disjoint.Raw)
	}
	if out := stringSliceFromResult(disjoint.Get("modalities.output")); len(out) != 1 || out[0] != "image" {
		t.Fatalf("disjoint output modalities = %v, want [image]", out)
	}

	// Cross-route identity isolation: Codex route keeps gpt-6.1-sol (272k) instead of inheriting claude-opus-4-6 (1M).
	iso := entries["gpt-6.1-sol"].Get("metadata.muse-code")
	if iso.Get("limit.context").Int() != 272000 {
		t.Fatalf("cross-route gpt-6.1-sol context = %d, want 272000", iso.Get("limit.context").Int())
	}
	if iso.Get("variants.xhigh").Exists() || iso.Get("variants.ultra").Exists() || !iso.Get("variants.high").Exists() {
		t.Fatalf("cross-route gpt-6.1-sol variants = %s, want intersection [low, medium, high, max]", iso.Get("variants").Raw)
	}
}

func TestBuildModelsResponseEmptyList(t *testing.T) {
	resp := BuildModelsResponse(nil)
	raw, errMarshal := MarshalCompact(resp)
	if errMarshal != nil {
		t.Fatalf("MarshalCompact: %v", errMarshal)
	}
	if string(raw) != `{"object":"list","data":[]}` {
		t.Fatalf("empty catalog JSON = %s, want {\"object\":\"list\",\"data\":[]}", string(raw))
	}
}

func TestHandleModelsLocalRegistryMultiProviderAndMethodValidation(t *testing.T) {
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()

	cfg := &config.Config{}
	manager := auth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor.NewMetaExecutor(cfg))

	metaCred := &auth.Auth{
		ID:       "meta-local-models-test",
		Provider: "meta",
		Status:   auth.StatusActive,
		Attributes: map[string]string{
			"api_key":  "meta-secret",
			"base_url": upstream.URL + "/v1",
		},
	}
	registerMetaTestCred(t, manager, metaCred, "muse-wonder-1.0")

	// Register a shared model across two local providers with different capabilities.
	registry.GetGlobalRegistry().RegisterClient("codex-local-models-test", "codex", []*registry.ModelInfo{
		{
			ID:                        "shared-local-model",
			DisplayName:               "Shared Local",
			OwnedBy:                   "openai",
			Type:                      "codex",
			ContextLength:             500000,
			MaxCompletionTokens:       128000,
			Thinking:                  &registry.ThinkingSupport{Levels: []string{"low", "medium", "high"}},
			SupportedInputModalities:  []string{"text", "image"},
			ExplicitInputModalities:   true,
			SupportedOutputModalities: []string{"text"},
		},
	})
	registry.GetGlobalRegistry().RegisterClient("compat-local-models-test", "openai-compat", []*registry.ModelInfo{
		{
			ID:                        "shared-local-model",
			DisplayName:               "Shared Local",
			OwnedBy:                   "openai",
			Type:                      "openai",
			ContextLength:             128000,
			MaxCompletionTokens:       32000,
			Thinking:                  nil,
			ExplicitThinking:          true,
			SupportedInputModalities:  []string{"text"},
			ExplicitInputModalities:   true,
			SupportedOutputModalities: []string{"text"},
		},
	})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient("codex-local-models-test")
		registry.GetGlobalRegistry().UnregisterClient("compat-local-models-test")
	})

	handler := NewHandler(manager, cfg)
	router := newTestRouter(handler)

	// Query params must still serve local catalog cleanly.
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/muse-code/models?channel=public", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if upstreamCalls.Load() != 0 {
		t.Fatalf("/muse-code/models must not call Meta upstream; calls=%d", upstreamCalls.Load())
	}

	obj, entries := parseModelsByID(t, rec.Body.Bytes())
	if obj != "list" {
		t.Fatalf("Object = %q, want list", obj)
	}
	if !entries["muse-wonder-1.0"].Exists() || !entries["shared-local-model"].Exists() {
		t.Fatalf("expected both Meta and shared multi-provider model; got %s", rec.Body.String())
	}
	shared := entries["shared-local-model"].Get("metadata.muse-code")
	if shared.Get("attachment").Bool() || shared.Get("reasoning").Bool() || shared.Get("limit.context").Int() != 128000 {
		t.Fatalf("multi-provider local model did not merge conservatively: %s", shared.Raw)
	}

	// Direct HandleModels invocation must reject non-GET methods with 404.
	directEngine := gin.New()
	directEngine.Any("/muse-code/models", handler.HandleModels)
	postRec := httptest.NewRecorder()
	directEngine.ServeHTTP(postRec, httptest.NewRequest(http.MethodPost, "/muse-code/models", nil))
	if postRec.Code != http.StatusNotFound {
		t.Fatalf("POST HandleModels status = %d, want 404", postRec.Code)
	}
}

func TestHandleModelsHomeModeHookAndFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Without homeModelsLoader, Home mode returns 503.
	cfg := &config.Config{Home: config.HomeConfig{Enabled: true}}
	hNoLoader := NewHandler(nil, cfg)
	routerNoLoader := newTestRouter(hNoLoader)
	rec503 := httptest.NewRecorder()
	routerNoLoader.ServeHTTP(rec503, httptest.NewRequest(http.MethodGet, "/muse-code/models", nil))
	if rec503.Code != http.StatusServiceUnavailable {
		t.Fatalf("status without home loader = %d, want 503; body=%s", rec503.Code, rec503.Body.String())
	}

	// With homeModelsLoader and custom writer, Home models are returned cleanly.
	hWithHooks := NewHandler(nil, cfg)
	var writerFormat string
	hWithHooks.SetModelListHooks(
		func(c *gin.Context) ([]ModelInput, bool) {
			return []ModelInput{
				{ID: "claude-opus-4-6", OwnedBy: "anthropic", Type: "claude"},
			}, true
		},
		func(c *gin.Context, sourceFormat string, payload any) {
			writerFormat = sourceFormat
			body, _ := payload.([]byte)
			c.Data(http.StatusOK, "application/json; charset=utf-8", body)
		},
	)
	routerWithHooks := newTestRouter(hWithHooks)
	recOK := httptest.NewRecorder()
	routerWithHooks.ServeHTTP(recOK, httptest.NewRequest(http.MethodGet, "/muse-code/models", nil))
	if recOK.Code != http.StatusOK {
		t.Fatalf("status with hooks = %d, want 200; body=%s", recOK.Code, recOK.Body.String())
	}
	if writerFormat != "openai" {
		t.Fatalf("writer sourceFormat = %q, want openai", writerFormat)
	}

	_, entries := parseModelsByID(t, recOK.Body.Bytes())
	if !entries["claude-opus-4-6"].Exists() {
		t.Fatalf("missing claude-opus-4-6 in Home hook response: %s", recOK.Body.String())
	}
	if !entries["claude-opus-4-6"].Get("metadata.muse-code.reasoning").Bool() {
		t.Fatalf("expected claude-opus-4-6 enriched with static reasoning metadata")
	}
}
