package registry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// codexClientLiveOverlayStore holds the Codex client model entries discovered
// live from the provider, per OAuth credential. They are laid over whichever
// base catalog is installed (embedded or remote), so a model the base does not
// carry yet is served to Codex clients with its real metadata.
type codexClientLiveOverlayStore struct {
	mu sync.Mutex
	// base is the last validated base catalog installed.
	base []byte
	// sources maps a credential ID to the valid entries it reported, in the
	// order the provider listed them.
	sources map[string][]codexClientLiveEntry
}

type codexClientLiveEntry struct {
	slug string
	raw  json.RawMessage
}

var codexClientLiveOverlay = &codexClientLiveOverlayStore{}

type codexClientRawModelsPayload struct {
	Models []json.RawMessage `json:"models"`
}

// install serves base with the live entries laid over it and reports whether
// the served catalog changed. The overlay stays locked until the result is
// stored, so a concurrent overlay change or base refresh is never lost.
func (o *codexClientLiveOverlayStore) install(base []byte) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.base = append([]byte(nil), base...)
	return storeCodexClientModels(o.applyLocked(o.base))
}

// reinstallLocked serves the last installed base again with the current live
// entries laid over it.
func (o *codexClientLiveOverlayStore) reinstallLocked() bool {
	if len(o.base) == 0 {
		return false
	}
	return storeCodexClientModels(o.applyLocked(o.base))
}

// liveEntriesLocked returns the live entry served for each slug. Credentials
// are read in ID order and the first to report a slug provides its entry, so
// the served catalog does not depend on which credential was read last.
func (o *codexClientLiveOverlayStore) liveEntriesLocked() ([]string, map[string]json.RawMessage) {
	ids := make([]string, 0, len(o.sources))
	for id := range o.sources {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var order []string
	entries := make(map[string]json.RawMessage)
	for _, id := range ids {
		for _, entry := range o.sources[id] {
			if _, ok := entries[entry.slug]; ok {
				continue
			}
			entries[entry.slug] = entry.raw
			order = append(order, entry.slug)
		}
	}
	return order, entries
}

// applyLocked returns base with the live entries laid over it. A live entry
// replaces the base entry with the same slug and is appended otherwise. The
// base is returned unchanged when there is nothing to lay over it or the
// result would not be a valid catalog.
func (o *codexClientLiveOverlayStore) applyLocked(base []byte) []byte {
	order, entries := o.liveEntriesLocked()
	if len(entries) == 0 {
		return base
	}
	var payload codexClientRawModelsPayload
	if err := json.Unmarshal(base, &payload); err != nil {
		return base
	}
	merged := make([]json.RawMessage, 0, len(payload.Models)+len(entries))
	used := make(map[string]struct{}, len(entries))
	changed := false
	for _, entry := range payload.Models {
		slug := codexClientModelSlug(entry)
		if live, ok := entries[slug]; ok {
			used[slug] = struct{}{}
			if !bytes.Equal(live, entry) {
				changed = true
			}
			merged = append(merged, live)
			continue
		}
		merged = append(merged, entry)
	}
	for _, slug := range order {
		if _, ok := used[slug]; ok {
			continue
		}
		merged = append(merged, entries[slug])
		changed = true
	}
	if !changed {
		return base
	}
	data, err := json.Marshal(codexClientRawModelsPayload{Models: merged})
	if err != nil || ValidateCodexClientModelsJSON(data) != nil {
		return base
	}
	return data
}

// update replaces the live entries of one credential with the valid entries of
// a catalog payload it read, and reports whether the served catalog changed.
func (o *codexClientLiveOverlayStore) update(authID string, payload []byte) (bool, error) {
	var parsed codexClientRawModelsPayload
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return false, fmt.Errorf("decode live Codex model catalog: %w", err)
	}
	if len(parsed.Models) == 0 {
		return false, fmt.Errorf("live Codex model catalog has no models")
	}
	entries := make([]codexClientLiveEntry, 0, len(parsed.Models))
	seen := make(map[string]struct{}, len(parsed.Models))
	for _, raw := range parsed.Models {
		var model map[string]any
		if err := json.Unmarshal(raw, &model); err != nil {
			continue
		}
		slug, err := requiredCodexClientModelString(model, "slug")
		if err != nil || validateCodexClientModel(model) != nil {
			continue
		}
		if _, duplicate := seen[slug]; duplicate {
			continue
		}
		compact := new(bytes.Buffer)
		if err = json.Compact(compact, raw); err != nil {
			continue
		}
		seen[slug] = struct{}{}
		entries = append(entries, codexClientLiveEntry{slug: slug, raw: compact.Bytes()})
	}
	if len(entries) == 0 {
		return false, fmt.Errorf("live Codex model catalog has no valid models")
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	if sameCodexClientLiveEntries(o.sources[authID], entries) {
		return false, nil
	}
	if o.sources == nil {
		o.sources = make(map[string][]codexClientLiveEntry)
	}
	o.sources[authID] = entries
	return o.reinstallLocked(), nil
}

// retain drops the live entries of every credential not listed and reports
// whether the served catalog changed.
func (o *codexClientLiveOverlayStore) retain(authIDs []string) bool {
	keep := make(map[string]struct{}, len(authIDs))
	for _, id := range authIDs {
		keep[id] = struct{}{}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	removed := false
	for id := range o.sources {
		if _, ok := keep[id]; !ok {
			delete(o.sources, id)
			removed = true
		}
	}
	if !removed {
		return false
	}
	return o.reinstallLocked()
}

func sameCodexClientLiveEntries(a, b []codexClientLiveEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].slug != b[i].slug || !bytes.Equal(a[i].raw, b[i].raw) {
			return false
		}
	}
	return true
}

func codexClientModelSlug(entry json.RawMessage) string {
	var model struct {
		Slug string `json:"slug"`
	}
	if err := json.Unmarshal(entry, &model); err != nil {
		return ""
	}
	return strings.TrimSpace(model.Slug)
}

// MergeLiveCodexClientModels lays the entries of a Codex model catalog that one
// OAuth credential read live from the provider over the installed catalog. A
// live entry replaces the installed entry with the same slug, and slugs the
// installed catalog lacks are added. Entries that fail catalog validation are
// skipped. When credentials report different entries for a slug, the one with
// the lowest credential ID is served. It reports whether the served catalog
// changed.
func MergeLiveCodexClientModels(authID string, payload []byte) (bool, error) {
	return codexClientLiveOverlay.update(authID, payload)
}

// RetainLiveCodexClientModels drops the live entries of every credential not
// listed and reports whether the served catalog changed. With no credential
// listed, the installed catalog is served alone again.
func RetainLiveCodexClientModels(authIDs []string) bool {
	return codexClientLiveOverlay.retain(authIDs)
}
