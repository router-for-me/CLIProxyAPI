package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
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

func TestApplyCodexClientModelsOverrideDeletesAndAddsEntries(t *testing.T) {
	base := testCodexClientCatalog(t,
		testCodexClientModelWithExtras("gpt-5.5", 1, map[string]any{"scratch": "remove-me"}),
		testCodexClientModel("gpt-5.6-sol", 2),
	)

	custom := testCodexClientModel("local-model", 3)
	models, err := applyOverrideForTest(t, base, map[string]any{
		"gpt-5.5":     map[string]any{"scratch": nil},
		"gpt-5.6-sol": nil,
		"local-model": custom,
	})
	if err != nil {
		t.Fatalf("apply override: %v", err)
	}

	if _, ok := models["gpt-5.5"]["scratch"]; ok {
		t.Fatal("null patch did not remove the field")
	}
	if _, ok := models["gpt-5.6-sol"]; ok {
		t.Fatal("null patch did not remove the model")
	}
	if models["local-model"] == nil {
		t.Fatal("override did not add the custom model")
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

	custom := testCodexClientModel("local-model", 3)
	document := map[string]any{
		"gpt-5.6-sol": map[string]any{"display_name": "Local Sol"},
		"local-model": custom,
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
	if len(state.Override) != 2 {
		t.Fatalf("override entries = %d, want 2", len(state.Override))
	}
	if state.OverrideError != "" {
		t.Fatalf("override error = %q, want empty", state.OverrideError)
	}
	wantOrigins := map[string]CodexClientModelsOrigin{
		"gpt-5.5":     CodexClientModelsOriginBase,
		"gpt-5.6-sol": CodexClientModelsOriginOverride,
		"local-model": CodexClientModelsOriginCustom,
	}
	for slug, want := range wantOrigins {
		if got := state.Origins[slug]; got != want {
			t.Fatalf("origin[%s] = %q, want %q", slug, got, want)
		}
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
	if len(cleared.Models) != len(state.Models)-1 {
		t.Fatalf("models after clear = %d, want %d", len(cleared.Models), len(state.Models)-1)
	}
	if cleared.Revision <= state.Revision {
		t.Fatalf("revision after clear = %d, want > %d", cleared.Revision, state.Revision)
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

	before, revision := GetCodexClientModelsSnapshot()
	for _, document := range []string{
		`{"gpt-5.5":"not-an-object"}`,
		`{"gpt-5.5":{"slug":"other"}}`,
		`{"gpt-5.5":{"context_window":null}}`,
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

	after, afterRevision := GetCodexClientModelsSnapshot()
	if string(after) != string(before) {
		t.Fatal("invalid override replaced the effective catalog")
	}
	if afterRevision != revision {
		t.Fatalf("revision after invalid override = %d, want %d", afterRevision, revision)
	}
	if _, errStat := os.Stat(overridePath); !os.IsNotExist(errStat) {
		t.Fatalf("invalid override wrote %s: %v", overridePath, errStat)
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

func applyOverrideForTest(t *testing.T, base []byte, document map[string]any) (map[string]map[string]any, error) {
	t.Helper()
	override := make(map[string]json.RawMessage, len(document))
	for slug, patch := range document {
		raw, errMarshal := json.Marshal(patch)
		if errMarshal != nil {
			t.Fatalf("marshal override patch for %q: %v", slug, errMarshal)
		}
		override[slug] = raw
	}

	data, errApply := applyCodexClientModelsOverride(base, override)
	if errApply != nil {
		return nil, errApply
	}
	if errValidate := ValidateCodexClientModelsJSON(data); errValidate != nil {
		return nil, errValidate
	}
	models, _, errParse := parseCodexClientModels(data)
	if errParse != nil {
		return nil, errParse
	}
	bySlug := make(map[string]map[string]any, len(models))
	for _, model := range models {
		slug, _ := model["slug"].(string)
		bySlug[slug] = model
	}
	return bySlug, nil
}

func codexClientModelStateValue(t *testing.T, state CodexClientModelsState, slug, field string) any {
	t.Helper()
	for _, model := range state.Models {
		if model["slug"] == slug {
			return model[field]
		}
	}
	t.Fatalf("model %q not found in catalog state", slug)
	return nil
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
		base:           store.base,
		baseSource:     store.baseSource,
		override:       cloneCodexClientModelsOverride(store.override),
		overridePath:   store.overridePath,
		overrideError:  store.overrideError,
		overrideIssues: append([]CodexClientModelsOverrideIssue(nil), store.overrideIssues...),
		data:           store.data,
		revision:       store.revision,
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
		store.overrideIssues = previous.overrideIssues
		store.data = previous.data
		store.revision = previous.revision
	}
}
func TestCodexClientModelsDegradedEntryKeepsBaseOrigin(t *testing.T) {
	restore := snapshotCodexClientModelsStore(t)
	defer restore()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	overridePath := filepath.Join(dir, CodexClientModelsOverrideFileName)
	base := testCodexClientCatalog(t, testCodexClientModelWithExtras("gpt-5.5", 1, map[string]any{"context_window": 372000}))
	if _, err := setCodexClientModelsBase(base, "test"); err != nil {
		t.Fatalf("set base catalog: %v", err)
	}
	document := `{"gpt-5.5":{"$inherit":{"context_window":"missing-model"}}}`
	if errWrite := os.WriteFile(overridePath, []byte(document), 0o600); errWrite != nil {
		t.Fatalf("write override file: %v", errWrite)
	}
	SyncCodexClientModelsOverrideFile(configPath)

	state := GetCodexClientModelsState()
	if len(state.OverrideErrors) != 1 || state.OverrideErrors[0].Slug != "gpt-5.5" {
		t.Fatalf("override errors = %#v, want one issue for gpt-5.5", state.OverrideErrors)
	}
	if got := state.Origins["gpt-5.5"]; got != CodexClientModelsOriginBase {
		t.Fatalf("origin[gpt-5.5] = %q, want %q while the patch is degraded", got, CodexClientModelsOriginBase)
	}
	if got := codexClientModelStateValue(t, state, "gpt-5.5", "context_window"); got != float64(372000) {
		t.Fatalf("context_window = %v, want the base value", got)
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
