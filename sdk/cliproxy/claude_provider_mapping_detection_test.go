package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

// 当 claude base_url 指向 MiniMax Anthropic 端点时，provider 应识别为 minimax
func TestRegisterModelsForAuth_ClaudeBaseURL_MiniMaxAnthropic_ProviderMinimax(t *testing.T) {
	s := &Service{}
	a := &coreauth.Auth{
		ID:       "claude:test:minimax-anthropic:provider",
		Provider: "claude",
		Attributes: map[string]string{
			"base_url": "https://api.minimaxi.com/anthropic",
		},
	}
	s.registerModelsForAuth(a)
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(a.ID) })

	provs := util.GetProviderName("MiniMax-M2")
	if len(provs) == 0 || provs[0] != "claude" {
		t.Fatalf("expected provider starts with claude for MiniMax-M2, got %#v", provs)
	}
}

// 当 claude base_url 指向 Zhipu Anthropic 端点时，provider 应识别为 zhipu
func TestRegisterModelsForAuth_ClaudeBaseURL_ZhipuAnthropic_ProviderZhipu(t *testing.T) {
	s := &Service{}
	a := &coreauth.Auth{
		ID:       "claude:test:zhipu-anthropic:provider",
		Provider: "claude",
		Attributes: map[string]string{
			"base_url": "https://open.bigmodel.cn/api/anthropic",
		},
	}
	s.registerModelsForAuth(a)
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(a.ID) })

	provs := util.GetProviderName("glm-4.6")
	if len(provs) == 0 || provs[0] != "claude" {
		t.Fatalf("expected provider starts with claude for glm-4.6, got %#v", provs)
	}
}
