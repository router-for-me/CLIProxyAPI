// Package cerebras implements thinking configuration for Cerebras models.
//
// Cerebras is largely OpenAI-compatible, but thinking/reasoning knobs vary by model family.
// In particular, Z.ai GLM models (e.g. "zai-glm-4.7") do NOT accept "reasoning_effort"
// and instead use "disable_reasoning".
package cerebras

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	openaiapplier "github.com/router-for-me/CLIProxyAPI/v6/internal/thinking/provider/openai"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Applier implements thinking.ProviderApplier for Cerebras models.
//
// Cerebras-specific behavior:
//   - GLM family: strip reasoning_effort; optionally map "none" -> disable_reasoning=true
//   - Other models: treat as OpenAI-compatible (pass reasoning_effort through)
type Applier struct{}

var _ thinking.ProviderApplier = (*Applier)(nil)

var openAICompatApplier = openaiapplier.NewApplier()

// NewApplier creates a new Cerebras thinking applier.
func NewApplier() *Applier {
	return &Applier{}
}

func init() {
	thinking.RegisterProvider("cerebras", NewApplier())
}

func (a *Applier) Apply(body []byte, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) ([]byte, error) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		body = []byte(`{}`)
	}

	model := ""
	if modelInfo != nil {
		model = strings.TrimSpace(modelInfo.ID)
	}
	if model == "" {
		model = strings.TrimSpace(gjson.GetBytes(body, "model").String())
	}
	model = strings.ToLower(model)
	if idx := strings.LastIndex(model, "/"); idx >= 0 {
		model = strings.TrimSpace(model[idx+1:])
	}

	// GLM: Cerebras expects "disable_reasoning" and rejects "reasoning_effort".
	if strings.HasPrefix(model, "zai-glm") {
		result := thinking.StripThinkingConfig(body, "openai")
		if config.Mode == thinking.ModeNone && !gjson.GetBytes(result, "disable_reasoning").Exists() {
			result, _ = sjson.SetBytes(result, "disable_reasoning", true)
		}
		return result, nil
	}

	// Default: behave like OpenAI-compatible provider.
	return openAICompatApplier.Apply(body, config, modelInfo)
}
