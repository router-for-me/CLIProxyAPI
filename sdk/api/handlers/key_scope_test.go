package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	claudemodels "github.com/router-for-me/CLIProxyAPI/v8/internal/client/claude/models"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

const (
	keyScopeClaudeModel = "key-scope-claude-model"
	keyScopeCodexModel  = "key-scope-codex-model"
	keyScopeMetaModel   = "key-scope-meta-model"
	keyScopeSharedModel = "key-scope-shared-model"
)

func keyScopeTestHandler(t *testing.T) *BaseAPIHandler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient("key-scope-claude-client", "claude", []*registry.ModelInfo{{ID: keyScopeClaudeModel}, {ID: keyScopeSharedModel}})
	modelRegistry.RegisterClient("key-scope-codex-client", "codex", []*registry.ModelInfo{{ID: keyScopeCodexModel}, {ID: keyScopeSharedModel}})
	modelRegistry.RegisterClient("key-scope-meta-client", "meta", []*registry.ModelInfo{{ID: keyScopeMetaModel}})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient("key-scope-claude-client")
		modelRegistry.UnregisterClient("key-scope-codex-client")
		modelRegistry.UnregisterClient("key-scope-meta-client")
	})

	cfg := &sdkconfig.SDKConfig{}
	cfg.Client.KeyScopes = &[]config.ClientKeyScope{
		{KeyPrefix: "sk-claude-", Providers: []string{"anthropic"}},
		{KeyPrefix: "sk-codex-", Providers: []string{"codex"}},
		{KeyPrefix: "sk-muse-", Providers: []string{"Muse"}},
		{KeyPrefix: "sk-", Providers: []string{"claude", "codex"}},
		{KeyPrefix: "sk-empty-", Providers: nil},
		{KeyPrefix: "", Providers: []string{"meta"}},
	}
	return NewBaseAPIHandlers(cfg, nil)
}

func keyScopeRequestContext(apiKey string) (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	if apiKey != "" {
		c.Set("userApiKey", apiKey)
	}
	return c, recorder
}

func keyScopeExecutionContext(apiKey string) context.Context {
	c, _ := keyScopeRequestContext(apiKey)
	return context.WithValue(context.Background(), "gin", c)
}

func TestKeyScopeMatchesLongestPrefix(t *testing.T) {
	handler := keyScopeTestHandler(t)

	for _, testCase := range []struct {
		name   string
		apiKey string
		want   []string
		scoped bool
	}{
		{name: "claude key", apiKey: "sk-claude-abc", want: []string{"claude"}, scoped: true},
		{name: "codex key", apiKey: "sk-codex-abc", want: []string{"codex"}, scoped: true},
		{name: "muse alias resolves to meta", apiKey: "sk-muse-abc", want: []string{"meta"}, scoped: true},
		{name: "shorter prefix applies when no longer one matches", apiKey: "sk-other", want: []string{"claude", "codex"}, scoped: true},
		{name: "entry without providers is skipped", apiKey: "sk-empty-abc", want: []string{"claude", "codex"}, scoped: true},
		{name: "key matching no entry is unrestricted", apiKey: "admin-key"},
		{name: "request without a key is unrestricted", apiKey: ""},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			c, _ := keyScopeRequestContext(testCase.apiKey)
			allowed, scoped := handler.keyScope(c)
			if scoped != testCase.scoped {
				t.Fatalf("scoped = %t, want %t", scoped, testCase.scoped)
			}
			// Each name also allows the openai-compatibility entry of that name.
			if len(allowed) != 2*len(testCase.want) {
				t.Fatalf("allowed = %v, want %v and their openai-compatible- keys", allowed, testCase.want)
			}
			for _, provider := range testCase.want {
				if _, ok := allowed[provider]; !ok {
					t.Fatalf("allowed = %v, want %v", allowed, testCase.want)
				}
			}
		})
	}
}

func TestKeyAllowsProvider(t *testing.T) {
	handler := keyScopeTestHandler(t)

	for _, testCase := range []struct {
		name     string
		apiKey   string
		provider string
		want     bool
	}{
		{name: "provider in scope", apiKey: "sk-muse-abc", provider: "meta", want: true},
		{name: "provider named by its alias", apiKey: "sk-muse-abc", provider: "Muse", want: true},
		{name: "provider out of scope", apiKey: "sk-claude-abc", provider: "meta", want: false},
		{name: "unrestricted key", apiKey: "admin-key", provider: "meta", want: true},
		{name: "request without a key", apiKey: "", provider: "meta", want: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			c, _ := keyScopeRequestContext(testCase.apiKey)
			if got := handler.KeyAllowsProvider(c, testCase.provider); got != testCase.want {
				t.Fatalf("KeyAllowsProvider = %t, want %t", got, testCase.want)
			}
		})
	}
}

func TestScopeProvidersNarrowsAndRejects(t *testing.T) {
	handler := keyScopeTestHandler(t)

	providers, errMsg := handler.scopeProviders(keyScopeExecutionContext("sk-claude-abc"), keyScopeSharedModel, []string{"codex", "claude"})
	if errMsg != nil || len(providers) != 1 || providers[0] != "claude" {
		t.Fatalf("shared model for a Claude key = %v, %v; want only claude", providers, errMsg)
	}

	providers, errMsg = handler.scopeProviders(keyScopeExecutionContext("sk-claude-abc"), keyScopeCodexModel, []string{"codex"})
	if errMsg == nil || providers != nil {
		t.Fatalf("Codex model for a Claude key = %v, %v; want a rejection", providers, errMsg)
	}
	if errMsg.StatusCode != http.StatusBadRequest {
		t.Fatalf("rejection status = %d, want 400", errMsg.StatusCode)
	}
	body := errMsg.Error.Error()
	if gjson.Get(body, "error.code").String() != "model_not_found" || !strings.Contains(gjson.Get(body, "error.message").String(), keyScopeCodexModel) {
		t.Fatalf("rejection body = %s", body)
	}

	providers, errMsg = handler.scopeProviders(keyScopeExecutionContext("admin-key"), keyScopeCodexModel, []string{"codex"})
	if errMsg != nil || len(providers) != 1 {
		t.Fatalf("unscoped key = %v, %v; want the providers unchanged", providers, errMsg)
	}

	providers, errMsg = handler.scopeProviders(context.Background(), keyScopeCodexModel, []string{"codex"})
	if errMsg != nil || len(providers) != 1 {
		t.Fatalf("context without a request = %v, %v; want the providers unchanged", providers, errMsg)
	}
}

// keyScopeRecordingExecutor records which provider served each request.
type keyScopeRecordingExecutor struct {
	provider string
	mu       *sync.Mutex
	calls    *[]string
}

func (e *keyScopeRecordingExecutor) record() {
	e.mu.Lock()
	defer e.mu.Unlock()
	*e.calls = append(*e.calls, e.provider)
}

func (e *keyScopeRecordingExecutor) Identifier() string { return e.provider }

func (e *keyScopeRecordingExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	e.record()
	return coreexecutor.Response{Payload: []byte(`{}`)}, nil
}

func (e *keyScopeRecordingExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.record()
	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: []byte("ok")}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *keyScopeRecordingExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *keyScopeRecordingExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	e.record()
	return coreexecutor.Response{Payload: []byte(`{"input_tokens":1}`)}, nil
}

func (e *keyScopeRecordingExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, &coreauth.Error{Code: "not_implemented", Message: "HttpRequest not implemented", HTTPStatus: http.StatusNotImplemented}
}

func TestKeyScopeAppliesToEveryExecutionPath(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(&keyScopeRecordingExecutor{provider: "claude", mu: &mu, calls: &calls})
	manager.RegisterExecutor(&keyScopeRecordingExecutor{provider: "codex", mu: &mu, calls: &calls})
	modelRegistry := registry.GetGlobalRegistry()
	for _, credential := range []struct {
		id, provider string
		models       []*registry.ModelInfo
	}{
		{"key-scope-exec-claude", "claude", []*registry.ModelInfo{{ID: keyScopeSharedModel}}},
		{"key-scope-exec-codex", "codex", []*registry.ModelInfo{{ID: keyScopeSharedModel}, {ID: keyScopeCodexModel}}},
	} {
		if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{ID: credential.id, Provider: credential.provider, Status: coreauth.StatusActive}); errRegister != nil {
			t.Fatal(errRegister)
		}
		modelRegistry.RegisterClient(credential.id, credential.provider, credential.models)
		id := credential.id
		t.Cleanup(func() { modelRegistry.UnregisterClient(id) })
	}
	cfg := &sdkconfig.SDKConfig{}
	cfg.Client.KeyScopes = &[]config.ClientKeyScope{{KeyPrefix: "sk-claude-", Providers: []string{"claude"}}}
	handler := NewBaseAPIHandlers(cfg, manager)
	ctx := keyScopeExecutionContext("sk-claude-abc")

	executors := func() []string {
		mu.Lock()
		defer mu.Unlock()
		called := calls
		calls = nil
		return called
	}
	execute := func(model string) *int {
		_, _, errMsg := handler.ExecuteWithAuthManager(ctx, "openai", model, []byte(`{"model":"`+model+`"}`), "")
		if errMsg == nil {
			return nil
		}
		return &errMsg.StatusCode
	}
	count := func(model string) *int {
		_, _, errMsg := handler.ExecuteCountWithAuthManager(ctx, "claude", model, []byte(`{"model":"`+model+`"}`), "")
		if errMsg == nil {
			return nil
		}
		return &errMsg.StatusCode
	}
	stream := func(model string) *int {
		data, _, errs := handler.ExecuteStreamWithAuthManager(ctx, "openai", model, []byte(`{"model":"`+model+`","stream":true}`), "")
		if data != nil {
			for range data {
			}
		}
		for errMsg := range errs {
			if errMsg != nil {
				return &errMsg.StatusCode
			}
		}
		return nil
	}
	for name, run := range map[string]func(string) *int{"execute": execute, "count": count, "stream": stream} {
		t.Run(name, func(t *testing.T) {
			if status := run(keyScopeCodexModel); status == nil || *status != http.StatusBadRequest {
				t.Fatalf("Codex model for a Claude key: status=%v, want 400", status)
			}
			if called := executors(); len(called) != 0 {
				t.Fatalf("a refused request reached %v", called)
			}
			if status := run(keyScopeSharedModel); status != nil {
				t.Fatalf("shared model for a Claude key failed with %d", *status)
			}
			if called := executors(); len(called) != 1 || called[0] != "claude" {
				t.Fatalf("shared model for a Claude key used %v, want only claude", called)
			}
		})
	}
}

// A model router can hand a request to an executor plugin, which picks its own
// credentials. A scoped key may only use the plugin when its scope names the
// plugin's ID; unscoped keys keep plugin routing.
func TestKeyScopeAppliesToExecutorPlugins(t *testing.T) {
	const targetPluginID = "plugin-executor"
	host := &handlerDirectExecutorRouteHost{}
	host.hasRouters = true
	host.route = func(context.Context, pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
		return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetExecutor, Target: targetPluginID}, true
	}
	cfg := &sdkconfig.SDKConfig{}
	cfg.Client.KeyScopes = &[]config.ClientKeyScope{
		{KeyPrefix: "sk-claude-", Providers: []string{"claude"}},
		{KeyPrefix: "sk-plugin-", Providers: []string{"claude", targetPluginID}},
	}
	handler := NewBaseAPIHandlers(cfg, nil)
	handler.SetModelRouterHost(host)
	body := []byte(`{"model":"plugin-routed"}`)
	run := map[string]func(context.Context) *int{
		"execute": func(ctx context.Context) *int {
			_, _, errMsg := handler.ExecuteWithAuthManager(ctx, "openai", "plugin-routed", body, "")
			if errMsg == nil {
				return nil
			}
			return &errMsg.StatusCode
		},
		"count": func(ctx context.Context) *int {
			_, _, errMsg := handler.ExecuteCountWithAuthManager(ctx, "openai", "plugin-routed", body, "")
			if errMsg == nil {
				return nil
			}
			return &errMsg.StatusCode
		},
		"stream": func(ctx context.Context) *int {
			data, _, errs := handler.ExecuteStreamWithAuthManager(ctx, "openai", "plugin-routed", body, "")
			if data != nil {
				for range data {
				}
			}
			for errMsg := range errs {
				if errMsg != nil {
					return &errMsg.StatusCode
				}
			}
			return nil
		},
	}
	for name, execute := range run {
		t.Run(name, func(t *testing.T) {
			host.lastPluginID = ""
			if status := execute(keyScopeExecutionContext("sk-claude-abc")); status == nil || *status != http.StatusBadRequest {
				t.Fatalf("plugin route for a Claude key: status=%v, want 400", status)
			}
			if host.lastPluginID != "" {
				t.Fatalf("a key whose scope lacks the plugin reached %q", host.lastPluginID)
			}
			for _, apiKey := range []string{"sk-plugin-abc", "unscoped-key"} {
				host.lastPluginID = ""
				if status := execute(keyScopeExecutionContext(apiKey)); status != nil {
					t.Fatalf("plugin route for %s failed with %d", apiKey, *status)
				}
				if host.lastPluginID != targetPluginID {
					t.Fatalf("%s used plugin %q, want %q", apiKey, host.lastPluginID, targetPluginID)
				}
			}
		})
	}
}

func TestScopeModelListFiltersEveryListShape(t *testing.T) {
	handler := keyScopeTestHandler(t)

	ids := func(body []byte, listKey, field string) []string {
		var out []string
		gjson.GetBytes(body, listKey).ForEach(func(_, entry gjson.Result) bool {
			out = append(out, entry.Get(field).String())
			return true
		})
		return out
	}
	equal := func(got, want []string) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}

	openAIList := []byte(`{"object":"list","data":[{"id":"` + keyScopeClaudeModel + `"},{"id":"` + keyScopeCodexModel + `"},{"id":"` + keyScopeSharedModel + `"},{"id":"` + keyScopeMetaModel + `"},{"id":"unregistered-model"},{"object":"model"}]}`)
	anthropicList := []byte(`{"data":[{"id":"` + keyScopeCodexModel + `"},{"id":"` + keyScopeClaudeModel + `"},{"id":"` + keyScopeSharedModel + `"}],"first_id":"` + keyScopeCodexModel + `","last_id":"` + keyScopeSharedModel + `","has_more":false}`)
	codexCatalog := []byte(`{"models":[{"slug":"` + keyScopeCodexModel + `","visibility":"list"},{"slug":"` + keyScopeClaudeModel + `"}]}`)
	geminiList := []byte(`{"models":[{"name":"models/` + keyScopeMetaModel + `","displayName":"Meta"},{"name":"models/` + keyScopeCodexModel + `"}]}`)

	claude, _ := keyScopeRequestContext("sk-claude-abc")
	if got := ids(handler.scopeModelList(claude, openAIList), "data", "id"); !equal(got, []string{keyScopeClaudeModel, keyScopeSharedModel}) {
		t.Fatalf("OpenAI list for a Claude key = %v", got)
	}
	scopedAnthropic := handler.scopeModelList(claude, anthropicList)
	if got := ids(scopedAnthropic, "data", "id"); !equal(got, []string{keyScopeClaudeModel, keyScopeSharedModel}) {
		t.Fatalf("Anthropic list for a Claude key = %v", got)
	}
	if first, last := gjson.GetBytes(scopedAnthropic, "first_id").String(), gjson.GetBytes(scopedAnthropic, "last_id").String(); first != keyScopeClaudeModel || last != keyScopeSharedModel {
		t.Fatalf("Anthropic first_id/last_id = %q/%q", first, last)
	}
	if !gjson.GetBytes(scopedAnthropic, "has_more").Exists() {
		t.Fatal("unrelated list fields were dropped")
	}

	codex, _ := keyScopeRequestContext("sk-codex-abc")
	if got := ids(handler.scopeModelList(codex, codexCatalog), "models", "slug"); !equal(got, []string{keyScopeCodexModel}) {
		t.Fatalf("Codex catalog for a Codex key = %v", got)
	}

	muse, _ := keyScopeRequestContext("sk-muse-abc")
	if got := ids(handler.scopeModelList(muse, geminiList), "models", "name"); !equal(got, []string{"models/" + keyScopeMetaModel}) {
		t.Fatalf("Gemini-shaped list for a Muse key = %v", got)
	}
	if got := ids(handler.scopeModelList(muse, codexCatalog), "models", "slug"); len(got) != 0 {
		t.Fatalf("Codex catalog for a Muse key = %v, want it empty", got)
	}
	if !json.Valid(handler.scopeModelList(muse, codexCatalog)) {
		t.Fatal("an emptied list is not valid JSON")
	}

	admin, _ := keyScopeRequestContext("admin-key")
	if got := handler.scopeModelList(admin, openAIList); string(got) != string(openAIList) {
		t.Fatal("an unscoped key must get the list unchanged")
	}
	if got := handler.scopeModelList(claude, []byte("not json")); string(got) != "not json" {
		t.Fatal("a body that is not JSON must pass through unchanged")
	}
}

// An openai-compatibility entry registers its credentials and models under
// openai-compatible-<name>, so a scope may name the entry as it is configured.
func TestKeyScopeAcceptsOpenAICompatibilityNames(t *testing.T) {
	handler := keyScopeTestHandler(t)
	const routerModel = "key-scope-router-model"
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient("key-scope-router-client", "openai-compatible-openrouter", []*registry.ModelInfo{{ID: routerModel}})
	t.Cleanup(func() { modelRegistry.UnregisterClient("key-scope-router-client") })
	*handler.Cfg.Client.KeyScopes = append(*handler.Cfg.Client.KeyScopes,
		config.ClientKeyScope{KeyPrefix: "sk-router-", Providers: []string{"OpenRouter"}},
		config.ClientKeyScope{KeyPrefix: "sk-router-key-", Providers: []string{"openai-compatible-openrouter"}},
	)

	for _, apiKey := range []string{"sk-router-abc", "sk-router-key-abc"} {
		providers, errMsg := handler.scopeProviders(keyScopeExecutionContext(apiKey), routerModel, []string{"openai-compatible-openrouter"})
		if errMsg != nil || len(providers) != 1 {
			t.Fatalf("%s: compatibility model = %v, %v; want it allowed", apiKey, providers, errMsg)
		}
		if _, errMsg = handler.scopeProviders(keyScopeExecutionContext(apiKey), keyScopeClaudeModel, []string{"claude"}); errMsg == nil {
			t.Fatalf("%s: a Claude model was allowed", apiKey)
		}
		c, _ := keyScopeRequestContext(apiKey)
		list := handler.scopeModelList(c, []byte(`{"data":[{"id":"`+routerModel+`"},{"id":"`+keyScopeClaudeModel+`"}]}`))
		if data := gjson.GetBytes(list, "data").Array(); len(data) != 1 || data[0].Get("id").String() != routerModel {
			t.Fatalf("%s: model list = %s, want only the compatibility model", apiKey, list)
		}
	}
}

// The Anthropic model list disguises other providers' models as claude-* IDs.
// A key scoped to one of those providers must still see them there.
func TestScopeModelListResolvesDisguisedAnthropicIDs(t *testing.T) {
	handler := keyScopeTestHandler(t)
	disguised := claudemodels.EnsureClaudeModelIDPrefix(keyScopeCodexModel)
	if disguised == keyScopeCodexModel {
		t.Fatal("test model is not disguised in the Anthropic list")
	}
	list := claudemodels.BuildResponse([]map[string]any{
		{"id": keyScopeCodexModel, "display_name": "A"},
		{"id": keyScopeMetaModel, "display_name": "B"},
	}, false)

	codex, recorder := keyScopeRequestContext("sk-codex-abc")
	handler.WriteModelListResponse(codex, "claude", list)
	body := recorder.Body.Bytes()
	data := gjson.GetBytes(body, "data").Array()
	if len(data) != 1 || data[0].Get("id").String() != disguised {
		t.Fatalf("Anthropic list for a Codex key = %s, want only %s", body, disguised)
	}
	if first, last := gjson.GetBytes(body, "first_id").String(), gjson.GetBytes(body, "last_id").String(); first != disguised || last != disguised {
		t.Fatalf("first_id/last_id = %q/%q, want the listed ID %q", first, last, disguised)
	}
}

func TestWriteModelListResponseAppliesKeyScope(t *testing.T) {
	handler := keyScopeTestHandler(t)
	c, recorder := keyScopeRequestContext("sk-codex-abc")

	handler.WriteModelListResponse(c, "openai", gin.H{"object": "list", "data": []gin.H{{"id": keyScopeClaudeModel}, {"id": keyScopeCodexModel}}})

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	data := gjson.GetBytes(recorder.Body.Bytes(), "data").Array()
	if len(data) != 1 || data[0].Get("id").String() != keyScopeCodexModel {
		t.Fatalf("written list = %s, want only the Codex model", recorder.Body.String())
	}

	// /v1/models/{id} picks from the same scoped list.
	detail, detailRecorder := keyScopeRequestContext("sk-codex-abc")
	detail.Set(ModelDetailIDContextKey, keyScopeClaudeModel)
	handler.WriteModelListResponse(detail, "openai", gin.H{"object": "list", "data": []gin.H{{"id": keyScopeClaudeModel}, {"id": keyScopeCodexModel}}})
	if detailRecorder.Code != http.StatusNotFound {
		t.Fatalf("Claude model detail for a Codex key: status=%d body=%s, want 404", detailRecorder.Code, detailRecorder.Body.String())
	}
}

// A response interceptor may replace the catalog; the scope applies to what it
// returns, for the list and for the model detail picked from it.
func TestWriteModelListResponseScopesInterceptedCatalog(t *testing.T) {
	handler := keyScopeTestHandler(t)
	handler.SetPluginHost(&handlerInterceptorTestHost{
		interceptResponse: func(_ context.Context, req pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
			headers := cloneHeader(req.ResponseHeaders)
			headers.Set("Content-Length", "1024")
			return pluginapi.ResponseInterceptResponse{
				Headers: headers,
				Body:    []byte(`{"object":"list","data":[{"id":"` + keyScopeClaudeModel + `"},{"id":"` + keyScopeCodexModel + `"}]}`),
			}
		},
	})
	original := gin.H{"object": "list", "data": []gin.H{{"id": keyScopeCodexModel}}}

	c, recorder := keyScopeRequestContext("sk-codex-abc")
	handler.WriteModelListResponse(c, "openai", original)
	data := gjson.GetBytes(recorder.Body.Bytes(), "data").Array()
	if len(data) != 1 || data[0].Get("id").String() != keyScopeCodexModel {
		t.Fatalf("intercepted list for a Codex key = %s, want only the Codex model", recorder.Body.String())
	}
	if length := recorder.Header().Get("Content-Length"); length != "" {
		t.Fatalf("Content-Length = %s survived a filtered body", length)
	}

	detail, detailRecorder := keyScopeRequestContext("sk-codex-abc")
	detail.Set(ModelDetailIDContextKey, keyScopeClaudeModel)
	handler.WriteModelListResponse(detail, "openai", original)
	if detailRecorder.Code != http.StatusNotFound {
		t.Fatalf("intercepted Claude model detail for a Codex key: status=%d body=%s, want 404", detailRecorder.Code, detailRecorder.Body.String())
	}
}

// In Home mode Home picks the credential for a model, and the local registry
// does not know which provider that is, so key scopes leave requests and lists
// alone.
func TestKeyScopesDoNotApplyInHomeMode(t *testing.T) {
	handler := keyScopeTestHandler(t)
	handler.AuthManager = coreauth.NewManager(nil, nil, nil)
	handler.AuthManager.SetConfig(&config.Config{Home: config.HomeConfig{Enabled: true}})

	claude, recorder := keyScopeRequestContext("sk-claude-abc")
	if !handler.KeyAllowsProvider(claude, "codex") {
		t.Fatal("a scope applied in Home mode")
	}
	providers, errMsg := handler.scopeProviders(keyScopeExecutionContext("sk-claude-abc"), "any", []string{"home"})
	if errMsg != nil || len(providers) != 1 || providers[0] != "home" {
		t.Fatalf("Home routing = %v, %v; want it left alone", providers, errMsg)
	}
	handler.WriteModelListResponse(claude, "openai", gin.H{"object": "list", "data": []gin.H{{"id": "home-only-model"}}})
	if data := gjson.GetBytes(recorder.Body.Bytes(), "data").Array(); len(data) != 1 {
		t.Fatalf("Home model list for a scoped key = %s, want it unchanged", recorder.Body.String())
	}
}
