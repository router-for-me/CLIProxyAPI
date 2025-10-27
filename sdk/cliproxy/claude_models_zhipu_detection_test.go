package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

// 先测后码：当 claude 的 base_url 指向 Zhipu 的 Anthropic 兼容端点时，
// 期望仅注册 glm-4.6，且不注册任何 claude-* 模型。
func TestRegisterModelsForAuth_ClaudeBaseURL_ZhipuAnthropic_RegistersOnlyGLM46(t *testing.T) {
	// 不使用 t.Parallel，避免共享全局注册表的竞态影响

	s := &Service{}
	a := &coreauth.Auth{
		ID:       "claude:test:zhipu-anthropic",
		Provider: "claude",
		Attributes: map[string]string{
			"base_url": "https://open.bigmodel.cn/api/anthropic",
		},
	}

	// 执行注册
	s.registerModelsForAuth(a)
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(a.ID) })

	// 从 Claude handler 视角列出可用模型
	models := registry.GetGlobalRegistry().GetAvailableModels("claude")

	var hasGLM46 bool
	var hasAnyClaude bool
	for _, m := range models {
		id, _ := m["id"].(string)
		if id == "glm-4.6" {
			hasGLM46 = true
		}
		if len(id) >= 6 && id[:6] == "claude" {
			hasAnyClaude = true
		}
	}

	if !hasGLM46 {
		t.Fatalf("expected glm-4.6 to be registered for claude provider when base_url=open.bigmodel.cn/anthropic")
	}
	if hasAnyClaude {
		t.Fatalf("expected no claude-* models to be registered in this detection mode")
	}
}
