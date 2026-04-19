package management

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// findPolicy returns the APIKeyPolicy for key, or nil if none.
func findPolicy(policies []config.APIKeyPolicy, key string) *config.APIKeyPolicy {
	for i := range policies {
		if policies[i].Key == key {
			return &policies[i]
		}
	}
	return nil
}

func TestPatchAPIKeys_RenameViaOldNewMovesPolicy(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: config.SDKConfig{
				APIKeys: []string{"sk-old"},
				APIKeyPolicies: []config.APIKeyPolicy{
					{Key: "sk-old", AllowedModels: []string{"gpt-4o*"}},
				},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/api-keys",
		bytes.NewBufferString(`{"old":"sk-old","new":"sk-new"}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PatchAPIKeys(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := h.cfg.APIKeys; len(got) != 1 || got[0] != "sk-new" {
		t.Fatalf("APIKeys after rename = %#v, want [sk-new]", got)
	}
	if findPolicy(h.cfg.APIKeyPolicies, "sk-old") != nil {
		t.Fatalf("policy for sk-old must be removed after rename")
	}
	p := findPolicy(h.cfg.APIKeyPolicies, "sk-new")
	if p == nil {
		t.Fatalf("policy for sk-new must exist after rename")
	}
	if len(p.AllowedModels) != 1 || p.AllowedModels[0] != "gpt-4o*" {
		t.Fatalf("renamed policy lost AllowedModels: %#v", p.AllowedModels)
	}
	// The cached lookup must reflect the rename: the ACL check under sk-new
	// should use the migrated policy, not fall back to default.
	if !h.cfg.IsModelAllowedForKey("sk-new", "gpt-4o-mini") {
		t.Fatalf("sk-new must allow gpt-4o-mini via migrated policy")
	}
	if h.cfg.IsModelAllowedForKey("sk-new", "claude-3-5-sonnet-20241022") {
		t.Fatalf("sk-new must reject models outside its allowlist")
	}
}

func TestPatchAPIKeys_RenameViaIndexValueMovesPolicy(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: config.SDKConfig{
				APIKeys: []string{"sk-first", "sk-old"},
				APIKeyPolicies: []config.APIKeyPolicy{
					{Key: "sk-old", AllowedModels: []string{"claude-3-*"}},
				},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/api-keys",
		bytes.NewBufferString(`{"index":1,"value":"sk-rotated"}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PatchAPIKeys(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := h.cfg.APIKeys; len(got) != 2 || got[1] != "sk-rotated" {
		t.Fatalf("APIKeys after rotation = %#v, want [sk-first, sk-rotated]", got)
	}
	if findPolicy(h.cfg.APIKeyPolicies, "sk-old") != nil {
		t.Fatalf("policy for sk-old must be removed after rotation")
	}
	if p := findPolicy(h.cfg.APIKeyPolicies, "sk-rotated"); p == nil {
		t.Fatalf("policy for sk-rotated must exist after rotation")
	}
}

// If the NEW key already has its own policy, the rename must NOT overwrite it —
// the old policy is dropped and the target wins. This is the documented
// merge rule in renameAPIKeyPolicy.
func TestPatchAPIKeys_RenameWithExistingTargetPolicyKeepsTarget(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: config.SDKConfig{
				APIKeys: []string{"sk-src", "sk-dst"},
				APIKeyPolicies: []config.APIKeyPolicy{
					{Key: "sk-src", AllowedModels: []string{"claude-3-*"}},
					{Key: "sk-dst", AllowedModels: []string{"gpt-4o*"}},
				},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/api-keys",
		bytes.NewBufferString(`{"old":"sk-src","new":"sk-dst"}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PatchAPIKeys(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// Only the sk-dst policy should remain (sk-src's row is dropped).
	if findPolicy(h.cfg.APIKeyPolicies, "sk-src") != nil {
		t.Fatalf("sk-src policy must be dropped")
	}
	p := findPolicy(h.cfg.APIKeyPolicies, "sk-dst")
	if p == nil {
		t.Fatalf("sk-dst policy must survive")
	}
	if len(p.AllowedModels) != 1 || p.AllowedModels[0] != "gpt-4o*" {
		t.Fatalf("sk-dst policy mutated: %#v", p.AllowedModels)
	}
}

// A rename targeting a key that has no prior policy simply renames the row.
func TestPatchAPIKeys_AppendNewKeyLeavesExistingPolicies(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: config.SDKConfig{
				APIKeys: []string{"sk-a"},
				APIKeyPolicies: []config.APIKeyPolicy{
					{Key: "sk-a", AllowedModels: []string{"gpt-4o*"}},
				},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	// old=not-there, new=sk-brand-new triggers the append-new path.
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/api-keys",
		bytes.NewBufferString(`{"old":"sk-does-not-exist","new":"sk-brand-new"}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PatchAPIKeys(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// sk-a's policy must be untouched.
	p := findPolicy(h.cfg.APIKeyPolicies, "sk-a")
	if p == nil || len(p.AllowedModels) != 1 || p.AllowedModels[0] != "gpt-4o*" {
		t.Fatalf("sk-a policy mutated unexpectedly: %#v", h.cfg.APIKeyPolicies)
	}
	// sk-brand-new must NOT have a policy row (falls back to default-allow).
	if findPolicy(h.cfg.APIKeyPolicies, "sk-brand-new") != nil {
		t.Fatalf("sk-brand-new must not auto-create a policy")
	}
	if got := h.cfg.APIKeys; len(got) != 2 || got[1] != "sk-brand-new" {
		t.Fatalf("APIKeys after append = %#v", got)
	}
}

func TestDeleteAPIKeys_ByValueRemovesPolicy(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: config.SDKConfig{
				APIKeys: []string{"sk-a", "sk-b"},
				APIKeyPolicies: []config.APIKeyPolicy{
					{Key: "sk-a", AllowedModels: []string{"gpt-4o*"}},
					{Key: "sk-b", AllowedModels: []string{"claude-3-*"}},
				},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/api-keys?value=sk-a", nil)

	h.DeleteAPIKeys(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := h.cfg.APIKeys; len(got) != 1 || got[0] != "sk-b" {
		t.Fatalf("APIKeys after delete = %#v, want [sk-b]", got)
	}
	if findPolicy(h.cfg.APIKeyPolicies, "sk-a") != nil {
		t.Fatalf("policy for deleted sk-a must be removed — this is the review-flagged bug")
	}
	// sk-b's policy is unaffected.
	if findPolicy(h.cfg.APIKeyPolicies, "sk-b") == nil {
		t.Fatalf("policy for sk-b must survive")
	}
	// Cache must reflect the removal: sk-a is no longer a known key, so it
	// falls back to the default policy (allow-all here).
	if !h.cfg.IsModelAllowedForKey("sk-a", "gpt-4o") {
		t.Fatalf("after delete, sk-a falls back to default-allow")
	}
}

func TestDeleteAPIKeys_ByIndexRemovesPolicy(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: config.SDKConfig{
				APIKeys: []string{"sk-a", "sk-b"},
				APIKeyPolicies: []config.APIKeyPolicy{
					{Key: "sk-a", AllowedModels: []string{"gpt-4o*"}},
					{Key: "sk-b", AllowedModels: []string{"claude-3-*"}},
				},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/api-keys?index=1", nil)

	h.DeleteAPIKeys(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := h.cfg.APIKeys; len(got) != 1 || got[0] != "sk-a" {
		t.Fatalf("APIKeys after delete = %#v, want [sk-a]", got)
	}
	if findPolicy(h.cfg.APIKeyPolicies, "sk-b") != nil {
		t.Fatalf("policy for deleted sk-b must be removed")
	}
}

func TestDeleteAPIKeys_LastPolicyClearsSlice(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: config.SDKConfig{
				APIKeys: []string{"sk-only"},
				APIKeyPolicies: []config.APIKeyPolicy{
					{Key: "sk-only", AllowedModels: []string{"gpt-4o*"}},
				},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/api-keys?value=sk-only", nil)

	h.DeleteAPIKeys(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if h.cfg.APIKeyPolicies != nil {
		t.Fatalf("after removing last policy row the slice should be nil, got %#v", h.cfg.APIKeyPolicies)
	}
}

// TestPatchAPIKeys_RenameViaOldNewRenamesAllDuplicates pins down a subtle
// behavior for legacy configs that accidentally contain duplicate values in
// APIKeys. A name-based patch (old/new) is a bulk rename, so it must rewrite
// every slot that matches `old`. If it stopped at the first match, the
// corresponding policy migration (renameAPIKeyPolicy is always global) would
// leave the surviving duplicates unrestricted — falling back to the default
// allow-all policy.
func TestPatchAPIKeys_RenameViaOldNewRenamesAllDuplicates(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: config.SDKConfig{
				APIKeys: []string{"sk-dup", "sk-other", "sk-dup"},
				APIKeyPolicies: []config.APIKeyPolicy{
					{Key: "sk-dup", AllowedModels: []string{"gpt-4o*"}},
				},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/api-keys",
		bytes.NewBufferString(`{"old":"sk-dup","new":"sk-new"}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PatchAPIKeys(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// Both sk-dup entries must have become sk-new so nothing is left behind.
	wantKeys := []string{"sk-new", "sk-other", "sk-new"}
	if got := h.cfg.APIKeys; len(got) != len(wantKeys) {
		t.Fatalf("APIKeys after rename = %#v, want %#v", got, wantKeys)
	}
	for i := range wantKeys {
		if h.cfg.APIKeys[i] != wantKeys[i] {
			t.Fatalf("APIKeys[%d] = %q, want %q (full=%#v)", i, h.cfg.APIKeys[i], wantKeys[i], h.cfg.APIKeys)
		}
	}
	// And the policy must now apply under the new key — sanity check via the
	// ACL so a regression surfaces as a permissiveness change, not just a
	// shape mismatch.
	if !h.cfg.IsModelAllowedForKey("sk-new", "gpt-4o-mini") {
		t.Fatalf("sk-new must allow gpt-4o-mini via migrated policy")
	}
	if h.cfg.IsModelAllowedForKey("sk-new", "claude-3-5-sonnet-20241022") {
		t.Fatalf("sk-new must still reject models outside its allowlist")
	}
}

// TestPatchAPIKeys_IndexedReplaceLeavesPolicyWhenDuplicateRemains covers the
// indexed-patch path under duplicate-APIKeys conditions. When the caller
// rewrites one slot but another slot still holds the old value, migrating the
// policy away from that value would leave the surviving slot unrestricted.
// The policy must stay attached to the old value; the new value falls back to
// the default until the caller sets an explicit policy via PUT.
func TestPatchAPIKeys_IndexedReplaceLeavesPolicyWhenDuplicateRemains(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: config.SDKConfig{
				APIKeys: []string{"sk-dup", "sk-dup"},
				APIKeyPolicies: []config.APIKeyPolicy{
					{Key: "sk-dup", AllowedModels: []string{"gpt-4o*"}},
				},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/api-keys",
		bytes.NewBufferString(`{"index":0,"value":"sk-new"}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PatchAPIKeys(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// Remaining sk-dup must still be restricted by its policy.
	if findPolicy(h.cfg.APIKeyPolicies, "sk-dup") == nil {
		t.Fatalf("policy for surviving sk-dup duplicate must remain")
	}
	if !h.cfg.IsModelAllowedForKey("sk-dup", "gpt-4o-mini") {
		t.Fatalf("surviving sk-dup must still allow gpt-4o-mini")
	}
	if h.cfg.IsModelAllowedForKey("sk-dup", "claude-3-5-sonnet-20241022") {
		t.Fatalf("surviving sk-dup must still reject claude — policy must not have moved")
	}
}

// TestDeleteAPIKeys_ByIndexKeepsPolicyWhenDuplicateRemains mirrors the patch
// case above for the delete path: removing one of two entries with the same
// value must not drop the policy row, otherwise the surviving duplicate
// becomes unrestricted under the default policy.
func TestDeleteAPIKeys_ByIndexKeepsPolicyWhenDuplicateRemains(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: config.SDKConfig{
				APIKeys: []string{"sk-dup", "sk-dup"},
				APIKeyPolicies: []config.APIKeyPolicy{
					{Key: "sk-dup", AllowedModels: []string{"gpt-4o*"}},
				},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/api-keys?index=0", nil)

	h.DeleteAPIKeys(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := h.cfg.APIKeys; len(got) != 1 || got[0] != "sk-dup" {
		t.Fatalf("APIKeys after indexed delete = %#v, want [sk-dup]", got)
	}
	if findPolicy(h.cfg.APIKeyPolicies, "sk-dup") == nil {
		t.Fatalf("policy for surviving sk-dup must remain")
	}
	if !h.cfg.IsModelAllowedForKey("sk-dup", "gpt-4o-mini") {
		t.Fatalf("surviving sk-dup must still allow gpt-4o-mini")
	}
	if h.cfg.IsModelAllowedForKey("sk-dup", "claude-3-5-sonnet-20241022") {
		t.Fatalf("surviving sk-dup must still reject claude — policy must not have been dropped")
	}
}

// PutAPIKeys replaces the entire policy set; stale policies for keys that
// were dropped in the PUT body must not survive.
func TestPutAPIKeys_StructuredBodyRebuildsPolicies(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: config.SDKConfig{
				APIKeys: []string{"sk-stale"},
				APIKeyPolicies: []config.APIKeyPolicy{
					{Key: "sk-stale", AllowedModels: []string{"claude-3-*"}},
				},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/api-keys",
		bytes.NewBufferString(`[{"key":"sk-fresh","allowedModels":["gpt-4o*"]}]`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PutAPIKeys(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := h.cfg.APIKeys; len(got) != 1 || got[0] != "sk-fresh" {
		t.Fatalf("APIKeys after put = %#v", got)
	}
	if findPolicy(h.cfg.APIKeyPolicies, "sk-stale") != nil {
		t.Fatalf("stale sk-stale policy must be dropped by PUT")
	}
	if p := findPolicy(h.cfg.APIKeyPolicies, "sk-fresh"); p == nil || len(p.AllowedModels) != 1 {
		t.Fatalf("sk-fresh policy must be present: %#v", h.cfg.APIKeyPolicies)
	}
	// Cache must see the new set.
	if !h.cfg.IsModelAllowedForKey("sk-fresh", "gpt-4o") {
		t.Fatalf("sk-fresh must allow gpt-4o via new policy")
	}
}
