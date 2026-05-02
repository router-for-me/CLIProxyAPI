// Package registry — model variant suffix normalization.
//
// Google's Gemini CLI appends behavioural fine-tune markers such as
// "-customtools" to model names when it detects an API key. These variants
// share the same upstream provider/executor as the base model, but several
// CPA code paths — provider lookup, auth selection, OAuth /v1internal routing
// — only know the base model name and therefore fail when the suffixed name
// is propagated downstream.
//
// StripModelVariantSuffix removes these variant markers while preserving
// thinking-budget suffixes like "(8192)" / "(high)" and chained segments like
// "-thinking". It is the single source of truth for variant normalization
// across the request path, auth selection path, and capability probes.
package registry

import "strings"

// knownModelVariantSuffixes lists the variant markers (including the leading
// dash) that are safe to strip for provider/auth lookup. Adding new entries
// here automatically covers every call site through StripModelVariantSuffix.
var knownModelVariantSuffixes = []string{"-customtools"}

// StripModelVariantSuffix removes every known variant marker from model,
// regardless of its position in the name, as long as the match falls on a
// segment boundary ('-', '(', or end-of-string).
//
// Examples:
//
//	"gemini-3.1-pro-preview-customtools"         -> "gemini-3.1-pro-preview"
//	"gemini-3.1-pro-preview-customtools(8192)"   -> "gemini-3.1-pro-preview(8192)"
//	"gemini-3.1-pro-preview-customtools(high)"   -> "gemini-3.1-pro-preview(high)"
//	"gemini-2.0-flash-customtools-thinking"      -> "gemini-2.0-flash-thinking"
//	"gemini-2.0-flash-customtools-thinking(4096)" -> "gemini-2.0-flash-thinking(4096)"
//	"gemini-2.5-pro"                             -> "gemini-2.5-pro" (unchanged)
//
// The function is idempotent and never panics on empty input.
func StripModelVariantSuffix(model string) string {
	if model == "" {
		return model
	}
	result := model
	for _, variant := range knownModelVariantSuffixes {
		// Order matters: strip thinking-budget-adjacent occurrences first
		// ("-customtools(8192)" -> "(8192)"), then chained segments
		// ("-customtools-thinking" -> "-thinking"), then any terminal form.
		result = strings.ReplaceAll(result, variant+"(", "(")
		result = strings.ReplaceAll(result, variant+"-", "-")
		if strings.HasSuffix(result, variant) {
			result = strings.TrimSuffix(result, variant)
		}
	}
	return result
}
