package executor

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestApplyPatchActualExecutors(t *testing.T) {
	cfg := &config.Config{}
	for _, exec := range []coreauth.ProviderExecutor{
		NewOpenAICompatExecutor("arbitrary-plugin-provider", cfg), NewClaudeExecutor(cfg),
		NewGeminiExecutor(cfg), NewGeminiInteractionsExecutor(cfg), NewGeminiVertexExecutor(cfg),
		NewAntigravityExecutor(cfg), NewAIStudioExecutor(cfg, "aistudio", nil), NewDevinExecutor(cfg),
		NewKimiExecutor(cfg), NewCodexExecutor(cfg), NewCodexWebsocketsExecutor(cfg), NewCodexAutoExecutor(cfg),
		NewXAIExecutor(cfg), NewXAIWebsocketsExecutor(cfg), NewXAIAutoExecutor(cfg), NewMetaExecutor(cfg),
	} {
		support, okSupport := exec.(coreauth.ApplyPatchSupport)
		if !okSupport || !support.SupportsApplyPatch() {
			t.Errorf("%T (%s) does not advertise actual support", exec, exec.Identifier())
		}
		if scoped, okScoped := exec.(coreauth.APIKeyConfigExecutor); okScoped {
			bound := scoped.ForAPIKey()
			if bound.Identifier() != exec.Identifier() || !bound.(coreauth.ApplyPatchSupport).SupportsApplyPatch() {
				t.Errorf("%T ForAPIKey lost the actual executor contract", exec)
			}
		}
	}
	for _, exec := range []coreauth.ApplyPatchSupport{&CodexAutoExecutor{}, &XAIAutoExecutor{}} {
		if exec.SupportsApplyPatch() {
			t.Errorf("unconfigured %T advertises support", exec)
		}
	}
}
