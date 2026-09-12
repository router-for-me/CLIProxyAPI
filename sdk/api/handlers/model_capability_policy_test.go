package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestModelCapabilityPolicyRejectsExcessiveOutputAfterProviderSelection(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	const modelID = "shared-capability-policy-model"
	modelRegistry.RegisterClient("policy-factory-client", "factory", []*registry.ModelInfo{{ID: modelID, MaxCompletionTokens: 100}})
	modelRegistry.RegisterClient("policy-cursor-client", "cursor", []*registry.ModelInfo{{ID: modelID, MaxCompletionTokens: 200}})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient("policy-factory-client")
		modelRegistry.UnregisterClient("policy-cursor-client")
	})

	interceptor := (&BaseAPIHandler{}).requestAfterAuthInterceptor(nil, "request-1", "")
	if interceptor == nil {
		t.Fatal("after-auth interceptor is nil; capability policy would be skipped")
	}
	resp := interceptor(context.Background(), coreexecutor.RequestAfterAuthInterceptRequest{
		Provider:     "factory",
		SourceFormat: sdktranslator.FromString("openai-response"),
		Model:        modelID,
		Body:         []byte(`{"model":"shared-capability-policy-model","max_output_tokens":101}`),
	})

	if !resp.Terminate || resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("response = %#v, want terminating 400", resp)
	}
	var body struct {
		Error struct {
			Code  string `json:"code"`
			Param string `json:"param"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp.ResponseBody, &body); err != nil {
		t.Fatalf("decode response body: %v: %s", err, resp.ResponseBody)
	}
	if body.Error.Code != "max_output_tokens_exceeded" || body.Error.Param != "max_output_tokens" {
		t.Fatalf("error = %#v", body.Error)
	}
}

func TestModelCapabilityPolicyRemovesOnlyExplicitlyUnsupportedSamplingParameters(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	const clientID = "policy-unsupported-client"
	const modelID = "factory/policy-unsupported-model"
	modelRegistry.RegisterClient(clientID, "factory", []*registry.ModelInfo{
		{
			ID:                    modelID,
			MaxCompletionTokens:   100,
			UnsupportedParameters: []string{"temperature", "top_p", "tools"},
		},
	})
	t.Cleanup(func() { modelRegistry.UnregisterClient(clientID) })

	interceptor := (&BaseAPIHandler{}).requestAfterAuthInterceptor(nil, "request-2", "")
	original := []byte(`{"model":"factory/policy-unsupported-model","temperature":0.4,"top_p":0.9,"tools":[{"type":"function","function":{"name":"inspect"}}],"input":"hi"}`)
	resp := interceptor(context.Background(), coreexecutor.RequestAfterAuthInterceptRequest{
		Provider:     "factory",
		SourceFormat: sdktranslator.FromString("openai-response"),
		Model:        modelID,
		Body:         original,
	})

	if resp.Terminate {
		t.Fatalf("response unexpectedly terminated: %#v", resp)
	}
	if gjson.GetBytes(resp.Body, "temperature").Exists() || gjson.GetBytes(resp.Body, "top_p").Exists() {
		t.Fatalf("unsupported sampling parameters remain: %s", resp.Body)
	}
	if !gjson.GetBytes(resp.Body, "tools").Exists() {
		t.Fatalf("tool use was stripped: %s", resp.Body)
	}
}

func TestModelCapabilityPolicyPassesUnknownMetadataThroughUnchanged(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	const clientID = "policy-unknown-client"
	const modelID = "unknown/policy-model"
	modelRegistry.RegisterClient(clientID, "unknown", []*registry.ModelInfo{{ID: modelID}})
	t.Cleanup(func() { modelRegistry.UnregisterClient(clientID) })

	interceptor := (&BaseAPIHandler{}).requestAfterAuthInterceptor(nil, "request-3", "")
	original := []byte(`{"model":"unknown/policy-model","temperature":0.4,"max_output_tokens":999999}`)
	resp := interceptor(context.Background(), coreexecutor.RequestAfterAuthInterceptRequest{
		Provider:     "unknown",
		SourceFormat: sdktranslator.FromString("openai-response"),
		Model:        modelID,
		Body:         original,
	})
	if resp.Terminate || len(resp.Body) != 0 {
		t.Fatalf("unknown metadata changed request: %#v", resp)
	}
}

func TestModelCapabilityPolicyUsesRequestedModelForAliasedUpstreamModel(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	const clientID = "policy-alias-client"
	const requestedModel = "factory/policy-alias-model"
	modelRegistry.RegisterClient(clientID, "factory", []*registry.ModelInfo{{ID: requestedModel, MaxCompletionTokens: 100}})
	t.Cleanup(func() { modelRegistry.UnregisterClient(clientID) })

	resp := applyModelCapabilityPolicy(coreexecutor.RequestAfterAuthInterceptRequest{
		Provider:       "factory",
		SourceFormat:   sdktranslator.FromString("openai-response"),
		Model:          "upstream-model",
		RequestedModel: requestedModel,
		Body:           []byte(`{"model":"upstream-model","max_output_tokens":101}`),
	})
	if !resp.Terminate || resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("response = %#v, want terminating 400", resp)
	}
}

func TestModelCapabilityPolicyRunsAfterPluginRequestInterceptor(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	const clientID = "policy-order-client"
	const modelID = "factory/policy-order-model"
	modelRegistry.RegisterClient(clientID, "factory", []*registry.ModelInfo{{ID: modelID, MaxCompletionTokens: 100}})
	t.Cleanup(func() { modelRegistry.UnregisterClient(clientID) })

	handler := &BaseAPIHandler{}
	handler.SetPluginHost(&handlerInterceptorTestHost{
		interceptRequestAfterAuth: func(_ context.Context, req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
			return pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: []byte(`{"max_output_tokens":101}`)}
		},
	})
	resp := handler.requestAfterAuthInterceptor(nil, "request-order", "")(context.Background(), coreexecutor.RequestAfterAuthInterceptRequest{
		Provider:     "factory",
		SourceFormat: sdktranslator.FromString("openai-response"),
		Model:        modelID,
		Body:         []byte(`{"max_output_tokens":50}`),
	})
	if !resp.Terminate || resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("response = %#v, want post-interceptor capability 400", resp)
	}
}

func TestModelCapabilityPolicyRemovesGeminiSamplingFields(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	const clientID = "policy-gemini-client"
	const modelID = "factory/policy-gemini-model"
	modelRegistry.RegisterClient(clientID, "factory", []*registry.ModelInfo{
		{ID: modelID, UnsupportedParameters: []string{"frequency_penalty", "presence_penalty", "stop"}},
	})
	t.Cleanup(func() { modelRegistry.UnregisterClient(clientID) })

	resp := applyModelCapabilityPolicy(coreexecutor.RequestAfterAuthInterceptRequest{
		Provider:     "factory",
		SourceFormat: sdktranslator.FromString("gemini"),
		Model:        modelID,
		Body:         []byte(`{"generationConfig":{"frequencyPenalty":1,"presencePenalty":1,"stopSequences":["stop"]}}`),
	})
	if resp.Terminate || gjson.GetBytes(resp.Body, "generationConfig.frequencyPenalty").Exists() ||
		gjson.GetBytes(resp.Body, "generationConfig.presencePenalty").Exists() || gjson.GetBytes(resp.Body, "generationConfig.stopSequences").Exists() {
		t.Fatalf("Gemini unsupported parameters remain: %s", resp.Body)
	}
}

func TestPluginExecutorRouteAppliesModelCapabilityPolicy(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	const clientID = "policy-plugin-executor-client"
	const modelID = "factory/policy-plugin-executor-model"
	modelRegistry.RegisterClient(clientID, "factory", []*registry.ModelInfo{{ID: modelID, MaxCompletionTokens: 50}})
	t.Cleanup(func() { modelRegistry.UnregisterClient(clientID) })

	req := coreexecutor.Request{Model: modelID, Payload: []byte(`{"model":"factory/policy-plugin-executor-model","max_output_tokens":51}`)}
	opts := coreexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")}
	_, _, errMsg := (&BaseAPIHandler{}).applyRequestInterceptorsAfterPluginExecutorRoute(context.Background(), nil, "factory", "openai-response", modelID, "request-4", req, opts, "")
	if errMsg == nil || errMsg.StatusCode != http.StatusBadRequest {
		t.Fatalf("error = %#v, want capability 400", errMsg)
	}
}

func TestPluginExecutorRouteUsesRegisteredProviderAndValidatesFinalBody(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	const clientID = "policy-plugin-provider-client"
	const modelID = "factory/policy-plugin-provider-model"
	modelRegistry.RegisterClient(clientID, "factory", []*registry.ModelInfo{{ID: modelID, MaxCompletionTokens: 50}})
	t.Cleanup(func() { modelRegistry.UnregisterClient(clientID) })

	host := &handlerDirectExecutorInterceptorHost{
		handlerDirectExecutorRouteHost: handlerDirectExecutorRouteHost{executorProvider: "factory"},
		afterAuthBody:                  []byte(`{"max_output_tokens":51}`),
	}
	handler := &BaseAPIHandler{}
	handler.SetPluginHost(host)
	req := coreexecutor.Request{Model: modelID, Payload: []byte(`{"max_output_tokens":10}`)}
	opts := coreexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")}
	_, _, errMsg := handler.applyRequestInterceptorsAfterPluginExecutorRoute(context.Background(), host, "factory-plugin", "openai-response", modelID, "request-provider", req, opts, "")
	if errMsg == nil || errMsg.StatusCode != http.StatusBadRequest {
		t.Fatalf("error = %#v, want post-interceptor capability 400", errMsg)
	}
}

func TestModelCapabilityPolicyRejectsUnsupportedReasoningEffort(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	const clientID = "policy-reasoning-client"
	const modelID = "factory/policy-reasoning-model"
	modelRegistry.RegisterClient(clientID, "factory", []*registry.ModelInfo{{
		ID:       modelID,
		Thinking: &registry.ThinkingSupport{Levels: []string{"low", "high"}},
	}})
	t.Cleanup(func() { modelRegistry.UnregisterClient(clientID) })

	interceptor := (&BaseAPIHandler{}).requestAfterAuthInterceptor(nil, "request-5", "")
	resp := interceptor(context.Background(), coreexecutor.RequestAfterAuthInterceptRequest{
		Provider:     "factory",
		SourceFormat: sdktranslator.FromString("openai-response"),
		Model:        modelID,
		Body:         []byte(`{"model":"factory/policy-reasoning-model","reasoning":{"effort":"medium"}}`),
	})
	if !resp.Terminate || resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("response = %#v, want terminating 400", resp)
	}
	if gjson.GetBytes(resp.ResponseBody, "error.code").String() != "unsupported_reasoning_effort" || gjson.GetBytes(resp.ResponseBody, "error.param").String() != "reasoning.effort" {
		t.Fatalf("response body = %s", resp.ResponseBody)
	}

	valid := interceptor(context.Background(), coreexecutor.RequestAfterAuthInterceptRequest{
		Provider:     "factory",
		SourceFormat: sdktranslator.FromString("openai-response"),
		Model:        modelID,
		Body:         []byte(`{"model":"factory/policy-reasoning-model","reasoning":{"effort":"HIGH"}}`),
	})
	if valid.Terminate {
		t.Fatalf("valid reasoning effort rejected: %#v", valid)
	}
}
