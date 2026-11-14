package helps

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
)

// ErrCursorVariantUnavailable identifies a combination absent from this account.
var ErrCursorVariantUnavailable = errors.New("cursor model variant unavailable")

type cursorModelVariant struct {
	ID       string
	Effort   string
	Thinking bool
	Fast     bool
}

type cursorModelFamily struct {
	ID       string
	Variants []cursorModelVariant
}

// AddCursorModelFamilies returns one catalog entry per model family.
// Variant IDs that only encode effort, thinking, or speed are omitted.
// Callers that still need those IDs keep the original slice for routing.
func AddCursorModelFamilies(models []*registry.ModelInfo) []*registry.ModelInfo {
	families := cursorModelFamilies(models)
	hide := map[string]struct{}{}
	for _, family := range families {
		if cursorFamilyIsSingleton(family) {
			continue
		}
		for _, variant := range family.Variants {
			if variant.ID != family.ID {
				hide[variant.ID] = struct{}{}
			}
		}
	}

	result := make([]*registry.ModelInfo, 0, len(models))
	for _, model := range models {
		if model != nil {
			if _, hidden := hide[model.ID]; hidden {
				continue
			}
		}
		result = append(result, model)
	}

	for _, family := range families {
		if cursorFamilyIsSingleton(family) {
			continue
		}
		representative := cursorFamilyRepresentative(models, family)
		if representative == nil {
			continue
		}
		enriched := enrichCursorFamily(representative, family)
		replaced := false
		for i, model := range result {
			if model == nil || model.ID != family.ID {
				continue
			}
			result[i] = enriched
			replaced = true
		}
		if !replaced {
			result = append(result, enriched)
		}
	}
	return result
}

func cursorFamilyIsSingleton(family cursorModelFamily) bool {
	return len(family.Variants) == 1 && family.Variants[0].ID == family.ID
}

func cursorFamilyRepresentative(models []*registry.ModelInfo, family cursorModelFamily) *registry.ModelInfo {
	var representative *registry.ModelInfo
	for _, model := range models {
		if model == nil {
			continue
		}
		if model.ID == family.Variants[0].ID || model.ID == family.ID {
			representative = model
		}
	}
	return representative
}

func enrichCursorFamily(representative *registry.ModelInfo, family cursorModelFamily) *registry.ModelInfo {
	copy := *representative
	copy.ID = family.ID
	if representative.ID != family.ID {
		copy.DisplayName = family.ID
	}
	levels, hasThinking, hasFast := cursorEffortLevels(family.Variants)
	if len(levels) > 0 {
		copy.Thinking = &registry.ThinkingSupport{Levels: levels}
		if !cursorParameterListed(copy.SupportedParameters, "reasoning_effort") {
			copy.SupportedParameters = append(append([]string(nil), copy.SupportedParameters...), "reasoning_effort")
		}
		copy.ExplicitThinking = true
	} else if hasThinking && copy.Thinking == nil {
		copy.Thinking = &registry.ThinkingSupport{}
		copy.ExplicitThinking = true
	} else if copy.Thinking != nil {
		copy.ExplicitThinking = true
	}
	copy.SupportsFast = hasFast
	return &copy
}

func cursorEffortLevels(variants []cursorModelVariant) (levels []string, hasThinking, hasFast bool) {
	seen := map[string]struct{}{}
	for _, variant := range variants {
		hasThinking = hasThinking || variant.Thinking
		hasFast = hasFast || variant.Fast
		if variant.Effort != "" {
			seen[variant.Effort] = struct{}{}
		}
	}
	for _, level := range []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"} {
		if _, ok := seen[level]; !ok {
			continue
		}
		levels = append(levels, level)
		delete(seen, level)
	}
	if len(seen) == 0 {
		return levels, hasThinking, hasFast
	}
	extra := make([]string, 0, len(seen))
	for level := range seen {
		extra = append(extra, level)
	}
	sort.Strings(extra)
	return append(levels, extra...), hasThinking, hasFast
}

func cursorParameterListed(parameters []string, want string) bool {
	for _, parameter := range parameters {
		if parameter == want {
			return true
		}
	}
	return false
}

func cursorVariant(id string) (string, cursorModelVariant) {
	v := cursorModelVariant{ID: id}
	base := id
	for {
		cut := strings.LastIndexByte(base, '-')
		if cut < 0 {
			break
		}
		switch base[cut+1:] {
		case "fast":
			v.Fast = true
		case "thinking":
			v.Thinking = true
		case "none", "minimal", "low", "medium", "high", "xhigh", "max":
			if v.Effort != "" {
				return base, v
			}
			v.Effort = base[cut+1:]
			if v.Effort == "high" && strings.HasSuffix(base[:cut], "-extra") {
				v.Effort = "xhigh"
				cut -= len("-extra")
			}
		default:
			return base, v
		}
		base = base[:cut]
	}
	return base, v
}

func cursorModelFamilies(models []*registry.ModelInfo) []cursorModelFamily {
	groups := map[string][]cursorModelVariant{}
	seen := map[string]bool{}
	for _, model := range models {
		if model == nil || model.ID == "" || seen[model.ID] {
			continue
		}
		seen[model.ID] = true
		base, variant := cursorVariant(model.ID)
		groups[base] = append(groups[base], variant)
	}
	families := make([]cursorModelFamily, 0, len(groups))
	for id, variants := range groups {
		sort.Slice(variants, func(i, j int) bool { return variants[i].ID < variants[j].ID })
		families = append(families, cursorModelFamily{ID: id, Variants: variants})
	}
	sort.Slice(families, func(i, j int) bool { return families[i].ID < families[j].ID })
	return families
}

type cursorModelOptions struct {
	effort       string
	thinkingType string
	fast         bool
}

func cursorOptions(model string, payload []byte, format string) (cursorModelOptions, error) {
	options := cursorModelOptions{effort: thinking.ExtractReasoningEffort(nil, format, model)}
	if options.effort == "" {
		options.effort = thinking.ExtractReasoningEffort(payload, format, model)
		options.thinkingType = ojson.GetBytes(payload, "thinking.type").String()
		// Claude also accepts output_config.effort with enabled thinking.
		if format == "claude" {
			if option := ojson.GetBytes(payload, "output_config.effort"); option.Exists() {
				options.effort = strings.ToLower(strings.TrimSpace(option.String()))
			}
		}
	}
	for _, path := range []string{"service_tier", "speed"} {
		value := ojson.GetBytes(payload, path).String()
		switch value {
		case "", "auto", "default", "standard":
		case "fast", "priority":
			options.fast = true
		default:
			return options, fmt.Errorf("cursor model %s does not support %s=%s", model, path, value)
		}
	}
	return options, nil
}

// ResolveCursorModel preserves explicit variant IDs and maps family options to
// an advertised variant. ErrCursorVariantUnavailable allows another account to
// satisfy a valid combination; other errors describe invalid request options.
func ResolveCursorModel(model string, payload []byte, format string, models []*registry.ModelInfo) (string, error) {
	modelName := thinking.ParseSuffix(model).ModelName
	base, _ := cursorVariant(modelName)
	if base != modelName {
		return modelName, nil
	}
	var variants []cursorModelVariant
	for _, info := range models {
		if info == nil {
			continue
		}
		if family, variant := cursorVariant(info.ID); family == modelName {
			variants = append(variants, variant)
		}
	}
	if len(variants) == 0 {
		return model, nil
	}
	options, err := cursorOptions(model, payload, format)
	if err != nil {
		return "", err
	}
	effort, thinkingType, fast := options.effort, options.thinkingType, options.fast
	hasEffort, hasThinking := false, false
	for _, v := range variants {
		hasEffort = hasEffort || v.Effort != ""
		hasThinking = hasThinking || v.Thinking
	}
	// Harnesses can send a default effort even when the model has no such option.
	if !hasEffort && !hasThinking {
		effort, thinkingType = "", ""
	}
	if effort == "auto" {
		effort = ""
	}
	switch effort {
	case "", "none", "minimal", "low", "medium", "high", "xhigh", "max":
	default:
		return "", fmt.Errorf("cursor model %s: unsupported reasoning effort %s", model, effort)
	}
	if effort == "" && thinkingType == "" && !fast {
		for _, v := range variants {
			if v.ID == modelName {
				return modelName, nil
			}
		}
	}
	wantThinking := hasThinking
	if thinkingType == "disabled" || effort == "none" {
		wantThinking = false
		if thinkingType == "disabled" && !hasThinking {
			effort = "none"
		}
	}
	if thinkingType == "enabled" || thinkingType == "adaptive" || thinkingType == "auto" {
		if effort == "none" {
			return "", fmt.Errorf("cursor model %s: enabled thinking conflicts with effort none", model)
		}
		wantThinking = hasThinking
	}
	if thinkingType != "" && thinkingType != "disabled" && thinkingType != "enabled" && thinkingType != "adaptive" && thinkingType != "auto" {
		return "", fmt.Errorf("cursor model %s: unsupported thinking type %s", model, thinkingType)
	}
	best, bestScore := "", -1
	for _, v := range variants {
		if v.Fast != fast || v.Thinking != wantThinking {
			continue
		}
		if effort != "" && effort != "none" && v.Effort != effort {
			continue
		}
		if effort == "none" && !hasThinking && v.Effort != "none" && v.Effort != "" {
			continue
		}
		score := 0
		switch v.Effort {
		case "medium":
			score = 7
		case "high":
			score = 6
		case "":
			score = 5
		case "low":
			score = 4
		case "minimal":
			score = 3
		case "xhigh":
			score = 2
		case "max":
			score = 1
		}
		if score > bestScore || (score == bestScore && v.ID < best) {
			best, bestScore = v.ID, score
		}
	}
	if best != "" {
		return best, nil
	}
	return "", fmt.Errorf("%w: cursor model %s has no advertised variant for effort=%q thinking=%t fast=%t", ErrCursorVariantUnavailable, model, effort, wantThinking, fast)
}
