package models

import "strings"

// ApplyPatchCapabilityForModelFunc requires support from every actual routing
// candidate for the exact public model ID. false also represents unknown support.
type ApplyPatchCapabilityForModelFunc func(string) bool

func applyCodexClientApplyPatchCapability(entry map[string]any, id string, capability ApplyPatchCapabilityForModelFunc) {
	templateSupported := entry["apply_patch_tool_type"] == "freeform"
	entry["apply_patch_tool_type"] = nil
	// Built-in image, video, and speech IDs can inherit a generic text template in Home.
	// This classification is a model restriction, not routing capability evidence.
	baseID := strings.ToLower(strings.TrimSpace(id))
	if index := strings.LastIndex(baseID, "/"); index != -1 {
		baseID = strings.TrimSpace(baseID[index+1:])
	}
	if isCodexClientImageOrVideoModel(baseID) {
		return
	}

	// Hidden text models remain usable, but non-text catalog entries must not
	// advertise a conversation tool even when their executor supports it.
	supportsText := false
	hasModalities := false
	switch modalities := entry["input_modalities"].(type) {
	case []any:
		hasModalities = len(modalities) > 0
		for _, modality := range modalities {
			if modality == "text" {
				supportsText = true
			}
		}
	case []string:
		hasModalities = len(modalities) > 0
		for _, modality := range modalities {
			if modality == "text" {
				supportsText = true
			}
		}
	}
	if !supportsText && (hasModalities || entry["visibility"] == "hide") {
		return
	}
	if capability == nil {
		if templateSupported {
			entry["apply_patch_tool_type"] = "freeform"
		}
		return
	}
	if capability(strings.TrimSpace(id)) {
		entry["apply_patch_tool_type"] = "freeform"
	}
}
