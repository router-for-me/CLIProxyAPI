package cliproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestAntigravityFetchedLimitsPreserveMissingCapabilities(t *testing.T) {
	hints, _ := parseAntigravityModelCapabilityHints([]byte(`{"models":{
		" model-a ":{"maxTokens":250000,"maxOutputTokens":64000,"supportsThinking":true},
		"model-b":{"maxTokens":0,"maxOutputTokens":-1},
		"model-c":{"maxOutputTokens":8192}
	}}`))
	models := []*ModelInfo{
		{ID: "model-a", ContextLength: 200000, MaxCompletionTokens: 32000},
		{ID: "model-b", ContextLength: 100000, MaxCompletionTokens: 4000},
		{ID: "model-c", ContextLength: 100000, MaxCompletionTokens: 4000},
		nil,
	}
	applyAntigravityFetchedModelCapabilities(models, hints)
	if models[0].ContextLength != 250000 || models[0].MaxCompletionTokens != 64000 {
		t.Fatalf("live limits not applied: %+v", models[0])
	}
	if models[0].Thinking != nil || len(models[0].SupportedInputModalities) != 0 || len(models[0].SupportedOutputModalities) != 0 {
		t.Fatalf("limits must not invent efforts or modalities: %+v", models[0])
	}
	if models[1].ContextLength != 100000 || models[1].MaxCompletionTokens != 4000 || models[2].ContextLength != 100000 || models[2].MaxCompletionTokens != 8192 {
		t.Fatal("missing or nonpositive fields replaced existing limits")
	}
	before := *models[0]
	invalid, _ := parseAntigravityModelCapabilityHints([]byte(`invalid`))
	applyAntigravityFetchedModelCapabilities(models, invalid)
	if !reflect.DeepEqual(before, *models[0]) {
		t.Fatal("malformed discovery changed model")
	}
}

func TestAntigravityAsyncLimitsResolveAccountPrefixAndAlias(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":{"upstream-model":{"maxTokens":250000,"maxOutputTokens":64000}}}`))
	}))
	t.Cleanup(server.Close)
	svc := &Service{cfg: &config.Config{OAuthModelAlias: map[string][]config.OAuthModelAlias{
		"antigravity": {{Name: "upstream-model", Alias: "alias"}},
	}}}
	auth := &coreauth.Auth{ID: "ag-live-limits-test", Provider: "antigravity", Prefix: "personal-ag",
		Attributes: map[string]string{"base_url": server.URL}, Metadata: map[string]any{"access_token": "test-token"}}
	registry := GlobalModelRegistry()
	registry.RegisterClient(auth.ID, "antigravity", []*ModelInfo{{ID: "personal-ag/alias", ContextLength: 200000, MaxCompletionTokens: 32000}})
	registry.RegisterClient("ag-other-account-limits-test", "antigravity", []*ModelInfo{{ID: "work-ag/alias", ContextLength: 100000, MaxCompletionTokens: 8000}})
	t.Cleanup(func() { registry.UnregisterClient(auth.ID); registry.UnregisterClient("ag-other-account-limits-test") })
	svc.asyncProbeAntigravityCapabilities(context.Background(), auth, "antigravity")
	svc.WaitAntigravityProbes()
	personal := registry.GetModelsForClient(auth.ID)
	work := registry.GetModelsForClient("ag-other-account-limits-test")
	if len(personal) != 1 || personal[0].ContextLength != 250000 || personal[0].MaxCompletionTokens != 64000 {
		t.Fatalf("prefixed alias did not receive live limits: %+v", personal)
	}
	if len(work) != 1 || work[0].ContextLength != 100000 || work[0].MaxCompletionTokens != 8000 {
		t.Fatalf("unrelated account changed: %+v", work)
	}
}

func TestAntigravityCapabilityCloneRetainsIndependentLimits(t *testing.T) {
	original := antigravityModelCapabilityHints{ModelLimits: map[string]antigravityModelLimits{"model": {ContextLength: 250000, MaxCompletionTokens: 64000}}}
	cloned := original.clone()
	if !reflect.DeepEqual(original, cloned) {
		t.Fatalf("clone lost limits: %+v", cloned)
	}
	cloned.ModelLimits["model"] = antigravityModelLimits{ContextLength: 1}
	if original.ModelLimits["model"].ContextLength != 250000 {
		t.Fatal("clone shares mutable limits")
	}
}
