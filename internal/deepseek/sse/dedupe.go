package sse

import (
	"strings"
	"unicode/utf8"
)

const minContinuationSnapshotLen = 32

func TrimContinuationOverlap(existing, incoming string) string {
	if incoming == "" {
		return ""
	}
	if existing == "" {
		return incoming
	}
	if utf8.RuneCountInString(incoming) < minContinuationSnapshotLen {
		return incoming
	}
	if len(incoming) > len(existing) {
		if strings.HasPrefix(incoming, existing) {
			return incoming[len(existing):]
		}
		return incoming
	}
	if len(incoming) < len(existing) && strings.HasPrefix(existing, incoming) {
		return ""
	}
	return incoming
}

func TrimContinuationOverlapFromBuilder(existing *strings.Builder, incoming string) string {
	if incoming == "" {
		return ""
	}
	if existing == nil || existing.Len() == 0 {
		return incoming
	}
	if utf8.RuneCountInString(incoming) < minContinuationSnapshotLen {
		return incoming
	}
	existingLen := existing.Len()
	if len(incoming) > existingLen {
		existingStr := existing.String()
		if strings.HasPrefix(incoming, existingStr) {
			return incoming[existingLen:]
		}
		return incoming
	}
	if len(incoming) < existingLen {
		existingStr := existing.String()
		if strings.HasPrefix(existingStr, incoming) {
			return ""
		}
	}
	return incoming
}
