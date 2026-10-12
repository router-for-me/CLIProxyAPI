package registry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// codexClientLiveOverlayStore holds Codex client model entries discovered live
// from the provider with an OAuth credential. They are laid over whichever base
// catalog is installed (embedded or remote), so a model the base does not carry
// yet is served to Codex clients with its real metadata.
type codexClientLiveOverlayStore struct {
	mu sync.Mutex
	// base is the last validated base catalog handed to apply.
	base []byte
	// order keeps the slugs in the order they were first discovered.
	order []string
	// models maps a slug to its live catalog entry.
	models map[string]json.RawMessage
}

var codexClientLiveOverlay = &codexClientLiveOverlayStore{}

type codexClientRawModelsPayload struct {
	Models []json.RawMessage `json:"models"`
}

// apply remembers base and returns it with the live entries laid over it. A live
// entry replaces the base entry with the same slug and is appended otherwise.
// The base is returned unchanged when there is nothing to lay over it or the
// result would not be a valid catalog.
func (o *codexClientLiveOverlayStore) apply(base []byte) []byte {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.base = append([]byte(nil), base...)
	if len(o.models) == 0 {
		return base
	}

	var payload codexClientRawModelsPayload
	if err := json.Unmarshal(base, &payload); err != nil {
		return base
	}
	merged := make([]json.RawMessage, 0, len(payload.Models)+len(o.models))
	used := make(map[string]struct{}, len(o.models))
	changed := false
	for _, entry := range payload.Models {
		slug := codexClientModelSlug(entry)
		if live, ok := o.models[slug]; ok {
			used[slug] = struct{}{}
			if !bytes.Equal(live, entry) {
				changed = true
			}
			merged = append(merged, live)
			continue
		}
		merged = append(merged, entry)
	}
	for _, slug := range o.order {
		if _, ok := used[slug]; ok {
			continue
		}
		merged = append(merged, o.models[slug])
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

// update stores the valid entries of a live catalog payload and reports the
// base catalog to reinstall when any entry is new or changed.
func (o *codexClientLiveOverlayStore) update(payload []byte) ([]byte, int, error) {
	var parsed codexClientRawModelsPayload
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return nil, 0, fmt.Errorf("decode live Codex model catalog: %w", err)
	}
	if len(parsed.Models) == 0 {
		return nil, 0, fmt.Errorf("live Codex model catalog has no models")
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	if o.models == nil {
		o.models = make(map[string]json.RawMessage)
	}
	accepted := 0
	changed := false
	for _, entry := range parsed.Models {
		var model map[string]any
		if err := json.Unmarshal(entry, &model); err != nil {
			continue
		}
		slug, err := requiredCodexClientModelString(model, "slug")
		if err != nil || validateCodexClientModel(model) != nil {
			continue
		}
		accepted++
		compact := new(bytes.Buffer)
		if err = json.Compact(compact, entry); err != nil {
			continue
		}
		previous, known := o.models[slug]
		if known && bytes.Equal(previous, compact.Bytes()) {
			continue
		}
		if !known {
			o.order = append(o.order, slug)
		}
		o.models[slug] = json.RawMessage(compact.Bytes())
		changed = true
	}
	if accepted == 0 {
		return nil, 0, fmt.Errorf("live Codex model catalog has no valid models")
	}
	if !changed || len(o.base) == 0 {
		return nil, accepted, nil
	}
	return append([]byte(nil), o.base...), accepted, nil
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

// MergeLiveCodexClientModels lays the entries of a Codex model catalog fetched
// live from the provider over the installed catalog. Entries are keyed by slug:
// a live entry replaces the installed one, and slugs the payload does not carry
// are kept. Entries that fail catalog validation are skipped. It reports whether
// the served catalog changed.
func MergeLiveCodexClientModels(payload []byte) (bool, error) {
	base, _, err := codexClientLiveOverlay.update(payload)
	if err != nil {
		return false, err
	}
	if len(base) == 0 {
		return false, nil
	}
	return loadCodexClientModelsFromBytes(base, "live")
}
