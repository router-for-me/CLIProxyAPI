package management

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// fakePluginAuthParser mimics a plugin that owns provider "opencode-go" and
// assigns its own stable, content-derived auth ID instead of the file name.
type fakePluginAuthParser struct {
	provider string
	id       string
	calls    int
}

func (p *fakePluginAuthParser) ParseAuth(ctx context.Context, req pluginapi.AuthParseRequest) (*coreauth.Auth, bool, error) {
	auths, handled, err := p.ParseAuths(ctx, req)
	if err != nil || !handled || len(auths) == 0 {
		return nil, handled, err
	}
	return auths[0], true, nil
}

func (p *fakePluginAuthParser) ParseAuths(_ context.Context, req pluginapi.AuthParseRequest) ([]*coreauth.Auth, bool, error) {
	p.calls++
	if req.Provider != p.provider {
		return nil, false, nil
	}
	return []*coreauth.Auth{{
		ID:         p.id,
		Provider:   p.provider,
		FileName:   req.FileName,
		Attributes: map[string]string{"api_key": "plugin-owned"},
	}}, true, nil
}

var _ synthesizer.PluginMultiAuthParser = (*fakePluginAuthParser)(nil)

type fakeMultiPluginAuthParser struct {
	auths   []*coreauth.Auth
	handled bool
}

func (p *fakeMultiPluginAuthParser) ParseAuth(context.Context, pluginapi.AuthParseRequest) (*coreauth.Auth, bool, error) {
	return nil, false, nil
}

func (p *fakeMultiPluginAuthParser) ParseAuths(context.Context, pluginapi.AuthParseRequest) ([]*coreauth.Auth, bool, error) {
	return p.auths, p.handled, nil
}

type failingPluginAuthParser struct {
	handled bool
	err     error
}

func (p *failingPluginAuthParser) ParseAuth(context.Context, pluginapi.AuthParseRequest) (*coreauth.Auth, bool, error) {
	return nil, p.handled, p.err
}

type failingMultiPluginAuthParser struct{ failingPluginAuthParser }

func (p *failingMultiPluginAuthParser) ParseAuths(context.Context, pluginapi.AuthParseRequest) ([]*coreauth.Auth, bool, error) {
	return nil, p.handled, p.err
}

func newIdentityTestHandler(t *testing.T) (*Handler, string) {
	t.Helper()
	authDir := t.TempDir()
	return NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, coreauth.NewManager(nil, nil, nil)), authDir
}

// A file owned by a plugin must keep the plugin-assigned ID when it is
// (re)saved through the management API. Otherwise the upload registers
// "<file>.json" while the file watcher registers the plugin ID, and one
// credential shows up twice.
func TestBuildAuthFromFileData_KeepsPluginAssignedID(t *testing.T) {
	h, authDir := newIdentityTestHandler(t)
	parser := &fakePluginAuthParser{provider: "opencode-go", id: "opencode-go-key-abc123"}
	path := filepath.Join(authDir, "Primary-2.json")
	data := []byte(`{"type":"opencode-go","id":"opencode-go-key-abc123","label":"Primary","api_key":"k","excluded_models":["m1"]}`)

	auth, err := h.buildAuthFromFileDataWithParser(path, data, parser)
	if err != nil {
		t.Fatalf("buildAuthFromFileDataWithParser: %v", err)
	}
	if auth.ID != "opencode-go-key-abc123" {
		t.Fatalf("auth ID = %q, want plugin-assigned %q", auth.ID, "opencode-go-key-abc123")
	}
	if auth.FileName != "Primary-2.json" {
		t.Fatalf("auth FileName = %q, want %q", auth.FileName, "Primary-2.json")
	}
	if auth.Provider != "opencode-go" {
		t.Fatalf("auth Provider = %q, want opencode-go", auth.Provider)
	}
}

// Re-saving a plugin-owned file must update the already registered auth
// (same ID) instead of registering a second one.
func TestWriteAuthFile_PluginOwnedFileDoesNotDuplicate(t *testing.T) {
	h, authDir := newIdentityTestHandler(t)
	parser := &fakePluginAuthParser{provider: "opencode-go", id: "opencode-go-key-abc123"}
	h.pluginAuthParser = parser
	path := filepath.Join(authDir, "Primary-2.json")
	ctx := context.Background()

	// The watcher registered the file under its path-derived ID before the plugin reload.
	pre := &coreauth.Auth{
		ID:       filepath.Base(path),
		FileName: filepath.Base(path),
		Provider: "opencode-go",
		Attributes: map[string]string{
			coreauth.AttributePath:          path,
			coreauth.AttributeSource:        path,
			coreauth.AttributeSourceBackend: coreauth.AuthSourceFile,
		},
	}
	if _, err := h.authManager.Register(ctx, pre); err != nil {
		t.Fatalf("register initial path-derived auth: %v", err)
	}

	// The plugin reload now claims the same file with its stable plugin ID.
	edited := []byte(`{"type":"opencode-go","api_key":"k","excluded_models":["m1"]}`)
	if err := h.writeAuthFile(ctx, filepath.Base(path), edited); err != nil {
		t.Fatalf("write edited auth through management path: %v", err)
	}

	var forFile []*coreauth.Auth
	for _, a := range h.authManager.List() {
		if a.FileName == "Primary-2.json" {
			forFile = append(forFile, a)
		}
	}
	if len(forFile) != 1 {
		ids := make([]string, 0, len(forFile))
		for _, a := range forFile {
			ids = append(ids, a.ID)
		}
		t.Fatalf("auths for Primary-2.json = %d (%v), want exactly 1", len(forFile), ids)
	}
	if forFile[0].ID != "opencode-go-key-abc123" {
		t.Fatalf("auth ID = %q, want plugin-assigned ID", forFile[0].ID)
	}
}

func TestWriteAuthFileKeepsAllPluginOwnedAuthsAndRemovesOldIDs(t *testing.T) {
	h, authDir := newIdentityTestHandler(t)
	path := filepath.Join(authDir, "multi.json")
	ctx := context.Background()
	old := &coreauth.Auth{
		ID:       "old-path-id",
		FileName: "multi.json",
		Provider: "multi-provider",
		Attributes: map[string]string{
			coreauth.AttributePath:          path,
			coreauth.AttributeSource:        path,
			coreauth.AttributeSourceBackend: coreauth.AuthSourceFile,
		},
	}
	if _, err := h.authManager.Register(ctx, old); err != nil {
		t.Fatalf("register old auth: %v", err)
	}
	h.pluginAuthParser = &fakeMultiPluginAuthParser{
		handled: true,
		auths: []*coreauth.Auth{
			{ID: "plugin-a", Provider: "multi-provider"},
			{ID: "plugin-b", Provider: "multi-provider"},
		},
	}

	if err := h.writeAuthFile(ctx, filepath.Base(path), []byte(`{"type":"multi-provider"}`)); err != nil {
		t.Fatalf("write multi-auth file: %v", err)
	}
	for _, id := range []string{"plugin-a", "plugin-b"} {
		if _, ok := h.authManager.GetByID(id); !ok {
			t.Fatalf("missing plugin auth %q", id)
		}
	}
	if _, ok := h.authManager.GetByID("old-path-id"); ok {
		t.Fatal("old path-derived auth was not removed")
	}
}

func TestWriteAuthFileHonorsHandledEmptyPluginResult(t *testing.T) {
	h, authDir := newIdentityTestHandler(t)
	path := filepath.Join(authDir, "empty.json")
	ctx := context.Background()
	old := &coreauth.Auth{
		ID:       "old-empty-id",
		FileName: "empty.json",
		Provider: "multi-provider",
		Attributes: map[string]string{
			coreauth.AttributePath:          path,
			coreauth.AttributeSource:        path,
			coreauth.AttributeSourceBackend: coreauth.AuthSourceFile,
		},
	}
	if _, err := h.authManager.Register(ctx, old); err != nil {
		t.Fatalf("register old auth: %v", err)
	}
	h.pluginAuthParser = &fakeMultiPluginAuthParser{handled: true}

	if err := h.writeAuthFile(ctx, filepath.Base(path), []byte(`{"type":"multi-provider"}`)); err != nil {
		t.Fatalf("write handled-empty file: %v", err)
	}
	if _, ok := h.authManager.GetByID("old-empty-id"); ok {
		t.Fatal("handled-empty plugin result recreated or retained an old auth")
	}
	for _, auth := range h.authManager.List() {
		if auth.FileName == "empty.json" {
			t.Fatalf("handled-empty plugin result created auth: %#v", auth)
		}
	}
}

func TestWriteAuthFileRejectsPluginParserErrorWithoutMutation(t *testing.T) {
	parserErr := errors.New("plugin auth parser unavailable")
	parsers := []struct {
		name   string
		parser synthesizer.PluginAuthParser
	}{
		{"single_unhandled", &failingPluginAuthParser{err: parserErr}},
		{"single_handled", &failingPluginAuthParser{handled: true, err: parserErr}},
		{"multi_unhandled", &failingMultiPluginAuthParser{failingPluginAuthParser{err: parserErr}}},
		{"multi_handled", &failingMultiPluginAuthParser{failingPluginAuthParser{handled: true, err: parserErr}}},
	}
	for _, tc := range parsers {
		for _, overwrite := range []bool{false, true} {
			mode := "new_file"
			if overwrite {
				mode = "overwrite"
			}
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				h, authDir := newIdentityTestHandler(t)
				ctx := context.Background()
				const name = "plugin.json"
				path := filepath.Join(authDir, name)
				original := []byte(`{"type":"opencode-go","api_key":"original"}`)
				if overwrite {
					h.pluginAuthParser = &fakePluginAuthParser{provider: "opencode-go", id: "plugin-stable-id"}
					if err := h.writeAuthFile(ctx, name, original); err != nil {
						t.Fatalf("seed plugin auth: %v", err)
					}
				}
				before := h.authManager.List()
				h.pluginAuthParser = tc.parser
				hookCalled := false
				h.postAuthPersistHook = func(context.Context, *coreauth.Auth) error {
					hookCalled = true
					return nil
				}

				err := h.writeAuthFile(ctx, name, []byte(`{"type":"opencode-go","api_key":"replacement"}`))
				if !errors.Is(err, errInvalidAuthFile) || !errors.Is(err, parserErr) {
					t.Errorf("write error = %v, want invalid auth file wrapping parser error", err)
				}
				data, errRead := os.ReadFile(path)
				if overwrite {
					if errRead != nil || string(data) != string(original) {
						t.Errorf("auth file changed after parser error: data=%s err=%v", data, errRead)
					}
				} else if !errors.Is(errRead, os.ErrNotExist) {
					t.Errorf("failed upload created auth file: data=%s err=%v", data, errRead)
				}
				if after := h.authManager.List(); !reflect.DeepEqual(before, after) {
					t.Error("parser error changed auth records")
				}
				if hookCalled {
					t.Error("parser error invoked post-auth persist hook")
				}
			})
		}
	}
}

// Files no plugin claims keep the historical path-derived ID.
func TestBuildAuthFromFileData_UnclaimedFileKeepsPathID(t *testing.T) {
	h, authDir := newIdentityTestHandler(t)
	parser := &fakePluginAuthParser{provider: "opencode-go", id: "opencode-go-key-abc123"}
	path := filepath.Join(authDir, "codex-user@example.com-plus.json")
	data := []byte(`{"type":"codex","email":"user@example.com","access_token":"t"}`)

	auth, err := h.buildAuthFromFileDataWithParser(path, data, parser)
	if err != nil {
		t.Fatalf("buildAuthFromFileDataWithParser: %v", err)
	}
	if auth.ID != "codex-user@example.com-plus.json" {
		t.Fatalf("auth ID = %q, want path-derived ID", auth.ID)
	}
	if parser.calls == 0 {
		t.Fatalf("plugin parser was never consulted")
	}
}
