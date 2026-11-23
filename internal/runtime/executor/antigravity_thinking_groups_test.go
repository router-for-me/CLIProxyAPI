package executor

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type antigravityThinkingGroupsKV struct {
	*fakeAntigravityKVClient
	catalogReads int
}

func (c *antigravityThinkingGroupsKV) KVGet(ctx context.Context, key string) ([]byte, bool, error) {
	if strings.HasPrefix(key, "antigravity:model-enums:v1:") {
		c.catalogReads++
	}
	return c.fakeAntigravityKVClient.KVGet(ctx, key)
}

func TestAntigravityThinkingGroupsGenerate(t *testing.T) {
	client := &antigravityThinkingGroupsKV{fakeAntigravityKVClient: newFakeAntigravityKVClient()}
	useFakeAntigravityKVClient(t, client.fakeAntigravityKVClient, true, nil)
	kvstore.RegisterHomeProvider(func() (kvstore.KVClient, bool, error) { return client, true, nil })
	auth := newIssue7865TestAuth("thinking-groups-home")
	scope := strings.Repeat("a", 64)
	auth.Metadata["disable_cooling"] = true
	auth.Attributes = map[string]string{cliproxyauth.AntigravityModelEnumHomeScopeAttribute: scope}
	key := "antigravity:model-enums:v1:" + kvstore.HashKeyPart(auth.ID) + ":" + scope
	const high = "gemini-3.8-flash-high"
	const low = "gemini-3.8-flash-low"
	const tiered = "gemini-3.8-flash-tiered"
	const native = `{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingLevel":"LOW","includeThoughts":true}}}}`
	const noThinking = `{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`
	models := map[string]string{high: "M321", low: "M320", tiered: "M322", "gemini-3.8-flash-very-high": "M323", "gemini-3.8-flash-medium": "M324"}
	groups := map[string]map[string]int{"gemini-3.8-flash": {"low": 65536, "high": 32768, "very-high": 81920, "medium": 0}, "gemini-3.8-flash-very": {"high": 0}}
	for _, tc := range []struct {
		name, model, payload string
		legacy               bool
		wantModel, wantEnum  string
		wantLevel            string
		wantMax              int64
	}{
		{"variant", high, native, false, low, "M320", "LOW", 65536},
		{"group", "gemini-3.8-flash", native, false, low, "M320", "LOW", 65536},
		{"same variant", low, native, false, low, "M320", "LOW", 65536},
		{"more-specific group owns zero-limit member", "gemini-3.8-flash-very-high", native, false, "gemini-3.8-flash-very-high", "M323", "LOW", 0},
		{"zero limit is membership", "gemini-3.8-flash-medium", native, false, low, "M320", "LOW", 65536},
		{"blank target enum", high, native, false, high, "M321", "LOW", 0},
		{"missing target enum", high, native, false, high, "M321", "LOW", 0},
		{"declared custom suffix", high + "(very-high)", native, false, "gemini-3.8-flash-very-high", "M323", "VERY-HIGH", 81920},
		{"native thinking alias", high, strings.ReplaceAll(native, `"thinkingConfig"`, `"thinking_config"`), false, low, "M320", "LOW", 65536},
		{"tiered is not a member", tiered, native, false, tiered, "M322", "LOW", 0},
		{"missing level", high + "(medium)", native, false, high, "M321", "MEDIUM", 0},
		{"no thinking", high, noThinking, false, high, "M321", "", 0},
		{"empty thinking config", high, strings.Replace(noThinking, `{"contents"`, `{"generationConfig":{"thinkingConfig":{}},"contents"`, 1), false, high, "M321", "", 0},
		{"legacy", high, native, true, high, "M321", "LOW", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enums := maps.Clone(models)
			switch tc.name {
			case "blank target enum":
				enums[low] = " "
			case "missing target enum":
				delete(enums, low)
			}
			snapshot := map[string]any{"scope": scope, "models": enums}
			if !tc.legacy {
				snapshot["thinkingGroups"] = groups
			}
			client.values[key] = mustAntigravityJSON(t, snapshot)
			prevReads := client.catalogReads
			got := captureAntigravityThinkingGroupRequest(t, &config.Config{}, auth, tc.model, sdktranslator.FromString("antigravity"), tc.payload, antigravityGeneratePath)
			assertAntigravityThinkingGroupRequest(t, got, "model", tc.wantModel, tc.wantEnum, tc.wantLevel, tc.wantMax)
			if tc.wantMax > 0 && (!got.Get("request.generationConfig.thinkingConfig.includeThoughts").Bool() || got.Get("request.generationConfig.thinking_config").Exists()) {
				t.Errorf("expected normalized thinking summary configuration: %s", got.Raw)
			}
			if reads := client.catalogReads - prevReads; reads != 1 {
				t.Errorf("catalog reads = %d, want one", reads)
			}
		})
	}
}

func TestAntigravityThinkingGroupsProtocols(t *testing.T) {
	client := newFakeAntigravityKVClient()
	useFakeAntigravityKVClient(t, client, true, nil)
	auth := newIssue7865TestAuth("thinking-groups-protocols")
	scope := strings.Repeat("b", 64)
	auth.Attributes = map[string]string{cliproxyauth.AntigravityModelEnumHomeScopeAttribute: scope}
	key := "antigravity:model-enums:v1:" + kvstore.HashKeyPart(auth.ID) + ":" + scope
	client.values[key] = []byte(`{
		"scope":"` + scope + `",
		"models":{
			"gemini-demo-ultra-low":"M10","gemini-demo-ultra-xhigh":"M11",
			"claude-demo-low":"M20","claude-demo-xhigh":"M21",
			"claude-demo-ultra-low":"M30","claude-demo-ultra-xhigh":"M31"
		},
		"thinkingGroups":{
			"gemini-demo-ultra":{"low":70000,"xhigh":80000},
			"claude-demo":{"low":50000,"xhigh":60000},
			"claude-demo-ultra":{"low":90000,"xhigh":100000}
		}
	}`)
	const geminiInput = `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingLevel":"xhigh","includeThoughts":false}}}`
	const claudeInput = `{"thinking":{"type":"adaptive"},"output_config":{"effort":"xhigh"},"messages":[{"role":"user","content":"hi"}]}`
	for _, tc := range []struct {
		name, model, payload, path string
		format                     sdktranslator.Format
		wantModel, wantEnum, level string
		wantMax                    int64
	}{
		{"Gemini ultra original input", "gemini-demo-ultra-low", geminiInput, antigravityGeneratePath, sdktranslator.FormatGemini, "gemini-demo-ultra-xhigh", "M11", "XHIGH", 80000},
		{"Claude original input", "claude-demo-low", claudeInput, antigravityStreamPath, sdktranslator.FormatClaude, "claude-demo-xhigh", "M21", "XHIGH", 60000},
		{"Claude ultra suffix wins on stream", "claude-demo-ultra-xhigh(low)", claudeInput, antigravityStreamPath, sdktranslator.FormatClaude, "claude-demo-ultra-low", "M30", "LOW", 90000},
		{"OpenAI snake-case generation", "gemini-demo-ultra-low", `{"generation_config":{"thinking_config":{"thinking_level":"xhigh"}},"messages":[{"role":"user","content":"hi"}]}`, antigravityGeneratePath, sdktranslator.FormatOpenAI, "gemini-demo-ultra-xhigh", "M11", "XHIGH", 80000},
		{"OpenAI nested snake level overrides effort", "gemini-demo-ultra-xhigh", `{"reasoning_effort":"xhigh","generation_config":{"thinking_config":{"thinking_level":"low"}},"messages":[{"role":"user","content":"hi"}]}`, antigravityGeneratePath, sdktranslator.FormatOpenAI, "gemini-demo-ultra-low", "M10", "LOW", 70000},
		{"Interactions reasoning overrides generation", "gemini-demo-ultra-low", `{"reasoning":{"effort":"xhigh"},"generation_config":{"thinking_level":"low"},"input":"hi"}`, antigravityGeneratePath, sdktranslator.FormatInteractions, "gemini-demo-ultra-xhigh", "M11", "XHIGH", 80000},
		{"Interactions compatibility spelling", "gemini-demo-ultra-low", `{"reasoning":{"thinking_level":"xhigh"},"input":"hi"}`, antigravityGeneratePath, sdktranslator.FormatInteractions, "gemini-demo-ultra-xhigh", "M11", "XHIGH", 80000},
		{"Responses original input on stream", "gemini-demo-ultra-low", `{"reasoning":{"effort":"xhigh"},"input":"hi"}`, antigravityStreamPath, sdktranslator.FormatOpenAIResponse, "gemini-demo-ultra-xhigh", "M11", "XHIGH", 80000},
		{"Claude adaptive without effort", "claude-demo-low", `{"thinking":{"type":"adaptive"},"messages":[{"role":"user","content":"hi"}]}`, antigravityStreamPath, sdktranslator.FormatClaude, "claude-demo-low", "M20", "", 0},
		{"Count tokens honors numeric suffix", "gemini-demo-ultra-xhigh(1024)", geminiInput, antigravityCountTokensPath, sdktranslator.FormatGemini, "models/gemini-demo-ultra-low", "", "LOW", 70000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := captureAntigravityThinkingGroupRequest(t, &config.Config{}, auth, tc.model, tc.format, tc.payload, tc.path)
			modelPath := "model"
			if tc.path == antigravityCountTokensPath {
				modelPath = "request.model"
				if got.Get("model").Exists() || got.Get("request.labels").Exists() {
					t.Errorf("unexpected countTokens envelope: %s", got.Raw)
				}
			}
			assertAntigravityThinkingGroupRequest(t, got, modelPath, tc.wantModel, tc.wantEnum, tc.level, tc.wantMax)
			if tc.level == "" && got.Get("request.generationConfig.thinkingConfig.thinkingBudget").Int() != 24576 {
				t.Errorf("legacy Claude thinking budget changed: %s", got.Raw)
			}
		})
	}
}

func TestAntigravityThinkingGroupsPayloadRulesLast(t *testing.T) {
	client := newFakeAntigravityKVClient()
	useFakeAntigravityKVClient(t, client, true, nil)
	auth := newIssue7865TestAuth("thinking-groups-payload")
	scope := strings.Repeat("c", 64)
	auth.Attributes = map[string]string{cliproxyauth.AntigravityModelEnumHomeScopeAttribute: scope}
	key := "antigravity:model-enums:v1:" + kvstore.HashKeyPart(auth.ID) + ":" + scope
	client.values[key] = []byte(`{"scope":"` + scope + `","models":{"gemini-demo-high":"HIGH_ENUM","gemini-demo-low":"LOW_ENUM"},"thinkingGroups":{"gemini-demo":{"high":40000,"low":50000}}}`)
	const model = "gemini-demo-high"
	const payload = `{"reasoning_effort":"low","messages":[{"role":"user","content":"hi"}]}`
	cfg := &config.Config{Payload: config.PayloadConfig{
		Override: []config.PayloadRule{{
			Models: []config.PayloadModelRule{{Name: model, Protocol: "antigravity"}},
			Params: map[string]any{"generationConfig.maxOutputTokens": 1234, "labels.model_enum": "FILTER_ME"},
		}, {
			Models: []config.PayloadModelRule{{Name: "client-alias", Protocol: "antigravity"}},
			Params: map[string]any{"generationConfig.thinkingConfig.thinkingLevel": "OVERRIDE"},
		}},
		Filter: []config.PayloadFilterRule{{
			Models: []config.PayloadModelRule{{Name: "gemini-demo-low", Protocol: "antigravity"}},
			Params: []string{"labels.model_enum", "model"},
		}},
	}}
	got := captureAntigravityThinkingGroupRequest(t, cfg, auth, model, sdktranslator.FormatOpenAI, payload, antigravityGeneratePath, "client-alias")
	assertAntigravityThinkingGroupRequest(t, got, "model", "gemini-demo-low", "", "OVERRIDE", 1234)
	count := captureAntigravityThinkingGroupRequest(t, cfg, auth, model, sdktranslator.FormatOpenAI, payload, antigravityCountTokensPath, "client-alias")
	assertAntigravityThinkingGroupRequest(t, count, "request.model", "", "", "OVERRIDE", 1234)
}

type antigravityThinkingGroupsDispatcher struct {
	t     *testing.T
	auths []*cliproxyauth.Auth
	calls int
}

func (*antigravityThinkingGroupsDispatcher) HeartbeatOK() bool { return true }

func (*antigravityThinkingGroupsDispatcher) AbortAmbiguousDispatch() {}

func (d *antigravityThinkingGroupsDispatcher) RPopAuth(_ context.Context, model, _ string, _ http.Header, _ int) ([]byte, error) {
	if d.calls >= len(d.auths) {
		return nil, fmt.Errorf("unexpected Home dispatch %d", d.calls+1)
	}
	auth := d.auths[d.calls]
	d.calls++
	return mustAntigravityJSON(d.t, map[string]any{
		"model": model, "provider": "antigravity", "auth": auth,
		"model_info": map[string]any{"id": model, "type": "gemini", "thinking": map[string]any{"levels": []string{"low", "high"}}},
	}), nil
}

func TestAntigravityThinkingGroupsHomeRetry(t *testing.T) {
	client := newFakeAntigravityKVClient()
	useFakeAntigravityKVClient(t, client, true, nil)
	first := newIssue7865TestAuth("thinking-groups-retry-first")
	second := newIssue7865TestAuth("thinking-groups-retry-second")
	bodies := make(chan ojson.Result, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != antigravityGeneratePath {
			t.Errorf("unexpected path %s", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusBadRequest)
			return
		}
		raw, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Error(errRead)
			http.Error(w, "read failed", http.StatusBadRequest)
			return
		}
		bodies <- ojson.ParseBytes(raw)
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") == "Bearer token-"+first.ID {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"status":"RESOURCE_EXHAUSTED","message":"rate limit exceeded"}}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer token-"+second.ID {
			t.Errorf("unexpected credential in retry")
		}
		_, _ = io.WriteString(w, `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}}`)
	}))
	t.Cleanup(server.Close)
	const low = "gemini-retry-low"
	const high = "gemini-retry-high"
	const xhigh = "gemini-retry-xhigh"
	for i, auth := range []*cliproxyauth.Auth{first, second} {
		scope := strings.Repeat(string(rune('a'+i)), 64)
		auth.Attributes = map[string]string{cliproxyauth.AntigravityModelEnumHomeScopeAttribute: scope, "base_url": server.URL}
		auth.Metadata["disable_cooling"] = true
		key := "antigravity:model-enums:v1:" + kvstore.HashKeyPart(auth.ID) + ":" + scope
		models := map[string]string{low: "SECOND_LOW", high: "SECOND_HIGH"}
		levels := map[string]int{"low": 50000, "high": 60000}
		if i == 0 {
			models[xhigh] = "FIRST_XHIGH"
			levels["xhigh"] = 70000
		}
		client.values[key] = mustAntigravityJSON(t, map[string]any{"scope": scope, "models": models, "thinkingGroups": map[string]map[string]int{"gemini-retry": levels}})
	}
	cfg := &config.Config{Home: config.HomeConfig{Enabled: true}}
	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.SetRetryConfig(0, time.Second, 2)
	manager.RegisterExecutor(NewAntigravityExecutor(cfg))
	dispatch := &antigravityThinkingGroupsDispatcher{t: t, auths: []*cliproxyauth.Auth{first, second}}
	manager.PublishHomeDispatch(dispatch, executionregistry.New(), 1)

	req := cliproxyexecutor.Request{Model: low, Payload: []byte(`{"reasoning_effort":"xhigh","messages":[{"role":"user","content":"hi"}]}`)}
	if _, errExecute := manager.Execute(t.Context(), []string{"antigravity"}, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}); errExecute != nil {
		t.Fatal(errExecute)
	}
	if dispatch.calls != 2 || len(bodies) != 2 {
		t.Fatalf("dispatches = %d and upstream requests = %d, want 2 each", dispatch.calls, len(bodies))
	}
	assertAntigravityThinkingGroupRequest(t, <-bodies, "model", xhigh, "FIRST_XHIGH", "XHIGH", 70000)
	// Legacy validation clamps the wire level to HIGH, but may not route to the high variant.
	assertAntigravityThinkingGroupRequest(t, <-bodies, "model", low, "SECOND_LOW", "HIGH", 0)
}

func captureAntigravityThinkingGroupRequest(t *testing.T, cfg *config.Config, auth *cliproxyauth.Auth, model string, format sdktranslator.Format, payload, path string, requested ...string) ojson.Result {
	t.Helper()
	const response = `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}}`
	var body []byte
	var gotPath string
	ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		gotPath = r.URL.Path
		var errRead error
		body, errRead = io.ReadAll(r.Body)
		if errRead != nil {
			return nil, errRead
		}
		out, contentType := response, "application/json"
		if path == antigravityStreamPath {
			out, contentType = "data: "+response+"\n\n", "text/event-stream"
		} else if path == antigravityCountTokensPath {
			out = `{"totalTokens":2}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(out)), Header: http.Header{"Content-Type": []string{contentType}}}, nil
	}))
	exec := NewAntigravityExecutor(cfg)
	req := cliproxyexecutor.Request{Model: model, Payload: []byte(payload)}
	opts := cliproxyexecutor.Options{SourceFormat: format, ResponseFormat: sdktranslator.FormatGemini, OriginalRequest: req.Payload}
	if len(requested) > 0 {
		opts.Metadata = map[string]any{cliproxyexecutor.RequestedModelMetadataKey: requested[0]}
	}
	switch path {
	case antigravityCountTokensPath:
		if _, errCount := exec.CountTokens(ctx, auth, req, opts); errCount != nil {
			t.Fatal(errCount)
		}
	case antigravityStreamPath:
		result, errStream := exec.ExecuteStream(ctx, auth, req, opts)
		if errStream != nil {
			t.Fatal(errStream)
		}
		for chunk := range result.Chunks {
			if chunk.Err != nil {
				t.Fatal(chunk.Err)
			}
		}
	default:
		if _, errExecute := exec.Execute(ctx, auth, req, opts); errExecute != nil {
			t.Fatal(errExecute)
		}
	}
	if gotPath != path || len(body) == 0 {
		t.Fatalf("request path = %q, want %q; body=%s", gotPath, path, body)
	}
	return ojson.ParseBytes(body)
}

func assertAntigravityThinkingGroupRequest(t *testing.T, got ojson.Result, modelPath, model, enum, level string, maxTokens int64) {
	t.Helper()
	for path, want := range map[string]string{
		modelPath:                   model,
		"request.labels.model_enum": enum,
		"request.generationConfig.thinkingConfig.thinkingLevel": level,
	} {
		value := got.Get(path)
		if value.Str != want || value.Exists() != (want != "") {
			t.Errorf("%s = %s, want %q; body=%s", path, value.Raw, want, got.Raw)
		}
	}
	value := got.Get("request.generationConfig.maxOutputTokens")
	if value.Int() != maxTokens || value.Exists() != (maxTokens > 0) {
		t.Errorf("maxOutputTokens = %s, want %d; body=%s", value.Raw, maxTokens, got.Raw)
	}
}
