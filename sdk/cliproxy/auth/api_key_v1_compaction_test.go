package auth

import (
	"encoding/json"
	"strings"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestUseV1CompactionCapabilityFollowsSelectedAlias(t *testing.T) {
	const upstream = "shared-upstream"
	for _, tc := range []struct {
		name     string
		provider string
		aliases  [2]string
	}{
		{name: "codex", provider: "codex", aliases: [2]string{"codex-v1", "codex-v2"}},
		{name: "openai compatibility", provider: "compat", aliases: [2]string{"compat-v1", "compat-v2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			var auth *Auth
			modelsCodex := []internalconfig.CodexModel{
				{Name: upstream, Alias: tc.aliases[0], UseV1Compaction: true},
				{Name: upstream, Alias: tc.aliases[1]},
			}
			modelsCompat := []internalconfig.OpenAICompatibilityModel{
				{Name: upstream, Alias: tc.aliases[0], UseV1Compaction: true},
				{Name: upstream, Alias: tc.aliases[1]},
			}
			switch tc.provider {
			case "codex":
				manager.SetConfig(&internalconfig.Config{CodexKey: []internalconfig.CodexKey{{
					APIKey: "codex-test-key", Prefix: "tenant", BaseURL: "https://codex.example.invalid/v1", Models: modelsCodex,
				}}})
				auth = configuredCapabilityTestAuth("codex-v1-compaction", "codex-test-key")
				auth.Provider = "codex"
				auth.Attributes[AttributeSource] = "config:codex[0]"
				auth.Attributes["base_url"] = "https://codex.example.invalid/v1"
			case "compat":
				manager.SetConfig(&internalconfig.Config{OpenAICompatibility: []internalconfig.OpenAICompatibility{{
					Name: "compat", Prefix: "tenant", BaseURL: "https://compat.example.invalid/v1", Models: modelsCompat,
				}}})
				auth = &Auth{
					ID:       "compat-v1-compaction",
					Provider: "openai-compatibility:compat",
					Prefix:   "tenant",
					Attributes: map[string]string{
						AttributeSource: "config:compat[0]",
						"compat_name":   "compat",
						"provider_key":  "openai-compatibility:compat",
					},
				}
			}
			registerCapabilityTestAuth(t, manager, auth)

			requests := make([]cliproxyexecutor.Request, 0, 2)
			for index, want := range []bool{true, false} {
				req := manager.attachResolvedAPIKeyModelInfo(cliproxyexecutor.Request{}, auth, "tenant/"+tc.aliases[index], upstream)
				info, ok := ResolvedModelInfo(req)
				if !ok || info == nil || info.UseV1Compaction != want {
					t.Fatalf("selected alias %q capability = (%+v, %t), want UseV1Compaction=%t", tc.aliases[index], info, ok, want)
				}
				requests = append(requests, req)
			}
			first, ok := ResolvedModelInfo(requests[0])
			if !ok || first == nil || !first.UseV1Compaction {
				t.Fatalf("first attempt capability after resolving sibling = (%+v, %t), want true", first, ok)
			}
		})
	}
}

func TestResolvedModelInfoOmitsV1CompactionFromJSON(t *testing.T) {
	info := registry.ModelInfo{ID: "private-capability", UseV1Compaction: true}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "use-v1-compaction") || strings.Contains(string(data), "UseV1Compaction") {
		t.Fatalf("internal compaction capability leaked to JSON: %s", data)
	}
}
