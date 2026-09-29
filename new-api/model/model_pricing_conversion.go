package model

import (
	"errors"
	"maps"
	"math"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	hostreasoning "github.com/QuantumNous/new-api/setting/reasoning"
	"github.com/shopspring/decimal"
)

// ModelPricingConversion is a preview only. Saving still requires a versioned
// ModelPricingChange through the existing pricing transaction.
type ModelPricingConversion struct {
	ModelPricingDescription
	Expression        string `json:"expression,omitempty"`
	UnsupportedReason string `json:"unsupported_reason,omitempty"`
}

// ModelPricingDescription is shared by previews, conversions and snapshot
// entries. It describes effective prices without adding persisted settings.
type ModelPricingDescription struct {
	Effective      PricingValues        `json:"effective,omitempty"`
	CacheWriteMode CacheWriteMode       `json:"cache_write_mode,omitempty"`
	BillingDetails LegacyBillingDetails `json:"billing_details"`
}

func legacyInputPricePerMillion(ratio float64) (decimal.Decimal, error) {
	if common.QuotaPerUnit <= 0 || math.IsInf(common.QuotaPerUnit, 0) || math.IsNaN(common.QuotaPerUnit) {
		return decimal.Zero, errors.New("invalid quota unit")
	}
	if ratio < 0 || math.IsInf(ratio, 0) || math.IsNaN(ratio) {
		return decimal.Zero, errors.New("input ratio must be finite and non-negative")
	}
	return decimal.NewFromFloat(ratio).Mul(decimal.NewFromInt(1_000_000)).Div(decimal.NewFromFloat(common.QuotaPerUnit)), nil
}

type LegacyPricingRule struct {
	Condition  string  `json:"condition"`
	Multiplier float64 `json:"multiplier"`
}

// LegacyBillingDetails is display-only. Absolute audio prices cannot always be
// represented by a ratio: Gemini audio can be billable with a zero text price.
type LegacyBillingDetails struct {
	AudioInputPrice        *float64            `json:"audio_input_price,omitempty"`
	AudioOutputPrice       *float64            `json:"audio_output_price,omitempty"`
	ImageCount             bool                `json:"image_count,omitempty"`
	RequestRules           []LegacyPricingRule `json:"request_rules,omitempty"`
	AudioTextBranches      bool                `json:"audio_text_branches,omitempty"`
	InvalidAudioPrice      bool                `json:"-"`
	ConflictingAudioPrices bool                `json:"-"`
}

func ResolveLegacyBillingDetails(name string, effective, configured PricingValues) LegacyBillingDetails {
	details := LegacyBillingDetails{}
	if effective["billing_setting.billing_mode"] == billing_setting.BillingModeTieredExpr {
		return details
	}
	if _, fixed := effective["ModelPrice"]; fixed {
		details.ImageCount = common.IsImageGenerationModel(name)
		details.RequestRules = legacyDallePricingRules(name)
		return details
	}
	ratio, priced := effective["ModelRatio"].(float64)
	if !priced {
		return details
	}
	base, err := legacyInputPricePerMillion(ratio)
	if err != nil {
		return details
	}
	if price := operation_setting.GetGeminiInputAudioPricePerMillionTokens(name); price > 0 {
		details.AudioInputPrice = &price
		// Native Gemini settles with its dedicated input price; compatible
		// chat enters the audio-ratio path when those ratios are configured.
		// Do not silently replace one price with the other during migration.
		audio, hasInput := effective["AudioRatio"].(float64)
		output, hasOutput := effective["AudioCompletionRatio"].(float64)
		if hasInput || hasOutput {
			if !hasInput {
				audio = 1
			}
			if !hasOutput {
				output = 1
			}
			audioPrice := base.Mul(decimal.NewFromFloat(audio))
			textOutput := base.Mul(decimal.NewFromFloat(effective["CompletionRatio"].(float64)))
			details.ConflictingAudioPrices = !audioPrice.Equal(decimal.NewFromFloat(price)) || !audioPrice.Mul(decimal.NewFromFloat(output)).Equal(textOutput)
		}
		return details
	}
	audioRatio, hasAudio := effective["AudioRatio"].(float64)
	audioCompletionRatio, hasAudioCompletion := effective["AudioCompletionRatio"].(float64)
	if !hasAudio && !hasAudioCompletion {
		return details
	}
	if !hasAudio {
		audioRatio = 1
	}
	if !hasAudioCompletion {
		audioCompletionRatio = 1
	}
	input := base.Mul(decimal.NewFromFloat(audioRatio))
	inPrice, outPrice := input.InexactFloat64(), input.Mul(decimal.NewFromFloat(audioCompletionRatio)).InexactFloat64()
	if math.IsInf(inPrice, 0) || math.IsInf(outPrice, 0) {
		details.InvalidAudioPrice = true
		return details
	}
	details.AudioInputPrice, details.AudioOutputPrice = &inPrice, &outPrice
	details.AudioTextBranches = effective["CacheRatio"] != float64(1) || effective["ImageRatio"] != float64(1) || ResolveCacheWriteMode(name, configured) != CacheWriteNone
	return details
}

type CacheWriteMode string

const (
	CacheWriteNone      CacheWriteMode = "none"
	CacheWriteStandard  CacheWriteMode = "standard"
	CacheWriteClaudeTTL CacheWriteMode = "claude_ttl"
)

// ResolveCacheWriteMode describes display and migration only. A generic engine
// fallback is not configured cache-write pricing. Only Claude names inherit the
// legacy dual-TTL rule; other models retain their configured write price.
func ResolveCacheWriteMode(name string, configured PricingValues) CacheWriteMode {
	if strings.Contains(strings.ToLower(name), "claude") {
		return CacheWriteClaudeTTL
	}
	if _, exists := configured["CreateCacheRatio"]; exists {
		return CacheWriteStandard
	}
	return CacheWriteNone
}

func PreviewModelPricingConversion(name string, draft PricingValues) (*ModelPricingConversion, error) {
	if draft == nil {
		return nil, errors.New("pricing draft is required")
	}
	if err := ValidateModelPricing(name, draft); err != nil {
		return nil, err
	}
	if draft["billing_setting.billing_mode"] == billing_setting.BillingModeTieredExpr {
		return &ModelPricingConversion{UnsupportedReason: "This model already uses an expression."}, nil
	}
	// The draft is the complete editable configuration: omitted fields inherit
	// relay defaults rather than silently retaining discarded saved values.
	legacyDraft := make(PricingValues, len(draft)+1)
	maps.Copy(legacyDraft, draft)
	legacyDraft["billing_setting.billing_mode"] = billing_setting.BillingModeRatio
	effective, err := PreviewModelPricing(name, legacyDraft)
	if err != nil {
		return nil, err
	}
	if err := ValidateModelPricing(name, effective); err != nil {
		return nil, err
	}
	_, fixedPrice := effective["ModelPrice"]
	preview := &ModelPricingConversion{ModelPricingDescription: ModelPricingDescription{Effective: effective, BillingDetails: ResolveLegacyBillingDetails(name, effective, draft)}}
	if preview.BillingDetails.InvalidAudioPrice {
		return nil, errors.New("audio prices must be finite")
	}
	if preview.BillingDetails.ConflictingAudioPrices {
		return &ModelPricingConversion{UnsupportedReason: "Gemini and OpenAI audio prices differ for this model. Use separate billing model names to convert them."}, nil
	}

	generation := jsplugin.DefaultRegistry.Generation()
	if _, task := generation.GetByModel(name); task {
		return &ModelPricingConversion{UnsupportedReason: "Task pricing must be converted manually using the task usage schema."}, nil
	}
	if _, task := ResolveTaskModelAlias(generation, name); task {
		return &ModelPricingConversion{UnsupportedReason: "Task pricing must be converted manually using the task usage schema."}, nil
	}

	// Inspect non-secret routing metadata so aliases cannot disguise a special
	// settlement path as an ordinary text model.
	var channels []Channel
	candidates := DB.Model(&Ability{}).Select("channel_id").Where("model = ?", name)
	if err := DB.Select("type", "status", "models", "model_mapping").Where("id IN (?)", candidates).Find(&channels).Error; err != nil {
		return nil, err
	}
	names := []string{name}
	for _, channel := range channels {
		if !slices.Contains(channel.GetModels(), name) {
			continue
		}
		if channel.Type == constant.ChannelTypeTaskPlugin {
			return &ModelPricingConversion{UnsupportedReason: "Task pricing must be converted manually using the task usage schema."}, nil
		}
		if slices.Contains(common.GetEndpointTypesByChannelType(channel.Type, name), constant.EndpointTypeOpenAIVideo) {
			return &ModelPricingConversion{UnsupportedReason: "Video pricing must be converted manually."}, nil
		}
		if channel.Status == common.ChannelStatusEnabled && channel.Type == constant.ChannelTypeOpenRouter && strings.Contains(strings.ToLower(name), "claude") && !fixedPrice {
			defaultRatio, hasDefault := ratio_setting.GetDefaultModelRatioMap()[name]
			if hasDefault && effective["ModelRatio"] == defaultRatio && effective["CreateCacheRatio"] != float64(1) {
				return &ModelPricingConversion{UnsupportedReason: "This OpenRouter Claude price derives cache-write usage from upstream cost and must be converted manually."}, nil
			}
		}
		upstream := name
		if channel.ModelMapping != nil && *channel.ModelMapping != "" {
			var mapping map[string]string
			if err := common.UnmarshalJsonStr(*channel.ModelMapping, &mapping); err != nil {
				return &ModelPricingConversion{UnsupportedReason: "The model routing configuration could not be verified."}, nil
			}
			// Relay also resolves reasoning-suffixed names at any mapping hop.
			candidates := []string{name}
			for _, target := range mapping {
				candidates = append(candidates, target)
			}
			for _, candidate := range candidates {
				if mapping[candidate] == "" {
					mapping[candidate] = mapping[hostreasoning.BaseModelName(candidate)]
				}
			}
			var cycle bool
			upstream, cycle = followChannelModelMapping(mapping, name)
			if cycle {
				return &ModelPricingConversion{UnsupportedReason: "The model routing configuration could not be verified."}, nil
			}
			if upstream != name {
				names = append(names, upstream)
			}
		}
	}
	for _, modelName := range names {
		lower := strings.ToLower(modelName)
		if strings.Contains(lower, "realtime") {
			return &ModelPricingConversion{UnsupportedReason: "Realtime pricing must be converted manually."}, nil
		}
		if fixedPrice && ResolveLegacyBillingDetails(modelName, effective, draft).ImageCount {
			preview.BillingDetails.ImageCount = true
		}
	}
	var metadata []Model
	if err := DB.Select("model_name", "name_rule", "endpoints").
		Where("model_name IN ? OR name_rule <> ?", names, NameRuleExact).
		Where("endpoints <> ?", "").Find(&metadata).Error; err != nil {
		return nil, err
	}
	for _, entry := range metadata {
		if entry.Endpoints == "" || !slices.ContainsFunc(names, entry.MatchesName) {
			continue
		}
		var endpoints map[string]any
		if err := common.UnmarshalJsonStr(entry.Endpoints, &endpoints); err != nil {
			return &ModelPricingConversion{UnsupportedReason: "The model routing configuration could not be verified."}, nil
		}
		for endpoint := range endpoints {
			switch constant.EndpointType(endpoint) {
			case constant.EndpointTypeImageGeneration:
				if fixedPrice {
					preview.BillingDetails.ImageCount = true
				}
			case constant.EndpointTypeOpenAIVideo:
				return &ModelPricingConversion{UnsupportedReason: "Video pricing must be converted manually."}, nil
			case constant.EndpointTypeOpenAI, constant.EndpointTypeOpenAIResponse, constant.EndpointTypeAnthropic, constant.EndpointTypeGemini, constant.EndpointTypeEmbeddings, constant.EndpointTypeJinaRerank:
				// These endpoints use the ordinary token/fixed-request settlement.
			}
		}
	}
	var expression string
	if fixedPrice {
		price := effective["ModelPrice"].(float64)
		expression = `tier("request", fixed(` + decimal.NewFromFloat(price).String() + `))`
		if preview.BillingDetails.ImageCount {
			expression = `tier("image", fixed(` + decimal.NewFromFloat(price).String() + `)) * image_count`
			for _, rule := range preview.BillingDetails.RequestRules {
				expression += ` * (` + rule.Condition + ` ? ` + decimal.NewFromFloat(rule.Multiplier).String() + ` : 1)`
			}
		}
	} else {
		ratio, exists := effective["ModelRatio"].(float64)
		if !exists {
			return &ModelPricingConversion{UnsupportedReason: "Configure an input price before converting this model."}, nil
		}
		preview.CacheWriteMode = ResolveCacheWriteMode(name, draft)
		base, err := legacyInputPricePerMillion(ratio)
		if err != nil {
			return nil, err
		}
		// Cache reads can overlap other separately priced input categories.
		// Keep cr in that case: clamping the input remainder makes folding it
		// into p non-equivalent. A zero base price is equivalent either way.
		ordinaryAudio := preview.BillingDetails.AudioOutputPrice != nil
		if ordinaryAudio && preview.CacheWriteMode != CacheWriteNone {
			preview.BillingDetails.AudioTextBranches = true
		}
		mergeCacheRead := base.IsZero() || preview.CacheWriteMode == CacheWriteNone && effective["ImageRatio"].(float64) == 1 && (preview.BillingDetails.AudioInputPrice == nil || ordinaryAudio)
		var expressionBody strings.Builder
		expressionBody.WriteString(`tier("base", p * `)
		expressionBody.WriteString(base.String())
		// Retain explicit zero coefficients for included categories. Only
		// Claude names inherit the relay's 1h / 5m factor of 6 / 3.75.
		for _, lane := range []struct {
			variable, key string
			multiplier    float64
		}{
			{"c", "CompletionRatio", 1},
			{"cr", "CacheRatio", 1},
			{"cc", "CreateCacheRatio", 1},
			{"cc1h", "CreateCacheRatio", 6 / 3.75},
			{"img", "ImageRatio", 1},
		} {
			if ordinaryAudio && !preview.BillingDetails.AudioTextBranches && lane.variable != "c" {
				continue
			}
			if lane.variable == "cc" && preview.CacheWriteMode == CacheWriteNone ||
				lane.variable == "cc1h" && preview.CacheWriteMode != CacheWriteClaudeTTL {
				continue
			}
			multiplier := effective[lane.key].(float64)
			// Same-price input categories stay in p. Keep distinct prices,
			// including explicit zero, as independently priced terms.
			if (lane.variable == "img" || lane.variable == "cr" && mergeCacheRead) && multiplier == 1 {
				continue
			}
			expressionBody.WriteString(" + " + lane.variable + " * ")
			expressionBody.WriteString(base.Mul(decimal.NewFromFloat(multiplier)).Mul(decimal.NewFromFloat(lane.multiplier)).String())
		}
		for _, lane := range []struct {
			variable string
			price    *float64
		}{
			{"ai", preview.BillingDetails.AudioInputPrice},
			{"ao", preview.BillingDetails.AudioOutputPrice},
		} {
			if lane.price != nil && !preview.BillingDetails.AudioTextBranches {
				expressionBody.WriteString(" + " + lane.variable + " * " + decimal.NewFromFloat(*lane.price).String())
			}
		}
		expressionBody.WriteString(")")
		expression = expressionBody.String()
		if ordinaryAudio && preview.BillingDetails.AudioTextBranches {
			completion := base.Mul(decimal.NewFromFloat(effective["CompletionRatio"].(float64)))
			// The audio settlement ignores cache/image ratios. Use the full
			// input total here because variables in the text-only branch still
			// participate in AST-based token normalization.
			expression = `(ai > 0 || ao > 0) ? tier("audio", max(len - ai, 0) * ` + base.String() + ` + c * ` + completion.String() + ` + ai * ` + decimal.NewFromFloat(*preview.BillingDetails.AudioInputPrice).String() + ` + ao * ` + decimal.NewFromFloat(*preview.BillingDetails.AudioOutputPrice).String() + `) : ` + expression
		}
	}
	if err := billing_setting.SmokeTestExpr(expression); err != nil {
		return nil, err
	}
	preview.Expression = expression
	return preview, nil
}
