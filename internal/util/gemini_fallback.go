package util

import (
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// ResolveGeminiFamilyFallback resolves an unregistered gemini-* model name containing
// "flash" or "pro" to the highest version-sorted registered gemini-* model in the same family.
//
// Rules:
//  1. Exact registry hits win (callers verify GetProviderName is empty before calling).
//  2. Fail-closed: if the model already exists in the registry (e.g. registered but blocked or
//     no provider configured), no fallback is performed.
//  3. Only model names with the "gemini-" prefix containing "flash" or "pro" qualify.
//  4. Candidates are filtered for registered available Gemini models matching the requested family.
//  5. Candidates are version-sorted ascending by numeric segments, selecting the highest version.
func ResolveGeminiFamilyFallback(modelName string) string {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return ""
	}

	lower := strings.ToLower(modelName)
	cleanName := modelName
	cleanLower := lower
	if strings.HasPrefix(lower, "models/") {
		cleanName = strings.TrimPrefix(cleanName, "models/")
		cleanLower = strings.TrimPrefix(cleanLower, "models/")
	}

	if !strings.HasPrefix(cleanLower, "gemini-") {
		return ""
	}

	hasFlash := strings.Contains(cleanLower, "flash")
	hasPro := strings.Contains(cleanLower, "pro")
	if !hasFlash && !hasPro {
		return ""
	}

	family := "flash"
	if hasPro && !hasFlash {
		family = "pro"
	} else if hasFlash && hasPro {
		if strings.Index(cleanLower, "pro") < strings.Index(cleanLower, "flash") {
			family = "pro"
		}
	}

	reg := registry.GetGlobalRegistry()
	if reg == nil {
		return ""
	}

	// Fail-closed when the model exists on the registry/server but has no available provider/scope.
	if reg.HasModel(modelName) || reg.HasModel(cleanName) {
		return ""
	}

	candidates := getAvailableGeminiCandidates(reg, family)
	if len(candidates) == 0 {
		return ""
	}

	sort.Slice(candidates, func(i, j int) bool {
		return CompareModelVersions(candidates[i], candidates[j]) < 0
	})

	return candidates[len(candidates)-1]
}

func getAvailableGeminiCandidates(reg *registry.ModelRegistry, family string) []string {
	if reg == nil {
		return nil
	}

	seen := make(map[string]struct{})
	var candidates []string

	addCandidate := func(candID string) {
		candID = strings.TrimSpace(candID)
		if candID == "" {
			return
		}
		if _, exists := seen[candID]; exists {
			return
		}
		seen[candID] = struct{}{}

		lower := strings.ToLower(candID)
		if strings.HasPrefix(lower, "models/") {
			candID = strings.TrimPrefix(candID, "models/")
			lower = strings.TrimPrefix(lower, "models/")
		}

		if !strings.HasPrefix(lower, "gemini-") {
			return
		}

		switch family {
		case "flash":
			if !strings.Contains(lower, "flash") {
				return
			}
		case "pro":
			if !strings.Contains(lower, "pro") {
				return
			}
		default:
			return
		}

		if len(GetProviderName(candID)) == 0 {
			return
		}

		candidates = append(candidates, candID)
	}

	// Provider "gemini" available models
	for _, info := range reg.GetAvailableModelsByProvider("gemini") {
		if info != nil {
			id := info.ID
			if id == "" && info.Name != "" {
				id = info.Name
			}
			addCandidate(id)
		}
	}

	// Provider "gemini-interactions" available models
	for _, info := range reg.GetAvailableModelsByProvider("gemini-interactions") {
		if info != nil {
			id := info.ID
			if id == "" && info.Name != "" {
				id = info.Name
			}
			addCandidate(id)
		}
	}

	// Fallback to general available model infos if provider-specific lists were empty
	if len(candidates) == 0 {
		for _, info := range reg.GetAvailableModelInfos() {
			if info != nil {
				id := info.ID
				if id == "" && info.Name != "" {
					id = info.Name
				}
				addCandidate(id)
			}
		}
	}

	return candidates
}

// ExtractNumericSegments extracts consecutive digits as int64 numbers from a string.
func ExtractNumericSegments(s string) []int64 {
	var nums []int64
	var current int64
	inNum := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= '0' && c <= '9' {
			if !inNum {
				inNum = true
				current = int64(c - '0')
			} else {
				current = current*10 + int64(c-'0')
			}
		} else {
			if inNum {
				nums = append(nums, current)
				inNum = false
				current = 0
			}
		}
	}
	if inNum {
		nums = append(nums, current)
	}
	return nums
}

// CompareModelVersions compares two model identifier strings by extracting numeric
// segments and comparing them segment-by-segment. If numeric segments are identical,
// lexicographical comparison is used as a tie-breaker.
// Returns -1 if a < b, 1 if a > b, and 0 if a == b.
func CompareModelVersions(a, b string) int {
	numsA := ExtractNumericSegments(a)
	numsB := ExtractNumericSegments(b)
	maxLen := len(numsA)
	if len(numsB) > maxLen {
		maxLen = len(numsB)
	}
	for i := 0; i < maxLen; i++ {
		var valA, valB int64
		if i < len(numsA) {
			valA = numsA[i]
		}
		if i < len(numsB) {
			valB = numsB[i]
		}
		if valA < valB {
			return -1
		}
		if valA > valB {
			return 1
		}
	}
	if len(numsA) < len(numsB) {
		return -1
	}
	if len(numsA) > len(numsB) {
		return 1
	}
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}
