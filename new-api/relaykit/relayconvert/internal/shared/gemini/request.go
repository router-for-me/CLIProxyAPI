package gemini

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/internal/convdiag"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/reasoning"
	"github.com/QuantumNous/new-api/relaykit/types"
)

var SupportedMimeTypes = map[string]bool{
	"application/pdf": true,
	"audio/mpeg":      true,
	"audio/mp3":       true,
	"audio/wav":       true,
	"image/png":       true,
	"image/jpeg":      true,
	"image/jpg":       true,
	"image/webp":      true,
	"image/heic":      true,
	"image/heif":      true,
	"text/plain":      true,
	"video/mov":       true,
	"video/mpeg":      true,
	"video/mp4":       true,
	"video/mpg":       true,
	"video/avi":       true,
	"video/wmv":       true,
	"video/mpegps":    true,
	"video/flv":       true,
}

var SafetySettingCategories = []string{
	"HARM_CATEGORY_HARASSMENT",
	"HARM_CATEGORY_HATE_SPEECH",
	"HARM_CATEGORY_SEXUALLY_EXPLICIT",
	"HARM_CATEGORY_DANGEROUS_CONTENT",
}

const ThoughtSignatureBypassValue = "context_engineering_is_the_way_to_go"

func ShouldAttachThoughtSignature(opts *convmeta.Options) bool {
	return opts != nil && opts.Gemini.FunctionCallThoughtSignatureEnabled
}

func AttachThoughtSignatureBypass(opts *convmeta.Options, part *dto.GeminiPart) bool {
	if part == nil || len(part.ThoughtSignature) > 0 || !ShouldAttachThoughtSignature(opts) {
		return false
	}
	part.ThoughtSignature = []byte(strconv.Quote(ThoughtSignatureBypassValue))
	return true
}

func AttachFunctionCallThoughtSignature(opts *convmeta.Options, part *dto.GeminiPart) bool {
	if part == nil || !HasFunctionCallContent(part.FunctionCall) {
		return false
	}
	return AttachThoughtSignatureBypass(opts, part)
}

func AttachFirstTextThoughtSignature(opts *convmeta.Options, parts []dto.GeminiPart) bool {
	if !ShouldAttachThoughtSignature(opts) {
		return false
	}
	for i := range parts {
		if parts[i].Text != "" && len(parts[i].ThoughtSignature) == 0 {
			parts[i].ThoughtSignature = []byte(strconv.Quote(ThoughtSignatureBypassValue))
			return true
		}
	}
	return false
}

// ApplyThinkingConfig resolves every reasoning control that can reach a
// Gemini request (provider-native thinkingConfig, generic OpenAI fields, and
// host model-name aliases) into the target model's dialect. Precedence is
// model name, then provider-native config, then generic fields; each override
// and each capability coercion is reported through convdiag on ctx.
func ApplyThinkingConfig(ctx context.Context, geminiRequest *dto.GeminiChatRequest, info convmeta.Meta, oaiRequest ...dto.GeneralOpenAIRequest) error {
	opts := convmeta.OptionsOf(info)
	if geminiRequest == nil {
		return nil
	}

	modelName := convmeta.UpstreamModelName(info)
	var source reasoning.Intent
	crossProtocol := len(oaiRequest) > 0
	if len(oaiRequest) > 0 {
		if modelName == "" {
			modelName = oaiRequest[0].Model
		}
		var diagnostics []types.ConversionDiagnostic
		var err error
		source, diagnostics, err = reasoning.FromOpenAIChat(&oaiRequest[0])
		if err != nil {
			return err
		}
		convdiag.Add(ctx, diagnostics...)
	}

	baseModel := modelName
	suffix := reasoning.IntentFromState(convmeta.ReasoningStateOf(info))
	preserveSuffix := opts.ShouldPreserveThinkingSuffix(modelName)
	if info != nil && opts.ShouldPreserveThinkingSuffix(info.GetOriginModelName()) {
		preserveSuffix = true
	}
	if preserveSuffix {
		suffix = reasoning.Intent{}
	}
	// Native Gemini requests already use the target protocol. Without a host
	// modifier, read portable effort metadata without running the capability
	// renderer or rewriting provider-native controls.
	if !crossProtocol && suffix.IsEmpty() {
		if info != nil {
			effort := ""
			if config := geminiRequest.GenerationConfig.ThinkingConfig; config != nil {
				effort = config.ThinkingLevel
				// Gemini accepts thinkingLevel case-insensitively; record the
				// canonical effort so logs match other protocols. Unknown values
				// stay as sent because this path does not validate.
				if canonical, err := reasoning.ParseEffort(effort); err == nil {
					effort = string(canonical)
				}
				if effort == "" && config.ThinkingBudget != nil {
					effort = string(reasoning.EffortFromBudget(*config.ThinkingBudget))
				}
			}
			info.SetReasoningEffort(effort)
		}
		return nil
	}
	// Rewrite the provider-native config into the target model's dialect
	// first, so a budget on Gemini 3 or a level on Gemini 2.5 is converted
	// rather than rejected and later comparisons see what will be sent.
	nativeEffort, diagnostics, err := reasoning.NormalizeGeminiThinkingConfig(baseModel, &geminiRequest.GenerationConfig)
	if err != nil {
		return err
	}
	convdiag.Add(ctx, diagnostics...)
	native, diagnostics, err := reasoning.FromGemini(geminiRequest)
	if err != nil {
		return err
	}
	convdiag.Add(ctx, diagnostics...)
	if native.HasStrength() && source.HasStrength() {
		// Native Gemini configuration is the lossless representation and wins
		// over the generic OpenAI fields. Only a generic effort or budget can
		// disagree with it; a bare enable states no strength to compare.
		if source.Effort != "" || source.BudgetTokens != nil {
			equivalent, compareErr := reasoning.EquivalentGeminiStrength(baseModel, native, source)
			if compareErr != nil {
				return compareErr
			}
			if !equivalent {
				convdiag.Add(ctx, types.ConversionDiagnostic{
					Code:     "native_overrode_standard",
					Path:     "generationConfig.thinkingConfig",
					Message:  fmt.Sprintf("model %q: Gemini thinking_config effort %q overrides the standard reasoning effort %q", modelName, reasoning.EffectiveEffort(native), reasoning.EffectiveEffort(source)),
					Severity: types.ConversionDiagnosticWarning,
				})
			}
		}
		if native.IncludeThoughts == nil {
			native.IncludeThoughts = source.IncludeThoughts
		}
		source = reasoning.Intent{}
	}
	explicit, diagnostics, err := reasoning.MergeExplicit(native, source, modelName)
	if err != nil {
		return err
	}
	convdiag.Add(ctx, diagnostics...)
	if explicit.HasStrength() && suffix.HasStrength() {
		equivalent, compareErr := reasoning.EquivalentGeminiStrength(baseModel, explicit, suffix)
		if compareErr != nil {
			return compareErr
		}
		if equivalent {
			if explicit.IncludeThoughts == nil {
				explicit.IncludeThoughts = suffix.IncludeThoughts
			}
			suffix = reasoning.Intent{}
		}
	}
	requested, diagnostics, err := reasoning.MergeExplicitAndSuffix(explicit, suffix, modelName)
	if err != nil {
		return err
	}
	convdiag.Add(ctx, diagnostics...)
	requested = reasoning.ResolveGeminiEnabledDefault(baseModel, requested, geminiRequest.GenerationConfig.MaxOutputTokens)

	if native.HasStrength() && !suffix.HasStrength() {
		// The normalized native config is what goes upstream; only portable
		// visibility metadata is taken from the standard representation.
		if explicit.IncludeThoughts != nil {
			geminiRequest.GenerationConfig.ThinkingConfig.IncludeThoughts = explicit.IncludeThoughts
		}
		if info != nil && nativeEffort != "" {
			info.SetReasoningEffort(string(nativeEffort))
		}
		return nil
	}
	if requested.IsEmpty() {
		return nil
	}
	rendered, err := reasoning.RenderGemini(
		baseModel,
		requested,
		geminiRequest.GenerationConfig.MaxOutputTokens,
		opts.Gemini.ThinkingAdapterBudgetTokensPercentage,
	)
	if err != nil {
		return err
	}
	convdiag.Add(ctx, rendered.Diagnostics...)
	geminiRequest.GenerationConfig.ThinkingConfig = rendered.Config
	if info != nil && rendered.EffectiveEffort != "" {
		info.SetReasoningEffort(string(rendered.EffectiveEffort))
	}
	return nil
}

func ParseStopSequences(stop any) []string {
	if stop == nil {
		return nil
	}

	switch v := stop.(type) {
	case string:
		if v != "" {
			return []string{v}
		}
	case []string:
		return v
	case []interface{}:
		sequences := make([]string, 0, len(v))
		for _, item := range v {
			if str, ok := item.(string); ok && str != "" {
				sequences = append(sequences, str)
			}
		}
		return sequences
	}
	return nil
}

func HasFunctionCallContent(call *dto.FunctionCall) bool {
	if call == nil {
		return false
	}
	if strings.TrimSpace(call.FunctionName) != "" {
		return true
	}

	switch v := call.Arguments.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(v) != ""
	case map[string]interface{}:
		return len(v) > 0
	case []interface{}:
		return len(v) > 0
	default:
		return true
	}
}

func SupportedMimeTypesList() []string {
	keys := make([]string, 0, len(SupportedMimeTypes))
	for key := range SupportedMimeTypes {
		keys = append(keys, key)
	}
	return keys
}
