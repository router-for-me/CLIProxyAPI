package api

import (
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

const (
	testServerIDHeader = "X-CPA-SERVER-ID"
	testServerID       = "20fb88e4-73ab-44db-b5b5-4e7120ba3368"
)

func TestManagementServerIDAuthenticationAndV0Compatibility(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	s := newTestServerWithOptions(t, WithLocalManagementPassword("local-test"))
	cfg := s.cfg.CloneForRuntime()
	cfg.ServerID = testServerID
	cfg.RemoteManagement.SecretKey = "configured-secret"
	s.mgmt.SetConfig(cfg)

	for _, tc := range []struct {
		name, method, path, key, address string
		status                           int
		wantID                           string
	}{
		{"authenticated", "GET", "/v8/management/server/server-id", "local-test", "127.0.0.1:9000", 200, testServerID},
		{"wrong key", "GET", "/v8/management/server/server-id", "wrong", "127.0.0.1:9000", 401, ""},
		{"missing key", "GET", "/v8/management/server/server-id", "", "127.0.0.1:9000", 401, ""},
		{"remote forbidden", "GET", "/v8/management/server/server-id", "local-test", "192.0.2.2:9000", 403, ""},
		{"preflight", "OPTIONS", "/v8/management/server/server-id", "local-test", "127.0.0.1:9000", 204, ""},
		{"v0 JSON", "GET", "/v0/management/config", "local-test", "127.0.0.1:9000", 200, testServerID},
		{"v0 wrong key", "GET", "/v0/management/config", "wrong", "127.0.0.1:9000", 401, ""},
		{"unauthenticated callback", "GET", "/v8/management/oauth/callback", "", "127.0.0.1:9000", 400, ""},
		{"public route", "GET", "/no-management-route", "local-test", "127.0.0.1:9000", 404, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.RemoteAddr = tc.address
			req.Header.Set("Origin", "http://localhost:5173")
			req.Header.Set("X-Management-Key", tc.key)
			resp := httptest.NewRecorder()
			s.engine.ServeHTTP(resp, req)
			if resp.Code != tc.status || resp.Header().Get(testServerIDHeader) != tc.wantID {
				t.Fatalf("status=%d, ID=%q, body=%s", resp.Code, resp.Header().Get(testServerIDHeader), resp.Body)
			}
			if tc.wantID == "" && strings.Contains(resp.Body.String(), testServerID) {
				t.Fatalf("server ID leaked in body: %s", resp.Body)
			}
			if tc.name == "authenticated" {
				if strings.TrimSpace(resp.Body.String()) != `{"server-id":"`+testServerID+`"}` {
					t.Fatalf("identity body = %s", resp.Body)
				}
				if !strings.Contains(resp.Header().Get("Access-Control-Expose-Headers"), testServerIDHeader) {
					t.Fatalf("missing exposed server ID header: %v", resp.Header())
				}
			}
			if tc.name == "v0 JSON" {
				var got, want map[string]any
				original, errMarshal := json.Marshal(cfg)
				if errMarshal != nil || json.Unmarshal(original, &want) != nil || json.Unmarshal(resp.Body.Bytes(), &got) != nil {
					t.Fatalf("invalid legacy JSON: %v %s", errMarshal, resp.Body)
				}
				if len(got) == 0 || !reflect.DeepEqual(got, want) || strings.Contains(resp.Body.String(), "server-id") {
					t.Fatalf("legacy JSON changed: %s", resp.Body)
				}
			}
		})
	}
}

func TestManagementServerIDConfigWrites(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")
	const trailing = "--- # supplied by user\r\nsecond: 'not reformatted'\r\n"
	for _, tc := range []struct {
		name, method, path, body, wantID, suffix string
		status                                   int
	}{
		{"normalize explicit", "PUT", "/config/server/server-id", `" ` + strings.ToUpper(testServerID) + ` "`, testServerID, trailing, 200},
		{"keep during unrelated edit", "PATCH", "/config", `{"server":{"port":8318}}`, testServerID, trailing, 200},
		{"replace invalid scalar", "PUT", "/config/server/server-id", `"not-a-uuid"`, "", trailing, 200},
		{"replace invalid type", "PATCH", "/config/server", `{"server-id":{"invalid":true}}`, "", trailing, 200},
		{"replace nil UUID", "PATCH", "/config", `{"server":{"server-id":"00000000-0000-0000-0000-000000000000"}}`, "", trailing, 200},
		{"replace blank", "PUT", "/config/server/server-id", `"   "`, "", trailing, 200},
		{"replace null", "PATCH", "/config/server/server-id", `null`, "", trailing, 200},
		{"delete identity", "DELETE", "/config/server/server-id", "", "", trailing, 200},
		{"delete whole server", "DELETE", "/config/server", "", "", trailing, 200},
		{"reset whole server", "PATCH", "/config", `{ "server": null }`, "", trailing, 200},
		{"replace whole server", "PUT", "/config/server", `{"port":8318}`, "", trailing, 200},
		{"replace whole config", "PUT", "/config", `{"server":{"port":8318}}`, "", trailing, 200},
		{"replace YAML stream", "PUT", "/config.yaml", "server: {port: 8318, server-id: ''}\n---\nuntouched: {a: b}\n", "", "---\nuntouched: {a: b}\n", 200},
		{"normalize effective merge", "PUT", "/config.yaml", "plugins:\n  configs:\n    defaults: &defaults {port: 8318, server-id: ' " + strings.ToUpper(testServerID) + " '}\nserver: {<<: *defaults}\n", testServerID, "", 200},
		{"repair aliased value", "PUT", "/config.yaml", "plugins:\n  configs:\n    defaults: {value: &identity invalid}\nserver: {port: 8318, server-id: *identity}\n", "", "", 200},
		{"invalid trailing document", "PUT", "/config.yaml", "server: {port: 8318}\n---\nx: [\n", "", "", 400},
		{"cross-document alias", "PUT", "/config.yaml", "server: &server {port: 8318}\n---\nother: *server\n", "", "", 400},
		{"duplicate identity", "PUT", "/config.yaml", "server: {server-id: one, server-id: two}\n", "", "", 400},
		{"unrelated invalid setting", "PUT", "/config", `{"server":{"server-id":null,"port":"bad"}}`, "", "", 422},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(t)
			// Legacy layout until the first successful v8 write.
			const prefix = "port: 8317\nserver: {server-id: " + testServerID + "}\n"
			if errCreate := config.CreateConfigFile(s.configFilePath, []byte(prefix+trailing)); errCreate != nil {
				t.Fatal(errCreate)
			}
			cfg, errLoad := config.LoadConfig(s.configFilePath)
			if errLoad != nil {
				t.Fatal(errLoad)
			}
			s.mgmt.SetConfig(cfg)
			request := func(method, path, body string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(method, "/v8/management"+path, strings.NewReader(body))
				req.Header.Set("Authorization", "Bearer test-management-key")
				resp := httptest.NewRecorder()
				s.engine.ServeHTTP(resp, req)
				return resp
			}
			resp := request(tc.method, tc.path, tc.body)
			if resp.Code != tc.status {
				t.Fatalf("status=%d, want %d, body=%s", resp.Code, tc.status, resp.Body)
			}
			stored, errRead := config.ReadConfigFile(s.configFilePath)
			if errRead != nil {
				t.Fatal(errRead)
			}
			id := resp.Header().Get(testServerIDHeader)
			if resp.Code != 200 {
				if string(stored) != prefix+trailing || id != testServerID {
					t.Fatalf("rejected write changed file or ID: %q; header=%q", stored, id)
				}
				return
			}
			if tc.wantID != "" {
				if id != tc.wantID {
					t.Fatalf("ID=%q, want %q", id, tc.wantID)
				}
			} else if parsed, errParse := uuid.Parse(id); errParse != nil || parsed.Version() != 4 || id == testServerID || id != parsed.String() {
				t.Fatalf("expected newly generated canonical v4 UUID, got %q", id)
			}
			var payload map[string]any
			if errDecode := json.Unmarshal(resp.Body.Bytes(), &payload); errDecode != nil || payload["server-id"] != id || payload["status"] != "ok" {
				t.Fatalf("write body=%s, header=%q", resp.Body, id)
			}
			if etag := resp.Header().Get("ETag"); etag != `"`+config.ComputeSourceHash(stored)+`"` {
				t.Fatalf("ETag=%q for saved bytes %q", etag, stored)
			}
			disk, errParse := config.ParseConfigBytes(stored)
			if errParse != nil || disk.ServerID != id || !strings.HasSuffix(string(stored), tc.suffix) {
				t.Fatalf("saved bytes=%q, parse error=%v, header=%q", stored, errParse, id)
			}
			if tc.name == "repair aliased value" && !strings.Contains(string(stored), "value: invalid") {
				t.Fatalf("repair also changed anchor source: %s", stored)
			}
			got := request("GET", "/config/server/server-id", "")
			if got.Code != 200 || got.Header().Get(testServerIDHeader) != id || strings.TrimSpace(got.Body.String()) != `"`+id+`"` {
				t.Fatalf("config read status=%d, header=%q, body=%s", got.Code, got.Header().Get(testServerIDHeader), got.Body)
			}
		})
	}
}
