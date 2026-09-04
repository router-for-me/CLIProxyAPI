package helps

import (
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/antigravity"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/claude"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/codex"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/gemini"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/interactions"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/kimi"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/openai"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/xai"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// ApplyThinking applies the configured thinking policies for an executor request.
// It threads the opt-in effort-mapping rules from cfg and the client-requested
// model alias from opts into the thinking pipeline.
func ApplyThinking(cfg *config.Config, opts cliproxyexecutor.Options, body []byte, model, fromFormat, toFormat, providerKey string) ([]byte, error) {
	return thinking.ApplyThinkingWithOptions(body, model, fromFormat, toFormat, providerKey, buildThinkingApplyOptions(cfg, opts, model))
}

// buildThinkingApplyOptions translates the opt-in config thinking policy and the
// client-requested model alias into thinking.ApplyOptions. It is shared by every
// executor thinking entry point so effort mapping reaches all provider paths.
func buildThinkingApplyOptions(cfg *config.Config, opts cliproxyexecutor.Options, model string) thinking.ApplyOptions {
	options := thinking.ApplyOptions{RequestedModel: PayloadRequestedModel(opts, model)}
	if cfg != nil && len(cfg.Thinking.EffortMapping) > 0 {
		options.EffortMapping = make([]thinking.EffortMappingRule, len(cfg.Thinking.EffortMapping))
		for i, rule := range cfg.Thinking.EffortMapping {
			options.EffortMapping[i] = thinking.EffortMappingRule{
				From:           rule.From,
				To:             rule.To,
				SourceProtocol: rule.SourceProtocol,
				TargetProtocol: rule.TargetProtocol,
				TargetProvider: rule.TargetProvider,
				Models:         append([]string(nil), rule.Models...),
			}
		}
	}
	return options
}
