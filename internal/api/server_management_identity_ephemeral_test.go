package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestManagementServerIDEphemeralReadOnlyConfig(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")
	s := newTestServer(t)
	const raw = "port: 8317\n---\nextra: unchanged\n"
	if errCreate := config.CreateConfigFile(s.configFilePath, []byte(raw)); errCreate != nil {
		t.Fatal(errCreate)
	}
	if errChmod := os.Chmod(s.configFilePath, 0o400); errChmod != nil {
		t.Fatal(errChmod)
	}
	t.Cleanup(func() {
		if errChmod := os.Chmod(s.configFilePath, 0o600); errChmod != nil {
			t.Error(errChmod)
		}
	})
	initial, errLoad := config.LoadConfig(s.configFilePath)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	if initial.SourceHash != config.ComputeSourceHash([]byte(raw)) {
		t.Skip("the process can write to the read-only file")
	}
	if initial.ServerID == "" {
		t.Fatal("read-only startup did not allocate an ID")
	}
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "/v8/management"+path, strings.NewReader(body))
		req.Header.Set("X-Management-Key", "test-management-key")
		resp := httptest.NewRecorder()
		s.engine.ServeHTTP(resp, req)
		if got := resp.Header().Get(testServerIDHeader); got != initial.ServerID {
			t.Fatalf("%s %s: server ID changed to %q: %s", method, path, got, resp.Body)
		}
		return resp
	}
	for i := 0; i < 2; i++ {
		loaded, errReload := config.LoadConfig(s.configFilePath)
		if errReload != nil || loaded.ServerID != initial.ServerID {
			t.Fatalf("read-only reload failed to reuse its ID: %v", errReload)
		}
		s.mgmt.SetConfig(loaded)
		identity := request(http.MethodGet, "/server/server-id", "")
		if identity.Code != http.StatusOK || identity.Body.String() != `{"server-id":"`+initial.ServerID+`"}` {
			t.Fatalf("server identity response: %d %s", identity.Code, identity.Body)
		}
		view := request(http.MethodGet, "/config.yaml", "")
		projected, errParse := config.ParseConfigBytes(view.Body.Bytes())
		if view.Code != http.StatusOK || errParse != nil || projected.ServerID != initial.ServerID || view.Header().Get("ETag") != `"`+initial.SourceHash+`"` {
			t.Fatalf("read-only config view: %d %s; %v", view.Code, view.Body, errParse)
		}
	}
	failed := request(http.MethodPatch, "/config", `{"server":{"server-id":null}}`)
	if failed.Code != http.StatusInternalServerError {
		t.Fatalf("write to read-only file: %d %s", failed.Code, failed.Body)
	}
	data, errRead := config.ReadConfigFile(s.configFilePath)
	if errRead != nil || string(data) != raw {
		t.Fatalf("read-only config changed: %v %s", errRead, data)
	}
}
