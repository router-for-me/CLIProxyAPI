package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	proxyconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestManagementWebServesLiveFilesRelativeToConfig(t *testing.T) {
	server := newTestServer(t)
	webRoot := filepath.Join(filepath.Dir(server.configFilePath), "web", "management")
	if errMkdir := os.MkdirAll(webRoot, 0o700); errMkdir != nil {
		t.Fatalf("create management web root: %v", errMkdir)
	}
	writeManagementWebTestFile(t, filepath.Join(webRoot, "index.html"), "<!doctype html><title>disk dashboard</title>")
	writeManagementWebTestFile(t, filepath.Join(webRoot, "app.css"), ":root{--version:1}")

	tests := []struct {
		path        string
		contentType string
		body        string
	}{
		{path: "/management.html", contentType: "text/html; charset=utf-8", body: "disk dashboard"},
		{path: "/management/usage", contentType: "text/html; charset=utf-8", body: "disk dashboard"},
		{path: "/management/app.css", contentType: "text/css; charset=utf-8", body: "--version:1"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			rr := serveManagementWebTestRequest(server, http.MethodGet, tt.path)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d body=%s", rr.Code, http.StatusOK, rr.Body.String())
			}
			if got := rr.Header().Get("Content-Type"); got != tt.contentType {
				t.Fatalf("Content-Type = %q, want %q", got, tt.contentType)
			}
			if !strings.Contains(rr.Body.String(), tt.body) {
				t.Fatalf("body does not contain %q: %s", tt.body, rr.Body.String())
			}
			assertManagementWebSecurityHeaders(t, rr)
		})
	}

	// Assets are read for each request, so front-end edits do not require a rebuild.
	writeManagementWebTestFile(t, filepath.Join(webRoot, "app.css"), ":root{--version:2}")
	rrUpdated := serveManagementWebTestRequest(server, http.MethodGet, "/management/app.css")
	if rrUpdated.Code != http.StatusOK || !strings.Contains(rrUpdated.Body.String(), "--version:2") {
		t.Fatalf("updated asset was not served live: status=%d body=%s", rrUpdated.Code, rrUpdated.Body.String())
	}
}

func TestManagementWebDirectoryOverrideAndRedirect(t *testing.T) {
	server := newTestServer(t)
	webRoot := t.TempDir()
	server.cfg.RemoteManagement.WebDirectory = webRoot
	writeManagementWebTestFile(t, filepath.Join(webRoot, "index.html"), "custom management directory")

	rrRedirect := serveManagementWebTestRequest(server, http.MethodGet, "/management?section=keys")
	if rrRedirect.Code != http.StatusPermanentRedirect {
		t.Fatalf("redirect status = %d, want %d", rrRedirect.Code, http.StatusPermanentRedirect)
	}
	if location := rrRedirect.Header().Get("Location"); location != "/management.html?section=keys" {
		t.Fatalf("redirect Location = %q", location)
	}

	rrPage := serveManagementWebTestRequest(server, http.MethodGet, "/management.html")
	if rrPage.Code != http.StatusOK || !strings.Contains(rrPage.Body.String(), "custom management directory") {
		t.Fatalf("override page status=%d body=%s", rrPage.Code, rrPage.Body.String())
	}

	rrHead := serveManagementWebTestRequest(server, http.MethodHead, "/management.html")
	if rrHead.Code != http.StatusOK || rrHead.Body.Len() != 0 {
		t.Fatalf("HEAD status=%d body=%q", rrHead.Code, rrHead.Body.String())
	}
	if rrHead.Header().Get("Content-Length") == "" {
		t.Fatal("HEAD response is missing Content-Length")
	}
}

func TestManagementWebRootResolvesFromConfigLocation(t *testing.T) {
	launchDirectory := t.TempDir()
	server := &Server{
		cfg:            &proxyconfig.Config{},
		configFilePath: filepath.Join("configs", "config.yaml"),
		currentPath:    launchDirectory,
	}

	gotDefault, errDefault := server.managementWebRoot()
	if errDefault != nil {
		t.Fatalf("resolve default root: %v", errDefault)
	}
	wantDefault := filepath.Join(launchDirectory, "configs", "web", "management")
	if gotDefault != wantDefault {
		t.Fatalf("default root = %q, want %q", gotDefault, wantDefault)
	}

	server.cfg.RemoteManagement.WebDirectory = filepath.Join("..", "shared", "admin-ui")
	gotRelative, errRelative := server.managementWebRoot()
	if errRelative != nil {
		t.Fatalf("resolve relative root: %v", errRelative)
	}
	wantRelative := filepath.Join(launchDirectory, "shared", "admin-ui")
	if gotRelative != wantRelative {
		t.Fatalf("relative root = %q, want %q", gotRelative, wantRelative)
	}

	if runtime.GOOS == "windows" {
		for _, ambiguousPath := range []string{`C:admin-ui`, `\admin-ui`} {
			server.cfg.RemoteManagement.WebDirectory = ambiguousPath
			if _, errAmbiguous := server.managementWebRoot(); errAmbiguous == nil {
				t.Fatalf("ambiguous Windows path %q was accepted", ambiguousPath)
			}
		}
	}
}

func TestManagementWebRejectsTraversalAndNonPublicFiles(t *testing.T) {
	server := newTestServer(t)
	parent := t.TempDir()
	webRoot := filepath.Join(parent, "public")
	if errMkdir := os.MkdirAll(webRoot, 0o700); errMkdir != nil {
		t.Fatalf("create web root: %v", errMkdir)
	}
	server.cfg.RemoteManagement.WebDirectory = webRoot
	writeManagementWebTestFile(t, filepath.Join(webRoot, "index.html"), "safe")
	secretPath := filepath.Join(parent, "secret.css")
	writeManagementWebTestFile(t, secretPath, "must-not-leak")
	writeManagementWebTestFile(t, filepath.Join(webRoot, "private.env"), "must-not-serve")

	for _, requested := range []string{"../secret.css", `..\secret.css`, "C:/secret.css", "\x00.css"} {
		if _, ok := normalizeManagementWebAssetPath(requested); ok {
			t.Fatalf("normalize accepted unsafe path %q", requested)
		}
	}
	if _, _, errRead := readManagementWebAsset(webRoot, "../secret.css"); !errors.Is(errRead, errManagementWebAssetNotFound) {
		t.Fatalf("direct traversal error = %v, want not found", errRead)
	}

	for _, requestPath := range []string{"/management/private.env", "/management/missing.js"} {
		rr := serveManagementWebTestRequest(server, http.MethodGet, requestPath)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want %d body=%s", requestPath, rr.Code, http.StatusNotFound, rr.Body.String())
		}
	}

	linkPath := filepath.Join(webRoot, "outside.css")
	if errLink := os.Symlink(secretPath, linkPath); errLink == nil {
		rr := serveManagementWebTestRequest(server, http.MethodGet, "/management/outside.css")
		if rr.Code != http.StatusNotFound {
			t.Fatalf("symlink escape status = %d, want %d body=%s", rr.Code, http.StatusNotFound, rr.Body.String())
		}
	}
}

func TestManagementWebHonorsControlPanelDisable(t *testing.T) {
	server := newTestServer(t)
	server.cfg.RemoteManagement.WebDirectory = t.TempDir()
	server.cfg.RemoteManagement.DisableControlPanel = true
	for _, requestPath := range []string{"/management", "/management.html", "/management/", "/management/app.js", "/management/legacy"} {
		rr := serveManagementWebTestRequest(server, http.MethodGet, requestPath)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want %d", requestPath, rr.Code, http.StatusNotFound)
		}
	}
}

func writeManagementWebTestFile(t *testing.T, target, content string) {
	t.Helper()
	if errWrite := os.WriteFile(target, []byte(content), 0o600); errWrite != nil {
		t.Fatalf("write %s: %v", target, errWrite)
	}
}

func serveManagementWebTestRequest(server *Server, method, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)
	return rr
}

func assertManagementWebSecurityHeaders(t *testing.T, rr *httptest.ResponseRecorder) {
	t.Helper()
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
	csp := rr.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
		t.Fatalf("unexpected Content-Security-Policy: %q", csp)
	}
}
