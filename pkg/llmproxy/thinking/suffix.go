// Package thinking provides unified thinking configuration processing.
//
// This file implements suffix parsing functionality for extracting
// thinking configuration from model names in the format model(value).
package thinking

import (
	"strconv"
	"strings"
)

// ParseSuffix extracts thinking suffix from a model name.
//
// The suffix format is: model-name(value)
// Examples:
//   - "claude-sonnet-4-5(16384)" -> ModelName="claude-sonnet-4-5", RawSuffix="16384"
//   - "gpt-5.2(high)" -> ModelName="gpt-5.2", RawSuffix="high"
//   - "gemini-2.5-pro" -> ModelName="gemini-2.5-pro", HasSuffix=false
//
// This function only extracts the suffix; it does not validate or interpret
// the suffix content. Use ParseNumericSuffix, ParseLevelSuffix, etc. for
// content interpretation.
func ParseSuffix(model string) SuffixResult {
	// Find the last opening parenthesis
	lastOpen := strings.LastIndex(model, "(")
	if lastOpen == -1 {
		return SuffixResult{ModelName: model, HasSuffix: false}
	}

	// Check if the string ends with a closing parenthesis
	if !strings.HasSuffix(model, ")") {
		return SuffixResult{ModelName: model, HasSuffix: false}
	}

	// Extract components
	modelName := model[:lastOpen]
	rawSuffix := model[lastOpen+1 : len(model)-1]

	return SuffixResult{
		ModelName: modelName,
		HasSuffix: true,
		RawSuffix: rawSuffix,
	}
}

// ParseNumericSuffix attempts to parse a raw suffix as a numeric budget value.
//
// This function parses the raw suffix content (from ParseSuffix.RawSuffix) as an integer.
// Only non-negative integers are considered valid numeric suffixes.
//
// Platform note: The budget value uses Go's int type, which is 32-bit on 32-bit
// systems and 64-bit on 64-bit systems. Values exceeding the platform's int range
// will return ok=false.
//
// Leading zeros are accepted: "08192" parses as 8192.
//
// Examples:
//   - "8192" -> budget=8192, ok=true
//   - "0" -> budget=0, ok=true (represents ModeNone)
//   - "08192" -> budget=8192, ok=true (leading zeros accepted)
//   - "-1" -> budget=0, ok=false (negative numbers are not valid numeric suffixes)
//   - "high" -> budget=0, ok=false (not a number)
//   - "9223372036854775808" -> budget=0, ok=false (overflow on 64-bit systems)
//
// For special handling of -1 as auto mode, use ParseSpecialSuffix instead.
func ParseNumericSuffix(rawSuffix string) (budget int, ok bool) {
	if rawSuffix == "" {
		return 0, false
	}

	value, err := strconv.Atoi(rawSuffix)
	if err != nil {
		return 0, false
	}

	// Negative numbers are not valid numeric suffixes
	// -1 should be handled by special value parsing as "auto"
	if value < 0 {
		return 0, false
	}

	return value, true
}

// ParseSpecialSuffix attempts to parse a raw suffix as a special thinking mode value.
//
// This function handles special strings that represent a change in thinking mode:
//   - "none" -> ModeNone (disables thinking)
//   - "auto" -> ModeAuto (automatic/dynamic thinking)
//   - "-1"   -> ModeAuto (numeric representation of auto mode)
//
// String values are case-insensitive.
func ParseSpecialSuffix(rawSuffix string) (mode ThinkingMode, ok bool) {
	if rawSuffix == "" {
		return ModeBudget, false
	}

	// Case-insensitive matching
	switch strings.ToLower(rawSuffix) {
	case "none":
		return ModeNone, true
	case "auto", "-1":
		return ModeAuto, true
	default:
		return ModeBudget, false
	}
}

// ParseLevelSuffix attempts to parse a raw suffix as a discrete thinking level.
//
// This function parses the raw suffix content (from ParseSuffix.RawSuffix) as a level.
// Only discrete effort levels are valid: minimal, low, medium, high, xhigh.
// Level matching is case-insensitive.
//
// Special values (none, auto) are NOT handled by this function; use ParseSpecialSuffix
// instead. This separation allows callers to prioritize special value handling.
//
// Examples:
//   - "high" -> level=LevelHigh, ok=true
//   - "HIGH" -> level=LevelHigh, ok=true (case insensitive)
//   - "medium" -> level=LevelMedium, ok=true
//   - "none" -> level="", ok=false (special value, use ParseSpecialSuffix)
//   - "auto" -> level="", ok=false (special value, use ParseSpecialSuffix)
//   - "8192" -> level="", ok=false (numeric, use ParseNumericSuffix)
//   - "ultra" -> level="", ok=false (unknown level)
func ParseLevelSuffix(rawSuffix string) (level ThinkingLevel, ok bool) {
	if rawSuffix == "" {
		return "", false
	}

	// Case-insensitive matching
	switch strings.ToLower(rawSuffix) {
	case "minimal":
		return LevelMinimal, true
	case "low":
		return LevelLow, true
	case "medium":
		return LevelMedium, true
	case "high":
		return LevelHigh, true
	case "xhigh":
		return LevelXHigh, true
	default:
		return "", false
	}
}
