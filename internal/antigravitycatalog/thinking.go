package antigravitycatalog

// ValidThinkingSuffix reports whether a catalog suffix consists of ASCII
// alphanumeric segments joined by single hyphens.
func ValidThinkingSuffix(suffix string) bool {
	lastAlnum := false
	for i := range len(suffix) {
		switch ch := suffix[i]; {
		case ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9':
			lastAlnum = true
		case ch == '-' && lastAlnum:
			lastAlnum = false
		default:
			return false
		}
	}
	return lastAlnum
}
