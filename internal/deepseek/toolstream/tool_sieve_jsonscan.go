package toolstream

import "strings"

func trimWrappingJSONFence(prefix, suffix string) (string, string) {
	trimmedPrefix := strings.TrimRight(prefix, " \t\r\n")
	fenceIdx := strings.LastIndex(trimmedPrefix, "```")
	if fenceIdx < 0 {
		return prefix, suffix
	}
	// Only strip when the trailing fence in prefix behaves like an opening fence.
	// A legitimate closing fence before a standalone tool JSON must be preserved.
	if strings.Count(trimmedPrefix[:fenceIdx+3], "```")%2 == 0 {
		return prefix, suffix
	}
	fenceHeader := strings.TrimSpace(trimmedPrefix[fenceIdx+3:])
	if fenceHeader != "" && !strings.EqualFold(fenceHeader, "json") {
		return prefix, suffix
	}

	trimmedSuffix := strings.TrimLeft(suffix, " \t\r\n")
	if !strings.HasPrefix(trimmedSuffix, "```") {
		return prefix, suffix
	}
	consumedLeading := len(suffix) - len(trimmedSuffix)
	return trimmedPrefix[:fenceIdx], suffix[consumedLeading+3:]
}
