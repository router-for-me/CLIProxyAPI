package auth

import (
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/openai"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

// End to end: an openai-compatibility model without a thinking block gets the
// fabricated default levels at the capability route, and a requested effort
// must survive both resolution and application verbatim (#5499). The openai
// provider applier is blank-imported so the assertion covers the real write,
// not an unapplied passthrough body.
func TestAttachResolvedAPIKeyModelInfoForwardsEffortForAssumedLevels(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{OpenAICompatibility: []internalconfig.OpenAICompatibility{{
		Name:    "sglang",
		BaseURL: "http://127.0.0.1:12345/v1",
		Models: []internalconfig.OpenAICompatibilityModel{
			{Name: "qwen3.8-27b", Alias: "qwen3.8-27b-sglang"},
		},
	}}})
	auth := &Auth{
		ID:       "auth-assumed-levels",
		Provider: "openai-compatibility:sglang",
		Attributes: map[string]string{
			AttributeAuthKind: AuthKindAPIKey,
			AttributeAPIKey:   "sk-test",
			AttributeSource:   "config:openai-compatibility[0]",
			"compat_name":     "sglang",
			"provider_key":    "openai-compatibility:sglang",
		},
	}
	registerCapabilityTestAuth(t, manager, auth)

	req := manager.attachResolvedAPIKeyModelInfo(cliproxyexecutor.Request{}, auth, "qwen3.8-27b-sglang", "qwen3.8-27b")
	modelInfo, ok := ResolvedAPIKeyModelInfo(req)
	if !ok || modelInfo == nil || modelInfo.Thinking == nil {
		t.Fatalf("resolved model info missing thinking support: %+v", modelInfo)
	}
	if !modelInfo.Thinking.LevelsAssumed {
		t.Fatalf("fabricated levels lost the assumed marker through resolution: %+v", modelInfo.Thinking)
	}

	body := []byte(`{"reasoning_effort":"xhigh"}`)
	source := []byte(`{"reasoning":{"effort":"xhigh"}}`)
	out, err := thinking.ApplyThinkingWithModelInfo(body, source, modelInfo.ID, "openai-response", "openai", auth.Provider, modelInfo)
	if err != nil {
		t.Fatalf("ApplyThinkingWithModelInfo() error = %v", err)
	}
	if got := gjson.GetBytes(out, "reasoning_effort").String(); got != "xhigh" {
		t.Fatalf("reasoning_effort = %q, want verbatim xhigh; body=%s", got, out)
	}
}
