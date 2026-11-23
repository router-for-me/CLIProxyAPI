package management

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MoeForge/moego/base64"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	textunicode "golang.org/x/text/encoding/unicode"
)

func TestConfigV8MutationDoesNotOrphanPlaintextInArchive(t *testing.T) {
	for _, tc := range []struct {
		name, setting, copy string
	}{
		{"scalar", "secret-key: local-archived-management-key\n", "obsolete: local-archived-management-key\n"},
		{"literal", "secret-key: |-\n    local-archived\n    management-key\n", "obsolete: |-\n  local-archived\n  management-key\n"},
		{"folded", "secret-key: \"local-archived\\nmanagement-key\"\n", "obsolete: >-\n  local-archived\n\n  management-key\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertConfigV8ArchiveProtection(t, tc.setting, tc.copy)
		})
	}
}

func assertConfigV8ArchiveProtection(t *testing.T, setting, copy string) {
	t.Helper()
	raw := []byte("config-version: 8\nserver: {server-id: " + previousServerID + "}\n" +
		"management: {secret-key: ''}\nremote-management:\n  " + setting + copy)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if errCreate := config.CreateConfigFile(path, raw); errCreate != nil {
		t.Fatal(errCreate)
	}
	if prepared, errPrepare := config.PrepareConfigFileForUploadWithOptional(path, false); errPrepare == nil || prepared != nil {
		t.Fatal("initial plaintext copy did not block upload")
	}
	h := NewHandler(&config.Config{}, path, nil)
	router := gin.New()
	router.PATCH("/v8/management/config", h.ConfigV8)
	res := httptest.NewRecorder()
	router.ServeHTTP(res, httptest.NewRequest(http.MethodPatch, "/v8/management/config", strings.NewReader(`{"server":{"port":9443}}`)))
	if res.Code != http.StatusInternalServerError || !strings.Contains(res.Body.String(), "without its identifying setting") || strings.Contains(res.Body.String(), "local-archived") {
		t.Fatalf("unrelated PATCH did not refuse orphaning during archive: %d %s", res.Code, res.Body)
	}
	updated, err := config.ReadConfigFile(path)
	if err != nil || !bytes.Equal(updated, raw) {
		t.Fatalf("refused PATCH modified its source snapshot: %v", err)
	}
}

func TestConfigV8BinaryArchiveRequiresExplicitRemoval(t *testing.T) {
	const secret = "local-archived-management-key"
	const header = "config-version: 8\nserver: {server-id: " + previousServerID + "}\n"
	const suffix = "--- # retained document\r\nvalue: unchanged\r\n"
	encoded := base64.StdEncoding.EncodeToString([]byte("Bearer " + secret))
	for _, tc := range []struct{ name, body string }{
		{"shadowed key and root field", "management: {secret-key: ''}\nremote-management: {secret-key: " + secret + "}\nobsolete: !!binary " + encoded + "\n"},
		{"active key and nested field", "management: {secret-key: " + secret + "}\nserver-extra: {backup: !!binary " + encoded + "}\n"},
		{"active key and block value", "management:\n  secret-key: " + secret + "\n  obsolete: !!binary |-\n    " + encoded[:20] + "\n    " + encoded[20:] + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(header + tc.body + suffix)
			path := filepath.Join(t.TempDir(), "config.yaml")
			if errCreate := config.CreateConfigFile(path, raw); errCreate != nil {
				t.Fatal(errCreate)
			}
			h := NewHandler(&config.Config{}, path, nil)
			router := gin.New()
			router.PATCH("/v8/management/config", h.ConfigV8)
			router.PUT("/v8/management/config.yaml", h.ConfigV8)
			res := httptest.NewRecorder()
			router.ServeHTTP(res, httptest.NewRequest(http.MethodPatch, "/v8/management/config", strings.NewReader(`{"server":{"port":9443}}`)))
			if res.Code != http.StatusInternalServerError || !strings.Contains(res.Body.String(), "without its identifying setting") || strings.Contains(res.Body.String(), secret) {
				t.Errorf("PATCH must not archive a decoded plaintext copy: %d %s", res.Code, res.Body)
			}
			if saved, errRead := config.ReadConfigFile(path); errRead != nil || !bytes.Equal(saved, raw) {
				t.Errorf("refused PATCH modified the source snapshot: %v", errRead)
			}
			if prepared, errPrepare := config.PrepareConfigFileForUploadWithOptional(path, false); errPrepare == nil || prepared != nil {
				t.Error("upload accepted the copy after the attempted save")
			}

			const cleared = header + "management: {secret-key: ''}\n"
			for _, moved := range []struct{ name, body string }{
				{"reformatted block", cleared + "# backup: !!binary |-\n#   " + encoded[:24] + "\n#   " + encoded[24:] + "\n" + suffix},
				{"inline comment", header + "management:\n  secret-key: '' # " + encoded + "\n" + suffix},
				{"inline comment in following document", cleared + "---\r\nvalue: unchanged # " + encoded + "\r\n"},
				{"comment on end marker", cleared + suffix + "... # " + encoded + "\r\n"},
			} {
				res := httptest.NewRecorder()
				router.ServeHTTP(res, httptest.NewRequest(http.MethodPut, "/v8/management/config.yaml", strings.NewReader(moved.body)))
				if res.Code != http.StatusInternalServerError || !strings.Contains(res.Body.String(), "without its identifying setting") {
					t.Fatalf("%s: explicit replacement kept the encoded copy: %d %s", moved.name, res.Code, res.Body)
				}
				if saved, errRead := config.ReadConfigFile(path); errRead != nil || !bytes.Equal(saved, raw) {
					t.Fatalf("%s: refused replacement modified the source snapshot: %v", moved.name, errRead)
				}
			}

			res = httptest.NewRecorder()
			router.ServeHTTP(res, httptest.NewRequest(http.MethodPut, "/v8/management/config.yaml", strings.NewReader(cleared+suffix)))
			if res.Code != http.StatusOK {
				t.Fatalf("explicit replacement removing both the key and the copy: %d %s", res.Code, res.Body)
			}
			prepared, errPrepare := config.PrepareConfigFileForUploadWithOptional(path, false)
			if errPrepare != nil || bytes.Contains(prepared, []byte(encoded)) || bytes.Contains(prepared, []byte(secret)) || !bytes.HasSuffix(prepared, []byte(suffix)) {
				t.Fatalf("upload after explicit removal failed, retained the copy, or lost the suffix: %v", errPrepare)
			}
		})
	}
}

func TestConfigV0YAMLPutPreservesBlockScalarAndSuffix(t *testing.T) {
	const body = "port: 9443\nserver: {server-id: " + previousServerID + "}\n" +
		"plugins:\n  configs:\n    fixture:\n      text: |-\n        # literal text, not a YAML comment\n        still in the block\n" +
		"--- # retained document\r\nvalue: |-\r\n  # also literal text\r\n  preserve spacing\r\n"
	path := filepath.Join(t.TempDir(), "config.yaml")
	if errCreate := config.CreateConfigFile(path, []byte("port: 8317\n")); errCreate != nil {
		t.Fatal(errCreate)
	}
	h := NewHandler(&config.Config{}, path, nil)
	router := gin.New()
	router.PUT("/v0/management/config.yaml", h.PutConfigYAML)
	res := httptest.NewRecorder()
	router.ServeHTTP(res, httptest.NewRequest(http.MethodPut, "/v0/management/config.yaml", strings.NewReader(body)))
	if res.Code != http.StatusOK {
		t.Fatalf("YAML PUT: %d %s", res.Code, res.Body)
	}
	saved, err := config.ReadConfigFile(path)
	if err != nil || string(saved) != body {
		t.Fatalf("legacy YAML PUT changed block or suffix bytes: %v\n%s", err, saved)
	}
}

func TestConfigV8YAMLPutRejectsDuplicateKeysInsideShadowedMappingsWithoutWriting(t *testing.T) {
	const original = "port: 8317\n"
	const body = "config-version: 8\nserver: {port: 9443}\n---\n<<: {nested: {x: one, x: two}}\nnested: ok\n"
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.CreateConfigFile(path, []byte(original)); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(&config.Config{Port: 8317}, path, nil)
	router := gin.New()
	router.PUT("/v8/management/config.yaml", h.ConfigV8)
	res := httptest.NewRecorder()
	router.ServeHTTP(res, httptest.NewRequest(http.MethodPut, "/v8/management/config.yaml", strings.NewReader(body)))
	if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "document 2: mapping key") {
		t.Fatalf("YAML PUT did not reject the hidden duplicate: %d %s", res.Code, res.Body)
	}
	if saved, err := config.ReadConfigFile(path); err != nil || string(saved) != original {
		t.Fatalf("rejected YAML PUT changed the original file: %v", err)
	}
}

func TestConfigV8YAMLPutAcceptsRedefinedScalarAliasKeys(t *testing.T) {
	const body = "config-version: 8\nplugins:\n  configs:\n    fixture:\n" +
		"      first: &k alpha\n      *k: first binding\n      second: &k beta\n      *k: second binding\n" +
		"--- # retain\nextra: unchanged\n"
	path := filepath.Join(t.TempDir(), "config.yaml")
	if errCreate := config.CreateConfigFile(path, []byte("port: 8317\n")); errCreate != nil {
		t.Fatal(errCreate)
	}
	h := NewHandler(&config.Config{}, path, nil)
	router := gin.New()
	router.PUT("/v8/management/config.yaml", h.ConfigV8)
	res := httptest.NewRecorder()
	router.ServeHTTP(res, httptest.NewRequest(http.MethodPut, "/v8/management/config.yaml", strings.NewReader(body)))
	if res.Code != http.StatusOK {
		t.Fatalf("valid scalar anchor redefinitions were rejected: %d %s", res.Code, res.Body)
	}
	saved, err := config.ReadConfigFile(path)
	if err != nil || !bytes.Contains(saved, []byte("alpha: first binding")) || !bytes.Contains(saved, []byte("beta: second binding")) ||
		!bytes.HasSuffix(saved, []byte("--- # retain\nextra: unchanged\n")) {
		t.Fatalf("alias bindings or trailing bytes were lost: %v\n%s", err, saved)
	}
}

func TestConfigV8YAMLGetReportsStreamCharset(t *testing.T) {
	for _, tc := range []struct {
		name  string
		order textunicode.Endianness
	}{
		{"little-endian", textunicode.LittleEndian},
		{"big-endian", textunicode.BigEndian},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const suffix = "--- # keep\r\nmessage: '世界 🌎'\r\n"
			codec := textunicode.UTF16(tc.order, textunicode.UseBOM)
			raw, err := codec.NewEncoder().String("port: 8317\n" + suffix)
			if err != nil {
				t.Fatal(err)
			}
			encodedSuffix, errSuffix := codec.NewEncoder().String(suffix)
			if errSuffix != nil {
				t.Fatal(errSuffix)
			}
			path := filepath.Join(t.TempDir(), "config.yaml")
			if errCreate := config.CreateConfigFile(path, []byte(raw)); errCreate != nil {
				t.Fatal(errCreate)
			}
			h := NewHandler(&config.Config{ServerID: previousServerID}, path, nil)
			router := gin.New()
			router.GET("/v8/management/config.yaml", h.ConfigV8)
			res := httptest.NewRecorder()
			router.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/v8/management/config.yaml", nil))
			if res.Code != http.StatusOK || res.Header().Get("Content-Type") != "application/yaml; charset=utf-16" {
				t.Fatalf("YAML GET: %d %s", res.Code, res.Header().Get("Content-Type"))
			}
			if !bytes.HasPrefix(res.Body.Bytes(), []byte(raw[:2])) || !bytes.HasSuffix(res.Body.Bytes(), []byte(encodedSuffix[2:])) {
				t.Fatal("YAML GET did not preserve the BOM and tail bytes")
			}
			parsed, errParse := config.ParseConfigBytes(res.Body.Bytes())
			if errParse != nil || parsed.Port != 8317 {
				t.Fatalf("YAML GET lost the first document: %v", errParse)
			}
		})
	}
}
