package registry

import (
	"slices"
	"strings"
	"sync"
)

// devinUpstreamMaxCompletionTokens is the completion budget used for upstream
// models that the curated Devin catalog does not describe.
const devinUpstreamMaxCompletionTokens = 64000

// DevinUpstreamModel is one raw entry returned by Devin's GetCliModelConfigs RPC.
type DevinUpstreamModel struct {
	UID           string
	Label         string
	ContextLength int
	Multimodal    bool
	VendorID      uint64
}

// devinUpstreamStore keeps the latest upstream-fetched definition per model ID
// so request-time UID resolution can see models missing from the catalog.
var devinUpstreamStore = struct {
	sync.RWMutex
	models map[string]*ModelInfo
}{models: make(map[string]*ModelInfo)}

// DevinVendorName maps a Devin vendor enum value to an owner name.
func DevinVendorName(id uint64, uid string) string {
	switch id {
	case 1:
		return "cognition"
	case 2:
		return "openai"
	case 3:
		return "anthropic"
	case 4:
		return "google"
	case 6:
		return "deepseek"
	case 7:
		return "moonshot"
	case 9:
		return "zhipu"
	case 11:
		return "nvidia"
	default:
		if strings.Contains(strings.ToLower(uid), "grok") {
			return "xai"
		}
		return "devin"
	}
}

// BuildDevinModelsFromUpstream aggregates raw upstream variants (e.g. "gpt-6-astra-high")
// into namespaced base models with thinking levels. Thinking levels and completion
// limits curated in the Devin catalog are preserved for models it already describes.
func BuildDevinModelsFromUpstream(raw []DevinUpstreamModel) []*ModelInfo {
	variants := make([]*ModelInfo, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, r := range raw {
		uid := strings.ToLower(strings.TrimSpace(r.UID))
		if uid == "" {
			continue
		}
		if _, exists := seen[uid]; exists {
			continue
		}
		seen[uid] = struct{}{}
		inputModalities := []string{"text"}
		if r.Multimodal {
			inputModalities = append(inputModalities, "image")
		}
		variants = append(variants, &ModelInfo{
			ID:                        uid,
			Object:                    "model",
			Type:                      "devin",
			OwnedBy:                   DevinVendorName(r.VendorID, uid),
			DisplayName:               strings.TrimSpace(r.Label),
			ContextLength:             r.ContextLength,
			SupportedInputModalities:  inputModalities,
			SupportedOutputModalities: []string{"text"},
		})
	}
	if len(variants) == 0 {
		return nil
	}

	models := aggregateDevinModels(variants)
	for _, m := range models {
		catalog := lookupDevinCatalogModel(m.ID)
		if catalog != nil && catalog.Thinking != nil && len(catalog.Thinking.Levels) > 0 {
			levels := slices.Clone(catalog.Thinking.Levels)
			if m.Thinking != nil {
				levels = append(levels, m.Thinking.Levels...)
			}
			m.Thinking = &ThinkingSupport{Levels: sortDevinLevels(levels)}
		}
		if catalog != nil && catalog.MaxCompletionTokens > 0 {
			m.MaxCompletionTokens = catalog.MaxCompletionTokens
		}
		if m.MaxCompletionTokens == 0 {
			m.MaxCompletionTokens = devinUpstreamMaxCompletionTokens
		}
		m.OutputTokenLimit = m.MaxCompletionTokens
	}
	return WithDevinBuiltins(models)
}

// RecordDevinUpstreamModels publishes upstream-fetched models for request-time lookups.
func RecordDevinUpstreamModels(models []*ModelInfo) {
	devinUpstreamStore.Lock()
	defer devinUpstreamStore.Unlock()
	for _, m := range models {
		if m == nil || m.ID == "" {
			continue
		}
		devinUpstreamStore.models[strings.ToLower(m.ID)] = cloneModelInfo(m)
	}
}

func lookupDevinUpstreamModel(cleanID string) *ModelInfo {
	devinUpstreamStore.RLock()
	defer devinUpstreamStore.RUnlock()
	if m := devinUpstreamStore.models["devin/"+cleanID]; m != nil {
		return cloneModelInfo(m)
	}
	return nil
}

// lookupDevinCatalogModel finds a model in the curated catalog only, ignoring upstream data.
func lookupDevinCatalogModel(modelID string) *ModelInfo {
	clean := strings.ToLower(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(modelID)), "devin/"))
	for _, m := range GetDevinModels() {
		if strings.TrimPrefix(strings.ToLower(m.ID), "devin/") == clean {
			return m
		}
	}
	return nil
}

func sortDevinLevels(levels []string) []string {
	unique := make([]string, 0, len(levels))
	for _, l := range levels {
		if l != "" && l != "priority" && !slices.Contains(unique, l) {
			unique = append(unique, l)
		}
	}
	slices.SortStableFunc(unique, func(a, b string) int {
		rankA, okA := devinLevelOrder[a]
		if !okA {
			rankA = 99
		}
		rankB, okB := devinLevelOrder[b]
		if !okB {
			rankB = 99
		}
		if rankA != rankB {
			return rankA - rankB
		}
		return strings.Compare(a, b)
	})
	return unique
}
