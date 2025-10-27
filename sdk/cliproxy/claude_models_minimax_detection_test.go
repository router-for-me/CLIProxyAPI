package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

// 先测后码：当 claude 的 base_url 指向 MiniMax 的 Anthropic 兼容端点时，
// 期望仅注册 MiniMax-M2，且不注册任何 claude-* 模型。
func TestRegisterModelsForAuth_ClaudeBaseURL_MiniMaxAnthropic_RegistersOnlyM2(t *testing.T) {
	s := &Service{}
	a := &coreauth.Auth{
		ID:       "claude:test:minimax-anthropic",
		Provider: "claude",
		Attributes: map[string]string{
			"base_url": "https://api.minimaxi.com/anthropic",
		},
	}

	s.registerModelsForAuth(a)
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(a.ID) })

	models := registry.GetGlobalRegistry().GetAvailableModels("claude")

	var hasM2 bool
	var hasAnyClaude bool
	for _, m := range models {
		id, _ := m["id"].(string)
		if id == "MiniMax-M2" {
			hasM2 = true
		}
		if len(id) >= 6 && id[:6] == "claude" {
			hasAnyClaude = true
		}
	}

	if !hasM2 {
		t.Fatalf("expected MiniMax-M2 to be registered for claude provider when base_url=api.minimaxi.com/anthropic")
	}
	if hasAnyClaude {
		t.Fatalf("expected no claude-* models to be registered in this detection mode")
	}
}
