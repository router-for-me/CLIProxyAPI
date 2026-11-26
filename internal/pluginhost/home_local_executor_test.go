package pluginhost

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
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
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/gemini"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestHomeLocalExecutorHTTP(t *testing.T) {
	const model = "agy/gemini-2.5-pro"
	const pluginID = "alpha"
	const pluginVersion = "1.0.0"
	const pluginYAML = "enabled: true\npriority: 100\n"
	configDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(pluginYAML)))
	const alias = "acp/" + model
	const key = "acp-test-key"
	const chat = "/v1/chat/completions"
	const configuredStop = "configured-stop"
	var calls, grants, credentialPops atomic.Int32
	var disabled, unauthenticated, fail, oldHome, credential, preferBeta, staleConfig atomic.Bool
	var onGrant atomic.Pointer[func()]
	client := newHomeExecutorTestClient(t, func(command, argument string) []byte {
		doc := ojson.NewDocString(argument)
		if command == "RPOP" {
			credentialPops.Add(1)
			return []byte(`{"error":{"type":"model_not_found","message":"credential route checked"}}`)
		}
		if doc.Get("type").String() != "local-plugin-dispatch" || doc.Get("model").String() != alias {
			t.Errorf("unexpected request: %s %s", command, doc.Get("type").String())
			return nil
		}
		grants.Add(1)
		if oldHome.Load() {
			return []byte(`{"error":{"type":"error","message":"unsupported type"}}`)
		}
		if doc.Get("headers.authorization").String() != "Bearer "+key && doc.Get("query.key").String() != key {
			return []byte(`{"error":{"type":"model_forbidden","message":"no model access"}}`)
		}
		if disabled.Load() {
			return []byte(`{"error":{"type":"model_not_found","message":"local plugin unavailable"}}`)
		}
		if doc.Get("local_executor").Exists() {
			t.Error("CPA sent a local capability report to authorize")
		}
		if credential.Load() {
			return []byte(`{"kind":"credential"}`)
		}
		if invalidate := onGrant.Load(); invalidate != nil {
			(*invalidate)()
		}
		selected := pluginID
		if preferBeta.Load() {
			selected = "beta"
		}
		digest := configDigest
		if staleConfig.Load() {
			digest = "stale"
		}
		return []byte(fmt.Sprintf(`{"kind":"local-plugin","plugin":%q,"version":%q,"config_digest":%q,"model":%q,"principal":%q,"access_provider":"config-api-key"}`, selected, pluginVersion, digest, model, key))
	})
	manager := coreauth.NewManager(nil, nil, nil)
	homeCfg := &config.Config{Home: config.HomeConfig{Enabled: true}}
	manager.SetConfig(homeCfg)
	manager.PublishHomeDispatch(client, executionregistry.New(), 1)
	checkRequest := func(req pluginapi.ExecutorRequest) {
		calls.Add(1)
		if req.AuthID != "" || req.AuthProvider != "" || len(req.StorageJSON) > 0 {
			t.Error("local executor received a Home credential")
		}
		switch {
		case manager.HomeEnabled() && req.Model == model:
			generationConfigPath := "generationConfig"
			original := ojson.NewDoc(req.OriginalRequest)
			if original.Get("request").IsObject() && !original.Get("contents").Exists() {
				generationConfigPath = "request.generationConfig"
				if ojson.GetBytesNoCopy(req.Payload, "generationConfig").Exists() {
					t.Error("plugin payload has a top-level generationConfig beside request")
				}
			} else if ojson.GetBytesNoCopy(req.Payload, "request.generationConfig").Exists() {
				t.Error("plugin payload added request.generationConfig for a top-level request")
			}
			generationConfig := ojson.GetBytesNoCopy(req.Payload, generationConfigPath)
			if temperature := generationConfig.Get("temperature").Float(); temperature != 0.17 {
				t.Errorf("plugin payload lost the final temperature override: %v", temperature)
			}
			if stops := generationConfig.Get("stopSequences"); len(stops.Array()) != 1 || stops.Get("0").String() != configuredStop {
				t.Errorf("plugin payload %s.stopSequences = %s, want [%q]", generationConfigPath, stops.Raw, configuredStop)
			}
			for _, payload := range [][]byte{req.Payload, req.OriginalRequest} {
				for _, path := range []string{"model", "request.model"} {
					if value := ojson.GetBytesNoCopy(payload, path); value.Exists() && value.String() != model {
						t.Errorf("plugin body model = %q, want %s", value.String(), model)
					}
				}
			}
			if got, _ := req.Metadata["requested_model"].(string); strings.HasSuffix(got, "(high)") && req.Metadata["reasoning_effort"] != "high" {
				t.Errorf("reasoning effort = %v", req.Metadata["reasoning_effort"])
			}
		case !manager.HomeEnabled() && (req.Model == model || req.Model == "gemini-2.5-pro" || req.Model == "acp/native"):
		default:
			t.Errorf("unexpected model %s reached executor", req.Model)
		}
	}
	attr := &pluginapi.UsageAttribution{AuthIndex: "agy-local-slot", AuthType: "oauth", Source: "local"}
	streamStarted := make(chan struct{})
	finishStream := make(chan struct{})
	const candidate = `{"candidates":[{"content":{"role":"model","parts":[{"text":"local answer"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":5,"totalTokenCount":8}}`
	executor := &fakeExecutor{
		identifier: pluginID,
		execute: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			checkRequest(req)
			if unauthenticated.Load() {
				return pluginapi.ExecutorResponse{}, rpcError{Code: "authentication_required", message: "agy login is required", statusCode: 401}
			}
			if fail.Load() {
				return pluginapi.ExecutorResponse{}, rpcError{Code: "slot_exhausted", message: "local slot exhausted", statusCode: 429, retryAfterSeconds: 7}
			}
			return pluginapi.ExecutorResponse{Payload: []byte(candidate), Attribution: attr}, nil
		},
		executeStream: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
			checkRequest(req)
			chunks := make(chan pluginapi.ExecutorStreamChunk, 1)
			if req.Metadata["reasoning_effort"] == "high" {
				chunks <- pluginapi.ExecutorStreamChunk{Payload: []byte(candidate)}
				close(chunks)
				return pluginapi.ExecutorStreamResponse{Chunks: chunks, Attribution: attr}, nil
			}
			close(streamStarted)
			go func() {
				<-finishStream
				chunks <- pluginapi.ExecutorStreamChunk{Payload: []byte(candidate)}
				close(chunks)
			}()
			return pluginapi.ExecutorStreamResponse{Chunks: chunks, Attribution: attr}, nil
		},
		countTokens: func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			checkRequest(req)
			if ojson.GetBytesNoCopy(req.Payload, "model").Exists() {
				t.Error("Gemini count fixture must exercise model fallback")
			}
			wantOriginal := ""
			if req.Query.Get("original") == "1" {
				wantOriginal = model
			}
			if gotOriginal := ojson.GetBytesNoCopy(req.OriginalRequest, "model").String(); gotOriginal != wantOriginal {
				t.Errorf("OriginalRequest model = %q, want %q", gotOriginal, wantOriginal)
			}
			return pluginapi.ExecutorResponse{Payload: []byte(`{"totalTokens":3}`)}, nil
		},
	}
	record := normalizeTestCapabilityRecord(capabilityRecord{id: pluginID, version: pluginVersion, plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
		Executor: executor, ExecutorModelScope: pluginapi.ExecutorModelScopeBoth,
		ExecutorInputFormats: []string{"gemini"}, ExecutorOutputFormats: []string{"gemini"},
		ModelProvider: modelProviderFunc{staticModels: func(context.Context, pluginapi.StaticModelRequest) (pluginapi.ModelResponse, error) {
			return pluginapi.ModelResponse{Provider: pluginID, Models: []pluginapi.ModelInfo{
				{ID: model, Name: "gemini-2.5-pro", Thinking: &pluginapi.ThinkingSupport{Levels: []string{"low", "medium", "high"}}},
				{ID: "gemini-2.5-pro"}, {ID: "acp/native"},
			}}, nil
		}},
		ModelRouter: modelRouterFunc(func(context.Context, pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
			return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetSelf}, nil
		}),
	}}})
	record.loaded = &loadedPlugin{id: record.id, path: record.path, version: record.version, configYAML: []byte(pluginYAML)}
	host := newHostWithRecords(record)
	host.loaded[record.id] = record.loaded
	host.runtimeConfig = &config.Config{Payload: config.PayloadConfig{
		Override: []config.PayloadRule{{
			Models: []config.PayloadModelRule{{Name: model, Protocol: "gemini"}},
			Params: map[string]any{
				"generationConfig.temperature":   0.17,
				"generationConfig.stopSequences": []string{configuredStop},
			},
		}},
	}}
	host.RegisterModels(t.Context(), registry.GetGlobalRegistry())
	handler := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	handler.SetModelRouterHost(host)
	router := gin.New()
	openAI := openai.NewOpenAIAPIHandler(handler)
	router.POST(chat, openAI.ChatCompletions)
	router.GET("/v1/models", openAI.OpenAIModels)
	router.POST("/v1beta/models/*action", gemini.NewGeminiAPIHandler(handler).GeminiHandler)
	usage := newTestUsageCapturePlugin(pluginID)
	registerTestUsagePlugin(t, "local-acp-execution-usage", usage)

	request := func(path, payload, requestKey string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+requestKey)
		req.Header.Set("X-Route-Acp", "true")
		router.ServeHTTP(recorder, req)
		return recorder
	}
	perform := func(t *testing.T, path, payload, requestKey string, want int, contains string) {
		t.Helper()
		recorder := request(path, payload, requestKey)
		if recorder.Code != want || !strings.Contains(recorder.Body.String(), contains) {
			t.Fatalf("response = %d %s, want %d containing %q", recorder.Code, recorder.Body.String(), want, contains)
		}
	}
	chatBody := func(name string, stream bool) string {
		body := `{"model":"` + name + `","messages":[{"role":"user","content":"test"}],"temperature":0.9}`
		if stream {
			body = strings.TrimSuffix(body, "}") + `,"stream":true}`
		}
		return body
	}
	assertUsage := func(stream bool) {
		t.Helper()
		usageRecord := usage.waitRecord(t, 3*time.Second)
		if usageRecord.APIKey != key || usageRecord.Model != model || usageRecord.Alias != alias ||
			usageRecord.ExecutorType != "plugin:"+pluginID || usageRecord.AuthID != "" ||
			usageRecord.AuthIndex != attr.AuthIndex || usageRecord.Stream != stream {
			t.Fatal("local execution usage lost caller/model/plugin attribution")
		}
	}
	unauthenticated.Store(true)
	perform(t, chat, chatBody(alias, false), key, 503, "agy login is required")
	unauthenticated.Store(false)
	_ = usage.waitRecord(t, 3*time.Second)
	perform(t, chat, chatBody(alias, false), key, 200, "local answer")
	assertUsage(false)
	streamDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { streamDone <- request(chat, chatBody(alias, true), key) }()
	t.Cleanup(func() {
		select {
		case <-finishStream:
		default:
			close(finishStream)
		}
	})
	select {
	case <-streamStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("plugin stream did not start")
	}
	disabled.Store(true)
	perform(t, chat, chatBody(alias, false), key, 404, "local plugin unavailable")
	close(finishStream)
	select {
	case recorder := <-streamDone:
		result := recorder.Result()
		defer func() { _ = result.Body.Close() }()
		body, errRead := io.ReadAll(result.Body)
		if errRead != nil || result.StatusCode != 200 || !strings.Contains(string(body), "local answer") {
			t.Fatalf("started stream was interrupted: %d %s, error %v", result.StatusCode, body, errRead)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("started stream did not drain")
	}
	disabled.Store(false)
	assertUsage(true)
	perform(t, "/v1beta/models/"+alias+":countTokens?key="+key, `{"contents":[{"parts":[{"text":"test"}]}]}`, "", 200, "totalTokens")
	if calls.Load() != 4 || grants.Load() != 5 || credentialPops.Load() != 0 {
		t.Fatalf("calls=%d grants=%d credential POPs=%d", calls.Load(), grants.Load(), credentialPops.Load())
	}
	usage.assertNoRecord(t, 40*time.Millisecond)
	fail.Store(true)
	if result := request(chat, chatBody(alias, false), key); result.Code != 429 || result.Header().Get("Retry-After") != "7" {
		t.Fatalf("limit status/header = %d/%q, body %s", result.Code, result.Header().Get("Retry-After"), result.Body.String())
	}
	fail.Store(false)
	perform(t, chat, chatBody(alias, false), "restricted-key", 403, "no model access")
	disabled.Store(true)
	perform(t, chat, chatBody(alias, false), key, 404, "local plugin unavailable")
	disabled.Store(false)
	invalidate := func() { setHostSnapshotForTest(host, false, record) }
	onGrant.Store(&invalidate)
	perform(t, chat, chatBody(alias, false), key, 503, "unavailable")
	onGrant.Store(nil)
	setHostSnapshotForTest(host, true, record)
	host.runtimeConfig.Payload.Override[0].Params["model"] = "different-model"
	perform(t, chat, chatBody(alias, false), key, 400, "changes the model authorized by Home")
	delete(host.runtimeConfig.Payload.Override[0].Params, "model")
	perform(t, chat, chatBody(model, false), key, 404, "credential route checked")
	oldHome.Store(true)
	perform(t, chat, chatBody(alias, false), key, 404, "credential route checked")
	if calls.Load() != 5 || credentialPops.Load() != 2 {
		t.Fatalf("calls=%d credential POPs=%d", calls.Load(), credentialPops.Load())
	}
	oldHome.Store(false)
	credential.Store(true)
	perform(t, chat, chatBody(alias, false), key, 404, "credential route checked")
	credential.Store(false)
	staleConfig.Store(true)
	perform(t, chat, chatBody(alias, false), key, 503, "unavailable")
	staleConfig.Store(false)

	preferBeta.Store(true)
	perform(t, chat, chatBody(alias, false), key, 503, "unavailable")
	beta := record
	beta.id, beta.path = "beta", ""
	beta = normalizeTestCapabilityRecord(beta)
	beta.loaded = &loadedPlugin{id: beta.id, path: beta.path, version: beta.version, configYAML: []byte(pluginYAML)}
	var betaCalls int
	betaExecutor := *executor
	betaExecutor.execute = func(_ context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
		checkRequest(req)
		betaCalls++
		return pluginapi.ExecutorResponse{Payload: []byte(candidate), Attribution: attr}, nil
	}
	beta.plugin.Capabilities.Executor = &betaExecutor
	host.loaded[beta.id] = beta.loaded
	setHostSnapshotForTest(host, true, beta, record)
	perform(t, chat, chatBody(alias, false), key, 200, "local answer")
	if betaCalls != 1 {
		t.Fatalf("beta calls = %d", betaCalls)
	}
	preferBeta.Store(false)

	const geminiFields = `"contents":[{"parts":[{"text":"test"}]}],"generationConfig":{"stopSequences":["client-stop"]}`
	const geminiBody = `{` + geminiFields + `}`
	const wrapped = `{"request":{"model":"client-model",` + geminiFields + `}}`
	const withRequestMetadata = `{"request":{"metadata":{"origin":"client"}},` + geminiFields + `}`
	for _, tc := range []struct {
		action string
		want   string
	}{
		{"generateContent", "local answer"},
		{"streamGenerateContent?alt=sse", "local answer"},
		{"countTokens", "totalTokens"},
	} {
		t.Run(tc.action, func(t *testing.T) {
			path := "/v1beta/models/" + alias + "(high):" + tc.action
			perform(t, path, geminiBody, key, 200, tc.want)
			perform(t, path, wrapped, key, 200, tc.want)
			perform(t, path, withRequestMetadata, key, 200, tc.want)
		})
	}
	host.runtimeConfig.Payload.Filter = []config.PayloadFilterRule{{
		Models: []config.PayloadModelRule{{Name: model, Protocol: "gemini"}}, Params: []string{"model"},
	}}
	perform(t, "/v1beta/models/"+alias+"(high):countTokens?original=1", `{"model":"mismatched-original",`+geminiBody[1:], key, 200, "totalTokens")
	host.runtimeConfig.Payload.Filter = nil
	host.runtimeConfig.Payload.Override[0].Params["model"] = "different-model"
	perform(t, "/v1beta/models/"+alias+":generateContent", wrapped, key, 400, "changes the model authorized by Home")
	delete(host.runtimeConfig.Payload.Override[0].Params, "model")

	manager.SetConfig(&config.Config{})
	setHostSnapshotForTest(host, true, record)
	reg := registry.GetGlobalRegistry()
	host.RegisterModels(t.Context(), reg)
	host.RegisterExecutors(manager, reg)
	t.Cleanup(func() { reg.UnregisterClient(pluginExecutorModelClientID(record.id, pluginID)) })
	listing := httptest.NewRecorder()
	router.ServeHTTP(listing, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	models := make(map[string]bool)
	for _, entry := range ojson.NewDoc(listing.Body.Bytes()).Get("data").Array() {
		models[entry.Get("id").String()] = true
	}
	for _, standalone := range []string{model, "gemini-2.5-pro", "acp/native"} {
		if !models[standalone] {
			t.Fatalf("standalone catalog omitted %q: %s", standalone, listing.Body.String())
		}
		perform(t, chat, chatBody(standalone, false), key, 200, "local answer")
	}
}
