package registry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	log "github.com/sirupsen/logrus"
)

// CodexClientModelsOverrideFileName is the file name of the local Codex client
// model override layer, resolved next to the active configuration file.
const CodexClientModelsOverrideFileName = "codex_client_models_override.json"

// CodexClientModelsOverrideMaxFileSize bounds the override document in both
// directions: a file above it is ignored, and a document that would serialize
// above it is refused instead of being persisted.
const CodexClientModelsOverrideMaxFileSize = 1 << 20

// The override file is a JSON Merge Patch (RFC 7386) document keyed by Codex client
// model slug, for example:
//
//	{
//	  "gpt-5.6-sol": {"display_name": "Local Sol"},
//	  "my-local-model": {"slug": "my-local-model"},
//	  "obsolete-model": null
//	}
//
// The entries the server assembles for the models it can serve are the default
// configuration, and this document is applied on top of them: patches merge field by
// field (nested objects merge recursively, scalars and arrays replace, null removes),
// a slug without a patch is served exactly as assembled, and a null patch keeps the
// model out of the served list. Problems are reported per entry and never change the
// rest of the list: an override that cannot be applied leaves its model on the default
// entry.

// CodexClientModelsOrigin reports where the entry of a served model comes from.
type CodexClientModelsOrigin string

const (
	// CodexClientModelsOriginBase marks a model that is served without a local override.
	CodexClientModelsOriginBase CodexClientModelsOrigin = "base"
	// CodexClientModelsOriginOverride marks a served model with a local override.
	CodexClientModelsOriginOverride CodexClientModelsOrigin = "override"
	// CodexClientModelsOriginServed marks a served model the base catalog has no entry
	// for, so the default template supplied its entry.
	CodexClientModelsOriginServed CodexClientModelsOrigin = "served"
	// CodexClientModelsOriginRemoved marks a served model that a null override keeps out
	// of the served list.
	CodexClientModelsOriginRemoved CodexClientModelsOrigin = "removed"
	// CodexClientModelsOriginUnserved marks an override for a model no provider serves,
	// which therefore has no effect.
	CodexClientModelsOriginUnserved CodexClientModelsOrigin = "unserved"
)

// CodexClientModelsState is a consistent snapshot of the base Codex client model
// catalog source together with the local override layer that shapes the served entries.
type CodexClientModelsState struct {
	// Source names where the base catalog came from: "embed" or a source URL.
	Source string `json:"source"`
	// Revision changes whenever the base catalog or the override layer changes.
	Revision uint64 `json:"revision"`
	// Override holds the local override document keyed by model slug.
	Override map[string]json.RawMessage `json:"override"`
	// OverridePath is the override file location; empty when no location is known.
	OverridePath string `json:"override_path"`
	// OverrideError reports why the on-disk override file could not be read.
	OverrideError string `json:"override_error"`
}

// GetCodexClientModelsState returns the base catalog source and the local override
// layer that shapes the entries the server assembles for it.
func GetCodexClientModelsState() CodexClientModelsState {
	store := codexClientCatalogStore
	store.mu.RLock()
	defer store.mu.RUnlock()
	return CodexClientModelsState{
		Source:        store.baseSource,
		Revision:      store.revision,
		Override:      cloneCodexClientModelsOverride(store.override),
		OverridePath:  store.overridePath,
		OverrideError: store.overrideError,
	}
}

// GetCodexClientModelsOverride returns the local override document keyed by model slug.
func GetCodexClientModelsOverride() map[string]json.RawMessage {
	store := codexClientCatalogStore
	store.mu.RLock()
	defer store.mu.RUnlock()
	return cloneCodexClientModelsOverride(store.override)
}

// SetCodexClientModelsOverride replaces the local override layer with the given
// JSON Merge Patch document and persists it next to the configuration file. An
// empty document clears the layer. The override takes effect only when the merged
// catalog stays valid.
func SetCodexClientModelsOverride(document []byte) error {
	doc, errDecode := decodeCodexClientModelsOverride(document)
	if errDecode != nil {
		return errDecode
	}
	store := codexClientCatalogStore
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.applyOverrideLocked(doc)
}

// SetCodexClientModelsOverrideEntry replaces the override patch of a single model
// slug. Other overrides stay untouched.
func SetCodexClientModelsOverrideEntry(slug string, patch json.RawMessage) error {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return rejectCodexClientModelsOverride(errors.New("model slug is required"))
	}
	store := codexClientCatalogStore
	store.mu.Lock()
	defer store.mu.Unlock()
	doc := cloneCodexClientModelsOverride(store.override)
	doc[slug] = append(json.RawMessage(nil), patch...)
	return store.applyOverrideLocked(doc)
}

// DeleteCodexClientModelsOverrideEntry removes the override patch of a single
// model slug and clears the layer once no overrides remain.
func DeleteCodexClientModelsOverrideEntry(slug string) error {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return rejectCodexClientModelsOverride(errors.New("model slug is required"))
	}
	store := codexClientCatalogStore
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, ok := store.override[slug]; !ok {
		return nil
	}
	doc := cloneCodexClientModelsOverride(store.override)
	delete(doc, slug)
	return store.applyOverrideLocked(doc)
}

// ClearCodexClientModelsOverride removes the local override layer and its file.
func ClearCodexClientModelsOverride() error {
	return SetCodexClientModelsOverride(nil)
}

// SyncCodexClientModelsOverrideFile loads the override file that belongs to the given
// configuration file. It is safe to call at startup and on every configuration reload:
// a missing file clears the override layer, and a file that cannot be read or parsed
// keeps the last one and reports the error in CodexClientModelsState.
func SyncCodexClientModelsOverrideFile(configFilePath string) {
	store := codexClientCatalogStore
	path := codexClientModelsOverridePath(configFilePath)

	store.mu.Lock()
	defer store.mu.Unlock()
	store.overridePath = path

	doc, errRead := readCodexClientModelsOverrideFile(path)
	if errRead != nil {
		store.overrideError = errRead.Error()
		log.Warnf("Codex client model override file %s ignored: %v", path, errRead)
		return
	}
	if errShape := validateCodexClientModelsOverrideDocument(doc); errShape != nil {
		store.overrideError = errShape.Error()
		log.Warnf("Codex client model override file %s ignored: %v", path, errShape)
		return
	}

	previous := store.override
	store.override = doc
	store.overrideError = ""
	if !overrideDocumentsEqual(previous, doc) {
		store.revision++
	}
}

// override document instead of a state or filesystem failure.
func IsCodexClientModelsOverrideRejected(err error) bool {
	var rejected *codexClientModelsOverrideRejected
	return errors.As(err, &rejected)
}

type codexClientModelsOverrideRejected struct {
	err error
}

func (e *codexClientModelsOverrideRejected) Error() string { return e.err.Error() }

func (e *codexClientModelsOverrideRejected) Unwrap() error { return e.err }

func rejectCodexClientModelsOverride(err error) error {
	return &codexClientModelsOverrideRejected{err: err}
}

// applyOverrideLocked validates the shape of the override document, persists it and
// publishes a new revision. Problems that need the served models are reported when the
// entries are resolved. Callers must hold the store lock.
func (s *codexClientModelsStore) applyOverrideLocked(doc map[string]json.RawMessage) error {
	if strings.TrimSpace(s.overridePath) == "" {
		return errors.New("codex client model override file location is not configured")
	}
	if errShape := validateCodexClientModelsOverrideDocument(doc); errShape != nil {
		return rejectCodexClientModelsOverride(errShape)
	}

	if len(doc) == 0 {
		if errRemove := removeCodexClientModelsOverrideFile(s.overridePath); errRemove != nil {
			return errRemove
		}
	} else if errWrite := writeCodexClientModelsOverrideFile(s.overridePath, doc); errWrite != nil {
		return errWrite
	}

	// The document just persisted replaces whatever the previous on-disk content
	// reported, so the diagnostics that described it must not survive this write.
	previous := s.override
	s.override = doc
	s.overrideError = ""
	if !overrideDocumentsEqual(previous, doc) {
		s.revision++
	}
	return nil
}

// validateCodexClientModelsOverrideDocument checks the shape of an override document:
// every entry must be a JSON object or null. Problems that need the served models, such
// as an unknown inheritance source, are reported when the entries are resolved.
func validateCodexClientModelsOverrideDocument(doc map[string]json.RawMessage) error {
	for slug, patch := range doc {
		if strings.TrimSpace(slug) == "" {
			return errors.New("override contains an empty model slug")
		}
		if len(bytes.TrimSpace(patch)) == 0 {
			return fmt.Errorf("model %q override is empty", slug)
		}
		var decoded any
		if errUnmarshal := json.Unmarshal(patch, &decoded); errUnmarshal != nil {
			return fmt.Errorf("model %q override: %w", slug, errUnmarshal)
		}
		if decoded == nil {
			continue
		}
		if _, ok := decoded.(map[string]any); !ok {
			return fmt.Errorf("model %q override must be a JSON object or null", slug)
		}
	}
	return nil
}

// overrideDocumentsEqual reports whether two override documents hold the same entries,
// so a reload that changes nothing does not publish a new revision.
func overrideDocumentsEqual(left, right map[string]json.RawMessage) bool {
	if len(left) != len(right) {
		return false
	}
	for slug, patch := range left {
		other, ok := right[slug]
		if !ok || !bytes.Equal(bytes.TrimSpace(patch), bytes.TrimSpace(other)) {
			return false
		}
	}
	return true
}

// ResolveCodexClientModelOverrides applies the local override document to the entries
// the server assembled for the models it can serve.
//
// The assembled entries are the default configuration: a model without an override is
// served exactly as assembled, a patch replaces or removes fields of it, and a null
// patch keeps the model out of the served list. An override that cannot be applied is
// reported and its model keeps the default entry, so one broken entry never changes the
// rest of the list.
func ResolveCodexClientModelOverrides(
	defaults map[string]map[string]any,
	doc map[string]json.RawMessage,
) (map[string]map[string]any, []CodexClientModelsOverrideIssue) {
	served := make(map[string]map[string]any, len(defaults))
	for slug, entry := range defaults {
		served[slug] = entry
	}
	if len(doc) == 0 {
		return served, nil
	}

	slugs := make([]string, 0, len(doc))
	for slug := range doc {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)

	var issues []CodexClientModelsOverrideIssue
	// fail records a problem with a single override entry. The model keeps the default
	// entry, so the rest of the document still applies.
	fail := func(slug, fieldPath string, err error) {
		issues = append(issues, CodexClientModelsOverrideIssue{Slug: slug, Path: fieldPath, Error: err.Error()})
	}

	removed := make(map[string]struct{}, len(slugs))
	local := make(map[string]map[string]any, len(slugs))
	sources := make(map[string][]codexClientModelsInheritSource, len(slugs))
	for _, slug := range slugs {
		if strings.TrimSpace(slug) == "" {
			fail(slug, "", errors.New("override contains an empty model slug"))
			continue
		}
		var patch any
		if errUnmarshal := json.Unmarshal(doc[slug], &patch); errUnmarshal != nil {
			fail(slug, "", errUnmarshal)
			continue
		}
		if patch == nil {
			removed[slug] = struct{}{}
			continue
		}
		patchObject, ok := patch.(map[string]any)
		if !ok {
			fail(slug, "", errors.New("override must be a JSON object or null"))
			continue
		}
		if patchedSlug, okSlug := patchObject["slug"].(string); okSlug && strings.TrimSpace(patchedSlug) != slug {
			fail(slug, "slug", fmt.Errorf("override must not change the slug to %q", patchedSlug))
			continue
		}
		if !hasCodexClientModelsModel(defaults, slug) {
			// Nothing is served under this slug, so there is no entry to override.
			fail(slug, "", errors.New("model is not served by any provider, the override has no effect"))
			continue
		}
		entrySources, entryLocal, errSplit := splitCodexClientModelsInherit(patchObject)
		if errSplit != nil {
			fail(slug, "", errSplit)
			continue
		}
		local[slug] = entryLocal
		sources[slug] = entrySources
	}

	for _, slug := range slugs {
		for _, source := range sources[slug] {
			if source.disabled {
				// An opt-out names no model, so there is nothing to resolve.
				continue
			}
			var errSource error
			switch {
			case source.slug == slug:
				errSource = errors.New("a model cannot inherit from itself")
			case hasCodexClientModelsSlug(removed, source.slug):
				errSource = fmt.Errorf("inheritance source %q is removed by this document", source.slug)
			case !hasCodexClientModelsModel(defaults, source.slug):
				errSource = fmt.Errorf("inheritance source %q is not a served model", source.slug)
			}
			if errSource == nil {
				continue
			}
			fail(slug, source.path, errSource)
			delete(local, slug)
			delete(sources, slug)
			break
		}
	}

	// A chain of inheritance sources is allowed up to a fixed number of hops. The
	// check is static so the outcome does not depend on the order entries resolve
	// in, and it counts the chain each field resolution actually follows.
	chainDepths := make(map[string]int, len(sources))
	chainVisiting := make(map[string]bool, len(sources))
	for _, slug := range slugs {
		if len(sources[slug]) == 0 {
			continue
		}
		depth, resolved := codexClientModelsInheritFieldDepth(slug, "", sources, chainDepths, chainVisiting)
		if !resolved || depth <= maxCodexClientModelsInheritDepth {
			continue
		}
		fail(slug, "", fmt.Errorf("inheritance chain is %d hops deep, the limit is %d hops", depth, maxCodexClientModelsInheritDepth))
		delete(local, slug)
		delete(sources, slug)
	}

	resolver := newCodexClientModelsInheritance(defaults, local, sources)
	for _, slug := range slugs {
		if _, hidden := removed[slug]; hidden {
			delete(served, slug)
			continue
		}
		if _, patched := local[slug]; !patched {
			continue
		}
		resolved, errResolve := resolver.entry(slug)
		if errResolve == nil {
			errResolve = validateCodexClientModel(resolved)
		}
		if errResolve != nil {
			fail(slug, "", errResolve)
			continue
		}
		served[slug] = resolved
	}
	return served, issues
}

func hasCodexClientModelsSlug(set map[string]struct{}, slug string) bool {
	_, ok := set[slug]
	return ok
}

func hasCodexClientModelsModel(set map[string]map[string]any, slug string) bool {
	_, ok := set[slug]
	return ok
}

// mergeCodexClientModelsPatch merges patch into target following RFC 7386: nested
// objects merge recursively, other values replace the target, and null removes the key.
func mergeCodexClientModelsPatch(target, patch map[string]any) map[string]any {
	merged := make(map[string]any, len(target)+len(patch))
	for key, value := range target {
		merged[key] = value
	}
	for key, value := range patch {
		if value == nil {
			delete(merged, key)
			continue
		}
		patchObject, ok := value.(map[string]any)
		if !ok {
			merged[key] = value
			continue
		}
		// Merging an object into a missing or non-object target follows RFC 7386 by
		// treating the target as an empty object.
		targetObject, _ := merged[key].(map[string]any)
		merged[key] = mergeCodexClientModelsPatch(targetObject, patchObject)
	}
	return merged
}

// parseCodexClientModels returns the catalog entries in file order and a slug index.
func parseCodexClientModels(data []byte) ([]map[string]any, map[string]int, error) {
	var payload codexClientModelsPayload
	if errUnmarshal := json.Unmarshal(data, &payload); errUnmarshal != nil {
		return nil, nil, fmt.Errorf("decode Codex client model catalog: %w", errUnmarshal)
	}
	index := make(map[string]int, len(payload.Models))
	for position, model := range payload.Models {
		slug, _ := model["slug"].(string)
		index[strings.TrimSpace(slug)] = position
	}
	return payload.Models, index, nil
}

func decodeCodexClientModelsOverride(document []byte) (map[string]json.RawMessage, error) {
	if len(bytes.TrimSpace(document)) == 0 {
		return map[string]json.RawMessage{}, nil
	}
	var doc map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(document, &doc); errUnmarshal != nil {
		return nil, rejectCodexClientModelsOverride(fmt.Errorf("override document must be a JSON object: %w", errUnmarshal))
	}
	if doc == nil {
		return map[string]json.RawMessage{}, nil
	}
	return doc, nil
}

func cloneCodexClientModelsOverride(doc map[string]json.RawMessage) map[string]json.RawMessage {
	cloned := make(map[string]json.RawMessage, len(doc)+1)
	for slug, patch := range doc {
		cloned[slug] = append(json.RawMessage(nil), patch...)
	}
	return cloned
}

// codexClientModelsOverridePath resolves the override file that belongs to a
// configuration file. It is empty when no location is available.
func codexClientModelsOverridePath(configFilePath string) string {
	configFilePath = strings.TrimSpace(configFilePath)
	if configFilePath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(configFilePath), CodexClientModelsOverrideFileName)
}

func readCodexClientModelsOverrideFile(path string) (map[string]json.RawMessage, error) {
	if strings.TrimSpace(path) == "" {
		return map[string]json.RawMessage{}, nil
	}
	info, errStat := os.Stat(path)
	if errStat != nil {
		if errors.Is(errStat, os.ErrNotExist) {
			return map[string]json.RawMessage{}, nil
		}
		return nil, fmt.Errorf("stat override file: %w", errStat)
	}
	if info.Size() > CodexClientModelsOverrideMaxFileSize {
		return nil, fmt.Errorf("override file exceeds %d bytes", CodexClientModelsOverrideMaxFileSize)
	}
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		return nil, fmt.Errorf("read override file: %w", errRead)
	}
	return decodeCodexClientModelsOverride(data)
}

func writeCodexClientModelsOverrideFile(path string, doc map[string]json.RawMessage) error {
	data, errMarshal := json.MarshalIndent(doc, "", "  ")
	if errMarshal != nil {
		return fmt.Errorf("encode override document: %w", errMarshal)
	}
	data = append(data, '\n')
	// The reader refuses a file above the cap, so a document the next reload would
	// drop must not be reported as a successful change.
	if len(data) > CodexClientModelsOverrideMaxFileSize {
		return rejectCodexClientModelsOverride(fmt.Errorf("override file exceeds %d bytes", CodexClientModelsOverrideMaxFileSize))
	}

	dir := filepath.Dir(path)
	if errMkdir := os.MkdirAll(dir, 0o755); errMkdir != nil {
		return fmt.Errorf("create override directory: %w", errMkdir)
	}
	tmpFile, errCreate := os.CreateTemp(dir, CodexClientModelsOverrideFileName+".*.tmp")
	if errCreate != nil {
		return fmt.Errorf("create temporary override file: %w", errCreate)
	}
	tmpPath := tmpFile.Name()
	defer func() {
		if errRemove := os.Remove(tmpPath); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
			log.Debugf("failed to remove temporary override file %s: %v", tmpPath, errRemove)
		}
	}()

	if _, errWrite := tmpFile.Write(data); errWrite != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("write override file: %w", errWrite)
	}
	if errClose := tmpFile.Close(); errClose != nil {
		return fmt.Errorf("close override file: %w", errClose)
	}
	if errRename := os.Rename(tmpPath, path); errRename != nil {
		return fmt.Errorf("replace override file: %w", errRename)
	}
	return nil
}

func removeCodexClientModelsOverrideFile(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if errRemove := os.Remove(path); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
		return fmt.Errorf("remove override file: %w", errRemove)
	}
	return nil
}
