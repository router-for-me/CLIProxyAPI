package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// TestPutGetAPIKeys_RoundTripsObjectForm ensures the object form
// ({api-key, allowed-models}) survives a PUT then GET without losing fields.
func TestPutGetAPIKeys_RoundTripsObjectForm(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg:            &config.Config{},
		configFilePath: writeTestConfigFile(t),
	}

	// PUT a mixed list: plain string + object with allowlist + object deny-all.
	// PUT accepts the bare array form (or {"items":[...]}); GET returns {"api-keys":[...]}.
	body := `["sk-admin",{"api-key":"sk-guest","allowed-models":["gpt-4o-mini","claude-*"]},{"api-key":"sk-frozen","allowed-models":[]}]`
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/api-keys", bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PutAPIKeys(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if len(h.cfg.APIKeys) != 3 {
		t.Fatalf("after PUT, APIKeys len = %d, want 3", len(h.cfg.APIKeys))
	}

	// Verify the allowlist entry is intact.
	entry, ok := h.cfg.APIKeys.Lookup("sk-guest")
	if !ok {
		t.Fatal("sk-guest missing after PUT")
	}
	if entry.AllowedModels == nil {
		t.Fatal("sk-guest allowlist lost (nil)")
	}
	if got := *entry.AllowedModels; len(got) != 2 || got[0] != "gpt-4o-mini" || got[1] != "claude-*" {
		t.Fatalf("sk-guest allowlist = %v, want [gpt-4o-mini claude-*]", got)
	}

	// Verify deny-all survives.
	deny, ok := h.cfg.APIKeys.Lookup("sk-frozen")
	if !ok || deny.AllowedModels == nil || len(*deny.AllowedModels) != 0 {
		t.Fatalf("sk-frozen deny-all shape wrong: %+v", deny)
	}

	// GET it back via the handler.
	rec2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(rec2)
	c2.Request = httptest.NewRequest(http.MethodGet, "/v0/management/api-keys", nil)

	h.GetAPIKeys(c2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", rec2.Code)
	}
	var resp struct {
		Keys config.APIKeyList `json:"api-keys"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &resp); err != nil {
		t.Fatalf("GET body unmarshal failed: %v; body=%s", err, rec2.Body.String())
	}
	if len(resp.Keys) != 3 {
		t.Fatalf("GET returned %d keys, want 3", len(resp.Keys))
	}
	got, ok := resp.Keys.Lookup("sk-guest")
	if !ok || got.AllowedModels == nil {
		t.Fatalf("GET: sk-guest allowlist lost; resp=%+v", resp.Keys)
	}
	if got := *got.AllowedModels; len(got) != 2 {
		t.Fatalf("GET: sk-guest allowlist len = %d, want 2", len(got))
	}
}

// TestPatchAPIKeys_StringPatchPreservesAllowlist verifies that editing a key
// via the string form (as the TUI does) preserves the existing allowlist.
func TestPatchAPIKeys_StringPatchPreservesAllowlist(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: config.SDKConfig{
				APIKeys: config.APIKeyList{
					{APIKey: "old-name", AllowedModels: ptrStringList("gpt-4o")},
				},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	patchBody := `{"old":"old-name","new":"new-name"}`
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/api-keys", bytes.NewBufferString(patchBody))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PatchAPIKeys(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	entry, ok := h.cfg.APIKeys.Lookup("new-name")
	if !ok {
		t.Fatal("renamed key missing after PATCH")
	}
	if entry.AllowedModels == nil || len(*entry.AllowedModels) != 1 {
		t.Fatalf("allowlist not preserved on rename: %+v", entry)
	}
	if old, ok := h.cfg.APIKeys.Lookup("old-name"); ok {
		t.Fatalf("old name should be gone, still found: %+v", old)
	}
}

func ptrStringList(vals ...string) *[]string {
	out := make([]string, 0, len(vals))
	out = append(out, vals...)
	return &out
}
