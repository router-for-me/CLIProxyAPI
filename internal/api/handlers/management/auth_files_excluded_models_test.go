package management

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/diff"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func newExcludedModelsPatchTestHandler(t *testing.T, kind string) (*Handler, *coreauth.Manager, *coreauth.Auth) {
	t.Helper()
	manager := coreauth.NewManager(nil, nil, nil)
	record := &coreauth.Auth{
		ID: "credential.json", FileName: "credential.json", Provider: "codex",
		Attributes: map[string]string{"auth_kind": kind, "excluded_models": "stale-*", "excluded_models_hash": "stale-hash"},
		Metadata:   map[string]any{"excluded-models": []string{"old-*"}},
	}
	if _, err := manager.Register(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	// Global rules must never be copied into the credential attributes.
	h := NewHandlerWithoutConfigFilePath(&config.Config{OAuthExcludedModels: map[string][]string{"codex": {"global-*"}}}, manager)
	return h, manager, record
}

func patchExcludedModelsFields(h *Handler, fields string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/v8/management/credentials/fields", strings.NewReader(`{"name":"credential.json",`+fields+`}`))
	c.Request.Header.Set("Content-Type", "application/json")
	h.PatchAuthFileFields(c)
	return rec
}

func TestPatchAuthFileFieldsExcludedModelsRuntime(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tt := range []struct {
		name   string
		fields string
		kind   string
		want   string
	}{
		{"override", `"excluded_models":[" Local-* ","LOCAL-*"]`, "oauth", "local-*"},
		{"alias override", `"excluded-models":["local-*"]`, "oauth", "local-*"},
		{"canonical wins", `"excluded_models":[],"excluded-models":["alias-*"]`, "oauth", ""},
		{"empty clears", `"excluded_models":[]`, "oauth", ""},
		{"whitespace clears", `"excluded_models":[" ","\t"]`, "oauth", ""},
		{"null clears", `"excluded_models":null`, "oauth", ""},
		{"apikey override", `"excluded_models":["local-*"]`, "apikey", "local-*"},
		{"apikey clear", `"excluded_models":[]`, "apikey", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h, manager, record := newExcludedModelsPatchTestHandler(t, tt.kind)
			hookCalled := false
			check := func(auth *coreauth.Auth) {
				t.Helper()
				if got := auth.Attributes["excluded_models"]; got != tt.want {
					t.Errorf("excluded_models = %q, want %q", got, tt.want)
				}
				var rules []string
				if tt.want != "" {
					rules = strings.Split(tt.want, ",")
				}
				if got, want := auth.Attributes["excluded_models_hash"], diff.ComputeExcludedModelsHash(rules); got != want {
					t.Errorf("excluded_models_hash = %q, want %q", got, want)
				}
				if tt.want == "" {
					for _, key := range []string{"excluded_models", "excluded_models_hash"} {
						if _, exists := auth.Attributes[key]; exists {
							t.Errorf("stale attribute %s retained", key)
						}
					}
				}
				if got := auth.Attributes["auth_kind"]; got != tt.kind {
					t.Errorf("auth_kind = %q, want %q", got, tt.kind)
				}
			}
			h.postAuthPersistHook = func(_ context.Context, auth *coreauth.Auth) error { hookCalled = true; check(auth); return nil }
			rec := patchExcludedModelsFields(h, tt.fields)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
			}
			updated, ok := manager.GetByID(record.ID)
			if !ok {
				t.Fatal("updated auth missing")
			}
			check(updated)
			if !hookCalled {
				t.Error("post-persist hook not called")
			}
			if _, exists := updated.Metadata["excluded-models"]; exists {
				t.Error("legacy alias retained")
			}
		})
	}
}

func TestUploadAuthFileRejectsInvalidExcludedModels(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)
	for _, content := range []string{
		`{"type":"codex","email":"bad@example.test","excluded_models":123}`,
		`{"type":"gemini-cli","email":"bad@example.test","excluded_models":123}`,
		`{"type":"codex","email":"bad@example.test","excluded_models":["codex-new",1]}`,
	} {
		for _, form := range []string{"multipart", "raw"} {
			t.Run(form+"/"+content, func(t *testing.T) {
				authDir := t.TempDir()
				manager := coreauth.NewManager(nil, nil, nil)
				h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)

				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				if form == "multipart" {
					var body bytes.Buffer
					writer := multipart.NewWriter(&body)
					part, errCreate := writer.CreateFormFile("file", "bad.json")
					if errCreate != nil {
						t.Fatal(errCreate)
					}
					if _, errWrite := part.Write([]byte(content)); errWrite != nil {
						t.Fatal(errWrite)
					}
					if errClose := writer.Close(); errClose != nil {
						t.Fatal(errClose)
					}
					c.Request = httptest.NewRequest(http.MethodPost, "/v8/management/credentials", &body)
					c.Request.Header.Set("Content-Type", writer.FormDataContentType())
				} else {
					c.Request = httptest.NewRequest(http.MethodPost, "/v8/management/credentials?name=bad.json", strings.NewReader(content))
					c.Request.Header.Set("Content-Type", "application/json")
				}
				h.UploadAuthFile(c)

				// Invalid client input is a 400, not a server failure.
				if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "excluded_models") || !strings.Contains(rec.Body.String(), "must be a string") {
					t.Fatalf("status = %d body = %s, want 400 excluded_models validation error", rec.Code, rec.Body.String())
				}
				if entries, errRead := os.ReadDir(authDir); errRead != nil || len(entries) != 0 {
					t.Fatalf("auth dir entries = %v (err %v), want no file written", entries, errRead)
				}
				if auths := manager.List(); len(auths) != 0 {
					t.Fatalf("registered auths = %d, want none", len(auths))
				}
			})
		}
	}
}

func TestPatchAuthFileFieldsRejectsConfigCredential(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	record := &coreauth.Auth{
		ID: "credential.json", Provider: "claude",
		Attributes: map[string]string{"auth_kind": "apikey", "source": "config:claude[token]", "excluded_models": "config-*"},
	}
	if _, err := manager.Register(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)
	before, _ := manager.GetByID(record.ID)
	h.postAuthPersistHook = func(context.Context, *coreauth.Auth) error {
		t.Error("post-persist hook called for config credential")
		return nil
	}

	// Config credentials are not auth files, so this endpoint must not find them.
	rec := patchExcludedModelsFields(h, `"excluded_models":[]`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d: %s, want 404", rec.Code, rec.Body.String())
	}
	after, _ := manager.GetByID(record.ID)
	if !reflect.DeepEqual(after.Attributes, before.Attributes) || !reflect.DeepEqual(after.Metadata, before.Metadata) {
		t.Fatalf("config credential changed: before=%+v/%+v after=%+v/%+v", before.Attributes, before.Metadata, after.Attributes, after.Metadata)
	}
}

func TestPatchAuthFileFieldsRejectsInvalidExcludedModels(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, fields := range []string{
		`"abc":true,"excluded_models":123`,
		`"abc":true,"excluded-models":true`,
		`"abc":true,"excluded_models":{}`,
		`"abc":true,"excluded_models":["codex-new",null]`,
		`"abc":true,"excluded_models":[42,false]`,
		`"abc":true,"excluded_models":[["codex-new"]]`,
		`"abc":true,"excluded_models":"codex-new"`,
		`"abc":true,"excluded_models":" codex-a , codex-b "`,
		`"abc":true,"excluded-models":"codex-new"`,
		`"abc":true,"excluded_models":"  "`,
		`"abc":true,"excluded_models.child":["codex-new"]`,
		`"abc":true,"excluded-models.0":"codex-new"`,
	} {
		t.Run(fields, func(t *testing.T) {
			h, manager, record := newExcludedModelsPatchTestHandler(t, "oauth")
			before, _ := manager.GetByID(record.ID)
			h.postAuthPersistHook = func(context.Context, *coreauth.Auth) error {
				t.Error("post-persist hook called for rejected patch")
				return nil
			}
			rec := patchExcludedModelsFields(h, fields)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d: %s, want 400", rec.Code, rec.Body.String())
			}
			after, _ := manager.GetByID(record.ID)
			if !reflect.DeepEqual(after.Attributes, before.Attributes) || !reflect.DeepEqual(after.Metadata, before.Metadata) {
				t.Fatalf("auth changed after rejected patch: before=%+v/%+v after=%+v/%+v", before.Attributes, before.Metadata, after.Attributes, after.Metadata)
			}
		})
	}
}
