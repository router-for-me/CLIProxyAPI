package helps

import (
	"context"
	"strings"

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

const minClaudeThinkingBudget int64 = 1024

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
		"request.generation_config.maxOutputTokens",
		"request.generation_config.max_output_tokens",
	} {
		value := doc.Get(path)
		if value.Exists() && value.Type == ojson.Number && value.Int() > 0 && value.Int() < effectiveMaxOutputTokens {
			effectiveMaxOutputTokens = value.Int()
		}
	}
	CanonicalizeAntigravityGenerationConfigDoc(doc)
	_ = doc.Delete("request.generationConfig.max_output_tokens")
	_ = doc.Set("request.generationConfig.maxOutputTokens", effectiveMaxOutputTokens)
	if !isClaudeModel {
		return
	}
	for _, budgetPath := range []string{
		"request.generationConfig.thinkingConfig.thinkingBudget",
		"request.generationConfig.thinkingConfig.thinking_budget",
		"request.generationConfig.thinking_config.thinkingBudget",
		"request.generationConfig.thinking_config.thinking_budget",
	} {
		budget := doc.Get(budgetPath)
		if !budget.Exists() || budget.Type != ojson.Number {
			continue
		}
		budgetVal := budget.Int()
		if budgetVal < 0 {
			continue
		}
		if budgetVal >= effectiveMaxOutputTokens {
			budgetVal = effectiveMaxOutputTokens - 1
		}
		if budgetVal < minClaudeThinkingBudget {
			_ = doc.Delete("request.generationConfig.thinkingConfig")
			_ = doc.Delete("request.generationConfig.thinking_config")
			return
		}
		if budgetVal != budget.Int() {
			_ = doc.Set(budgetPath, budgetVal)
		}
	}
}

// CanonicalizeAntigravityGenerationConfigDoc merges snake_case generation_config
// into camelCase generationConfig and removes generation_config so outbound
// payloads never emit duplicate proto-JSON containers.
func CanonicalizeAntigravityGenerationConfigDoc(doc *ojson.Doc) {
	if doc == nil {
		return
	}
	snakeGen := doc.Get("request.generation_config")
	if !snakeGen.Exists() {
		return
	}
	if snakeGen.IsObject() {
		MergeAntigravityProtoObjectDoc(doc, "request.generationConfig", snakeGen, "maxoutputtokens")
	}
	_ = doc.Delete("request.generation_config")
}

// MergeAntigravityProtoObjectDoc merges non-conflicting fields from src into the
// JSON object at dstPath, matching keys by proto-JSON canonical form (case- and
// underscore-insensitive) so existing destination fields take precedence without
// introducing duplicate proto-JSON aliases.
func MergeAntigravityProtoObjectDoc(doc *ojson.Doc, dstPath string, src ojson.Result, skipNormalizedKeys ...string) {
	if doc == nil || !src.IsObject() {
		return
	}
	dst := doc.Get(dstPath)
	if !dst.IsObject() {
		if len(skipNormalizedKeys) == 0 {
			_ = doc.SetRaw(dstPath, src.Raw)
			return
		}
		if dst.Exists() {
			_ = doc.Delete(dstPath)
		}
	}
	existing := make(map[string]struct{}, len(skipNormalizedKeys))
	for _, skip := range skipNormalizedKeys {
		existing[normalizeAntigravityProtoKey(skip)] = struct{}{}
	}
	if dst.IsObject() {
		dst.ForEach(func(key, _ ojson.Result) bool {
			existing[normalizeAntigravityProtoKey(key.String())] = struct{}{}
			return true
		})
	}
	src.ForEach(func(key, value ojson.Result) bool {
		k := key.String()
		norm := normalizeAntigravityProtoKey(k)
		if _, exists := existing[norm]; exists {
			return true
		}
		_ = doc.SetRaw(dstPath+"."+escapeAntigravityOJSONKey(k), value.Raw)
		existing[norm] = struct{}{}
		return true
	})
}

func normalizeAntigravityProtoKey(key string) string {
	var b strings.Builder
	b.Grow(len(key))
	for i := 0; i < len(key); i++ {
		c := key[i]
		if c == '_' {
			continue
		}
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b.WriteByte(c)
	}
	return b.String()
}

func escapeAntigravityOJSONKey(key string) string {
	var b strings.Builder
	b.Grow(len(key))
	for i := 0; i < len(key); i++ {
		c := key[i]
		if (i == 0 && (c == ':' || c == '[' || c == '{' || c == '!')) ||
			c == '.' || c == '\\' || c == '|' || c == '#' || c == '*' || c == '?' || c == '@' {
			b.WriteByte('\\')
		}
		b.WriteByte(c)
	}
	return b.String()
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
