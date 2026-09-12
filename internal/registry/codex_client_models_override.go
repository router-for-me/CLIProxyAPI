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

// maxCodexClientModelsOverrideFileSize bounds the override document read from disk.
const maxCodexClientModelsOverrideFileSize = 1 << 20

// The override file is a JSON Merge Patch (RFC 7386) document keyed by Codex client
// model slug, for example:
//
//	{
//	  "gpt-5.6-sol": {"display_name": "Local Sol"},
//	  "my-local-model": {"slug": "my-local-model", ...other required fields...},
//	  "obsolete-model": null
//	}
//
// Patches merge field by field: nested objects merge recursively, scalars and arrays
// replace the base value, and null removes the base field. A slug the base catalog
// does not define adds a new model entry, and a null patch removes a base entry from
// the effective catalog. The merged catalog must still pass
// ValidateCodexClientModelsJSON; otherwise the override is rejected and the current
// catalog stays in effect.

// CodexClientModelsOrigin reports where an effective catalog entry came from.
type CodexClientModelsOrigin string

const (
	// CodexClientModelsOriginBase marks an entry only the base catalog defines.
	CodexClientModelsOriginBase CodexClientModelsOrigin = "base"
	// CodexClientModelsOriginOverride marks a base entry the override layer patches.
	CodexClientModelsOriginOverride CodexClientModelsOrigin = "override"
	// CodexClientModelsOriginCustom marks an entry only the override layer defines.
	CodexClientModelsOriginCustom CodexClientModelsOrigin = "custom"
)

// CodexClientModelsState is a consistent snapshot of the effective Codex client
// model catalog together with the local override layer that shapes it.
type CodexClientModelsState struct {
	// Source names where the base catalog came from: "embed" or a source URL.
	Source string `json:"source"`
	// Revision changes whenever the effective catalog content changes.
	Revision uint64 `json:"revision"`
	// Models holds the effective catalog entries served to Codex clients.
	Models []map[string]any `json:"models"`
	// Origins maps each effective slug to where the entry came from.
	Origins map[string]CodexClientModelsOrigin `json:"origins"`
	// Override holds the local override document keyed by model slug.
	Override map[string]json.RawMessage `json:"override"`
	// OverridePath is the override file location; empty when no location is known.
	OverridePath string `json:"override_path"`
	// OverrideError reports why the on-disk override file could not be read, or why
	// the catalog it produces stayed invalid.
	OverrideError string `json:"override_error"`
	// OverrideErrors lists the override entries that were dropped because they could
	// not be applied. Every other entry of the same file still takes effect.
	OverrideErrors []CodexClientModelsOverrideIssue `json:"override_errors,omitempty"`
}

// GetCodexClientModelsState returns the effective catalog and its override layer.
func GetCodexClientModelsState() CodexClientModelsState {
	store := codexClientCatalogStore
	store.mu.RLock()
	base := append([]byte(nil), store.base...)
	effective := append([]byte(nil), store.data...)
	state := CodexClientModelsState{
		Source:         store.baseSource,
		Revision:       store.revision,
		Override:       cloneCodexClientModelsOverride(store.override),
		OverridePath:   store.overridePath,
		OverrideError:  store.overrideError,
		OverrideErrors: append([]CodexClientModelsOverrideIssue(nil), store.overrideIssues...),
	}
	store.mu.RUnlock()

	models, modelIndex, errParse := parseCodexClientModels(effective)
	if errParse != nil {
		return state
	}
	_, baseIndex, errBase := parseCodexClientModels(base)
	if errBase != nil {
		return state
	}

	state.Models = models
	state.Origins = make(map[string]CodexClientModelsOrigin, len(models))
	for _, model := range models {
		slug, _ := model["slug"].(string)
		state.Origins[strings.TrimSpace(slug)] = CodexClientModelsOriginBase
	}
	// An entry that a resilient apply could not patch keeps its base value, so it
	// has to stay reported as base instead of contradicting the effective catalog.
	degraded := make(map[string]struct{}, len(state.OverrideErrors))
	for _, issue := range state.OverrideErrors {
		degraded[strings.TrimSpace(issue.Slug)] = struct{}{}
	}
	for slug := range state.Override {
		trimmed := strings.TrimSpace(slug)
		if _, ok := modelIndex[trimmed]; !ok {
			// A null patch removed this entry from the effective catalog.
			continue
		}
		if _, failed := degraded[trimmed]; failed {
			continue
		}
		if _, ok := baseIndex[trimmed]; ok {
			state.Origins[trimmed] = CodexClientModelsOriginOverride
			continue
		}
		state.Origins[trimmed] = CodexClientModelsOriginCustom
	}
	return state
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

// SyncCodexClientModelsOverrideFile resolves and applies the override file that
// belongs to the given configuration file. It is safe to call at startup and on
// every configuration reload: a missing file clears the override layer, while a
// broken file keeps the last valid catalog and reports the error in
// CodexClientModelsState.
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
	effective, issues, errApply := applyCodexClientModelsOverrideResilient(store.base, doc)
	if errApply != nil {
		store.overrideError = errApply.Error()
		log.Warnf("Codex client model override file %s ignored: %v", path, errApply)
		return
	}
	if errValidate := ValidateCodexClientModelsJSON(effective); errValidate != nil {
		store.overrideError = fmt.Sprintf("override produces an invalid catalog: %v", errValidate)
		log.Warnf("Codex client model override file %s ignored: %s", path, store.overrideError)
		return
	}
	for _, issue := range issues {
		log.Warnf("Codex client model override file %s: %s ignored: %s", path, issue.Slug, issue.Error)
	}
	store.override = doc
	store.overrideError = ""
	store.overrideIssues = issues
	store.publishLocked(effective)
}

// IsCodexClientModelsOverrideRejected reports whether err was caused by an invalid
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

// applyOverrideLocked validates the override document, persists it and publishes
// the resulting catalog. Callers must hold the store lock.
func (s *codexClientModelsStore) applyOverrideLocked(doc map[string]json.RawMessage) error {
	if strings.TrimSpace(s.overridePath) == "" {
		return errors.New("codex client model override file location is not configured")
	}
	effective, errValidate := s.validateOverrideLocked(doc)
	if errValidate != nil {
		return errValidate
	}

	if len(doc) == 0 {
		if errRemove := removeCodexClientModelsOverrideFile(s.overridePath); errRemove != nil {
			return errRemove
		}
	} else if errWrite := writeCodexClientModelsOverrideFile(s.overridePath, doc); errWrite != nil {
		return errWrite
	}

	s.override = doc
	s.publishLocked(effective)
	return nil
}

// validateOverrideLocked returns the catalog produced by doc without publishing it.
// Callers must hold the store lock.
func (s *codexClientModelsStore) validateOverrideLocked(doc map[string]json.RawMessage) ([]byte, error) {
	effective, errMerge := applyCodexClientModelsOverride(s.base, doc)
	if errMerge != nil {
		return nil, rejectCodexClientModelsOverride(errMerge)
	}
	if errValidate := ValidateCodexClientModelsJSON(effective); errValidate != nil {
		return nil, rejectCodexClientModelsOverride(fmt.Errorf("override produces an invalid catalog: %w", errValidate))
	}
	return effective, nil
}

// publishLocked stores an already validated catalog. Callers must hold the store lock.
func (s *codexClientModelsStore) publishLocked(effective []byte) {
	if bytes.Equal(s.data, effective) {
		return
	}
	s.data = effective
	s.revision++
}

// applyCodexClientModelsOverride merges the slug-keyed override document into the
// base catalog. The base catalog is returned unchanged when no override applies.
// Every override entry must resolve and stay valid; the first problem is reported.
func applyCodexClientModelsOverride(base []byte, override map[string]json.RawMessage) ([]byte, error) {
	data, _, errApply := applyCodexClientModelsOverrideWithPolicy(base, override, false)
	return data, errApply
}

// applyCodexClientModelsOverrideResilient merges the override document while
// degrading the entries that cannot be applied: a base model keeps its base value
// and a custom model stays out of the catalog. Degraded entries are reported as
// issues, so one broken entry never hides the rest of the file.
func applyCodexClientModelsOverrideResilient(base []byte, override map[string]json.RawMessage) ([]byte, []CodexClientModelsOverrideIssue, error) {
	return applyCodexClientModelsOverrideWithPolicy(base, override, true)
}

func applyCodexClientModelsOverrideWithPolicy(base []byte, override map[string]json.RawMessage, resilient bool) ([]byte, []CodexClientModelsOverrideIssue, error) {
	if len(override) == 0 {
		return base, nil, nil
	}

	models, index, errParse := parseCodexClientModels(base)
	if errParse != nil {
		return nil, nil, errParse
	}
	baseBySlug := make(map[string]map[string]any, len(models))
	for position := range models {
		slug, _ := models[position]["slug"].(string)
		baseBySlug[strings.TrimSpace(slug)] = models[position]
	}

	slugs := make([]string, 0, len(override))
	for slug := range override {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)

	var issues []CodexClientModelsOverrideIssue
	// fail records a problem with a single override entry. A strict caller stops at
	// the first problem, a resilient caller degrades that entry and keeps the rest.
	fail := func(slug, fieldPath string, err error) error {
		if resilient {
			issues = append(issues, CodexClientModelsOverrideIssue{Slug: slug, Path: fieldPath, Error: err.Error()})
			return nil
		}
		if fieldPath == "" {
			return fmt.Errorf("model %q override: %w", slug, err)
		}
		return fmt.Errorf("model %q override field %q: %w", slug, fieldPath, err)
	}

	removed := make(map[string]struct{}, len(slugs))
	local := make(map[string]map[string]any, len(slugs))
	sources := make(map[string][]codexClientModelsInheritSource, len(slugs))
	for _, slug := range slugs {
		if slug == "" {
			return nil, nil, errors.New("override contains an empty model slug")
		}
		var patch any
		if errUnmarshal := json.Unmarshal(override[slug], &patch); errUnmarshal != nil {
			if errFail := fail(slug, "", errUnmarshal); errFail != nil {
				return nil, nil, errFail
			}
			continue
		}
		if patch == nil {
			// RFC 7386: a null patch removes the target, here the whole entry.
			if _, exists := index[slug]; exists {
				removed[slug] = struct{}{}
			}
			continue
		}
		patchObject, ok := patch.(map[string]any)
		if !ok {
			if errFail := fail(slug, "", errors.New("override must be a JSON object or null")); errFail != nil {
				return nil, nil, errFail
			}
			continue
		}
		if patchedSlug, okSlug := patchObject["slug"].(string); okSlug && strings.TrimSpace(patchedSlug) != slug {
			if errFail := fail(slug, "slug", fmt.Errorf("override must not change the slug to %q", patchedSlug)); errFail != nil {
				return nil, nil, errFail
			}
			continue
		}
		entrySources, entryLocal, errSplit := splitCodexClientModelsInherit(patchObject)
		if errSplit != nil {
			if errFail := fail(slug, "", errSplit); errFail != nil {
				return nil, nil, errFail
			}
			continue
		}
		local[slug] = entryLocal
		sources[slug] = entrySources
	}

	known := make(map[string]struct{}, len(baseBySlug)+len(local))
	for slug := range baseBySlug {
		known[slug] = struct{}{}
	}
	for slug := range local {
		known[slug] = struct{}{}
	}
	for _, slug := range slugs {
		for _, source := range sources[slug] {
			var errSource error
			switch {
			case source.slug == slug:
				errSource = errors.New("a model cannot inherit from itself")
			case hasCodexClientModelsSlug(removed, source.slug):
				errSource = fmt.Errorf("inheritance source %q is removed by this document", source.slug)
			case !hasCodexClientModelsSlug(known, source.slug):
				errSource = fmt.Errorf("inheritance source %q is not a known model", source.slug)
			}
			if errSource == nil {
				continue
			}
			if errFail := fail(slug, source.path, errSource); errFail != nil {
				return nil, nil, errFail
			}
			delete(local, slug)
			delete(sources, slug)
			break
		}
	}

	// A chain of inheritance sources is allowed up to a fixed number of hops. The
	// check is static so the outcome does not depend on the order entries resolve in.
	chainDepths := make(map[string]int, len(sources))
	chainVisiting := make(map[string]bool, len(sources))
	for _, slug := range slugs {
		if len(sources[slug]) == 0 {
			continue
		}
		depth, resolved := codexClientModelsInheritChainDepth(slug, sources, chainDepths, chainVisiting)
		if !resolved || depth <= maxCodexClientModelsInheritDepth {
			continue
		}
		if errFail := fail(slug, "", fmt.Errorf("inheritance chain is %d hops deep, the limit is %d hops", depth, maxCodexClientModelsInheritDepth)); errFail != nil {
			return nil, nil, errFail
		}
		delete(local, slug)
		delete(sources, slug)
	}

	resolver := newCodexClientModelsInheritance(baseBySlug, local, sources)

	effective := make([]map[string]any, 0, len(models)+len(local))
	for position := range models {
		slug, _ := models[position]["slug"].(string)
		slug = strings.TrimSpace(slug)
		if hasCodexClientModelsSlug(removed, slug) {
			continue
		}
		entry := models[position]
		if _, patched := local[slug]; patched {
			resolved, errResolve := resolver.entry(slug)
			if errResolve == nil && resilient {
				errResolve = validateCodexClientModel(resolved)
			}
			if errResolve != nil {
				issues = append(issues, CodexClientModelsOverrideIssue{Slug: slug, Error: errResolve.Error()})
				if !resilient {
					return nil, nil, fmt.Errorf("model %q override: %w", slug, errResolve)
				}
			} else {
				entry = resolved
			}
		}
		effective = append(effective, entry)
	}

	customSlugs := make([]string, 0, len(local))
	for slug := range local {
		if hasCodexClientModelsModel(baseBySlug, slug) {
			continue
		}
		customSlugs = append(customSlugs, slug)
	}
	sort.Strings(customSlugs)
	for _, slug := range customSlugs {
		resolved, errResolve := resolver.entry(slug)
		if errResolve == nil && resilient {
			errResolve = validateCodexClientModel(resolved)
		}
		if errResolve != nil {
			issues = append(issues, CodexClientModelsOverrideIssue{Slug: slug, Error: errResolve.Error()})
			if !resilient {
				return nil, nil, fmt.Errorf("model %q override: %w", slug, errResolve)
			}
			continue
		}
		effective = append(effective, resolved)
	}

	data, errMarshal := json.Marshal(codexClientModelsPayload{Models: effective})
	if errMarshal != nil {
		return nil, nil, fmt.Errorf("encode Codex client model catalog: %w", errMarshal)
	}
	return data, issues, nil
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
	if info.Size() > maxCodexClientModelsOverrideFileSize {
		return nil, fmt.Errorf("override file exceeds %d bytes", maxCodexClientModelsOverrideFileSize)
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
