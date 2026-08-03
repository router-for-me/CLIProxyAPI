package auth

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestExtractAccessToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		metadata map[string]any
		expected string
	}{
		{
			"antigravity top-level access_token",
			map[string]any{"access_token": "tok-abc"},
			"tok-abc",
		},
		{
			"gemini nested token.access_token",
			map[string]any{
				"token": map[string]any{"access_token": "tok-nested"},
			},
			"tok-nested",
		},
		{
			"top-level takes precedence over nested",
			map[string]any{
				"access_token": "tok-top",
				"token":        map[string]any{"access_token": "tok-nested"},
			},
			"tok-top",
		},
		{
			"empty metadata",
			map[string]any{},
			"",
		},
		{
			"whitespace-only access_token",
			map[string]any{"access_token": "   "},
			"",
		},
		{
			"wrong type access_token",
			map[string]any{"access_token": 12345},
			"",
		},
		{
			"token is not a map",
			map[string]any{"token": "not-a-map"},
			"",
		},
		{
			"nested whitespace-only",
			map[string]any{
				"token": map[string]any{"access_token": "  "},
			},
			"",
		},
		{
			"fallback to nested when top-level empty",
			map[string]any{
				"access_token": "",
				"token":        map[string]any{"access_token": "tok-fallback"},
			},
			"tok-fallback",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := extractAccessToken(tt.metadata)
			if got != tt.expected {
				t.Errorf("extractAccessToken() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestFileTokenStoreSaveExistingMetadataSetsFileAttributes(t *testing.T) {
	tests := []struct {
		name          string
		existingToken string
		savedToken    string
	}{
		{name: "unchanged content", existingToken: "token", savedToken: "token"},
		{name: "overwritten content", existingToken: "old-token", savedToken: "new-token"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			baseDir := t.TempDir()
			fileName := "antigravity-user.json"
			path := filepath.Join(baseDir, fileName)
			existing := []byte(`{"type":"antigravity","access_token":"` + tt.existingToken + `","disabled":false}`)
			if errWrite := os.WriteFile(path, existing, 0o600); errWrite != nil {
				t.Fatalf("write existing auth file: %v", errWrite)
			}

			store := NewFileTokenStore()
			store.SetBaseDir(baseDir)
			auth := &cliproxyauth.Auth{
				ID:       fileName,
				FileName: fileName,
				Metadata: map[string]any{
					"type":         "antigravity",
					"access_token": tt.savedToken,
				},
			}

			savedPath, errSave := store.Save(context.Background(), auth)
			if errSave != nil {
				t.Fatalf("Save() error = %v", errSave)
			}
			if savedPath != path {
				t.Fatalf("Save() path = %q, want %q", savedPath, path)
			}
			if got := auth.Attributes[cliproxyauth.AttributePath]; got != path {
				t.Errorf("path attribute = %q, want %q", got, path)
			}
			if got := auth.Attributes[cliproxyauth.AttributeSource]; got != path {
				t.Errorf("source attribute = %q, want %q", got, path)
			}
			if got := auth.Attributes[cliproxyauth.AttributeSourceBackend]; got != cliproxyauth.AuthSourceFile {
				t.Errorf("source backend attribute = %q, want %q", got, cliproxyauth.AuthSourceFile)
			}
			persisted, errRead := os.ReadFile(path)
			if errRead != nil {
				t.Fatalf("read saved auth file: %v", errRead)
			}
			expected := []byte(`{"type":"antigravity","access_token":"` + tt.savedToken + `","disabled":false}`)
			if !jsonEqual(persisted, expected) {
				t.Errorf("saved auth file = %s, want JSON equal to %s", persisted, expected)
			}
		})
	}
}

func TestFileTokenStoreSaveRejectsInvalidWeight(t *testing.T) {
	baseDir := t.TempDir()
	store := NewFileTokenStore()
	store.SetBaseDir(baseDir)
	auth := &cliproxyauth.Auth{
		ID:       "invalid.json",
		FileName: "invalid.json",
		Metadata: map[string]any{
			"type":                       "test",
			cliproxyauth.AttributeWeight: 1.5,
		},
	}

	if _, errSave := store.Save(context.Background(), auth); errSave == nil {
		t.Fatal("Save() accepted an invalid weight")
	}
	if _, errStat := os.Stat(filepath.Join(baseDir, auth.FileName)); !os.IsNotExist(errStat) {
		t.Fatalf("invalid auth file was persisted: %v", errStat)
	}
}

func TestFileTokenStoreListSkipsInvalidPluginSourceWeight(t *testing.T) {
	baseDir := t.TempDir()
	path := filepath.Join(baseDir, "plugin.json")
	if errWrite := os.WriteFile(path, []byte(`{"type":"plugin","weight":"invalid"}`), 0o600); errWrite != nil {
		t.Fatalf("write auth file: %v", errWrite)
	}

	parserCalled := false
	RegisterPluginAuthParser(fileStoreMultiAuthParserFunc(func(context.Context, pluginapi.AuthParseRequest) ([]*cliproxyauth.Auth, bool, error) {
		parserCalled = true
		return []*cliproxyauth.Auth{{ID: "plugin.json", Provider: "plugin"}}, true, nil
	}))
	t.Cleanup(func() {
		RegisterPluginAuthParser(nil)
	})

	store := NewFileTokenStore()
	store.SetBaseDir(baseDir)
	auths, errList := store.List(context.Background())
	if errList != nil {
		t.Fatalf("List() error = %v", errList)
	}
	if parserCalled {
		t.Fatal("plugin parser was called for an invalid persisted source")
	}
	if len(auths) != 0 {
		t.Fatalf("List() returned invalid plugin auths: %#v", auths)
	}
}

func TestFileTokenStoreListExpandsPluginMultiAuths(t *testing.T) {
	baseDir := t.TempDir()
	path := filepath.Join(baseDir, "geminicli.json")
	if errWrite := os.WriteFile(path, []byte(`{"type":"gemini-cli","weight":3,"headers":{"X-Test":"value"}}`), 0o600); errWrite != nil {
		t.Fatalf("write auth file: %v", errWrite)
	}

	RegisterPluginAuthParser(fileStoreMultiAuthParserFunc(func(ctx context.Context, req pluginapi.AuthParseRequest) ([]*cliproxyauth.Auth, bool, error) {
		if req.Provider != "gemini-cli" || req.Path != path || req.FileName != "geminicli.json" {
			t.Fatalf("ParseAuths request = %#v, want file context", req)
		}
		return []*cliproxyauth.Auth{
			{
				ID:       "geminicli.json",
				Provider: "gemini-cli",
				Metadata: map[string]any{
					"type": "gemini-cli",
					"headers": map[string]any{
						"X-Test": "value",
					},
				},
			},
			nil,
			{
				ID:       "geminicli-project-a.json",
				Provider: "gemini-cli",
				Metadata: map[string]any{
					"type":       "gemini-cli",
					"project_id": "project-a",
					"headers": map[string]any{
						"X-Test": "value",
					},
				},
			},
		}, true, nil
	}))
	t.Cleanup(func() {
		RegisterPluginAuthParser(nil)
	})

	store := NewFileTokenStore()
	store.SetBaseDir(baseDir)
	auths, errList := store.List(context.Background())
	if errList != nil {
		t.Fatalf("List() error = %v", errList)
	}
	if len(auths) != 2 {
		t.Fatalf("List() len = %d, want two plugin auths", len(auths))
	}
	if firstIndex, secondIndex := auths[0].EnsureIndex(), auths[1].EnsureIndex(); firstIndex == "" || firstIndex == secondIndex {
		t.Fatalf("auth indexes = %q/%q, want distinct non-empty indexes", firstIndex, secondIndex)
	}
	for _, auth := range auths {
		if !cliproxyauth.IsPluginVirtualAuth(auth) {
			t.Fatalf("auth attributes = %#v, want plugin virtual marker", auth.Attributes)
		}
		if auth.Attributes[cliproxyauth.AttributeVirtualSource] != path {
			t.Fatalf("virtual_source = %q, want %q", auth.Attributes[cliproxyauth.AttributeVirtualSource], path)
		}
		if auth.Attributes["path"] != path || auth.Attributes["source"] != path {
			t.Fatalf("auth attributes = %#v, want source path", auth.Attributes)
		}
		if gotHeader := auth.Attributes["header:X-Test"]; gotHeader != "value" {
			t.Fatalf("header:X-Test = %q, want value", gotHeader)
		}
		if gotWeight := auth.Attributes[cliproxyauth.AttributeWeight]; gotWeight != "3" {
			t.Fatalf("weight = %q, want 3", gotWeight)
		}
	}
	if gotProject := auths[1].Metadata["project_id"]; gotProject != "project-a" {
		t.Fatalf("project_id = %#v, want project-a", gotProject)
	}
}

func TestFileTokenStoreListAppliesSourceDisabledToPluginMultiAuths(t *testing.T) {
	baseDir := t.TempDir()
	path := filepath.Join(baseDir, "geminicli.json")
	if errWrite := os.WriteFile(path, []byte(`{"type":"gemini-cli","disabled":true}`), 0o600); errWrite != nil {
		t.Fatalf("write auth file: %v", errWrite)
	}

	RegisterPluginAuthParser(fileStoreMultiAuthParserFunc(func(context.Context, pluginapi.AuthParseRequest) ([]*cliproxyauth.Auth, bool, error) {
		return []*cliproxyauth.Auth{
			{ID: "geminicli.json", Provider: "gemini-cli", Metadata: map[string]any{"type": "gemini-cli"}},
			{ID: "geminicli-project-a.json", Provider: "gemini-cli", Metadata: map[string]any{"type": "gemini-cli", "project_id": "project-a"}},
		}, true, nil
	}))
	t.Cleanup(func() {
		RegisterPluginAuthParser(nil)
	})

	store := NewFileTokenStore()
	store.SetBaseDir(baseDir)
	auths, errList := store.List(context.Background())
	if errList != nil {
		t.Fatalf("List() error = %v", errList)
	}
	if len(auths) != 2 {
		t.Fatalf("List() len = %d, want two plugin auths", len(auths))
	}
	for _, auth := range auths {
		if !auth.Disabled || auth.Status != cliproxyauth.StatusDisabled {
			t.Fatalf("auth %s disabled/status = %v/%s, want disabled", auth.ID, auth.Disabled, auth.Status)
		}
		if got, _ := auth.Metadata["disabled"].(bool); !got {
			t.Fatalf("auth %s metadata disabled = %#v, want true", auth.ID, auth.Metadata["disabled"])
		}
	}
}

func TestFileTokenStoreListPluginHandledEmptySuppressesBuiltin(t *testing.T) {
	baseDir := t.TempDir()
	path := filepath.Join(baseDir, "codex.json")
	if errWrite := os.WriteFile(path, []byte(`{"type":"codex","access_token":"token"}`), 0o600); errWrite != nil {
		t.Fatalf("write auth file: %v", errWrite)
	}

	RegisterPluginAuthParser(fileStoreMultiAuthParserFunc(func(context.Context, pluginapi.AuthParseRequest) ([]*cliproxyauth.Auth, bool, error) {
		return nil, true, nil
	}))
	t.Cleanup(func() {
		RegisterPluginAuthParser(nil)
	})

	store := NewFileTokenStore()
	store.SetBaseDir(baseDir)
	auths, errList := store.List(context.Background())
	if errList != nil {
		t.Fatalf("List() error = %v", errList)
	}
	if len(auths) != 0 {
		t.Fatalf("List() len = %d, want plugin-handled empty result", len(auths))
	}
}

type fileStoreMultiAuthParserFunc func(context.Context, pluginapi.AuthParseRequest) ([]*cliproxyauth.Auth, bool, error)

func (f fileStoreMultiAuthParserFunc) ParseAuth(context.Context, pluginapi.AuthParseRequest) (*cliproxyauth.Auth, bool, error) {
	return nil, false, nil
}

func (f fileStoreMultiAuthParserFunc) ParseAuths(ctx context.Context, req pluginapi.AuthParseRequest) ([]*cliproxyauth.Auth, bool, error) {
	return f(ctx, req)
}
