package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyCodexClientModelsOverrideMergesFields(t *testing.T) {
	base := testCodexClientCatalog(t,
		testCodexClientModelWithExtras("gpt-5.5", 1, map[string]any{
			"tool_format": map[string]any{"type": "function", "strict": true},
		}),
		testCodexClientModel("gpt-5.6-sol", 2),
	)

	models, err := applyOverrideForTest(t, base, map[string]any{
		"gpt-5.5": map[string]any{
			"display_name": "Local GPT",
			"tool_format":  map[string]any{"type": "custom"},
		},
	})
	if err != nil {
		t.Fatalf("apply override: %v", err)
	}

	merged := models["gpt-5.5"]
	if merged["display_name"] != "Local GPT" {
		t.Fatalf("display_name = %v, want %q", merged["display_name"], "Local GPT")
	}
	if merged["description"] != "Test model" {
		t.Fatalf("untouched field description = %v, want %q", merged["description"], "Test model")
	}

	toolFormat, ok := merged["tool_format"].(map[string]any)
	if !ok {
		t.Fatalf("tool_format = %#v, want object", merged["tool_format"])
	}
	if toolFormat["type"] != "custom" {
		t.Fatalf("tool_format.type = %v, want %q", toolFormat["type"], "custom")
	}
	if toolFormat["strict"] != true {
		t.Fatalf("tool_format.strict = %v, want true", toolFormat["strict"])
	}

	other := models["gpt-5.6-sol"]
	if other["display_name"] != "Test gpt-5.6-sol" {
		t.Fatalf("unpatched model changed: %v", other["display_name"])
	}
}

func TestApplyCodexClientModelsOverrideReplacesArrays(t *testing.T) {
	base := testCodexClientCatalog(t, testCodexClientModel("gpt-5.5", 1))

	models, err := applyOverrideForTest(t, base, map[string]any{
		"gpt-5.5": map[string]any{
			"supported_reasoning_levels": []map[string]any{
				{"effort": "high", "description": "Only high"},
			},
			"default_reasoning_level": "high",
		},
	})
	if err != nil {
		t.Fatalf("apply override: %v", err)
	}

	levels, ok := models["gpt-5.5"]["supported_reasoning_levels"].([]any)
	if !ok {
		t.Fatalf("supported_reasoning_levels = %#v, want array", models["gpt-5.5"]["supported_reasoning_levels"])
	}
	if len(levels) != 1 {
		t.Fatalf("supported_reasoning_levels length = %d, want 1", len(levels))
	}
	if models["gpt-5.5"]["default_reasoning_level"] != "high" {
		t.Fatalf("default_reasoning_level = %v, want %q", models["gpt-5.5"]["default_reasoning_level"], "high")
	}
}

func TestApplyCodexClientModelsOverrideRemovesFieldsAndModels(t *testing.T) {
	base := testCodexClientCatalog(t,
		testCodexClientModelWithExtras("gpt-5.5", 1, map[string]any{"scratch": "remove-me"}),
		testCodexClientModel("gpt-5.6-sol", 2),
	)

	models, err := applyOverrideForTest(t, base, map[string]any{
		"gpt-5.5":     map[string]any{"scratch": nil},
		"gpt-5.6-sol": nil,
	})
	if err != nil {
		t.Fatalf("apply override: %v", err)
	}

	if _, ok := models["gpt-5.5"]["scratch"]; ok {
		t.Fatal("null patch did not remove the field")
	}
	if _, ok := models["gpt-5.6-sol"]; ok {
		t.Fatal("null patch did not keep the model out of the served list")
	}
}

func TestCodexClientModelsOverrideReportsModelsThatAreNotServed(t *testing.T) {
	base := testCodexClientCatalog(t, testCodexClientModel("gpt-5.5", 1))

	served, issues := resolveOverrideForTest(t, base, map[string]any{
		"local-model": testCodexClientModel("local-model", 3),
	})
	if len(issues) != 1 || issues[0].Slug != "local-model" {
		t.Fatalf("issues = %#v, want one issue for %q", issues, "local-model")
	}
	if !strings.Contains(issues[0].Error, "not served") {
		t.Fatalf("issue = %q, want it to report that the model is not served", issues[0].Error)
	}
	if _, ok := served["local-model"]; ok {
		t.Fatal("an override for a model that is not served changed the served list")
	}
}

func TestApplyCodexClientModelsOverrideRejectsInvalidPatches(t *testing.T) {
	base := testCodexClientCatalog(t, testCodexClientModel("gpt-5.5", 1))

	tests := []struct {
		name string
		doc  map[string]any
	}{
		{name: "string patch", doc: map[string]any{"gpt-5.5": "not-an-object"}},
		{name: "empty slug", doc: map[string]any{"": map[string]any{"display_name": "x"}}},
		{name: "slug mismatch", doc: map[string]any{"gpt-5.5": map[string]any{"slug": "other"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := applyOverrideForTest(t, base, tt.doc); err == nil {
				t.Fatal("apply override error = nil, want error")
			}
		})
	}
}

func TestSetCodexClientModelsOverridePersistsAndApplies(t *testing.T) {
	restore := snapshotCodexClientModelsStore(t)
	defer restore()

	dir := t.TempDir()
	overridePath := filepath.Join(dir, CodexClientModelsOverrideFileName)
	base := testCodexClientCatalog(t, testCodexClientModel("gpt-5.5", 1), testCodexClientModel("gpt-5.6-sol", 2))
	if _, err := setCodexClientModelsBase(base, "test"); err != nil {
		t.Fatalf("set base catalog: %v", err)
	}
	SyncCodexClientModelsOverrideFile(filepath.Join(dir, "config.yaml"))
	beforeRevision := GetCodexClientModelsState().Revision

	document := map[string]any{
		"gpt-5.6-sol": map[string]any{"display_name": "Local Sol"},
	}
	raw, errMarshal := json.Marshal(document)
	if errMarshal != nil {
		t.Fatalf("marshal override document: %v", errMarshal)
	}
	if err := SetCodexClientModelsOverride(raw); err != nil {
		t.Fatalf("set override: %v", err)
	}

	state := GetCodexClientModelsState()
	if state.OverridePath != overridePath {
		t.Fatalf("override path = %q, want %q", state.OverridePath, overridePath)
	}
	if len(state.Override) != 1 {
		t.Fatalf("override entries = %d, want 1", len(state.Override))
	}
	if state.OverrideError != "" {
		t.Fatalf("override error = %q, want empty", state.OverrideError)
	}
	if got := codexClientModelStateValue(t, state, "gpt-5.6-sol", "display_name"); got != "Local Sol" {
		t.Fatalf("gpt-5.6-sol display_name = %v, want %q", got, "Local Sol")
	}
	if state.Revision <= beforeRevision {
		t.Fatalf("revision = %d, want > %d", state.Revision, beforeRevision)
	}

	onDisk, errRead := os.ReadFile(overridePath)
	if errRead != nil {
		t.Fatalf("read persisted override: %v", errRead)
	}
	if errValidate := decodeAndValidateOverrideForTest(t, onDisk); errValidate != nil {
		t.Fatalf("persisted override invalid: %v", errValidate)
	}

	if err := ClearCodexClientModelsOverride(); err != nil {
		t.Fatalf("clear override: %v", err)
	}
	if _, errStat := os.Stat(overridePath); !os.IsNotExist(errStat) {
		t.Fatalf("override file still present after clear: %v", errStat)
	}
	cleared := GetCodexClientModelsState()
	if len(cleared.Override) != 0 {
		t.Fatalf("override entries after clear = %d, want 0", len(cleared.Override))
	}
	if cleared.Revision <= state.Revision {
		t.Fatalf("revision after clear = %d, want > %d", cleared.Revision, state.Revision)
	}
	// Without the override the model is served exactly as the catalog assembles it.
	if got := codexClientModelStateValue(t, cleared, "gpt-5.6-sol", "display_name"); got != "Test gpt-5.6-sol" {
		t.Fatalf("display_name after clear = %v, want the assembled default", got)
	}
}

func TestSetCodexClientModelsOverrideRejectsInvalidDocument(t *testing.T) {
	restore := snapshotCodexClientModelsStore(t)
	defer restore()

	dir := t.TempDir()
	overridePath := filepath.Join(dir, CodexClientModelsOverrideFileName)
	base := testCodexClientCatalog(t, testCodexClientModel("gpt-5.5", 1))
	if _, err := setCodexClientModelsBase(base, "test"); err != nil {
		t.Fatalf("set base catalog: %v", err)
	}
	SyncCodexClientModelsOverrideFile(filepath.Join(dir, "config.yaml"))

	// A document that is not a slug to object map is refused before anything is stored.
	for _, document := range []string{
		`{"gpt-5.5":"not-an-object"}`,
		`[]`,
	} {
		err := SetCodexClientModelsOverride([]byte(document))
		if err == nil {
			t.Fatalf("SetCodexClientModelsOverride(%s) error = nil, want error", document)
		}
		if !IsCodexClientModelsOverrideRejected(err) {
			t.Fatalf("SetCodexClientModelsOverride(%s) error = %v, want rejection", document, err)
		}
	}
	if _, errStat := os.Stat(overridePath); !os.IsNotExist(errStat) {
		t.Fatalf("invalid override wrote %s: %v", overridePath, errStat)
	}
	if state := GetCodexClientModelsState(); len(state.Override) != 0 {
		t.Fatalf("override entries = %d, want 0", len(state.Override))
	}
}

func TestSetCodexClientModelsOverrideKeepsEntriesItCannotApply(t *testing.T) {
	restore := snapshotCodexClientModelsStore(t)
	defer restore()

	dir := t.TempDir()
	base := testCodexClientCatalog(t, testCodexClientModel("gpt-5.5", 1))
	if _, err := setCodexClientModelsBase(base, "test"); err != nil {
		t.Fatalf("set base catalog: %v", err)
	}
	SyncCodexClientModelsOverrideFile(filepath.Join(dir, "config.yaml"))

	// An entry that cannot be applied is still stored, reported when the entries are
	// resolved, and leaves its model on the entry the server assembled for it.
	for _, document := range []string{
		`{"gpt-5.5":{"slug":"other"}}`,
		`{"gpt-5.5":{"display_name":"Local","context_window":null}}`,
	} {
		if err := SetCodexClientModelsOverride([]byte(document)); err != nil {
			t.Fatalf("SetCodexClientModelsOverride(%s) error = %v, want the entry stored", document, err)
		}
		state := GetCodexClientModelsState()
		if len(state.Override) != 1 {
			t.Fatalf("override entries = %d, want the entry stored", len(state.Override))
		}
		raw, _ := GetCodexClientModelsSnapshot()
		_, issues := ResolveCodexClientModelOverrides(codexClientModelDefaultsForTest(t, raw), state.Override)
		if len(issues) != 1 || issues[0].Slug != "gpt-5.5" {
			t.Fatalf("issues = %#v, want one issue for %q", issues, "gpt-5.5")
		}
		if got := codexClientModelStateValue(t, state, "gpt-5.5", "context_window"); got != float64(372000) {
			t.Fatalf("context_window = %v, want the assembled default", got)
		}
	}
}

func TestDeleteCodexClientModelsOverrideEntry(t *testing.T) {
	restore := snapshotCodexClientModelsStore(t)
	defer restore()

	dir := t.TempDir()
	base := testCodexClientCatalog(t, testCodexClientModel("gpt-5.5", 1))
	if _, err := setCodexClientModelsBase(base, "test"); err != nil {
		t.Fatalf("set base catalog: %v", err)
	}
	SyncCodexClientModelsOverrideFile(filepath.Join(dir, "config.yaml"))

	if err := SetCodexClientModelsOverrideEntry("gpt-5.5", json.RawMessage(`{"display_name":"Entry Patch"}`)); err != nil {
		t.Fatalf("set entry override: %v", err)
	}
	if got := codexClientModelStateValue(t, GetCodexClientModelsState(), "gpt-5.5", "display_name"); got != "Entry Patch" {
		t.Fatalf("display_name = %v, want %q", got, "Entry Patch")
	}

	if err := DeleteCodexClientModelsOverrideEntry("gpt-5.5"); err != nil {
		t.Fatalf("delete entry override: %v", err)
	}
	state := GetCodexClientModelsState()
	if len(state.Override) != 0 {
		t.Fatalf("override entries = %d, want 0", len(state.Override))
	}
	if got := codexClientModelStateValue(t, state, "gpt-5.5", "display_name"); got != "Test gpt-5.5" {
		t.Fatalf("display_name after delete = %v, want base value", got)
	}
}

func TestSyncCodexClientModelsOverrideFileKeepsLastValidCatalog(t *testing.T) {
	restore := snapshotCodexClientModelsStore(t)
	defer restore()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	overridePath := filepath.Join(dir, CodexClientModelsOverrideFileName)
	base := testCodexClientCatalog(t, testCodexClientModel("gpt-5.5", 1))
	if _, err := setCodexClientModelsBase(base, "test"); err != nil {
		t.Fatalf("set base catalog: %v", err)
	}

	SyncCodexClientModelsOverrideFile(configPath)
	state := GetCodexClientModelsState()
	if len(state.Override) != 0 || state.OverrideError != "" {
		t.Fatalf("missing override file changed state: %#v", state)
	}

	if errWrite := os.WriteFile(overridePath, []byte(`{"gpt-5.5":{"display_name":"From File"}}`), 0o600); errWrite != nil {
		t.Fatalf("write override file: %v", errWrite)
	}
	SyncCodexClientModelsOverrideFile(configPath)
	if got := codexClientModelStateValue(t, GetCodexClientModelsState(), "gpt-5.5", "display_name"); got != "From File" {
		t.Fatalf("display_name = %v, want %q", got, "From File")
	}

	if errWrite := os.WriteFile(overridePath, []byte(`{"gpt-5.5":`), 0o600); errWrite != nil {
		t.Fatalf("corrupt override file: %v", errWrite)
	}
	SyncCodexClientModelsOverrideFile(configPath)
	state = GetCodexClientModelsState()
	if state.OverrideError == "" {
		t.Fatal("corrupt override file reported no error")
	}
	if got := codexClientModelStateValue(t, state, "gpt-5.5", "display_name"); got != "From File" {
		t.Fatalf("display_name = %v, want the last valid override %q", got, "From File")
	}

	if errRemove := os.Remove(overridePath); errRemove != nil {
		t.Fatalf("remove override file: %v", errRemove)
	}
	SyncCodexClientModelsOverrideFile(configPath)
	state = GetCodexClientModelsState()
	if len(state.Override) != 0 || state.OverrideError != "" {
		t.Fatalf("removed override file left state behind: %#v", state)
	}
	if got := codexClientModelStateValue(t, state, "gpt-5.5", "display_name"); got != "Test gpt-5.5" {
		t.Fatalf("display_name = %v, want base value", got)
	}
}

func testCodexClientModelWithExtras(slug string, priority int, extras map[string]any) map[string]any {
	model := testCodexClientModel(slug, priority)
	for key, value := range extras {
		model[key] = value
	}
	return model
}

// codexClientModelDefaultsForTest turns a catalog fixture into the default entries the
// override layer is applied to: one assembled entry per slug.
func codexClientModelDefaultsForTest(t *testing.T, catalog []byte) map[string]map[string]any {
	t.Helper()
	models, _, errParse := parseCodexClientModels(catalog)
	if errParse != nil {
		t.Fatalf("parse test Codex client catalog: %v", errParse)
	}
	defaults := make(map[string]map[string]any, len(models))
	for _, model := range models {
		slug, _ := model["slug"].(string)
		cloned, _ := cloneCodexClientModelsValue(model).(map[string]any)
		defaults[strings.TrimSpace(slug)] = cloned
	}
	return defaults
}

func codexClientModelOverrideDocumentForTest(t *testing.T, document map[string]any) map[string]json.RawMessage {
	t.Helper()
	doc := make(map[string]json.RawMessage, len(document))
	for slug, patch := range document {
		raw, errMarshal := json.Marshal(patch)
		if errMarshal != nil {
			t.Fatalf("marshal override patch for %q: %v", slug, errMarshal)
		}
		doc[slug] = raw
	}
	return doc
}

// resolveOverrideForTest applies an override document to the entries a catalog fixture
// stands in for: the defaults the server assembles.
func resolveOverrideForTest(t *testing.T, catalog []byte, document map[string]any) (map[string]map[string]any, []CodexClientModelsOverrideIssue) {
	t.Helper()
	return ResolveCodexClientModelOverrides(
		codexClientModelDefaultsForTest(t, catalog),
		codexClientModelOverrideDocumentForTest(t, document),
	)
}

func applyOverrideForTest(t *testing.T, base []byte, document map[string]any) (map[string]map[string]any, error) {
	t.Helper()
	served, issues := resolveOverrideForTest(t, base, document)
	if len(issues) > 0 {
		return served, errors.New(issues[0].String())
	}
	return served, nil
}

func applyResilientOverrideForTest(t *testing.T, base []byte, document map[string]any) (map[string]map[string]any, []CodexClientModelsOverrideIssue, error) {
	t.Helper()
	served, issues := resolveOverrideForTest(t, base, document)
	return served, issues, nil
}

// codexClientModelStateValue reports one field of a served entry: the base catalog with
// the state's override layer applied, which is what clients receive.
func codexClientModelStateValue(t *testing.T, state CodexClientModelsState, slug, field string) any {
	t.Helper()
	raw, _ := GetCodexClientModelsSnapshot()
	// Problems with individual entries are reported separately, so this helper looks
	// up the served entry without turning them into a failure of its own.
	served, _ := ResolveCodexClientModelOverrides(codexClientModelDefaultsForTest(t, raw), state.Override)
	model, ok := served[strings.TrimSpace(slug)]
	if !ok {
		t.Fatalf("model %q is not served", slug)
	}
	return model[field]
}

func decodeAndValidateOverrideForTest(t *testing.T, data []byte) error {
	t.Helper()
	decoded, errDecode := decodeCodexClientModelsOverride(data)
	if errDecode != nil {
		return errDecode
	}
	if len(decoded) == 0 {
		t.Fatal("persisted override document is empty")
	}
	return nil
}

// snapshotCodexClientModelsStore captures the global catalog store so tests can
// mutate it and restore the original state afterwards.
func snapshotCodexClientModelsStore(t *testing.T) func() {
	t.Helper()
	store := codexClientCatalogStore
	store.mu.Lock()
	previous := codexClientModelsStore{
		base:          store.base,
		baseSource:    store.baseSource,
		override:      cloneCodexClientModelsOverride(store.override),
		overridePath:  store.overridePath,
		overrideError: store.overrideError,
		revision:      store.revision,
	}
	store.mu.Unlock()

	return func() {
		store.mu.Lock()
		defer store.mu.Unlock()
		store.base = previous.base
		store.baseSource = previous.baseSource
		store.override = previous.override
		store.overridePath = previous.overridePath
		store.overrideError = previous.overrideError
		store.revision = previous.revision
	}
}
func TestCodexClientModelsDegradedOverrideKeepsTheDefaultEntry(t *testing.T) {
	restore := snapshotCodexClientModelsStore(t)
	defer restore()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	overridePath := filepath.Join(dir, CodexClientModelsOverrideFileName)
	base := testCodexClientCatalog(t, testCodexClientModelWithExtras("gpt-5.5", 1, map[string]any{"context_window": 372000}))
	if _, err := setCodexClientModelsBase(base, "test"); err != nil {
		t.Fatalf("set base catalog: %v", err)
	}
	document := `{"gpt-5.5":{"$inherit":{"base_instructions":"missing-model"}}}`
	if errWrite := os.WriteFile(overridePath, []byte(document), 0o600); errWrite != nil {
		t.Fatalf("write override file: %v", errWrite)
	}
	SyncCodexClientModelsOverrideFile(configPath)

	state := GetCodexClientModelsState()
	// The entry that cannot be applied is reported, and the model keeps the entry the
	// server assembled for it instead of leaving the served list.
	if got := codexClientModelStateValue(t, state, "gpt-5.5", "context_window"); got != float64(372000) {
		t.Fatalf("context_window = %v, want the assembled default", got)
	}
	if got := codexClientModelStateValue(t, state, "gpt-5.5", "display_name"); got != "Test gpt-5.5" {
		t.Fatalf("display_name = %v, want the assembled default", got)
	}
}

func TestCodexClientModelsOverrideFileRewriteReplacesExistingFile(t *testing.T) {
	restore := snapshotCodexClientModelsStore(t)
	defer restore()

	dir := t.TempDir()
	overridePath := filepath.Join(dir, CodexClientModelsOverrideFileName)
	base := testCodexClientCatalog(t, testCodexClientModel("gpt-5.5", 1), testCodexClientModel("gpt-5.6-sol", 2))
	if _, err := setCodexClientModelsBase(base, "test"); err != nil {
		t.Fatalf("set base catalog: %v", err)
	}
	SyncCodexClientModelsOverrideFile(filepath.Join(dir, "config.yaml"))

	if err := SetCodexClientModelsOverride([]byte(`{"gpt-5.5":{"display_name":"First"}}`)); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if _, errStat := os.Stat(overridePath); errStat != nil {
		t.Fatalf("first write did not create %s: %v", overridePath, errStat)
	}

	// The next writes rename a temporary file over the file created above, which has
	// to replace the existing destination on every platform.
	if err := SetCodexClientModelsOverride([]byte(`{"gpt-5.5":{"display_name":"Second"}}`)); err != nil {
		t.Fatalf("second write over an existing file: %v", err)
	}
	if got := codexClientModelStateValue(t, GetCodexClientModelsState(), "gpt-5.5", "display_name"); got != "Second" {
		t.Fatalf("display_name after second write = %v, want %q", got, "Second")
	}
	if err := SetCodexClientModelsOverrideEntry("gpt-5.5", json.RawMessage(`{"display_name":"Third"}`)); err != nil {
		t.Fatalf("entry write over an existing file: %v", err)
	}
	if got := codexClientModelStateValue(t, GetCodexClientModelsState(), "gpt-5.5", "display_name"); got != "Third" {
		t.Fatalf("display_name after entry write = %v, want %q", got, "Third")
	}
}

func TestSetCodexClientModelsOverrideClearsFileErrors(t *testing.T) {
	restore := snapshotCodexClientModelsStore(t)
	defer restore()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	overridePath := filepath.Join(dir, CodexClientModelsOverrideFileName)
	base := testCodexClientCatalog(t, testCodexClientModel("gpt-5.5", 1), testCodexClientModel("gpt-5.6-sol", 2))
	if _, err := setCodexClientModelsBase(base, "test"); err != nil {
		t.Fatalf("set base catalog: %v", err)
	}

	// A hand edit that cannot be parsed reports a file level diagnostic.
	if errWrite := os.WriteFile(overridePath, []byte("{not json"), 0o644); errWrite != nil {
		t.Fatalf("write unparsable override file: %v", errWrite)
	}
	SyncCodexClientModelsOverrideFile(configPath)
	if state := GetCodexClientModelsState(); state.OverrideError == "" {
		t.Fatal("override error is empty after an unparsable file")
	}

	// A successful write replaces that file, so the file level diagnostic must go.
	if err := SetCodexClientModelsOverride([]byte(`{"gpt-5.5":{"display_name":"Local 5.5"}}`)); err != nil {
		t.Fatalf("set override after an unparsable file: %v", err)
	}
	if state := GetCodexClientModelsState(); state.OverrideError != "" {
		t.Fatalf("override error after a successful write = %q, want empty", state.OverrideError)
	}

	// A hand edit whose entries cannot be applied is still loaded as written: those
	// problems are reported when the entries are resolved, so the file itself is not
	// treated as broken.
	if errWrite := os.WriteFile(overridePath, []byte(`{"gpt-5.6-sol":{"$inherit":"missing-model"}}`), 0o644); errWrite != nil {
		t.Fatalf("write degraded override file: %v", errWrite)
	}
	SyncCodexClientModelsOverrideFile(configPath)
	if state := GetCodexClientModelsState(); state.OverrideError != "" {
		t.Fatalf("override error = %q, want empty for a readable file", state.OverrideError)
	}

	// A successful management write replaces the document.
	if err := SetCodexClientModelsOverride([]byte(`{"gpt-5.6-sol":{"display_name":"Local Sol"}}`)); err != nil {
		t.Fatalf("set override: %v", err)
	}
	state := GetCodexClientModelsState()
	if state.OverrideError != "" {
		t.Fatalf("override error after a successful write = %q, want empty", state.OverrideError)
	}
	if len(state.Override) != 1 {
		t.Fatalf("override entries after a successful write = %d, want 1", len(state.Override))
	}
	if got := codexClientModelStateValue(t, state, "gpt-5.6-sol", "display_name"); got != "Local Sol" {
		t.Fatalf("display_name = %v, want %q", got, "Local Sol")
	}
}

func TestSetCodexClientModelsOverrideRejectsOversizedDocument(t *testing.T) {
	restore := snapshotCodexClientModelsStore(t)
	defer restore()

	dir := t.TempDir()
	overridePath := filepath.Join(dir, CodexClientModelsOverrideFileName)
	base := testCodexClientCatalog(t, testCodexClientModel("gpt-5.5", 1))
	if _, err := setCodexClientModelsBase(base, "test"); err != nil {
		t.Fatalf("set base catalog: %v", err)
	}
	SyncCodexClientModelsOverrideFile(filepath.Join(dir, "config.yaml"))

	document := fmt.Sprintf(`{"gpt-5.5":{"base_instructions":%q}}`, strings.Repeat("x", CodexClientModelsOverrideMaxFileSize))
	err := SetCodexClientModelsOverride([]byte(document))
	if err == nil {
		t.Fatal("oversized override document was accepted")
	}
	if !IsCodexClientModelsOverrideRejected(err) {
		t.Fatalf("oversized override error = %v, want rejection", err)
	}
	if _, errStat := os.Stat(overridePath); !os.IsNotExist(errStat) {
		t.Fatalf("oversized override wrote %s: %v", overridePath, errStat)
	}
	if state := GetCodexClientModelsState(); len(state.Override) != 0 {
		t.Fatalf("override entries = %d, want 0", len(state.Override))
	}
}
