package management

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type v8UploadTestAuthProvider struct {
	testAuthProvider
	parseFn func(context.Context, pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error)
}

func (p *v8UploadTestAuthProvider) ParseAuth(ctx context.Context, req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
	if p.parseFn != nil {
		return p.parseFn(ctx, req)
	}
	return pluginapi.AuthParseResponse{}, nil
}

func newV8AndV0AuthRouter(h *Handler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	v8 := router.Group("/v8/management", func(c *gin.Context) {
		c.Set(ConfigV8ContextKey, true)
		c.Next()
	})
	v8.POST("/credentials", h.UploadAuthFileV8)
	v8.DELETE("/credentials", h.DeleteAuthFileV8)
	v8.GET("/credentials", h.ListAuthFiles)

	router.POST("/v0/management/auth-files", h.UploadAuthFile)
	router.DELETE("/v0/management/auth-files", h.DeleteAuthFile)
	router.GET("/v0/management/auth-files", h.ListAuthFiles)
	return router
}

func performUploadRequest(t *testing.T, router http.Handler, basePath string, multipartMode bool, files map[string]string, fileOrder []string) *httptest.ResponseRecorder {
	t.Helper()
	if !multipartMode {
		if len(fileOrder) != 1 {
			t.Fatalf("raw upload requires exactly 1 file, got %d", len(fileOrder))
		}
		name := fileOrder[0]
		req := httptest.NewRequest(http.MethodPost, basePath+"?name="+name, strings.NewReader(files[name]))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for index, name := range fileOrder {
		part, errPart := writer.CreateFormFile(fmt.Sprintf("file_%02d", index), name)
		if errPart != nil {
			t.Fatalf("CreateFormFile(%s): %v", name, errPart)
		}
		if _, errWrite := part.Write([]byte(files[name])); errWrite != nil {
			t.Fatalf("write multipart(%s): %v", name, errWrite)
		}
	}
	if errClose := writer.Close(); errClose != nil {
		t.Fatalf("close multipart writer: %v", errClose)
	}
	req := httptest.NewRequest(http.MethodPost, basePath, &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func managerAuthIDs(manager *coreauth.Manager) []string {
	list := manager.List()
	ids := make([]string, 0, len(list))
	for _, item := range list {
		if item != nil {
			ids = append(ids, item.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

func registerDummyModelForClient(t *testing.T, clientID, provider string) {
	t.Helper()
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(clientID, provider, []*registry.ModelInfo{
		{ID: "test-model-" + clientID, Object: "model", OwnedBy: provider},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(clientID)
	})
}

func TestUploadAuthFile_V8VsV0_PluginSingleMultiHandledEmptyAndParserError(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	newPluginHost := func() *pluginhost.Host {
		host := pluginhost.New()
		host.RegisterPluginForTest("vendor-plugin", pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{
				AuthProvider: &v8UploadTestAuthProvider{
					testAuthProvider: testAuthProvider{
						identifier:  "vendor",
						identifiers: []string{"vendor"},
					},
					parseFn: func(_ context.Context, req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
						doc := ojson.ParseBytes(req.RawJSON)
						mode := doc.Get("mode").String()
						switch mode {
						case "error":
							return pluginapi.AuthParseResponse{}, errors.New("synthetic plugin parse error")
						case "empty":
							return pluginapi.AuthParseResponse{Handled: true}, nil
						case "unclaimed":
							return pluginapi.AuthParseResponse{Handled: false}, nil
						case "multi":
							items := doc.Get("projects").Array()
							auths := make([]pluginapi.AuthData, 0, len(items))
							for _, item := range items {
								proj := item.String()
								auths = append(auths, pluginapi.AuthData{
									Provider:    "vendor",
									ID:          "vendor-" + proj,
									FileName:    "virtual-" + proj + ".json",
									Label:       proj,
									StorageJSON: req.RawJSON,
									Metadata:    map[string]any{"type": "vendor", "project_id": proj},
								})
							}
							return pluginapi.AuthParseResponse{Handled: true, Auths: auths}, nil
						default:
							credID := doc.Get("cred_id").String()
							fileName := doc.Get("custom_filename").String()
							return pluginapi.AuthParseResponse{
								Handled: true,
								Auth: pluginapi.AuthData{
									Provider:    "vendor",
									ID:          credID,
									FileName:    fileName,
									StorageJSON: req.RawJSON,
									Metadata:    map[string]any{"type": "vendor", "cred_id": credID},
								},
							}, nil
						}
					},
				},
			},
		})
		return host
	}

	for _, multipartMode := range []bool{false, true} {
		modeName := "raw"
		if multipartMode {
			modeName = "multipart"
		}
		t.Run("v8/"+modeName, func(t *testing.T) {
			authDir := t.TempDir()
			manager := coreauth.NewManager(nil, nil, nil)
			h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
			h.tokenStore = &memoryAuthStore{}
			host := newPluginHost()
			h.SetPluginHost(host)

			var hookedIDs []string
			h.SetPostAuthPersistHook(func(_ context.Context, auth *coreauth.Auth) error {
				if auth != nil {
					hookedIDs = append(hookedIDs, auth.ID)
				}
				return nil
			})
			router := newV8AndV0AuthRouter(h)

			// 1. Single plugin record upload preserves plugin ID & custom FileName, and watcher scan does not duplicate.
			firstPayload := `{"type":"vendor","mode":"single","cred_id":"vendor-cred-1","custom_filename":"display-1.json"}`
			rec := performUploadRequest(t, router, "/v8/management/credentials", multipartMode, map[string]string{"plugin.json": firstPayload}, []string{"plugin.json"})
			if rec.Code != http.StatusOK {
				t.Fatalf("initial v8 upload status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if got := managerAuthIDs(manager); len(got) != 1 || got[0] != "vendor-cred-1" {
				t.Fatalf("after initial v8 upload IDs = %v, want [vendor-cred-1]", got)
			}
			auth1, _ := manager.GetByID("vendor-cred-1")
			if auth1.FileName != "display-1.json" {
				t.Fatalf("FileName = %q, want display-1.json", auth1.FileName)
			}
			if len(hookedIDs) != 1 || hookedIDs[0] != "vendor-cred-1" {
				t.Fatalf("hookedIDs = %v, want [vendor-cred-1]", hookedIDs)
			}
			registerDummyModelForClient(t, "vendor-cred-1", "vendor")

			// Verify watcher synthesis on the written file produces the exact same single ID.
			w, errWatcher := watcher.NewWatcher("", authDir, nil)
			if errWatcher != nil {
				t.Fatalf("NewWatcher: %v", errWatcher)
			}
			t.Cleanup(func() {
				if errStop := w.Stop(); errStop != nil {
					t.Logf("stop watcher: %v", errStop)
				}
			})
			w.SetConfig(&config.Config{AuthDir: authDir})
			w.SetPluginAuthParser(host)
			snap, errSnap := w.SnapshotAuthFile(filepath.Join(authDir, "plugin.json"))
			if errSnap != nil || len(snap) != 1 || snap[0].ID != "vendor-cred-1" {
				t.Fatalf("watcher SnapshotAuthFile = %#v (err=%v), want [vendor-cred-1]", snap, errSnap)
			}

			// 2. Re-uploading the same file with multi-auth expansion reconciles away vendor-cred-1 and registers virtual auths.
			hookedIDs = nil
			multiPayload := `{"type":"vendor","mode":"multi","projects":["alpha","beta"]}`
			rec = performUploadRequest(t, router, "/v8/management/credentials", multipartMode, map[string]string{"plugin.json": multiPayload}, []string{"plugin.json"})
			if rec.Code != http.StatusOK {
				t.Fatalf("multi v8 upload status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if got := managerAuthIDs(manager); len(got) != 2 || got[0] != "vendor-alpha" || got[1] != "vendor-beta" {
				t.Fatalf("after multi v8 upload IDs = %v, want [vendor-alpha vendor-beta]", got)
			}
			if registry.GetGlobalRegistry().GetModelCount("test-model-vendor-cred-1") != 0 {
				t.Fatal("stale model registration for vendor-cred-1 remained after reconciliation")
			}
			for _, id := range []string{"vendor-alpha", "vendor-beta"} {
				item, ok := manager.GetByID(id)
				if !ok || !coreauth.IsPluginVirtualAuth(item) {
					t.Fatalf("expected virtual auth %s, got %#v (ok=%v)", id, item, ok)
				}
			}
			if len(hookedIDs) != 2 || hookedIDs[0] != "vendor-alpha" || hookedIDs[1] != "vendor-beta" {
				t.Fatalf("hookedIDs = %v, want [vendor-alpha vendor-beta]", hookedIDs)
			}

			// 3. Parser error rejects upload BEFORE replacing file on disk, mutating records, or invoking hooks.
			hookedIDs = nil
			errPayload := `{"type":"vendor","mode":"error"}`
			rec = performUploadRequest(t, router, "/v8/management/credentials", multipartMode, map[string]string{"plugin.json": errPayload}, []string{"plugin.json"})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("parser error v8 upload status = %d, want 400; body = %s", rec.Code, rec.Body.String())
			}
			diskBytes, errRead := os.ReadFile(filepath.Join(authDir, "plugin.json"))
			if errRead != nil {
				t.Fatalf("read plugin.json: %v", errRead)
			}
			if string(diskBytes) != multiPayload {
				t.Fatalf("parser error mutated disk: got %s, want %s", string(diskBytes), multiPayload)
			}
			if got := managerAuthIDs(manager); len(got) != 2 || got[0] != "vendor-alpha" || got[1] != "vendor-beta" {
				t.Fatalf("parser error mutated manager IDs = %v, want [vendor-alpha vendor-beta]", got)
			}
			if len(hookedIDs) != 0 {
				t.Fatalf("parser error invoked postAuthPersistHook: %v", hookedIDs)
			}

			// 4. Handled-empty replaces file on disk and reconciles away prior file-owned virtual records without fallback ID.
			hookedIDs = nil
			emptyPayload := `{"type":"vendor","mode":"empty"}`
			rec = performUploadRequest(t, router, "/v8/management/credentials", multipartMode, map[string]string{"plugin.json": emptyPayload}, []string{"plugin.json"})
			if rec.Code != http.StatusOK {
				t.Fatalf("handled-empty v8 upload status = %d, body = %s", rec.Code, rec.Body.String())
			}
			diskBytes, errRead = os.ReadFile(filepath.Join(authDir, "plugin.json"))
			if errRead != nil || string(diskBytes) != emptyPayload {
				t.Fatalf("handled-empty disk = %q (err=%v), want %q", string(diskBytes), errRead, emptyPayload)
			}
			if got := managerAuthIDs(manager); len(got) != 0 {
				t.Fatalf("after handled-empty upload IDs = %v, want []", got)
			}
			if len(hookedIDs) != 0 {
				t.Fatalf("handled-empty invoked postAuthPersistHook: %v", hookedIDs)
			}

			// 5. Ordinary files not claimed by the plugin keep path-derived IDs (both typed and untyped fallback).
			for _, tc := range []struct {
				name    string
				payload string
			}{
				{name: "unclaimed-vendor.json", payload: `{"type":"vendor","mode":"unclaimed","email":"u@example.com"}`},
				{name: "ordinary-untyped.json", payload: `{"email":"plain@example.com"}`},
			} {
				rec = performUploadRequest(t, router, "/v8/management/credentials", multipartMode, map[string]string{tc.name: tc.payload}, []string{tc.name})
				if rec.Code != http.StatusOK {
					t.Fatalf("ordinary upload %s status = %d, body = %s", tc.name, rec.Code, rec.Body.String())
				}
				ordinaryAuth, ok := manager.GetByID(tc.name)
				if !ok || ordinaryAuth == nil || ordinaryAuth.FileName != tc.name {
					t.Fatalf("expected path-derived auth %s, got %#v (ok=%v)", tc.name, ordinaryAuth, ok)
				}
			}
		})

		t.Run("v0/"+modeName, func(t *testing.T) {
			authDir := t.TempDir()
			manager := coreauth.NewManager(nil, nil, nil)
			h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
			h.tokenStore = &memoryAuthStore{}
			h.SetPluginHost(newPluginHost())
			router := newV8AndV0AuthRouter(h)

			// Deprecated v0 retains single-record, path-ID, parser-free semantics across multi, empty, and parser-error payloads.
			for _, payload := range []string{
				`{"type":"vendor","mode":"single","cred_id":"vendor-cred-1","custom_filename":"display-1.json"}`,
				`{"type":"vendor","mode":"multi","projects":["alpha","beta"]}`,
				`{"type":"vendor","mode":"empty"}`,
				`{"type":"vendor","mode":"error"}`,
			} {
				rec := performUploadRequest(t, router, "/v0/management/auth-files", multipartMode, map[string]string{"legacy.json": payload}, []string{"legacy.json"})
				if rec.Code != http.StatusOK {
					t.Fatalf("v0 upload(%s) status = %d, body = %s", payload, rec.Code, rec.Body.String())
				}
				if got := managerAuthIDs(manager); len(got) != 1 || got[0] != "legacy.json" {
					t.Fatalf("v0 upload(%s) IDs = %v, want [legacy.json]", payload, got)
				}
				legacyAuth, _ := manager.GetByID("legacy.json")
				if legacyAuth.FileName != "legacy.json" {
					t.Fatalf("v0 FileName = %q, want legacy.json", legacyAuth.FileName)
				}
			}
		})
	}

	t.Run("v8/multipart-batch-partial-parser-error", func(t *testing.T) {
		authDir := t.TempDir()
		manager := coreauth.NewManager(nil, nil, nil)
		h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
		h.tokenStore = &memoryAuthStore{}
		h.SetPluginHost(newPluginHost())
		router := newV8AndV0AuthRouter(h)

		rec := performUploadRequest(
			t,
			router,
			"/v8/management/credentials",
			true,
			map[string]string{
				"good.json": `{"type":"vendor","mode":"single","cred_id":"vendor-good"}`,
				"bad.json":  `{"type":"vendor","mode":"error"}`,
			},
			[]string{"good.json", "bad.json"},
		)
		if rec.Code != http.StatusMultiStatus {
			t.Fatalf("batch multipart status = %d, want 207; body = %s", rec.Code, rec.Body.String())
		}
		if _, errStat := os.Stat(filepath.Join(authDir, "bad.json")); !os.IsNotExist(errStat) {
			t.Fatalf("bad.json must not be written on parser error, stat err = %v", errStat)
		}
		if got := managerAuthIDs(manager); len(got) != 1 || got[0] != "vendor-good" {
			t.Fatalf("IDs = %v, want [vendor-good]", got)
		}
	})

	t.Run("v8/parser-error-preserves-siwc-binding", func(t *testing.T) {
		f := setupSIWCFlow(t, false)
		f.h.SetPluginHost(newPluginHost())
		f.router.POST("/credentials", f.h.UploadAuthFileV8)
		state, flow := f.start(t)

		w := siwcRequest(t, f.router, http.MethodPost, "/credentials?name=codex.json", map[string]any{
			"type": "vendor",
			"mode": "error",
		})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
		}
		if flow.ctx.Err() != nil || !f.store.IsPending(state, "codex") {
			t.Fatal("plugin parser error invalidated pending SIWC binding")
		}
	})
}

func TestUploadAuthFileV8_FileNameMatchesWatcherSynthesisAndEmptyDefaults(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	for _, tc := range []struct {
		name       string
		pluginID   string
		pluginFile string
		wantID     string
		wantFile   string
	}{
		{
			name:       "explicit-id-and-filename",
			pluginID:   "vendor-explicit",
			pluginFile: "custom-virtual.json",
			wantID:     "vendor-explicit",
			wantFile:   "custom-virtual.json",
		},
		{
			name:       "explicit-id-empty-filename",
			pluginID:   "vendor-empty-name",
			pluginFile: "",
			wantID:     "vendor-empty-name",
			wantFile:   "source.json",
		},
		{
			name:       "empty-id-and-empty-filename",
			pluginID:   "",
			pluginFile: "",
			wantID:     "source.json",
			wantFile:   "source.json",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workDir := t.TempDir()
			t.Chdir(workDir)
			relAuthDir := "rel-auths"
			absAuthDir := filepath.Join(workDir, relAuthDir)
			if errMkdir := os.MkdirAll(absAuthDir, 0o700); errMkdir != nil {
				t.Fatalf("mkdir: %v", errMkdir)
			}

			host := pluginhost.New()
			host.RegisterPluginForTest("vendor-plugin", pluginapi.Plugin{
				Capabilities: pluginapi.Capabilities{
					AuthProvider: &v8UploadTestAuthProvider{
						testAuthProvider: testAuthProvider{
							identifier:  "vendor",
							identifiers: []string{"vendor"},
						},
						parseFn: func(_ context.Context, req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
							return pluginapi.AuthParseResponse{
								Handled: true,
								Auth: pluginapi.AuthData{
									Provider:    "vendor",
									ID:          tc.pluginID,
									FileName:    tc.pluginFile,
									StorageJSON: req.RawJSON,
								},
							}, nil
						},
					},
				},
			})

			manager := coreauth.NewManager(nil, nil, nil)
			h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: relAuthDir}, manager)
			h.tokenStore = &memoryAuthStore{}
			h.SetPluginHost(host)
			router := newV8AndV0AuthRouter(h)

			payload := `{"type":"vendor"}`
			rec := performUploadRequest(t, router, "/v8/management/credentials", false, map[string]string{"source.json": payload}, []string{"source.json"})
			if rec.Code != http.StatusOK {
				t.Fatalf("upload status = %d, body = %s", rec.Code, rec.Body.String())
			}

			uploadedAuth, ok := manager.GetByID(tc.wantID)
			if !ok || uploadedAuth == nil {
				t.Fatalf("uploaded auth %q missing; manager IDs = %v", tc.wantID, managerAuthIDs(manager))
			}
			if uploadedAuth.FileName != tc.wantFile {
				t.Fatalf("uploaded FileName = %q, want %q", uploadedAuth.FileName, tc.wantFile)
			}

			w, errWatcher := watcher.NewWatcher("", relAuthDir, nil)
			if errWatcher != nil {
				t.Fatalf("NewWatcher: %v", errWatcher)
			}
			t.Cleanup(func() {
				if errStop := w.Stop(); errStop != nil {
					t.Logf("stop watcher: %v", errStop)
				}
			})
			w.SetConfig(&config.Config{AuthDir: relAuthDir})
			w.SetPluginAuthParser(host)

			snap, errSnap := w.SnapshotAuthFile(filepath.Join(absAuthDir, "source.json"))
			if errSnap != nil || len(snap) != 1 {
				t.Fatalf("SnapshotAuthFile = %#v, err = %v", snap, errSnap)
			}
			if snap[0].ID != uploadedAuth.ID || snap[0].FileName != uploadedAuth.FileName {
				t.Fatalf("watcher (%q, %q) != management (%q, %q)", snap[0].ID, snap[0].FileName, uploadedAuth.ID, uploadedAuth.FileName)
			}
		})
	}

	t.Run("raw-plugin-parser-empty-id-and-filename-defaults", func(t *testing.T) {
		authDir := t.TempDir()
		fullPath := filepath.Join(authDir, "raw-source.json")
		raw := []byte(`{"type":"vendor"}`)
		parser := oauthProviderFileParser(func(_ context.Context, _ pluginapi.AuthParseRequest) (*coreauth.Auth, bool, error) {
			return &coreauth.Auth{
				Provider: "vendor",
				Metadata: map[string]any{"type": "vendor"},
			}, true, nil
		})
		auths, errSynth := synthesizer.SynthesizeAuthFile(&synthesizer.SynthesisContext{
			AuthDir:           authDir,
			Now:               time.Now(),
			PluginAuthParser:  parser,
			StrictFileParsing: true,
		}, fullPath, raw)
		if errSynth != nil || len(auths) != 1 {
			t.Fatalf("SynthesizeAuthFile = %#v, err = %v", auths, errSynth)
		}
		if auths[0].ID != "raw-source.json" || auths[0].FileName != "raw-source.json" {
			t.Fatalf("got ID=%q FileName=%q, want raw-source.json for both", auths[0].ID, auths[0].FileName)
		}
	})

	t.Run("explicit-source-path-plugin-id-preserved", func(t *testing.T) {
		authDir := t.TempDir()
		host := pluginhost.New()
		host.RegisterPluginForTest("vendor-plugin", pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{
				AuthProvider: &v8UploadTestAuthProvider{
					testAuthProvider: testAuthProvider{
						identifier:  "vendor",
						identifiers: []string{"vendor"},
					},
					parseFn: func(_ context.Context, req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
						return pluginapi.AuthParseResponse{
							Handled: true,
							Auth: pluginapi.AuthData{
								Provider:    "vendor",
								ID:          req.Path,
								FileName:    "explicit-path.json",
								StorageJSON: req.RawJSON,
							},
						}, nil
					},
				},
			},
		})

		manager := coreauth.NewManager(nil, nil, nil)
		cfg := &config.Config{AuthDir: authDir}
		h := NewHandlerWithoutConfigFilePath(cfg, manager)
		h.tokenStore = &memoryAuthStore{}
		h.SetPluginHost(host)
		router := newV8AndV0AuthRouter(h)

		rec := performUploadRequest(t, router, "/v8/management/credentials", false, map[string]string{
			"source.json": `{"type":"vendor"}`,
		}, []string{"source.json"})
		if rec.Code != http.StatusOK {
			t.Fatalf("upload status=%d body=%s", rec.Code, rec.Body.String())
		}

		expectedExplicitID := filepath.Join(authDir, "source.json")
		if abs, errAbs := filepath.Abs(expectedExplicitID); errAbs == nil {
			expectedExplicitID = filepath.Clean(abs)
		}
		uploaded, ok := manager.GetByID(expectedExplicitID)
		if !ok || uploaded == nil {
			t.Fatalf("expected explicit path ID %q to be preserved in manager, got IDs=%v", expectedExplicitID, managerAuthIDs(manager))
		}

		w, errWatcher := watcher.NewWatcher("", authDir, nil)
		if errWatcher != nil {
			t.Fatalf("NewWatcher: %v", errWatcher)
		}
		t.Cleanup(func() {
			if errStop := w.Stop(); errStop != nil {
				t.Logf("stop watcher: %v", errStop)
			}
		})
		w.SetConfig(cfg)
		w.SetPluginAuthParser(host)
		snap, errSnap := w.SnapshotAuthFile(expectedExplicitID)
		if errSnap != nil || len(snap) != 1 {
			t.Fatalf("SnapshotAuthFile: len=%d err=%v", len(snap), errSnap)
		}
		if snap[0].ID != expectedExplicitID {
			t.Fatalf("SnapshotAuthFile ID = %q, want %q", snap[0].ID, expectedExplicitID)
		}
	})
}

func TestUploadAuthFileV8_AtomicReplacementSerializationAndSymlinkDestination(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	host := pluginhost.New()
	host.RegisterPluginForTest("vendor-plugin", pluginapi.Plugin{
		Capabilities: pluginapi.Capabilities{
			AuthProvider: &v8UploadTestAuthProvider{
				testAuthProvider: testAuthProvider{
					identifier:  "vendor",
					identifiers: []string{"vendor"},
				},
				parseFn: func(_ context.Context, req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
					credID := ojson.ParseBytes(req.RawJSON).Get("cred_id").String()
					return pluginapi.AuthParseResponse{
						Handled: true,
						Auth: pluginapi.AuthData{
							Provider:    "vendor",
							ID:          credID,
							StorageJSON: req.RawJSON,
						},
					}, nil
				},
			},
		},
	})

	t.Run("atomic-replace-regular-file-vs-v0-direct-write", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("open-handle inode replacement check uses POSIX unlink/rename semantics")
		}
		authDir := t.TempDir()
		filePath := filepath.Join(authDir, "atomic.json")
		initial := `{"type":"vendor","cred_id":"vendor-initial","padding":"` + strings.Repeat("a", 2048) + `"}`
		if errWrite := os.WriteFile(filePath, []byte(initial), 0o600); errWrite != nil {
			t.Fatalf("write initial: %v", errWrite)
		}
		oldHandle, errOpen := os.Open(filePath)
		if errOpen != nil {
			t.Fatalf("open initial: %v", errOpen)
		}
		defer func() {
			if errClose := oldHandle.Close(); errClose != nil {
				t.Logf("close old handle: %v", errClose)
			}
		}()
		infoBefore, errStatBefore := os.Stat(filePath)
		if errStatBefore != nil {
			t.Fatalf("stat before: %v", errStatBefore)
		}

		manager := coreauth.NewManager(nil, nil, nil)
		h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
		h.tokenStore = &memoryAuthStore{}
		h.SetPluginHost(host)
		router := newV8AndV0AuthRouter(h)

		replacement := `{"type":"vendor","cred_id":"vendor-replaced"}`
		rec := performUploadRequest(t, router, "/v8/management/credentials", false, map[string]string{"atomic.json": replacement}, []string{"atomic.json"})
		if rec.Code != http.StatusOK {
			t.Fatalf("v8 upload status = %d, body = %s", rec.Code, rec.Body.String())
		}
		infoAfter, errStatAfter := os.Stat(filePath)
		if errStatAfter != nil {
			t.Fatalf("stat after: %v", errStatAfter)
		}
		if os.SameFile(infoBefore, infoAfter) {
			t.Fatal("expected v8 upload to replace regular file atomically via rename (new inode), got in-place write")
		}
		oldBytes, errReadOld := io.ReadAll(oldHandle)
		if errReadOld != nil || string(oldBytes) != initial {
			t.Fatalf("open reader on previous inode observed mutated/partial content: %q (err=%v)", string(oldBytes), errReadOld)
		}
	})

	t.Run("symlink-destination-retains-direct-write", func(t *testing.T) {
		authDir := t.TempDir()
		targetDir := t.TempDir()
		targetPath := filepath.Join(targetDir, "real-target.json")
		if errWrite := os.WriteFile(targetPath, []byte(`{"type":"vendor","cred_id":"vendor-before"}`), 0o600); errWrite != nil {
			t.Fatalf("write target: %v", errWrite)
		}
		linkPath := filepath.Join(authDir, "linked.json")
		if errSymlink := os.Symlink(targetPath, linkPath); errSymlink != nil {
			t.Skipf("symlinks unavailable: %v", errSymlink)
		}

		manager := coreauth.NewManager(nil, nil, nil)
		h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
		h.tokenStore = &memoryAuthStore{}
		h.SetPluginHost(host)
		router := newV8AndV0AuthRouter(h)

		updated := `{"type":"vendor","cred_id":"vendor-after"}`
		rec := performUploadRequest(t, router, "/v8/management/credentials", false, map[string]string{"linked.json": updated}, []string{"linked.json"})
		if rec.Code != http.StatusOK {
			t.Fatalf("symlink v8 upload status = %d, body = %s", rec.Code, rec.Body.String())
		}
		lstatInfo, errLstat := os.Lstat(linkPath)
		if errLstat != nil {
			t.Fatalf("lstat symlink: %v", errLstat)
		}
		if lstatInfo.Mode()&os.ModeSymlink == 0 {
			t.Fatal("v8 upload replaced symlink destination with regular file instead of preserving symlink")
		}
		targetData, errReadTarget := os.ReadFile(targetPath)
		if errReadTarget != nil || string(targetData) != updated {
			t.Fatalf("target file = %q (err=%v), want %q", string(targetData), errReadTarget, updated)
		}
	})

	t.Run("concurrent-v8-uploads-serialize-cleanly", func(t *testing.T) {
		authDir := t.TempDir()
		manager := coreauth.NewManager(nil, nil, nil)
		h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
		h.tokenStore = &memoryAuthStore{}
		h.SetPluginHost(host)
		router := newV8AndV0AuthRouter(h)

		const workers = 12
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range workers {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				<-start
				payload := fmt.Sprintf(`{"type":"vendor","cred_id":"vendor-concurrent-%02d"}`, idx)
				rec := performUploadRequest(t, router, "/v8/management/credentials", false, map[string]string{"race.json": payload}, []string{"race.json"})
				if rec.Code != http.StatusOK {
					t.Errorf("worker %d status = %d, body = %s", idx, rec.Code, rec.Body.String())
				}
			}(i)
		}
		close(start)
		wg.Wait()

		diskRaw, errRead := os.ReadFile(filepath.Join(authDir, "race.json"))
		if errRead != nil {
			t.Fatalf("read race.json: %v", errRead)
		}
		diskCredID := ojson.ParseBytes(diskRaw).Get("cred_id").String()
		gotIDs := managerAuthIDs(manager)
		if len(gotIDs) != 1 || gotIDs[0] != diskCredID {
			t.Fatalf("serialized final state mismatch: manager IDs = %v, disk cred_id = %q", gotIDs, diskCredID)
		}
	})
}

func TestUploadAuthFileV8_CodexFileLockHandledEmptyAndSymlinkAlias(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	realDir := t.TempDir()
	aliasParent := t.TempDir()
	aliasDir := filepath.Join(aliasParent, "auths-link")
	if errSymlink := os.Symlink(realDir, aliasDir); errSymlink != nil {
		t.Skipf("symlinks unavailable: %v", errSymlink)
	}

	realCodexPath := filepath.Join(realDir, "codex.json")
	initialRaw := `{"type":"codex","email":"codex@example.com","access_token":"tok-1"}`
	if errWrite := os.WriteFile(realCodexPath, []byte(initialRaw), 0o600); errWrite != nil {
		t.Fatalf("write codex.json: %v", errWrite)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	if _, errReg := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "codex-plugin-assigned-id",
		FileName: "codex.json",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"path":   realCodexPath,
			"source": realCodexPath,
		},
		Metadata: map[string]any{"type": "codex", "email": "codex@example.com"},
	}); errReg != nil {
		t.Fatalf("register codex auth: %v", errReg)
	}

	host := pluginhost.New()
	enteredParse := make(chan struct{})
	var parseOnce sync.Once
	host.RegisterPluginForTest("empty-claiming-plugin", pluginapi.Plugin{
		Capabilities: pluginapi.Capabilities{
			AuthProvider: &v8UploadTestAuthProvider{
				testAuthProvider: testAuthProvider{
					identifier:  "vendor",
					identifiers: []string{"vendor"},
				},
				parseFn: func(_ context.Context, _ pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
					parseOnce.Do(func() { close(enteredParse) })
					return pluginapi.AuthParseResponse{Handled: true}, nil
				},
			},
		},
	})

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: aliasDir}, manager)
	h.tokenStore = &memoryAuthStore{}
	h.SetPluginHost(host)
	router := newV8AndV0AuthRouter(h)

	lockHeld := make(chan struct{})
	releaseLock := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { close(releaseLock) })
	}
	t.Cleanup(release)

	lockDone := make(chan error, 1)
	go func() {
		lockDone <- codex.WithFileLock(realCodexPath, func() error {
			close(lockHeld)
			<-releaseLock
			return nil
		})
	}()
	<-lockHeld

	replacement := `{"type":"vendor","note":"handled-empty-replacement"}`
	uploadDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		uploadDone <- performUploadRequest(t, router, "/v8/management/credentials", false, map[string]string{"codex.json": replacement}, []string{"codex.json"})
	}()

	select {
	case <-enteredParse:
	case recEarly := <-uploadDone:
		t.Fatalf("v8 handled-empty upload completed without invoking plugin parser or acquiring codex.WithFileLock (status=%d body=%s)", recEarly.Code, recEarly.Body.String())
	}
	for range 200 {
		runtime.Gosched()
	}
	select {
	case recEarly := <-uploadDone:
		t.Fatalf("v8 handled-empty upload bypassed codex.WithFileLock through symlink alias (status=%d body=%s)", recEarly.Code, recEarly.Body.String())
	default:
	}
	diskWhileLocked, errReadLocked := os.ReadFile(realCodexPath)
	if errReadLocked != nil {
		t.Fatalf("read while locked: %v", errReadLocked)
	}
	if string(diskWhileLocked) != initialRaw {
		t.Fatalf("disk modified while codex.WithFileLock was held: %s", string(diskWhileLocked))
	}

	release()
	if errLock := <-lockDone; errLock != nil {
		t.Fatalf("codex lock holder error: %v", errLock)
	}
	rec := <-uploadDone
	if rec.Code != http.StatusOK {
		t.Fatalf("v8 upload status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := managerAuthIDs(manager); len(got) != 0 {
		t.Fatalf("expected existing Codex record reconciled away on handled-empty upload, got %v", got)
	}
}

func TestAuthFilesV8_SymlinkAuthDirUploadReconcileAndVersionedDeleteAll(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	host := pluginhost.New()
	host.RegisterPluginForTest("vendor-plugin", pluginapi.Plugin{
		Capabilities: pluginapi.Capabilities{
			AuthProvider: &v8UploadTestAuthProvider{
				testAuthProvider: testAuthProvider{
					identifier:  "vendor",
					identifiers: []string{"vendor"},
				},
				parseFn: func(_ context.Context, req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
					credID := ojson.ParseBytes(req.RawJSON).Get("cred_id").String()
					return pluginapi.AuthParseResponse{
						Handled: true,
						Auth: pluginapi.AuthData{
							Provider:    "vendor",
							ID:          credID,
							StorageJSON: req.RawJSON,
						},
					}, nil
				},
			},
		},
	})

	t.Run("v8-upload-and-delete-all-resolve-symlink-aliases-and-virtual-records", func(t *testing.T) {
		realDir := t.TempDir()
		aliasDir := filepath.Join(t.TempDir(), "auths-symlink")
		if errSymlink := os.Symlink(realDir, aliasDir); errSymlink != nil {
			t.Skipf("symlinks unavailable: %v", errSymlink)
		}

		realSinglePath := filepath.Join(realDir, "single.json")
		realMultiPath := filepath.Join(realDir, "multi.json")
		if errWrite := os.WriteFile(realSinglePath, []byte(`{"type":"vendor","cred_id":"vendor-old-real"}`), 0o600); errWrite != nil {
			t.Fatalf("write single.json: %v", errWrite)
		}
		if errWrite := os.WriteFile(realMultiPath, []byte(`{"type":"vendor"}`), 0o600); errWrite != nil {
			t.Fatalf("write multi.json: %v", errWrite)
		}

		manager := coreauth.NewManager(nil, nil, nil)
		// Seed records whose path/virtual_source point to realDir while Handler uses aliasDir.
		if _, errReg := manager.Register(context.Background(), &coreauth.Auth{
			ID:       "vendor-old-real",
			FileName: "single.json",
			Provider: "vendor",
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"path":   realSinglePath,
				"source": realSinglePath,
			},
		}); errReg != nil {
			t.Fatalf("register single: %v", errReg)
		}
		for idx, id := range []string{"virtual-proj-1", "virtual-proj-2"} {
			vAuth := &coreauth.Auth{
				ID:       id,
				FileName: id + ".json",
				Provider: "vendor",
				Status:   coreauth.StatusActive,
				Attributes: map[string]string{
					"path":   realMultiPath,
					"source": realMultiPath,
				},
			}
			coreauth.MarkPluginVirtualAuth(vAuth, realMultiPath, idx)
			if _, errReg := manager.Register(context.Background(), vAuth); errReg != nil {
				t.Fatalf("register virtual %s: %v", id, errReg)
			}
			registerDummyModelForClient(t, id, "vendor")
		}

		h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: aliasDir}, manager)
		h.tokenStore = &memoryAuthStore{}
		h.SetPluginHost(host)
		router := newV8AndV0AuthRouter(h)

		// Uploading single.json via aliasDir reconciles away vendor-old-real registered under realDir.
		recUpload := performUploadRequest(t, router, "/v8/management/credentials", false, map[string]string{
			"single.json": `{"type":"vendor","cred_id":"vendor-new-alias"}`,
		}, []string{"single.json"})
		if recUpload.Code != http.StatusOK {
			t.Fatalf("v8 symlink upload status = %d, body = %s", recUpload.Code, recUpload.Body.String())
		}
		if got := managerAuthIDs(manager); len(got) != 3 || got[0] != "vendor-new-alias" || got[1] != "virtual-proj-1" || got[2] != "virtual-proj-2" {
			t.Fatalf("after symlink upload IDs = %v, want [vendor-new-alias virtual-proj-1 virtual-proj-2]", got)
		}

		// DELETE /v8/management/credentials?all=true removes both files and reconciles all plugin & virtual records.
		reqDel := httptest.NewRequest(http.MethodDelete, "/v8/management/credentials?all=true", nil)
		recDel := httptest.NewRecorder()
		router.ServeHTTP(recDel, reqDel)
		if recDel.Code != http.StatusOK {
			t.Fatalf("v8 delete-all status = %d, body = %s", recDel.Code, recDel.Body.String())
		}
		if got := managerAuthIDs(manager); len(got) != 0 {
			t.Fatalf("after v8 delete-all IDs = %v, want []", got)
		}
		for _, id := range []string{"virtual-proj-1", "virtual-proj-2"} {
			if registry.GetGlobalRegistry().GetModelCount("test-model-"+id) != 0 {
				t.Fatalf("model registration for %s remained after v8 delete-all", id)
			}
		}
	})

	t.Run("v0-delete-all-retains-legacy-path-id-behavior", func(t *testing.T) {
		authDir := t.TempDir()
		multiPath := filepath.Join(authDir, "multi.json")
		if errWrite := os.WriteFile(multiPath, []byte(`{"type":"vendor"}`), 0o600); errWrite != nil {
			t.Fatalf("write multi.json: %v", errWrite)
		}

		manager := coreauth.NewManager(nil, nil, nil)
		if _, errReg := manager.Register(context.Background(), &coreauth.Auth{
			ID:       "multi.json",
			FileName: "multi.json",
			Provider: "vendor",
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"path": multiPath,
			},
		}); errReg != nil {
			t.Fatalf("register path-id auth: %v", errReg)
		}
		vAuth := &coreauth.Auth{
			ID:       "virtual-only-v8-cleans",
			FileName: "virtual-only.json",
			Provider: "vendor",
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"path": multiPath,
			},
		}
		coreauth.MarkPluginVirtualAuth(vAuth, multiPath, 0)
		if _, errReg := manager.Register(context.Background(), vAuth); errReg != nil {
			t.Fatalf("register virtual auth: %v", errReg)
		}

		h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
		h.tokenStore = &memoryAuthStore{}
		router := newV8AndV0AuthRouter(h)

		reqDel := httptest.NewRequest(http.MethodDelete, "/v0/management/auth-files?all=true", nil)
		recDel := httptest.NewRecorder()
		router.ServeHTTP(recDel, reqDel)
		if recDel.Code != http.StatusOK {
			t.Fatalf("v0 delete-all status = %d, body = %s", recDel.Code, recDel.Body.String())
		}
		if got := managerAuthIDs(manager); len(got) != 1 || got[0] != "virtual-only-v8-cleans" {
			t.Fatalf("v0 delete-all changed legacy behavior: IDs = %v, want [virtual-only-v8-cleans]", got)
		}
	})
}

func TestUploadAuthFileV8_WithRealFileTokenStorePreservesSymlinkAndUploadedBytes(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	t.Run("regular_file_not_rewritten_in_place_by_filestore", func(t *testing.T) {
		authDir := t.TempDir()
		store := sdkAuth.NewFileTokenStore()
		store.SetBaseDir(authDir)
		cfg := &config.Config{AuthDir: authDir}
		manager := coreauth.NewManager(store, nil, nil)
		manager.SetConfig(cfg)
		h := NewHandlerWithoutConfigFilePath(cfg, manager)
		h.tokenStore = store
		router := newV8AndV0AuthRouter(h)

		filePath := filepath.Join(authDir, "regular.json")
		initialBytes := []byte(`{"type":"qwen","email":"old@example.com","access_token":"old"}`)
		if errWrite := os.WriteFile(filePath, initialBytes, 0o600); errWrite != nil {
			t.Fatalf("seed regular.json: %v", errWrite)
		}
		uploadedBytes := []byte(`{"type":"qwen","email":"new@example.com","access_token":"new"}`)
		rec := performUploadRequest(t, router, "/v8/management/credentials", false, map[string]string{
			"regular.json": string(uploadedBytes),
		}, []string{"regular.json"})
		if rec.Code != http.StatusOK {
			t.Fatalf("upload status=%d body=%s", rec.Code, rec.Body.String())
		}
		diskBytes, errRead := os.ReadFile(filePath)
		if errRead != nil {
			t.Fatalf("read regular.json: %v", errRead)
		}
		if !bytes.Equal(diskBytes, uploadedBytes) {
			t.Fatalf("regular.json was rewritten after upload: got %s, want exact %s", string(diskBytes), string(uploadedBytes))
		}
	})

	t.Run("plugin_symlink_destination_preserved_with_real_filestore", func(t *testing.T) {
		authDir := t.TempDir()
		targetDir := t.TempDir()
		targetPath := filepath.Join(targetDir, "real-linked.json")
		if errWrite := os.WriteFile(targetPath, []byte(`{"type":"vendor","cred_id":"vendor:old"}`), 0o600); errWrite != nil {
			t.Fatalf("seed symlink target: %v", errWrite)
		}
		symPath := filepath.Join(authDir, "linked.json")
		if errSym := os.Symlink(targetPath, symPath); errSym != nil {
			t.Skipf("symlinks unavailable: %v", errSym)
		}

		store := sdkAuth.NewFileTokenStore()
		store.SetBaseDir(authDir)
		cfg := &config.Config{AuthDir: authDir}
		manager := coreauth.NewManager(store, nil, nil)
		manager.SetConfig(cfg)

		host := pluginhost.New()
		host.RegisterPluginForTest("vendor-plugin", pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{
				AuthProvider: &v8UploadTestAuthProvider{
					testAuthProvider: testAuthProvider{
						identifier:  "vendor",
						identifiers: []string{"vendor"},
					},
					parseFn: func(_ context.Context, req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
						credID := ojson.ParseBytes(req.RawJSON).Get("cred_id").String()
						return pluginapi.AuthParseResponse{
							Handled: true,
							Auth: pluginapi.AuthData{
								Provider:    "vendor",
								ID:          credID,
								FileName:    req.FileName,
								StorageJSON: bytes.Clone(req.RawJSON),
							},
						}, nil
					},
				},
			},
		})

		h := NewHandlerWithoutConfigFilePath(cfg, manager)
		h.tokenStore = store
		h.SetPluginHost(host)
		router := newV8AndV0AuthRouter(h)

		uploadedBytes := []byte(`{"type":"vendor","cred_id":"vendor:new-sym"}`)
		rec := performUploadRequest(t, router, "/v8/management/credentials", false, map[string]string{
			"linked.json": string(uploadedBytes),
		}, []string{"linked.json"})
		if rec.Code != http.StatusOK {
			t.Fatalf("upload status=%d body=%s", rec.Code, rec.Body.String())
		}
		info, errLstat := os.Lstat(symPath)
		if errLstat != nil {
			t.Fatalf("lstat symlink: %v", errLstat)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("expected %s to remain a symlink even with FileTokenStore, got mode %v", symPath, info.Mode())
		}
		targetBytes, errRead := os.ReadFile(targetPath)
		if errRead != nil {
			t.Fatalf("read symlink target: %v", errRead)
		}
		if !bytes.Equal(targetBytes, uploadedBytes) {
			t.Fatalf("symlink target bytes = %s, want %s", string(targetBytes), string(uploadedBytes))
		}
	})

	t.Run("multi_credential_plugin_file_and_non_object_json_with_real_filestore", func(t *testing.T) {
		authDir := t.TempDir()
		store := sdkAuth.NewFileTokenStore()
		store.SetBaseDir(authDir)
		cfg := &config.Config{AuthDir: authDir}
		manager := coreauth.NewManager(store, nil, nil)
		manager.SetConfig(cfg)

		host := pluginhost.New()
		host.RegisterPluginForTest("vendor-plugin", pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{
				AuthProvider: &v8UploadTestAuthProvider{
					testAuthProvider: testAuthProvider{
						identifier:  "vendor",
						identifiers: []string{"vendor"},
					},
					parseFn: func(_ context.Context, req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
						items := ojson.ParseBytes(req.RawJSON).Get("accounts").Array()
						auths := make([]pluginapi.AuthData, 0, len(items))
						for _, item := range items {
							id := item.Get("id").String()
							auths = append(auths, pluginapi.AuthData{
								Provider:    "vendor",
								ID:          "vendor:" + id,
								FileName:    req.FileName,
								StorageJSON: []byte(item.Raw),
							})
						}
						return pluginapi.AuthParseResponse{Handled: true, Auths: auths}, nil
					},
				},
			},
		})

		h := NewHandlerWithoutConfigFilePath(cfg, manager)
		h.tokenStore = store
		h.SetPluginHost(host)
		router := newV8AndV0AuthRouter(h)

		containerBytes := []byte(`{"type":"vendor","accounts":[{"id":"one"},{"id":"two"}]}`)
		rec := performUploadRequest(t, router, "/v8/management/credentials", false, map[string]string{
			"multi.json": string(containerBytes),
		}, []string{"multi.json"})
		if rec.Code != http.StatusOK {
			t.Fatalf("upload status=%d body=%s", rec.Code, rec.Body.String())
		}
		diskBytes, errRead := os.ReadFile(filepath.Join(authDir, "multi.json"))
		if errRead != nil {
			t.Fatalf("read multi.json: %v", errRead)
		}
		if !bytes.Equal(diskBytes, containerBytes) {
			t.Fatalf("multi.json bytes = %s, want %s", string(diskBytes), string(containerBytes))
		}

		for _, badPayload := range []string{`[1,2]`, `"str"`, `null`, `{}{}`} {
			recBad := performUploadRequest(t, router, "/v8/management/credentials", false, map[string]string{
				"multi.json": badPayload,
			}, []string{"multi.json"})
			if recBad.Code != http.StatusBadRequest {
				t.Fatalf("bad payload %s status=%d, want 400", badPayload, recBad.Code)
			}
		}
		afterBadBytes, errReadAfter := os.ReadFile(filepath.Join(authDir, "multi.json"))
		if errReadAfter != nil {
			t.Fatalf("read multi.json after bad upload: %v", errReadAfter)
		}
		if !bytes.Equal(afterBadBytes, containerBytes) {
			t.Fatalf("multi.json modified after rejected payload: got %s", string(afterBadBytes))
		}
	})
}

func TestUploadAuthFileV8_AndSynthesizerPreserveFullPluginHostConfigSummary(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	var captured []pluginapi.HostConfigSummary
	var captureMu sync.Mutex

	registerVendorPlugin := func(host *pluginhost.Host) {
		host.RegisterPluginForTest("vendor-plugin", pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{
				AuthProvider: &v8UploadTestAuthProvider{
					testAuthProvider: testAuthProvider{
						identifier:  "vendor",
						identifiers: []string{"vendor"},
					},
					parseFn: func(_ context.Context, req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
						captureMu.Lock()
						captured = append(captured, req.Host)
						captureMu.Unlock()
						return pluginapi.AuthParseResponse{
							Handled: true,
							Auth: pluginapi.AuthData{
								Provider: "vendor",
								ID:       "vendor:cfg-check",
								FileName: req.FileName,
							},
						}, nil
					},
				},
			},
		})
	}

	host := pluginhost.New()
	runtimeCfg := &config.Config{
		AuthDir: authDir,
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"vendor": {{Name: "vendor-pro", Alias: "vp"}},
		},
		OAuthExcludedModels: map[string][]string{
			"vendor": {"vendor-hidden"},
		},
	}
	runtimeCfg.ProxyURL = "http://127.0.0.1:8899"
	runtimeCfg.ForceModelPrefix = true
	host.ApplyConfig(context.Background(), runtimeCfg)
	registerVendorPlugin(host)

	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(runtimeCfg)
	h := NewHandlerWithoutConfigFilePath(runtimeCfg, manager)
	h.tokenStore = &memoryAuthStore{}
	h.SetPluginHost(host)
	router := newV8AndV0AuthRouter(h)

	rawPayload := `{"type":"vendor"}`
	rec := performUploadRequest(t, router, "/v8/management/credentials", false, map[string]string{
		"cfg.json": rawPayload,
	}, []string{"cfg.json"})
	if rec.Code != http.StatusOK {
		t.Fatalf("upload status=%d body=%s", rec.Code, rec.Body.String())
	}

	_, errSynth := synthesizer.SynthesizeAuthFile(&synthesizer.SynthesisContext{
		Config:           &config.Config{AuthDir: authDir},
		AuthDir:          authDir,
		Now:              time.Now(),
		IDGenerator:      synthesizer.NewStableIDGenerator(),
		PluginAuthParser: host,
	}, filepath.Join(authDir, "cfg.json"), []byte(rawPayload))
	if errSynth != nil {
		t.Fatalf("SynthesizeAuthFile: %v", errSynth)
	}

	captureMu.Lock()
	if len(captured) != 2 {
		captureMu.Unlock()
		t.Fatalf("captured %d ParseAuth calls, want 2", len(captured))
	}
	initialSummaries := append([]pluginapi.HostConfigSummary(nil), captured...)
	captured = nil
	captureMu.Unlock()

	for idx, summary := range initialSummaries {
		if summary.AuthDir != authDir {
			t.Fatalf("call[%d] Host.AuthDir = %q, want %q", idx, summary.AuthDir, authDir)
		}
		if summary.ProxyURL != "http://127.0.0.1:8899" {
			t.Fatalf("call[%d] Host.ProxyURL = %q, want http://127.0.0.1:8899", idx, summary.ProxyURL)
		}
		if !summary.ForceModelPrefix {
			t.Fatalf("call[%d] Host.ForceModelPrefix = false, want true", idx)
		}
		if len(summary.OAuthModelAlias["vendor"]) != 1 || summary.OAuthModelAlias["vendor"][0].Alias != "vp" {
			t.Fatalf("call[%d] Host.OAuthModelAlias = %#v, want vendor alias vp", idx, summary.OAuthModelAlias)
		}
		if len(summary.ExcludedModels["vendor"]) != 1 || summary.ExcludedModels["vendor"][0] != "vendor-hidden" {
			t.Fatalf("call[%d] Host.ExcludedModels = %#v, want vendor-hidden", idx, summary.ExcludedModels)
		}
	}

	// Applying a cleared host config must propagate zero values without resurrecting old settings.
	clearedCfg := &config.Config{AuthDir: authDir}
	host.ApplyConfig(context.Background(), clearedCfg)
	registerVendorPlugin(host)
	h.SetConfig(clearedCfg)

	recCleared := performUploadRequest(t, router, "/v8/management/credentials", false, map[string]string{
		"cfg.json": rawPayload,
	}, []string{"cfg.json"})
	if recCleared.Code != http.StatusOK {
		t.Fatalf("cleared upload status=%d body=%s", recCleared.Code, recCleared.Body.String())
	}

	captureMu.Lock()
	defer captureMu.Unlock()
	if len(captured) != 1 {
		t.Fatalf("captured %d cleared ParseAuth calls, want 1", len(captured))
	}
	if captured[0].ProxyURL != "" || captured[0].ForceModelPrefix || len(captured[0].OAuthModelAlias) != 0 || len(captured[0].ExcludedModels) != 0 {
		t.Fatalf("cleared Host summary resurrected old fields: %#v", captured[0])
	}
}

func TestUploadAuthFileV8_ConcurrentWithManagementConfigEditsIsRaceFree(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	host := pluginhost.New()
	host.RegisterPluginForTest("vendor-plugin", pluginapi.Plugin{
		Capabilities: pluginapi.Capabilities{
			AuthProvider: &v8UploadTestAuthProvider{
				testAuthProvider: testAuthProvider{
					identifier:  "vendor",
					identifiers: []string{"vendor"},
				},
				parseFn: func(_ context.Context, req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
					return pluginapi.AuthParseResponse{
						Handled: true,
						Auth: pluginapi.AuthData{
							Provider: "vendor",
							ID:       "vendor:race-check",
							FileName: req.FileName,
							Attributes: map[string]string{
								"seen_proxy": req.Host.ProxyURL,
							},
						},
					}, nil
				},
			},
		},
	})

	initialCfg := &config.Config{
		AuthDir: authDir,
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"vendor": {{Name: "vendor-pro", Alias: "vp"}},
		},
		OAuthExcludedModels: map[string][]string{
			"vendor": {"vendor-hidden"},
		},
	}
	initialCfg.ProxyURL = "http://127.0.0.1:8080"
	initialCfg.ForceModelPrefix = true

	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(initialCfg)
	h := NewHandlerWithoutConfigFilePath(initialCfg, manager)
	h.tokenStore = &memoryAuthStore{}
	h.SetPluginHost(host)
	router := newV8AndV0AuthRouter(h)

	stopWriters := make(chan struct{})
	var writerWG sync.WaitGroup
	writerWG.Add(1)
	go func() {
		defer writerWG.Done()
		toggle := false
		for {
			select {
			case <-stopWriters:
				return
			default:
			}
			toggle = !toggle
			h.mu.Lock()
			if toggle {
				h.cfg.ProxyURL = "http://127.0.0.1:8081"
				h.cfg.ForceModelPrefix = false
				if h.cfg.OAuthModelAlias == nil {
					h.cfg.OAuthModelAlias = make(map[string][]config.OAuthModelAlias)
				}
				h.cfg.OAuthModelAlias["vendor"] = []config.OAuthModelAlias{{Name: "vendor-pro", Alias: "vp2"}}
				if h.cfg.OAuthExcludedModels == nil {
					h.cfg.OAuthExcludedModels = make(map[string][]string)
				}
				h.cfg.OAuthExcludedModels["vendor"] = []string{"vendor-hidden-2"}
			} else {
				h.cfg.ProxyURL = ""
				h.cfg.ForceModelPrefix = true
				delete(h.cfg.OAuthModelAlias, "vendor")
				delete(h.cfg.OAuthExcludedModels, "vendor")
			}
			h.mu.Unlock()
		}
	}()

	for idx := 0; idx < 40; idx++ {
		rec := performUploadRequest(t, router, "/v8/management/credentials", false, map[string]string{
			"race.json": `{"type":"vendor"}`,
		}, []string{"race.json"})
		if rec.Code != http.StatusOK {
			close(stopWriters)
			writerWG.Wait()
			t.Fatalf("iteration %d upload status=%d body=%s", idx, rec.Code, rec.Body.String())
		}
	}
	close(stopWriters)
	writerWG.Wait()
}
