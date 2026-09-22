package api

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestManagementUIAssetCacheGzipDecodedEqualsInjectedIdentity(t *testing.T) {
	staticDir := t.TempDir()
	t.Setenv("MANAGEMENT_STATIC_PATH", staticDir)
	writeUIAsset(t, filepath.Join(staticDir, "management.html"), `<html><body><main>management app</main></body></html>`)
	server := newTestServer(t)

	identity := performUIAssetRequest(t, server, http.MethodGet, "/management.html", nil)
	if identity.Code != http.StatusOK {
		t.Fatalf("identity status = %d, want %d body=%s", identity.Code, http.StatusOK, identity.Body.String())
	}
	if got := identity.Header().Get("Cache-Control"); got != "private, no-cache" {
		t.Fatalf("Cache-Control = %q, want private, no-cache", got)
	}
	if got := identity.Header().Get("Vary"); got != "Accept-Encoding" {
		t.Fatalf("Vary = %q, want Accept-Encoding", got)
	}
	identityBody := identity.Body.Bytes()
	if !bytes.Contains(identityBody, []byte("management app")) || !bytes.Contains(identityBody, []byte("cpa-billing-nav-link")) {
		t.Fatalf("identity body missing app or billing nav injection: %s", identityBody)
	}
	if got := identity.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("identity Content-Encoding = %q, want empty", got)
	}

	gzipped := performUIAssetRequest(t, server, http.MethodGet, "/management.html", map[string]string{"Accept-Encoding": "gzip"})
	if gzipped.Code != http.StatusOK {
		t.Fatalf("gzip status = %d, want %d body=%s", gzipped.Code, http.StatusOK, gzipped.Body.String())
	}
	if got := gzipped.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("gzip Content-Encoding = %q, want gzip", got)
	}
	decoded := gunzipTestBody(t, gzipped.Body.Bytes())
	if !bytes.Equal(decoded, identityBody) {
		t.Fatalf("decoded gzip body differs from identity\nidentity=%s\ndecoded=%s", identityBody, decoded)
	}
	if identity.Header().Get("ETag") == "" || gzipped.Header().Get("ETag") == "" {
		t.Fatalf("expected ETag headers identity=%q gzip=%q", identity.Header().Get("ETag"), gzipped.Header().Get("ETag"))
	}
	if identity.Header().Get("ETag") == gzipped.Header().Get("ETag") {
		t.Fatalf("identity and gzip ETags should be representation-specific, both were %q", identity.Header().Get("ETag"))
	}
}

func TestManagementUIAssetCacheAcceptEncodingQHandling(t *testing.T) {
	staticDir := t.TempDir()
	t.Setenv("MANAGEMENT_STATIC_PATH", staticDir)
	writeUIAsset(t, filepath.Join(staticDir, "billing-token-panel.js"), `console.log("billing");`)
	server := newTestServer(t)

	for _, tc := range []struct {
		name       string
		header     string
		wantGzip   bool
		wantBody   string
		decodeBody bool
	}{
		{name: "gzip q zero disables gzip", header: "gzip;q=0", wantBody: `console.log("billing");`},
		{name: "explicit gzip q zero beats wildcard", header: "br, gzip;q=0, *;q=1", wantBody: `console.log("billing");`},
		{name: "wildcard permits gzip", header: "br;q=1, *;q=0.5", wantGzip: true, wantBody: `console.log("billing");`, decodeBody: true},
		{name: "gzip positive q permits gzip", header: "gzip;q=0.2", wantGzip: true, wantBody: `console.log("billing");`, decodeBody: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := performUIAssetRequest(t, server, http.MethodGet, "/billing-token-panel.js", map[string]string{"Accept-Encoding": tc.header})
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d body=%s", rr.Code, http.StatusOK, rr.Body.String())
			}
			if got := rr.Header().Get("Content-Encoding"); (got == "gzip") != tc.wantGzip {
				t.Fatalf("Content-Encoding = %q, want gzip=%v", got, tc.wantGzip)
			}
			body := rr.Body.Bytes()
			if tc.decodeBody {
				body = gunzipTestBody(t, body)
			}
			if string(body) != tc.wantBody {
				t.Fatalf("body = %q, want %q", body, tc.wantBody)
			}
		})
	}
}

func TestManagementUIAssetCacheETagConditionalGET(t *testing.T) {
	staticDir := t.TempDir()
	t.Setenv("MANAGEMENT_STATIC_PATH", staticDir)
	writeUIAsset(t, filepath.Join(staticDir, "billing-token-page.html"), `<html>billing</html>`)
	server := newTestServer(t)

	identity := performUIAssetRequest(t, server, http.MethodGet, "/billing.html", nil)
	if identity.Code != http.StatusOK {
		t.Fatalf("identity status = %d", identity.Code)
	}
	identityETag := identity.Header().Get("ETag")
	if identityETag == "" {
		t.Fatal("missing identity ETag")
	}
	strongIdentityETag := strings.TrimPrefix(identityETag, "W/")
	conditional := performUIAssetRequest(t, server, http.MethodGet, "/billing.html", map[string]string{"If-None-Match": `"miss", ` + strongIdentityETag})
	if conditional.Code != http.StatusNotModified {
		t.Fatalf("weak/list conditional status = %d, want %d body=%q", conditional.Code, http.StatusNotModified, conditional.Body.String())
	}
	if conditional.Body.Len() != 0 {
		t.Fatalf("304 body length = %d, want 0", conditional.Body.Len())
	}
	star := performUIAssetRequest(t, server, http.MethodGet, "/billing.html", map[string]string{"If-None-Match": "*"})
	if star.Code != http.StatusNotModified {
		t.Fatalf("star conditional status = %d, want %d", star.Code, http.StatusNotModified)
	}

	gzipped := performUIAssetRequest(t, server, http.MethodGet, "/billing.html", map[string]string{"Accept-Encoding": "gzip"})
	if gzipped.Code != http.StatusOK {
		t.Fatalf("gzip status = %d", gzipped.Code)
	}
	gzipWithIdentityETag := performUIAssetRequest(t, server, http.MethodGet, "/billing.html", map[string]string{"Accept-Encoding": "gzip", "If-None-Match": identityETag})
	if gzipWithIdentityETag.Code != http.StatusOK {
		t.Fatalf("gzip request with identity ETag status = %d, want %d for representation-specific ETag", gzipWithIdentityETag.Code, http.StatusOK)
	}
	gzipETag := gzipped.Header().Get("ETag")
	gzipConditional := performUIAssetRequest(t, server, http.MethodGet, "/billing.html", map[string]string{"Accept-Encoding": "gzip", "If-None-Match": gzipETag})
	if gzipConditional.Code != http.StatusNotModified {
		t.Fatalf("gzip conditional status = %d, want %d", gzipConditional.Code, http.StatusNotModified)
	}
}

func TestManagementUIAssetCacheInvalidatesSameSizeAtomicReplacement(t *testing.T) {
	staticDir := t.TempDir()
	t.Setenv("MANAGEMENT_STATIC_PATH", staticDir)
	assetPath := filepath.Join(staticDir, "billing-token-panel.js")
	writeUIAsset(t, assetPath, `console.log(1);`)
	server := newTestServer(t)

	first := performUIAssetRequest(t, server, http.MethodGet, "/billing-token-panel.js", nil)
	if first.Code != http.StatusOK || first.Body.String() != `console.log(1);` {
		t.Fatalf("first response status=%d body=%q", first.Code, first.Body.String())
	}
	info, err := os.Stat(assetPath)
	if err != nil {
		t.Fatalf("stat old asset: %v", err)
	}
	oldMod := info.ModTime()
	replacement := filepath.Join(staticDir, "billing-token-panel.js.tmp")
	writeUIAsset(t, replacement, `console.log(2);`)
	if len(`console.log(1);`) != len(`console.log(2);`) {
		t.Fatal("test replacement must stay same size")
	}
	if err := os.Chtimes(replacement, oldMod, oldMod); err != nil {
		t.Fatalf("chtimes replacement: %v", err)
	}
	if err := os.Rename(replacement, assetPath); err != nil {
		t.Fatalf("rename replacement: %v", err)
	}
	if err := os.Chtimes(assetPath, oldMod, oldMod); err != nil {
		t.Fatalf("chtimes renamed asset: %v", err)
	}

	second := performUIAssetRequest(t, server, http.MethodGet, "/billing-token-panel.js", nil)
	if second.Code != http.StatusOK {
		t.Fatalf("second status = %d body=%s", second.Code, second.Body.String())
	}
	if second.Body.String() != `console.log(2);` {
		t.Fatalf("second body = %q, want replacement content", second.Body.String())
	}
	if first.Header().Get("ETag") == second.Header().Get("ETag") {
		t.Fatalf("ETag did not change across same-size atomic replacement: %q", first.Header().Get("ETag"))
	}
}

func TestManagementUIAssetCacheMissingFileDoesNotServeStale(t *testing.T) {
	staticDir := t.TempDir()
	t.Setenv("MANAGEMENT_STATIC_PATH", staticDir)
	assetPath := filepath.Join(staticDir, "billing-token-page.html")
	writeUIAsset(t, assetPath, `<html>billing</html>`)
	server := newTestServer(t)

	first := performUIAssetRequest(t, server, http.MethodGet, "/billing.html", nil)
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d", first.Code)
	}
	if err := os.Remove(assetPath); err != nil {
		t.Fatalf("remove asset: %v", err)
	}
	missing := performUIAssetRequest(t, server, http.MethodGet, "/billing.html", nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d, want %d body=%q", missing.Code, http.StatusNotFound, missing.Body.String())
	}
}

func TestManagementUIAssetCacheConcurrentRequests(t *testing.T) {
	staticDir := t.TempDir()
	t.Setenv("MANAGEMENT_STATIC_PATH", staticDir)
	writeUIAsset(t, filepath.Join(staticDir, "management.html"), `<html><body>concurrent app</body></html>`)
	server := newTestServer(t)

	// Also exercise lazy cache initialization for servers constructed without NewServer.
	server.uiAssetCache = nil
	const workers = 32
	var wg sync.WaitGroup
	errs := make(chan string, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			headers := map[string]string{"Accept-Encoding": "gzip"}
			if i%2 == 0 {
				headers = nil
			}
			rr := performUIAssetRequest(t, server, http.MethodGet, "/management.html", headers)
			if rr.Code != http.StatusOK {
				errs <- "unexpected status"
				return
			}
			body := rr.Body.Bytes()
			if rr.Header().Get("Content-Encoding") == "gzip" {
				body = gunzipTestBody(t, body)
			}
			if !bytes.Contains(body, []byte("concurrent app")) || !bytes.Contains(body, []byte("cpa-billing-nav-link")) {
				errs <- "missing transformed content"
				return
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func TestManagementUIAssetCacheDisabledControlPanelPreserved(t *testing.T) {
	staticDir := t.TempDir()
	t.Setenv("MANAGEMENT_STATIC_PATH", staticDir)
	writeUIAsset(t, filepath.Join(staticDir, "management.html"), `<html><body>disabled</body></html>`)
	server := newTestServer(t)
	server.cfg.RemoteManagement.DisableControlPanel = true

	rr := performUIAssetRequest(t, server, http.MethodGet, "/management.html", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d body=%q", rr.Code, http.StatusNotFound, rr.Body.String())
	}
}

func performUIAssetRequest(t *testing.T, server *Server, method string, target string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)
	return rr
}

func writeUIAsset(t *testing.T, path string, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir asset dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write asset %s: %v", path, err)
	}
	// Give filesystems with coarse timestamp behavior a stable timestamp for tests that set it explicitly later.
	now := time.Now().Add(-time.Second)
	_ = os.Chtimes(path, now, now)
}

func gunzipTestBody(t *testing.T, body []byte) []byte {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("create gzip reader: %v", err)
	}
	defer reader.Close()
	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read gzip body: %v", err)
	}
	return decoded
}

func TestManagementUIAssetCacheWarmHitsAndBoundedEntries(t *testing.T) {
	cache := newUIAssetCache(3)
	dir := t.TempDir()
	transforms := 0
	transform := func(b []byte) []byte { transforms++; return b }
	path := filepath.Join(dir, "warm.html")
	writeUIAsset(t, path, "warm content")
	first, err := cache.get(path, "text/html", transform)
	if err != nil {
		t.Fatal(err)
	}
	second, err := cache.get(path, "text/html", transform)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || transforms != 1 {
		t.Fatalf("warm cache rebuilt: transforms=%d", transforms)
	}
	for _, name := range []string{"a.html", "b.html", "c.html", "d.html"} {
		p := filepath.Join(dir, name)
		writeUIAsset(t, p, name)
		if _, err := cache.get(p, "text/html", nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(cache.entries) != 3 || len(cache.order) != 3 {
		t.Fatalf("unbounded cache: %d entries", len(cache.entries))
	}
}
