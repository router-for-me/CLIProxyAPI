package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

func TestGetAPIKeyProfilesReturnsMaskedMergedProfiles(t *testing.T) {
	const createdAt = "2026-08-12T03:04:05Z"
	h := &Handler{cfg: testClientAPIKeyConfig(
		[]string{"abcdefghijk", "short"},
		map[string]config.ClientAPIKeyMetadata{
			"abcdefghijk": {ID: "team-a", Alias: "Production", Disabled: true, CreatedAt: createdAt},
		},
	)}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/api-key-profiles", nil)

	h.GetAPIKeyProfiles(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "abcdefghijk") || strings.Contains(rec.Body.String(), `"short"`) {
		t.Fatalf("response exposed a raw API key: %s", rec.Body.String())
	}
	var response struct {
		Profiles []apiKeyProfileResponse `json:"api-key-profiles"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Profiles) != 2 {
		t.Fatalf("profiles len = %d, want 2", len(response.Profiles))
	}
	first := response.Profiles[0]
	if first.Index != 0 || first.ID != "team-a" || first.Alias != "Production" || !first.Disabled || first.MaskedKey != "abcd***hijk" || first.CreatedAt != createdAt {
		t.Fatalf("first profile = %#v", first)
	}
	second := response.Profiles[1]
	if second.Index != 1 || second.ID != sdkaccess.FallbackClientKeyID("short") || second.Alias != "" || second.Disabled || second.MaskedKey != "*****" {
		t.Fatalf("second profile = %#v", second)
	}
}

func TestAPIKeyCreationTimeLifecycle(t *testing.T) {
	h := &Handler{
		cfg:            testClientAPIKeyConfig(nil, nil),
		configFilePath: writeTestConfigFile(t),
	}

	appendRec := httptest.NewRecorder()
	appendContext, _ := gin.CreateTestContext(appendRec)
	appendContext.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/api-keys", strings.NewReader(`{"old":null,"new":"created-key"}`))
	appendContext.Request.Header.Set("Content-Type", "application/json")
	h.PatchAPIKeys(appendContext)
	if appendRec.Code != http.StatusOK {
		t.Fatalf("append status = %d, want %d; body=%s", appendRec.Code, http.StatusOK, appendRec.Body.String())
	}
	createdAt := h.cfg.APIKeyMetadata["created-key"].CreatedAt
	parsed, err := time.Parse(time.RFC3339, createdAt)
	if err != nil || parsed.Location() != time.UTC {
		t.Fatalf("created_at = %q, want RFC3339 UTC; parse error=%v", createdAt, err)
	}

	rotateRec := httptest.NewRecorder()
	rotateContext, _ := gin.CreateTestContext(rotateRec)
	rotateContext.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/api-keys", strings.NewReader(`{"index":0,"value":"rotated-key"}`))
	rotateContext.Request.Header.Set("Content-Type", "application/json")
	h.PatchAPIKeys(rotateContext)
	if rotateRec.Code != http.StatusOK {
		t.Fatalf("rotate status = %d, want %d; body=%s", rotateRec.Code, http.StatusOK, rotateRec.Body.String())
	}
	if got := h.cfg.APIKeyMetadata["rotated-key"].CreatedAt; got != createdAt {
		t.Fatalf("rotated created_at = %q, want preserved %q", got, createdAt)
	}

	profileRec := httptest.NewRecorder()
	profileContext, _ := gin.CreateTestContext(profileRec)
	profileContext.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/api-key-profiles", strings.NewReader(`{"index":0,"alias":"Rotated","created_at":"1999-01-01T00:00:00Z"}`))
	profileContext.Request.Header.Set("Content-Type", "application/json")
	h.PatchAPIKeyProfile(profileContext)
	if profileRec.Code != http.StatusOK {
		t.Fatalf("profile patch status = %d, want %d; body=%s", profileRec.Code, http.StatusOK, profileRec.Body.String())
	}
	if got := h.cfg.APIKeyMetadata["rotated-key"].CreatedAt; got != createdAt {
		t.Fatalf("profile patch changed created_at to %q, want %q", got, createdAt)
	}

	deleteRec := httptest.NewRecorder()
	deleteContext, _ := gin.CreateTestContext(deleteRec)
	deleteContext.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/api-key-profiles?index=0", nil)
	h.DeleteAPIKeyProfile(deleteContext)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("profile delete status = %d, want %d; body=%s", deleteRec.Code, http.StatusOK, deleteRec.Body.String())
	}
	metadata := h.cfg.APIKeyMetadata["rotated-key"]
	if metadata.CreatedAt != createdAt || metadata.ID != "" || metadata.Alias != "" || metadata.Disabled {
		t.Fatalf("profile delete metadata = %#v, want only preserved created_at", metadata)
	}
}

func TestGetAPIKeysReturnsProfilesFromSameSnapshot(t *testing.T) {
	h := &Handler{cfg: testClientAPIKeyConfig(
		[]string{"key-one"},
		map[string]config.ClientAPIKeyMetadata{"key-one": {ID: "team-a", Alias: "Production"}},
	)}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/api-keys", nil)

	h.GetAPIKeys(c)

	var response struct {
		Keys     []string                `json:"api-keys"`
		Profiles []apiKeyProfileResponse `json:"api-key-profiles"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Keys) != 1 || response.Keys[0] != "key-one" || len(response.Profiles) != 1 || response.Profiles[0].ID != "team-a" || response.Profiles[0].Index != 0 {
		t.Fatalf("response = %#v", response)
	}
}

func TestGetAPIKeyProfilesMarksIgnoredEmptyAndDuplicateKeys(t *testing.T) {
	h := &Handler{cfg: testClientAPIKeyConfig([]string{"   ", "same-key", " same-key "}, nil)}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/api-key-profiles", nil)

	h.GetAPIKeyProfiles(c)

	var response struct {
		Profiles []apiKeyProfileResponse `json:"api-key-profiles"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Profiles) != 3 {
		t.Fatalf("profiles len = %d, want 3", len(response.Profiles))
	}
	if response.Profiles[0].Effective || response.Profiles[0].Issue != "empty" {
		t.Fatalf("empty profile = %#v, want ignored empty", response.Profiles[0])
	}
	if !response.Profiles[1].Effective || response.Profiles[1].Issue != "" {
		t.Fatalf("first key profile = %#v, want effective", response.Profiles[1])
	}
	if response.Profiles[2].Effective || response.Profiles[2].Issue != "duplicate" {
		t.Fatalf("duplicate profile = %#v, want ignored duplicate", response.Profiles[2])
	}
}

func TestPatchAPIKeyProfileUpdatesMetadataAndRejectsDuplicateID(t *testing.T) {
	h := &Handler{
		cfg: testClientAPIKeyConfig(
			[]string{"key-one", "key-two"},
			map[string]config.ClientAPIKeyMetadata{
				"key-one": {ID: "team-one"},
				"key-two": {ID: "team-two"},
			},
		),
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/api-key-profiles", strings.NewReader(`{"index":1,"alias":"Staging","disabled":true}`))
	c.Request.Header.Set("Content-Type", "application/json")
	h.PatchAPIKeyProfile(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	metadata := h.cfg.APIKeyMetadata["key-two"]
	if metadata.ID != "team-two" || metadata.Alias != "Staging" || !metadata.Disabled {
		t.Fatalf("updated metadata = %#v", metadata)
	}

	rec = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/api-key-profiles", strings.NewReader(`{"index":1,"id":"team-one"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	h.PatchAPIKeyProfile(c)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("duplicate id status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if got := h.cfg.APIKeyMetadata["key-two"].ID; got != "team-two" {
		t.Fatalf("id changed after rejected patch: %q", got)
	}
}

func TestPatchAPIKeyProfileAppliesImmediateHookBeforeResponse(t *testing.T) {
	h := &Handler{
		cfg:            testClientAPIKeyConfig([]string{"key-one"}, nil),
		configFilePath: writeTestConfigFile(t),
	}
	called := false
	h.configImmediateHook = func(cfg *config.Config, _ uint64) {
		if cfg.APIKeyMetadata["key-one"].Disabled {
			called = true
		}
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/api-key-profiles", strings.NewReader(`{"index":0,"disabled":true}`))
	c.Request.Header.Set("Content-Type", "application/json")
	h.PatchAPIKeyProfile(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !called {
		t.Fatal("immediate config hook did not observe the disabled key")
	}
}

func TestSetConfigRejectsStaleReloadWhileManagementSaveIsPending(t *testing.T) {
	latest := testClientAPIKeyConfig([]string{"latest-key"}, nil)
	h := &Handler{
		cfg:                     latest,
		reloadGeneration:        2,
		appliedReloadGeneration: 1,
	}

	h.SetConfig(testClientAPIKeyConfig([]string{"stale-key"}, nil))
	if len(h.cfg.APIKeys) != 1 || h.cfg.APIKeys[0] != "latest-key" {
		t.Fatalf("stale reload replaced pending config: %#v", h.cfg.APIKeys)
	}

	h.appliedReloadGeneration = h.reloadGeneration
	h.SetConfig(testClientAPIKeyConfig([]string{"external-key"}, nil))
	if len(h.cfg.APIKeys) != 1 || h.cfg.APIKeys[0] != "external-key" {
		t.Fatalf("config remained guarded after reload completion: %#v", h.cfg.APIKeys)
	}
}

func TestPutAndDeleteAPIKeyProfilesDoNotChangeAPIKeys(t *testing.T) {
	h := &Handler{
		cfg: testClientAPIKeyConfig(
			[]string{"key-one", "key-two"},
			map[string]config.ClientAPIKeyMetadata{
				"key-one": {Alias: "Old"},
			},
		),
		configFilePath: writeTestConfigFile(t),
	}

	putRec := httptest.NewRecorder()
	putContext, _ := gin.CreateTestContext(putRec)
	putContext.Request = httptest.NewRequest(http.MethodPut, "/v0/management/api-key-profiles", strings.NewReader(`[{"index":0,"id":"first","alias":"Primary"},{"index":1,"id":"second","disabled":true}]`))
	h.PutAPIKeyProfiles(putContext)
	if putRec.Code != http.StatusOK {
		t.Fatalf("put status = %d, want %d; body=%s", putRec.Code, http.StatusOK, putRec.Body.String())
	}
	if len(h.cfg.APIKeys) != 2 || h.cfg.APIKeys[0] != "key-one" || h.cfg.APIKeys[1] != "key-two" {
		t.Fatalf("PUT changed API keys: %#v", h.cfg.APIKeys)
	}
	if got := h.cfg.APIKeyMetadata["key-two"]; got.ID != "second" || !got.Disabled {
		t.Fatalf("second profile = %#v", got)
	}

	deleteRec := httptest.NewRecorder()
	deleteContext, _ := gin.CreateTestContext(deleteRec)
	deleteContext.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/api-key-profiles?index=0", nil)
	h.DeleteAPIKeyProfile(deleteContext)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want %d; body=%s", deleteRec.Code, http.StatusOK, deleteRec.Body.String())
	}
	if len(h.cfg.APIKeys) != 2 {
		t.Fatalf("DELETE changed API keys: %#v", h.cfg.APIKeys)
	}
	if _, exists := h.cfg.APIKeyMetadata["key-one"]; exists {
		t.Fatal("profile metadata was not deleted")
	}
	if _, exists := h.cfg.APIKeyMetadata["key-two"]; !exists {
		t.Fatal("DELETE removed unrelated profile metadata")
	}

	emptyRec := httptest.NewRecorder()
	emptyContext, _ := gin.CreateTestContext(emptyRec)
	emptyContext.Request = httptest.NewRequest(http.MethodPut, "/v0/management/api-key-profiles", strings.NewReader(`[]`))
	h.PutAPIKeyProfiles(emptyContext)
	if emptyRec.Code != http.StatusOK {
		t.Fatalf("empty PUT status = %d, want %d; body=%s", emptyRec.Code, http.StatusOK, emptyRec.Body.String())
	}
	if len(h.cfg.APIKeyMetadata) != 0 {
		t.Fatalf("empty PUT did not clear profiles: %#v", h.cfg.APIKeyMetadata)
	}
}

func TestPutAPIKeyProfilesPreservesCreationTimes(t *testing.T) {
	const firstCreatedAt = "2026-08-01T02:03:04Z"
	const secondCreatedAt = "2026-08-02T03:04:05Z"
	h := &Handler{
		cfg: testClientAPIKeyConfig(
			[]string{"key-one", "key-two"},
			map[string]config.ClientAPIKeyMetadata{
				"key-one": {Alias: "Old", CreatedAt: firstCreatedAt},
				"key-two": {CreatedAt: secondCreatedAt},
			},
		),
		configFilePath: writeTestConfigFile(t),
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/api-key-profiles", strings.NewReader(`[{"index":0,"alias":"New","created_at":"1999-01-01T00:00:00Z"}]`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PutAPIKeyProfiles(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := h.cfg.APIKeyMetadata["key-one"]; got.Alias != "New" || got.CreatedAt != firstCreatedAt {
		t.Fatalf("first metadata = %#v, want updated alias and preserved created_at", got)
	}
	if got := h.cfg.APIKeyMetadata["key-two"]; got.CreatedAt != secondCreatedAt || got.ID != "" || got.Alias != "" || got.Disabled {
		t.Fatalf("second metadata = %#v, want only preserved created_at", got)
	}
}

func TestAPIKeyCRUDMigratesAndPrunesMetadata(t *testing.T) {
	h := &Handler{
		cfg: testClientAPIKeyConfig(
			[]string{"key-one", "key-two"},
			map[string]config.ClientAPIKeyMetadata{
				"key-one": {ID: "first", Alias: "Primary"},
				"key-two": {ID: "second", Alias: "Secondary"},
				"stale":   {ID: "stale"},
			},
		),
		configFilePath: writeTestConfigFile(t),
	}

	patchRec := httptest.NewRecorder()
	patchContext, _ := gin.CreateTestContext(patchRec)
	patchContext.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/api-keys", strings.NewReader(`{"index":0,"value":"key-new"}`))
	patchContext.Request.Header.Set("Content-Type", "application/json")
	h.PatchAPIKeys(patchContext)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("patch status = %d, want %d; body=%s", patchRec.Code, http.StatusOK, patchRec.Body.String())
	}
	if _, exists := h.cfg.APIKeyMetadata["key-one"]; exists {
		t.Fatal("old key metadata remained after replacement")
	}
	if got := h.cfg.APIKeyMetadata["key-new"]; got.ID != "first" || got.Alias != "Primary" {
		t.Fatalf("migrated metadata = %#v", got)
	}
	if _, exists := h.cfg.APIKeyMetadata["stale"]; exists {
		t.Fatal("stale metadata was not pruned")
	}

	deleteRec := httptest.NewRecorder()
	deleteContext, _ := gin.CreateTestContext(deleteRec)
	deleteContext.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/api-keys?index=0", nil)
	h.DeleteAPIKeys(deleteContext)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want %d; body=%s", deleteRec.Code, http.StatusOK, deleteRec.Body.String())
	}
	if _, exists := h.cfg.APIKeyMetadata["key-new"]; exists {
		t.Fatal("deleted key metadata remained")
	}
	if got := h.cfg.APIKeyMetadata["key-two"].ID; got != "second" {
		t.Fatalf("unrelated metadata changed after delete: %#v", h.cfg.APIKeyMetadata)
	}

	putRec := httptest.NewRecorder()
	putContext, _ := gin.CreateTestContext(putRec)
	putContext.Request = httptest.NewRequest(http.MethodPut, "/v0/management/api-keys", strings.NewReader(`[]`))
	h.PutAPIKeys(putContext)
	if putRec.Code != http.StatusOK {
		t.Fatalf("empty api-keys PUT status = %d, want %d; body=%s", putRec.Code, http.StatusOK, putRec.Body.String())
	}
	if len(h.cfg.APIKeys) != 0 || len(h.cfg.APIKeyMetadata) != 0 {
		t.Fatalf("empty PUT did not clear keys and metadata: keys=%#v metadata=%#v", h.cfg.APIKeys, h.cfg.APIKeyMetadata)
	}
}

func TestAPIKeyRotationPreservesLegacyFallbackIdentity(t *testing.T) {
	oldKey := "legacy-key"
	h := &Handler{
		cfg:            testClientAPIKeyConfig([]string{oldKey}, nil),
		configFilePath: writeTestConfigFile(t),
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/api-keys", strings.NewReader(`{"index":0,"value":"rotated-key"}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PatchAPIKeys(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := h.cfg.APIKeyMetadata["rotated-key"].ID; got != sdkaccess.FallbackClientKeyID(oldKey) {
		t.Fatalf("rotated stable ID = %q, want %q", got, sdkaccess.FallbackClientKeyID(oldKey))
	}
}

func TestPutAPIKeysPreservesIdentityForSameLengthRotation(t *testing.T) {
	oldKey := "legacy-key"
	const createdAt = "2026-08-01T02:03:04Z"
	h := &Handler{
		cfg: testClientAPIKeyConfig(
			[]string{oldKey},
			map[string]config.ClientAPIKeyMetadata{oldKey: {CreatedAt: createdAt}},
		),
		configFilePath: writeTestConfigFile(t),
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/api-keys", strings.NewReader(`["rotated-key"]`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PutAPIKeys(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := h.cfg.APIKeyMetadata["rotated-key"].ID; got != sdkaccess.FallbackClientKeyID(oldKey) {
		t.Fatalf("rotated stable ID = %q, want %q", got, sdkaccess.FallbackClientKeyID(oldKey))
	}
	if got := h.cfg.APIKeyMetadata["rotated-key"].CreatedAt; got != createdAt {
		t.Fatalf("rotated created_at = %q, want %q", got, createdAt)
	}
}

func TestPutAPIKeysStampsOnlyTrulyNewKeys(t *testing.T) {
	const createdAt = "2026-08-01T02:03:04Z"
	h := &Handler{
		cfg: testClientAPIKeyConfig(
			[]string{"existing-key"},
			map[string]config.ClientAPIKeyMetadata{"existing-key": {CreatedAt: createdAt}},
		),
		configFilePath: writeTestConfigFile(t),
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/api-keys", strings.NewReader(`["existing-key","new-key"]`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PutAPIKeys(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := h.cfg.APIKeyMetadata["existing-key"].CreatedAt; got != createdAt {
		t.Fatalf("existing created_at = %q, want %q", got, createdAt)
	}
	newCreatedAt := h.cfg.APIKeyMetadata["new-key"].CreatedAt
	if _, err := time.Parse(time.RFC3339, newCreatedAt); err != nil {
		t.Fatalf("new created_at = %q, want RFC3339: %v", newCreatedAt, err)
	}
}

func TestPutAPIKeysPreservesMovedKeysAndMigratesOnlyReplacements(t *testing.T) {
	h := &Handler{
		cfg: testClientAPIKeyConfig(
			[]string{"key-a", "key-b"},
			map[string]config.ClientAPIKeyMetadata{
				"key-a": {ID: "identity-a", Alias: "A"},
				"key-b": {ID: "identity-b", Alias: "B"},
			},
		),
		configFilePath: writeTestConfigFile(t),
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/api-keys", strings.NewReader(`["key-b","key-c"]`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PutAPIKeys(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := h.cfg.APIKeyMetadata["key-b"]; got.ID != "identity-b" || got.Alias != "B" {
		t.Fatalf("moved key metadata = %#v, want identity-b", got)
	}
	if got := h.cfg.APIKeyMetadata["key-c"]; got.ID != "identity-a" || got.Alias != "A" {
		t.Fatalf("replacement metadata = %#v, want migrated identity-a", got)
	}
}

func TestAPIKeyRotationToExistingKeyPreservesDestinationProfile(t *testing.T) {
	h := &Handler{
		cfg: testClientAPIKeyConfig(
			[]string{"old-key", "target-key"},
			map[string]config.ClientAPIKeyMetadata{
				"old-key":    {ID: "old-id", Alias: "Old"},
				"target-key": {ID: "target-id", Alias: "Target"},
			},
		),
		configFilePath: writeTestConfigFile(t),
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/api-keys", strings.NewReader(`{"index":0,"value":"target-key"}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PatchAPIKeys(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := h.cfg.APIKeyMetadata["target-key"]; got.ID != "target-id" || got.Alias != "Target" {
		t.Fatalf("destination metadata = %#v, want preserved target profile", got)
	}
	if _, exists := h.cfg.APIKeyMetadata["old-key"]; exists {
		t.Fatal("old metadata remained after merging into an existing key")
	}
}

func TestPatchAPIKeyProfileRejectsStaleExpectedID(t *testing.T) {
	h := &Handler{
		cfg: testClientAPIKeyConfig(
			[]string{"key-one"},
			map[string]config.ClientAPIKeyMetadata{"key-one": {ID: "current-id"}},
		),
		configFilePath: writeTestConfigFile(t),
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/api-key-profiles", strings.NewReader(`{"index":0,"expected_id":"stale-id","disabled":true}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PatchAPIKeyProfile(c)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	if h.cfg.APIKeyMetadata["key-one"].Disabled {
		t.Fatal("stale profile patch changed the current key")
	}
}

func TestDeleteAPIKeyProfileRejectsStaleExpectedID(t *testing.T) {
	h := &Handler{
		cfg: testClientAPIKeyConfig(
			[]string{"key-one"},
			map[string]config.ClientAPIKeyMetadata{"key-one": {ID: "current-id", Alias: "Production"}},
		),
		configFilePath: writeTestConfigFile(t),
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/api-key-profiles?index=0&expected_id=stale-id", nil)

	h.DeleteAPIKeyProfile(c)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	if got := h.cfg.APIKeyMetadata["key-one"].Alias; got != "Production" {
		t.Fatalf("profile changed after stale delete: %#v", h.cfg.APIKeyMetadata)
	}
}

func TestAPIKeyCRUDRejectsStaleExpectedID(t *testing.T) {
	h := &Handler{
		cfg: testClientAPIKeyConfig(
			[]string{"key-one"},
			map[string]config.ClientAPIKeyMetadata{"key-one": {ID: "current-id"}},
		),
		configFilePath: writeTestConfigFile(t),
	}

	patchRec := httptest.NewRecorder()
	patchContext, _ := gin.CreateTestContext(patchRec)
	patchContext.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/api-keys", strings.NewReader(`{"index":0,"expected_id":"stale-id","value":"rotated"}`))
	patchContext.Request.Header.Set("Content-Type", "application/json")
	h.PatchAPIKeys(patchContext)
	if patchRec.Code != http.StatusConflict || h.cfg.APIKeys[0] != "key-one" {
		t.Fatalf("stale PATCH status=%d keys=%#v body=%s", patchRec.Code, h.cfg.APIKeys, patchRec.Body.String())
	}

	deleteRec := httptest.NewRecorder()
	deleteContext, _ := gin.CreateTestContext(deleteRec)
	deleteContext.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/api-keys?index=0&expected_id=stale-id", nil)
	h.DeleteAPIKeys(deleteContext)
	if deleteRec.Code != http.StatusConflict || len(h.cfg.APIKeys) != 1 {
		t.Fatalf("stale DELETE status=%d keys=%#v body=%s", deleteRec.Code, h.cfg.APIKeys, deleteRec.Body.String())
	}
}

func TestAPIKeyCRUDRejectsStaleKeyRevisionWithStableID(t *testing.T) {
	h := &Handler{
		cfg: testClientAPIKeyConfig(
			[]string{"key-one"},
			map[string]config.ClientAPIKeyMetadata{"key-one": {ID: "stable-id"}},
		),
		configFilePath: writeTestConfigFile(t),
	}

	patchRec := httptest.NewRecorder()
	patchContext, _ := gin.CreateTestContext(patchRec)
	patchContext.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/api-keys", strings.NewReader(`{"index":0,"expected_id":"stable-id","expected_key_revision":"stale-revision","value":"rotated"}`))
	patchContext.Request.Header.Set("Content-Type", "application/json")
	h.PatchAPIKeys(patchContext)
	if patchRec.Code != http.StatusConflict || h.cfg.APIKeys[0] != "key-one" {
		t.Fatalf("stale PATCH status=%d keys=%#v body=%s", patchRec.Code, h.cfg.APIKeys, patchRec.Body.String())
	}

	deleteRec := httptest.NewRecorder()
	deleteContext, _ := gin.CreateTestContext(deleteRec)
	deleteContext.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/api-keys?index=0&expected_id=stable-id&expected_key_revision=stale-revision", nil)
	h.DeleteAPIKeys(deleteContext)
	if deleteRec.Code != http.StatusConflict || len(h.cfg.APIKeys) != 1 {
		t.Fatalf("stale DELETE status=%d keys=%#v body=%s", deleteRec.Code, h.cfg.APIKeys, deleteRec.Body.String())
	}
}

func TestPutAPIKeyProfilesRejectsStaleExpectedID(t *testing.T) {
	h := &Handler{
		cfg: testClientAPIKeyConfig(
			[]string{"key-one"},
			map[string]config.ClientAPIKeyMetadata{"key-one": {ID: "current-id"}},
		),
		configFilePath: writeTestConfigFile(t),
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/api-key-profiles", strings.NewReader(`[{"index":0,"expected_id":"stale-id","id":"stale-id","disabled":true}]`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PutAPIKeyProfiles(c)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	if got := h.cfg.APIKeyMetadata["key-one"]; got.ID != "current-id" || got.Disabled {
		t.Fatalf("stale bulk update changed metadata: %#v", got)
	}
}

func TestPatchAPIKeyProfileRejectsInvalidID(t *testing.T) {
	h := &Handler{
		cfg:            testClientAPIKeyConfig([]string{"key-one"}, nil),
		configFilePath: writeTestConfigFile(t),
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/api-key-profiles", strings.NewReader(`{"index":0,"id":"invalid id"}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PatchAPIKeyProfile(c)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if len(h.cfg.APIKeyMetadata) != 0 {
		t.Fatalf("invalid profile was persisted: %#v", h.cfg.APIKeyMetadata)
	}
}

func TestAPIKeyProfileAliasLimitUsesRunes(t *testing.T) {
	h := &Handler{
		cfg:            testClientAPIKeyConfig([]string{"key-one"}, nil),
		configFilePath: writeTestConfigFile(t),
	}

	validAlias := strings.Repeat("界", maxClientAPIKeyAliasLength)
	validBody, err := json.Marshal(map[string]any{"index": 0, "alias": validAlias})
	if err != nil {
		t.Fatalf("marshal valid alias: %v", err)
	}
	validRec := httptest.NewRecorder()
	validContext, _ := gin.CreateTestContext(validRec)
	validContext.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/api-key-profiles", strings.NewReader(string(validBody)))
	validContext.Request.Header.Set("Content-Type", "application/json")
	h.PatchAPIKeyProfile(validContext)
	if validRec.Code != http.StatusOK {
		t.Fatalf("valid alias status = %d, want %d; body=%s", validRec.Code, http.StatusOK, validRec.Body.String())
	}

	tooLongAlias := validAlias + "界"
	invalidBody, err := json.Marshal(map[string]any{"index": 0, "alias": tooLongAlias})
	if err != nil {
		t.Fatalf("marshal invalid alias: %v", err)
	}
	invalidRec := httptest.NewRecorder()
	invalidContext, _ := gin.CreateTestContext(invalidRec)
	invalidContext.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/api-key-profiles", strings.NewReader(string(invalidBody)))
	invalidContext.Request.Header.Set("Content-Type", "application/json")
	h.PatchAPIKeyProfile(invalidContext)
	if invalidRec.Code != http.StatusBadRequest {
		t.Fatalf("long alias status = %d, want %d; body=%s", invalidRec.Code, http.StatusBadRequest, invalidRec.Body.String())
	}
	if got := h.cfg.APIKeyMetadata["key-one"].Alias; got != validAlias {
		t.Fatalf("rejected alias changed metadata: rune count=%d", utf8.RuneCountInString(got))
	}
}

func TestAPIKeyProfileRejectsCredentialMaterialAndControlCharacters(t *testing.T) {
	rawKey := "sk-sensitive-value"
	tests := []struct {
		name string
		body string
		keys []string
	}{
		{name: "id", body: `{"index":0,"id":"sk-sensitive-value"}`},
		{name: "cross-key id", body: `{"index":0,"id":"sk-other-sensitive-value"}`, keys: []string{rawKey, "sk-other-sensitive-value"}},
		{name: "alias", body: `{"index":0,"alias":"Production sk-sensitive-value"}`},
		{name: "control", body: `{"index":0,"alias":"Production\nTeam"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			keys := test.keys
			if len(keys) == 0 {
				keys = []string{rawKey}
			}
			h := &Handler{
				cfg:            testClientAPIKeyConfig(keys, nil),
				configFilePath: writeTestConfigFile(t),
			}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/api-key-profiles", strings.NewReader(test.body))
			c.Request.Header.Set("Content-Type", "application/json")

			h.PatchAPIKeyProfile(c)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			if len(h.cfg.APIKeyMetadata) != 0 {
				t.Fatalf("rejected metadata was persisted: %#v", h.cfg.APIKeyMetadata)
			}
		})
	}
}

func TestMaskClientAPIKeyUsesRuneBoundaries(t *testing.T) {
	if got, want := maskClientAPIKey("甲乙丙丁戊己庚辛壬"), "甲乙丙丁*己庚辛壬"; got != want {
		t.Fatalf("maskClientAPIKey() = %q, want %q", got, want)
	}
}

func testClientAPIKeyConfig(keys []string, metadata map[string]config.ClientAPIKeyMetadata) *config.Config {
	cfg := &config.Config{}
	cfg.APIKeys = keys
	cfg.APIKeyMetadata = metadata
	return cfg
}
