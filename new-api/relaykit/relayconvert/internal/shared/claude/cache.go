package claude

func NormalizeCacheCreationSplit(totalTokens int, tokens5m int, tokens1h int) (int, int) {
	remainder := totalTokens - tokens5m - tokens1h
	if remainder < 0 {
		remainder = 0
	}
	return tokens5m + remainder, tokens1h
}
