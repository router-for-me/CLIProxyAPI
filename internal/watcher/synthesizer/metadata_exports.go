package synthesizer

// ExtractExcludedModelsFromMetadata reads credential exclusions using the same
// canonical-key precedence and value normalization as auth-file synthesis.
func ExtractExcludedModelsFromMetadata(metadata map[string]any) []string {
	return extractExcludedModelsFromMetadata(metadata)
}
