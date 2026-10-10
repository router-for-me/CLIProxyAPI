package auth

import "strings"

// HydrateAuthFromMetadata applies typed fields that are mirrored in
// auth.Metadata. Call once after constructing an Auth from any store
// backend (filesystem, postgres, git, object-storage) to ensure the
// typed fields match the persisted metadata blob.
//
// Currently hydrates:
//   - Prefix — model-name routing prefix (e.g. "qwenai").
//   - Custom headers — per-credential HTTP headers.
//   - Disabled — marks the auth as disabled when metadata["disabled"] is true.
//
// This function is the single source of truth for metadata-to-typed-field
// mapping. When a new metadata key needs to populate a typed field, add
// it here instead of patching each store individually.
func HydrateAuthFromMetadata(auth *Auth) {
	if auth == nil || len(auth.Metadata) == 0 {
		return
	}
	hydratePrefix(auth)
	ApplyCustomHeadersFromMetadata(auth)
	hydrateDisabled(auth)
}

func hydrateDisabled(auth *Auth) {
	if disabled, ok := auth.Metadata["disabled"].(bool); ok && disabled {
		auth.Disabled = true
		auth.Status = StatusDisabled
	}
}

// hydratePrefix extracts the "prefix" key from auth.Metadata and sets
// auth.Prefix. The value is trimmed of whitespace and leading/trailing
// slashes; values containing a slash are rejected (a prefix is a single
// namespace segment like "qwenai", not a path).
//
// Validation logic is adopted from the watcher/synthesizer — the most
// mature implementation of prefix parsing in the codebase.
func hydratePrefix(auth *Auth) {
	raw, ok := auth.Metadata["prefix"].(string)
	if !ok {
		return
	}
	trimmed := strings.TrimSpace(raw)
	trimmed = strings.Trim(trimmed, "/")
	if trimmed == "" || strings.Contains(trimmed, "/") {
		return
	}
	auth.Prefix = trimmed
}
