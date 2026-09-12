// Package modelmatch provides model name pattern matching shared by runtime policies.
package modelmatch

import "strings"

// Match reports whether model matches pattern. Matching is case-sensitive and
// supports '*' as the only wildcard, matching zero or more characters.
func Match(pattern, model string) bool {
	return match(strings.TrimSpace(pattern), strings.TrimSpace(model))
}

// MatchFold reports whether model matches pattern using case-insensitive matching.
func MatchFold(pattern, model string) bool {
	return match(strings.ToLower(strings.TrimSpace(pattern)), strings.ToLower(strings.TrimSpace(model)))
}

func match(pattern, model string) bool {
	if pattern == "" {
		return false
	}
	if pattern == "*" {
		return true
	}

	patternIndex, modelIndex := 0, 0
	starIndex := -1
	matchIndex := 0
	for modelIndex < len(model) {
		if patternIndex < len(pattern) && pattern[patternIndex] == model[modelIndex] {
			patternIndex++
			modelIndex++
			continue
		}
		if patternIndex < len(pattern) && pattern[patternIndex] == '*' {
			starIndex = patternIndex
			matchIndex = modelIndex
			patternIndex++
			continue
		}
		if starIndex != -1 {
			patternIndex = starIndex + 1
			matchIndex++
			modelIndex = matchIndex
			continue
		}
		return false
	}
	for patternIndex < len(pattern) && pattern[patternIndex] == '*' {
		patternIndex++
	}
	return patternIndex == len(pattern)
}
