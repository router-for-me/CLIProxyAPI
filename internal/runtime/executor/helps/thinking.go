package helps

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

// ApplyThinkingWithSourcePayload preserves summary visibility from the original
// client payload while applying thinking configuration to its translated target
// payload. currentSourcePayload is the payload that was translated, while
// originalSourcePayload retains intent removed by an earlier interceptor.
func ApplyThinkingWithSourcePayload(body, currentSourcePayload, originalSourcePayload []byte, model, fromFormat, toFormat, providerKey string) ([]byte, error) {
	summary := translatedRequestSummaryConfig(body, currentSourcePayload, originalSourcePayload, model, fromFormat, toFormat)
	return thinking.ApplyThinkingWithSummary(body, model, fromFormat, toFormat, providerKey, summary)
}

// translatedRequestSummaryConfig gives the translated target payload precedence
// so a plugin request normalizer can remove or rewrite a canonical summary field.
// The original source is consulted only when the payload that was translated no
// longer carries the inbound intent, or when the target could not represent that
// intent until model-aware thinking is applied later (notably Claude).
func translatedRequestSummaryConfig(body, currentSourcePayload, originalSourcePayload []byte, model, fromFormat, toFormat string) thinking.SummaryConfig {
	fromFormat = strings.ToLower(strings.TrimSpace(fromFormat))
	toFormat = strings.ToLower(strings.TrimSpace(toFormat))

	var targetSummary thinking.SummaryConfig
	if fromFormat == toFormat {
		targetSummary = thinking.ExtractSummaryConfig(body, toFormat)
	} else {
		targetSummary = thinking.ExtractExplicitSummaryConfig(body, toFormat)
	}
	if targetSummary.Mode != thinking.SummaryUnspecified {
		return targetSummary
	}

	currentSummary := thinking.ExtractSummaryConfig(currentSourcePayload, fromFormat)
	originalSummary := thinking.ExtractSummaryConfig(originalSourcePayload, fromFormat)
	if currentSummary.Mode == thinking.SummaryUnspecified {
		return originalSummary
	}

	from := sdktranslator.FromString(fromFormat)
	to := sdktranslator.FromString(toFormat)
	if !sdktranslator.HasRequestTransformer(from, to) {
		// A missing translation must remain source-shaped. Same-format requests
		// were handled by targetSummary above, including explicit native aliases.
		return thinking.SummaryConfig{}
	}

	candidate := thinking.ApplySummaryConfigForModel(body, toFormat, model, currentSummary)
	if thinking.ExtractExplicitSummaryConfig(candidate, toFormat).Mode != thinking.SummaryUnspecified {
		// Registry translation applied this field before plugin normalization. If
		// it is absent now but can be represented on the normalized body, the
		// normalizer deliberately removed it and must remain authoritative.
		return thinking.SummaryConfig{}
	}

	// Some intents cannot be represented until the final model-aware pass. For
	// example, Claude display is invalid on disabled thinking, but a suffix can
	// subsequently activate adaptive thinking. Preserve the source in that case.
	return currentSummary
}
