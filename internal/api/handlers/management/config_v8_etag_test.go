package management

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// TestConfigV8ETagIfMatch covers issue #6666: the v8 config endpoints must
// support optimistic concurrency so a stale whole-document write cannot
// resurrect a change another writer already saved (e.g. a revoked API key).
func TestConfigV8ETagIfMatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := "config-version: 8\nserver: {port: 8317}\naccess: {api-keys: [revoked-key]}\n"
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: cfg, configFilePath: path}
	router := gin.New()
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		router.Handle(method, "/v8/management/config", h.ConfigV8)
		router.Handle(method, "/v8/management/config.yaml", h.ConfigV8)
		router.Handle(method, "/v8/management/config/*path", h.ConfigV8)
	}

	do := func(method, url, body, ifMatch string) *httptest.ResponseRecorder {
		t.Helper()
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(method, url, strings.NewReader(body))
		if ifMatch != "" {
			req.Header.Set("If-Match", ifMatch)
		}
		router.ServeHTTP(recorder, req)
		return recorder
	}
	wantStatus := func(recorder *httptest.ResponseRecorder, status int) {
		t.Helper()
		if recorder.Code != status {
			t.Fatalf("status=%d want %d body=%s", recorder.Code, status, recorder.Body.String())
		}
	}
	fileBytes := func() []byte {
		t.Helper()
		data, errRead := os.ReadFile(path)
		if errRead != nil {
			t.Fatal(errRead)
		}
		return data
	}
	fileETag := func() string { return `"` + config.ComputeSourceHash(fileBytes()) + `"` }

	// PATCH at the root with a stale ETag fails fast with 412, so PATCH is
	// fenced by the same precondition instead of merging blindly.
	recorder := do(http.MethodPatch, "/v8/management/config", `{"server":{"port":8319}}`, `"stale"`)
	wantStatus(recorder, http.StatusPreconditionFailed)
	if string(fileBytes()) != raw {
		t.Fatal("stale PATCH changed the file")
	}
	// PATCH with the current ETag goes through and returns the new one.
	recorder = do(http.MethodPatch, "/v8/management/config", `{"server":{"port":8319}}`, fileETag())
	wantStatus(recorder, http.StatusOK)
	if got := recorder.Header().Get("ETag"); got != fileETag() {
		t.Fatalf("PATCH response ETag=%q want %q", got, fileETag())
	}
	loaded, err := config.LoadConfig(path)
	if err != nil || loaded.Port != 8319 {
		t.Fatalf("PATCH with matching If-Match not applied: port=%d err=%v", loaded.Port, err)
	}

	// GET responses advertise the on-disk version as a strong ETag.
	get := do(http.MethodGet, "/v8/management/config.yaml", "", "")
	wantStatus(get, http.StatusOK)
	etag := get.Header().Get("ETag")
	if etag != fileETag() {
		t.Fatalf("GET config.yaml ETag=%q want %q", etag, fileETag())
	}
	subPathGet := do(http.MethodGet, "/v8/management/config/server", "", "")
	wantStatus(subPathGet, http.StatusOK)
	if got := subPathGet.Header().Get("ETag"); got != etag {
		t.Fatalf("GET sub-path ETag=%q want %q", got, etag)
	}

	// Another writer revokes the API key between the read above and our write.
	revoked := do(http.MethodPut, "/v8/management/config/access/api-keys", `["other-key"]`, "")
	wantStatus(revoked, http.StatusOK)
	if got := revoked.Header().Get("ETag"); got != fileETag() {
		t.Fatalf("PUT response ETag=%q want %q", got, fileETag())
	}

	// A whole-document PUT based on the stale read must not resurrect the key.
	before := string(fileBytes())
	stale := do(http.MethodPut, "/v8/management/config.yaml", raw, etag)
	wantStatus(stale, http.StatusPreconditionFailed)
	if got := stale.Header().Get("ETag"); got != fileETag() {
		t.Fatalf("412 response ETag=%q want current %q", got, fileETag())
	}
	if string(fileBytes()) != before {
		t.Fatal("stale PUT changed the file")
	}
	loaded, err = config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.APIKeys) != 1 || loaded.APIKeys[0] != "other-key" {
		t.Fatalf("stale PUT resurrected revoked key: %v", loaded.APIKeys)
	}

	// DELETE with a stale ETag is rejected the same way.
	staleDelete := do(http.MethodDelete, "/v8/management/config/access/api-keys", "", etag)
	wantStatus(staleDelete, http.StatusPreconditionFailed)
	if string(fileBytes()) != before {
		t.Fatal("stale DELETE changed the file")
	}

	// Weak and malformed validators never match under strong comparison. A
	// comma inside quotes is tag content, not a list separator: neither a
	// quoted nor a weakly quoted "stale,*,tag" may be mistaken for the
	// wildcard, and only the whole field can be the wildcard.
	for _, ifMatch := range []string{"W/" + fileETag(), "not-an-etag", `"unterminated`, `"a"b"`, `"stale,*,tag"`, `W/"stale,*,tag"`, `"stale", *`} {
		recorder = do(http.MethodPut, "/v8/management/config.yaml", before, ifMatch)
		wantStatus(recorder, http.StatusPreconditionFailed)
	}

	// Entity tags split across repeated header lines are one list.
	recorder = httptest.NewRecorder()
	splitReq := httptest.NewRequest(http.MethodPut, "/v8/management/config/server/port", strings.NewReader(`8318`))
	splitReq.Header.Add("If-Match", `"stale"`)
	splitReq.Header.Add("If-Match", fileETag())
	router.ServeHTTP(recorder, splitReq)
	wantStatus(recorder, http.StatusOK)

	// A quoted current hash with a trailing comma inside the quotes is a
	// different (stale) tag, not the current one.
	quotedComma := `"` + config.ComputeSourceHash(fileBytes()) + `,"`
	recorder = do(http.MethodPut, "/v8/management/config/server/port", `8317`, quotedComma)
	wantStatus(recorder, http.StatusPreconditionFailed)

	// An explicitly present but empty/blank If-Match carries no tag and fails;
	// only a fully absent header stays unconditional. (The do helper cannot
	// express an empty header, so add it directly.)
	for _, blank := range []string{"", "  \t  "} {
		recorder = httptest.NewRecorder()
		blankReq := httptest.NewRequest(http.MethodPut, "/v8/management/config/server/port", strings.NewReader(`8317`))
		blankReq.Header.Add("If-Match", blank)
		router.ServeHTTP(recorder, blankReq)
		wantStatus(recorder, http.StatusPreconditionFailed)
	}

	// A list containing the current tag (with whitespace and weak members) passes.
	current := fileETag()
	listed := do(http.MethodDelete, "/v8/management/config/access/api-keys", "", `"stale", `+current+` , W/`+current)
	wantStatus(listed, http.StatusOK)
	loaded, err = config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.APIKeys) != 0 {
		t.Fatalf("DELETE with matching If-Match list not applied: %v", loaded.APIKeys)
	}

	// Star matches any existing version.
	star := do(http.MethodPut, "/v8/management/config/server/port", `8320`, "*")
	wantStatus(star, http.StatusOK)

	// A matching ETag chains: reuse the PUT response ETag without re-reading.
	first := do(http.MethodPut, "/v8/management/config/server/port", `8321`, fileETag())
	wantStatus(first, http.StatusOK)
	nextETag := first.Header().Get("ETag")
	if nextETag == "" || nextETag != fileETag() {
		t.Fatalf("PUT response ETag=%q want on-disk hash %q", nextETag, fileETag())
	}
	second := do(http.MethodPut, "/v8/management/config/server/port", `8322`, nextETag)
	wantStatus(second, http.StatusOK)

	// Without If-Match the behaviour is unchanged.
	unconditional := do(http.MethodPut, "/v8/management/config/server/port", `8323`, "")
	wantStatus(unconditional, http.StatusOK)

	// GET 404 responses do not advertise an ETag.
	missing := do(http.MethodGet, "/v8/management/config/no-such-key", "", "")
	wantStatus(missing, http.StatusNotFound)
	if got := missing.Header().Get("ETag"); got != "" {
		t.Fatalf("404 ETag=%q want empty", got)
	}
}
