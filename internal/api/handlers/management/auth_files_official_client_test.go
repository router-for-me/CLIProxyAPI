package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func patchAuthFileFieldsForTest(t *testing.T, h *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/fields", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	h.PatchAuthFileFields(ctx)
	return rec
}

func listAuthFilesForTest(t *testing.T, h *Handler) []map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)
	h.ListAuthFiles(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Files []map[string]any `json:"files"`
	}
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &payload); errDecode != nil {
		t.Fatalf("decode list: %v", errDecode)
	}
	return payload.Files
}

func newOfficialClientTestHandler(t *testing.T, auths ...*coreauth.Auth) (*Handler, *coreauth.Manager) {
	t.Helper()
	t.Setenv("MANAGEMENT_PASSWORD", "")
	manager := coreauth.NewManager(&memoryAuthStore{}, nil, nil)
	for _, auth := range auths {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register %s: %v", auth.ID, errRegister)
		}
	}
	return NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager), manager
}

func TestPatchAuthFileFieldsOfficialClientOnly(t *testing.T) {
	h, manager := newOfficialClientTestHandler(t,
		&coreauth.Auth{ID: "claude.json", FileName: "claude.json", Provider: "claude", Status: coreauth.StatusActive, Metadata: map[string]any{"type": "claude"}},
		&coreauth.Auth{ID: "codex.json", FileName: "codex.json", Provider: "codex", Status: coreauth.StatusActive, Metadata: map[string]any{"type": "codex"}},
		&coreauth.Auth{ID: "gemini.json", FileName: "gemini.json", Provider: "gemini", Status: coreauth.StatusActive, Metadata: map[string]any{"type": "gemini"}},
	)
	restricted := func(id string) bool {
		auth, ok := manager.GetByID(id)
		if !ok {
			t.Fatalf("auth %s missing", id)
		}
		return auth.OfficialClientOnly()
	}

	for _, id := range []string{"claude.json", "codex.json"} {
		if rec := patchAuthFileFieldsForTest(t, h, `{"name":"`+id+`","official_client_only":true}`); rec.Code != http.StatusOK {
			t.Fatalf("enable %s: status = %d body=%s", id, rec.Code, rec.Body.String())
		}
		if !restricted(id) {
			t.Fatalf("%s not restricted after enabling", id)
		}
	}

	// Anything that is not a strict boolean must be refused and leave the setting untouched.
	for _, bad := range []string{`"false"`, `"yes"`, `0`, `1`, `{}`, `[]`} {
		rec := patchAuthFileFieldsForTest(t, h, `{"name":"claude.json","official_client_only":`+bad+`}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("value %s: status = %d, want 400 body=%s", bad, rec.Code, rec.Body.String())
		}
		if !restricted("claude.json") {
			t.Fatalf("value %s lifted the restriction", bad)
		}
	}

	if rec := patchAuthFileFieldsForTest(t, h, `{"name":"claude.json","official_client_only.sub":true}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("nested field: status = %d, want 400 body=%s", rec.Code, rec.Body.String())
	}
	if rec := patchAuthFileFieldsForTest(t, h, `{"name":"gemini.json","official_client_only":true}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unsupported provider: status = %d, want 400 body=%s", rec.Code, rec.Body.String())
	}
	if auth, _ := manager.GetByID("gemini.json"); auth.Metadata[coreauth.MetadataOfficialClientOnly] != nil {
		t.Fatal("unsupported provider received the setting")
	}

	if rec := patchAuthFileFieldsForTest(t, h, `{"name":"claude.json","official_client_only":false}`); rec.Code != http.StatusOK {
		t.Fatalf("disable: status = %d body=%s", rec.Code, rec.Body.String())
	}
	if restricted("claude.json") {
		t.Fatal("claude.json still restricted after disabling")
	}
	if rec := patchAuthFileFieldsForTest(t, h, `{"name":"codex.json","official_client_only":null}`); rec.Code != http.StatusOK {
		t.Fatalf("clear: status = %d body=%s", rec.Code, rec.Body.String())
	}
	if auth, _ := manager.GetByID("codex.json"); restricted("codex.json") || auth.Metadata[coreauth.MetadataOfficialClientOnly] != nil {
		t.Fatal("null did not clear the setting")
	}
}

func TestListAuthFilesExposesOfficialClientOnly(t *testing.T) {
	authDir := t.TempDir()
	fileBacked := func(name, provider string) *coreauth.Auth {
		path := filepath.Join(authDir, name)
		if errWrite := os.WriteFile(path, []byte(`{"type":"`+provider+`"}`), 0o600); errWrite != nil {
			t.Fatalf("write %s: %v", name, errWrite)
		}
		return &coreauth.Auth{
			ID: name, FileName: name, Provider: provider, Status: coreauth.StatusActive,
			Attributes: map[string]string{"path": path},
			Metadata:   map[string]any{"type": provider},
		}
	}
	h, _ := newOfficialClientTestHandler(t, fileBacked("claude.json", "claude"), fileBacked("gemini.json", "gemini"))
	byID := func() map[string]map[string]any {
		out := map[string]map[string]any{}
		for _, file := range listAuthFilesForTest(t, h) {
			out[file["id"].(string)] = file
		}
		return out
	}

	if got, ok := byID()["claude.json"][coreauth.MetadataOfficialClientOnly]; !ok || got != false {
		t.Fatalf("claude.json official_client_only = %#v (present=%v), want false", got, ok)
	}
	if rec := patchAuthFileFieldsForTest(t, h, `{"name":"claude.json","official_client_only":true}`); rec.Code != http.StatusOK {
		t.Fatalf("enable: status = %d body=%s", rec.Code, rec.Body.String())
	}
	files := byID()
	if got := files["claude.json"][coreauth.MetadataOfficialClientOnly]; got != true {
		t.Fatalf("claude.json official_client_only = %#v, want true", got)
	}
	if _, present := files["gemini.json"][coreauth.MetadataOfficialClientOnly]; present {
		t.Fatal("unsupported provider should not advertise official_client_only")
	}
}

func TestListAuthFilesFromDiskExposesOfficialClientOnly(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	authDir := t.TempDir()
	write := func(name, body string) {
		if errWrite := os.WriteFile(filepath.Join(authDir, name), []byte(body), 0o600); errWrite != nil {
			t.Fatalf("write %s: %v", name, errWrite)
		}
	}
	write("on.json", `{"type":"codex","official_client_only":true}`)
	write("off.json", `{"type":"claude"}`)
	write("malformed.json", `{"type":"claude","official_client_only":"maybe"}`)
	write("other.json", `{"type":"gemini","official_client_only":true}`)

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)
	got := map[string]map[string]any{}
	for _, file := range listAuthFilesForTest(t, h) {
		got[file["name"].(string)] = file
	}
	if got["on.json"][coreauth.MetadataOfficialClientOnly] != true {
		t.Fatalf("on.json = %#v, want true", got["on.json"][coreauth.MetadataOfficialClientOnly])
	}
	if got["off.json"][coreauth.MetadataOfficialClientOnly] != false {
		t.Fatalf("off.json = %#v, want false", got["off.json"][coreauth.MetadataOfficialClientOnly])
	}
	if got["malformed.json"][coreauth.MetadataOfficialClientOnly] != true {
		t.Fatalf("malformed.json = %#v, want true (fail closed)", got["malformed.json"][coreauth.MetadataOfficialClientOnly])
	}
	if _, present := got["other.json"][coreauth.MetadataOfficialClientOnly]; present {
		t.Fatal("unsupported provider should not advertise official_client_only")
	}
}
