package util

import (
	"regexp"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
)

var openAIFastModePattern = regexp.MustCompile(`^(gpt-5(?:\.\d+)?(?:-codex)?)(?:-(minimal|low|medium|high|xhigh|max))?-fast$`)

// OpenAIFastModeCompatibility describes the normalized request model derived
// from legacy GPT-5.x fast aliases such as gpt-5.4-fast and gpt-5.4-high-fast.
type OpenAIFastModeCompatibility struct {
	OriginalModel     string
	NormalizedModel   string
	BaseModel         string
	Fast              bool
	UsedCompatibility bool
}

// NormalizeOpenAIFastModeModel converts supported GPT-5.x fast aliases into the
// canonical model form used by routing and thinking logic.
func NormalizeOpenAIFastModeModel(model string) OpenAIFastModeCompatibility {
	trimmed := strings.TrimSpace(model)
	baseModel := strings.TrimSpace(thinking.ParseSuffix(trimmed).ModelName)
	info := OpenAIFastModeCompatibility{
		OriginalModel:   trimmed,
		NormalizedModel: trimmed,
		BaseModel:       baseModel,
	}
	if trimmed == "" {
		return info
	}

	parsed := thinking.ParseSuffix(trimmed)
	if parsed.HasSuffix {
		info.BaseModel = strings.TrimSpace(parsed.ModelName)
		return info
	}

	matches := openAIFastModePattern.FindStringSubmatch(strings.ToLower(trimmed))
	if len(matches) != 3 {
		return info
	}

	canonicalModel := matches[1]
	level := matches[2]
	normalizedModel := canonicalModel
	if level != "" {
		normalizedModel += "(" + level + ")"
	}

	info.NormalizedModel = normalizedModel
	info.BaseModel = canonicalModel
	info.Fast = true
	info.UsedCompatibility = normalizedModel != trimmed
	return info
}

func HasOpenAIFastModeCompatibility(model string) bool {
	return NormalizeOpenAIFastModeModel(model).Fast
}
