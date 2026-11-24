package helps

import (
	"context"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// AntigravityRequestModel holds the selected upstream model and the explicit
// thinking level used for the selection on this attempt.
type AntigravityRequestModel struct {
	cliproxyauth.AntigravityModelSelection
	Level thinking.ThinkingLevel
}

// SelectAntigravityRequestModel reads caller intent before translation or static
// thinking validation can insert defaults or clamp an explicit level.
func SelectAntigravityRequestModel(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, model, baseModel, from string, source []byte, countTokens bool) AntigravityRequestModel {
	if from == "" {
		from = "antigravity"
	}
	level := thinking.RequestedAntigravityThinkingLevel(source, from, model)
	selection := cliproxyauth.AntigravityModelSelection{ID: baseModel}
	if level != "" || !countTokens {
		selection = cliproxyauth.SelectAntigravityModel(ctx, auth, baseModel, string(level), cfg)
	}
	return AntigravityRequestModel{AntigravityModelSelection: selection, Level: level}
}

// TranslationModelInfo uses the selected snapshot's explicit search capability
// when changing models. A missing capability is not a hint to consult the registry.
func (model AntigravityRequestModel) TranslationModelInfo(req cliproxyexecutor.Request, baseModel string, from sdktranslator.Format) *registry.ModelInfo {
	if model.ID == baseModel {
		if from == sdktranslator.FormatClaude {
			// Keep the legacy Claude capability lookup when no variant was selected.
			return nil
		}
		info, _ := cliproxyauth.ResolvedModelInfo(req)
		return info
	}
	return &registry.ModelInfo{ID: model.ID, NativeCapabilities: &registry.NativeCapabilities{WebSearch: &model.WebSearch}}
}

// CapAntigravityMaxOutputTokensDoc preserves a smaller caller limit and keeps
// Claude thinking budgets below the effective output limit.
func CapAntigravityMaxOutputTokensDoc(doc *ojson.Doc, limit int, isClaudeModel bool) {
	if doc == nil || limit <= 0 {
		return
	}
	effectiveMaxOutputTokens := int64(limit)
	for _, path := range []string{
		"request.generationConfig.maxOutputTokens",
		"request.generationConfig.max_output_tokens",
	} {
		value := doc.Get(path)
		if value.Exists() && value.Type == ojson.Number && value.Int() > 0 && value.Int() < effectiveMaxOutputTokens {
			effectiveMaxOutputTokens = value.Int()
		}
	}
	_ = doc.Delete("request.generationConfig.max_output_tokens")
	_ = doc.Set("request.generationConfig.maxOutputTokens", effectiveMaxOutputTokens)
	if !isClaudeModel {
		return
	}
	for _, budgetPath := range []string{
		"request.generationConfig.thinkingConfig.thinkingBudget",
		"request.generationConfig.thinkingConfig.thinking_budget",
	} {
		budget := doc.Get(budgetPath)
		if budget.Exists() && budget.Int() >= effectiveMaxOutputTokens {
			_ = doc.Set(budgetPath, effectiveMaxOutputTokens-1)
		}
	}
}

// ApplyAntigravityRequestThinkingDoc formats a selected level with the canonical
// provider applier while leaving unmatched requests on the original path.
func ApplyAntigravityRequestThinkingDoc(doc *ojson.Doc, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, model AntigravityRequestModel) error {
	from := opts.SourceFormat.String()
	if model.MaxOutputTokens == 0 {
		return ApplyRequestThinkingDoc(doc, req, opts, from, "antigravity", "antigravity")
	}
	if from == "" {
		from = "antigravity"
	}
	level := string(model.Level)
	modelInfo := &registry.ModelInfo{ID: model.ID, Thinking: &registry.ThinkingSupport{Levels: []string{level}}}
	originalSource := opts.OriginalRequest
	if len(originalSource) == 0 {
		originalSource = req.Payload
	}
	summary := translatedRequestSummaryConfigDoc(sdktranslator.Default(), doc, req.Payload, originalSource, req.Model, from, "antigravity")
	_ = doc.Delete("request.generationConfig.thinking_config")
	return thinking.ApplyThinkingWithModelInfoAndSummaryDoc(doc, req.Payload, model.ID+"("+level+")", from, "antigravity", "antigravity", modelInfo, summary)
}
