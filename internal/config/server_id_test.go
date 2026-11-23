package config

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/google/uuid"
	textunicode "golang.org/x/text/encoding/unicode"
	"gopkg.in/yaml.v3"
)

const testServerID = "69ec4306-978f-4dae-8365-5409c17985fc"

func TestServerIDPreservesLegacyLayoutAndStream(t *testing.T) {
	tail := "...\n# later document\n---\r\n{ note: 'leave me   as-is' }\r\n"
	path := writeUploadTestConfig(t, "# note\nport: 8317\nextra: { a: b }\n"+tail)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil || cfg.Port != 8317 {
		t.Fatalf("load = %#v, %v", cfg, err)
	}
	if id, errParse := uuid.Parse(cfg.ServerID); errParse != nil || id.Version() != 4 || id.String() != cfg.ServerID {
		t.Fatalf("generated ServerID = %q, %v", cfg.ServerID, errParse)
	}
	data := readUploadTestConfig(t, path)
	if !strings.HasSuffix(data, tail) || !strings.Contains(data, "# note") || !strings.Contains(data, "extra:") || strings.Contains(data, "config-version:") {
		t.Fatalf("rewrite changed unrelated configuration: %q", data)
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) || cfg.SourceHash != ComputeSourceHash([]byte(data)) {
		t.Fatalf("rewrite replaced inode or loaded stale hash: %v", err)
	}
	cfg2, err := LoadConfig(path)
	if err != nil || cfg2.ServerID != cfg.ServerID || readUploadTestConfig(t, path) != data {
		t.Fatalf("second load changed ID or bytes: %v", err)
	}
	var root yaml.Node
	if err = yaml.Unmarshal([]byte(data), &root); err != nil || IsV8ConfigLayout(root.Content[0]) {
		t.Fatalf("identity alone marked legacy file as v8: %v", err)
	}
	encoded, err := json.Marshal(cfg)
	if err != nil || bytes.Contains(encoded, []byte(cfg.ServerID)) {
		t.Fatalf("identity leaked into Config JSON: %s, %v", encoded, err)
	}
	encoded, err = yaml.Marshal(cfg)
	if err != nil || !bytes.Contains(encoded, []byte("server-id: "+cfg.ServerID)) {
		t.Fatalf("identity disappeared from Config YAML: %v", err)
	}
}

func TestServerIDPreservesAliasKeysMergesAndAnchorRedefinitions(t *testing.T) {
	raw := []byte("key: &key server\nid-key: &idkey server-id\none: &redef { note: first }\n*key:\n  !!merge '<<': *redef\n  *idkey : &id ' " + strings.ToUpper(testServerID) + " ' # identity\nfirst: *redef\nold-id: *id\ntwo: &redef { note: second }\nsecond: *redef\n")
	updated, id, err := EnsureServerIDInConfigBytesWithOptional(raw, false)
	if err != nil || id != testServerID {
		t.Fatalf("ensure ID = %q, %v", id, err)
	}
	for _, marker := range []string{"*key", "*idkey", "!!merge", "&redef", "*redef", "# identity"} {
		if !bytes.Contains(updated, []byte(marker)) {
			t.Errorf("lost %q: %s", marker, updated)
		}
	}
	var before, after map[string]any
	if err = yaml.Unmarshal(raw, &before); err != nil {
		t.Fatal(err)
	}
	if err = yaml.Unmarshal(updated, &after); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"first", "second", "old-id"} {
		if !equalConfigTestValues(before[name], after[name]) {
			t.Errorf("%s changed: %#v -> %#v", name, before[name], after[name])
		}
	}
}

func equalConfigTestValues(a, b any) bool {
	x, _ := yaml.Marshal(a)
	y, _ := yaml.Marshal(b)
	return bytes.Equal(x, y)
}

func TestServerIDStartupValidationParity(t *testing.T) {
	cases := []struct {
		name, raw string
		standby   bool
	}{
		{"empty", " \n", true},
		{"invalid", "port: [", true},
		{"null", "null\n", true},
		{"trailing invalid", "port: 1\n---\n[", true},
		{"bad weight", "gemini-api-key: [{api-key: x, weight: '0'}]\n", true},
		{"long secret", "management: {secret-key: " + strings.Repeat("x", 73) + "}\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeUploadTestConfig(t, tc.raw)
			cfg, errLoad := LoadConfigOptional(path, true)
			out, id, errEnsure := EnsureServerIDInConfigBytesWithOptional([]byte(tc.raw), true)
			upload, errUpload := PrepareConfigFileForUploadWithOptional(path, true)
			if tc.standby {
				if errLoad != nil || errEnsure != nil || errUpload != nil || cfg.ServerID != "" || id != "" || out != nil || upload != nil {
					t.Fatalf("not standby: load %v, ensure %v, upload %v", errLoad, errEnsure, errUpload)
				}
			} else if errLoad == nil || errEnsure == nil || errUpload == nil {
				t.Fatalf("not consistently rejected: load %v, ensure %v, upload %v", errLoad, errEnsure, errUpload)
			}
			if data := readUploadTestConfig(t, path); data != tc.raw {
				t.Fatalf("invalid config modified: %q", data)
			}
		})
	}
	for _, path := range []string{t.TempDir(), filepath.Join(t.TempDir(), "missing.yaml")} {
		cfg, err := LoadConfigOptional(path, true)
		data, errUpload := PrepareConfigFileForUploadWithOptional(path, true)
		if err != nil || cfg.ServerID != "" || errUpload != nil || data != nil {
			t.Fatalf("load/upload not on standby: %v, %v", err, errUpload)
		}
	}
}

func TestConfigSnapshotCASRejectsDifferentBytesAndInode(t *testing.T) {
	path := writeUploadTestConfig(t, "port: 1\n---\nold\n")
	snapshot, err := ReadConfigFileSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("port: 1\n---\nnew\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = WriteConfigFileCAS(path, snapshot, []byte("port: 2\n")); !errors.Is(err, ErrConfigFileChanged) {
		t.Fatalf("CAS on changed bytes = %v", err)
	}
	if err = os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, snapshot.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = WriteConfigFileCAS(path, snapshot, []byte("port: 2\n")); !errors.Is(err, ErrConfigFileChanged) {
		t.Fatalf("CAS on replaced inode = %v", err)
	}
}

func TestServerIDReadOnlyReuseAndPromotion(t *testing.T) {
	path := writeUploadTestConfig(t, "port: 8317\n")
	if errChmod := os.Chmod(path, 0o400); errChmod != nil {
		t.Fatal(errChmod)
	}
	t.Cleanup(func() {
		if errChmod := os.Chmod(path, 0o600); errChmod != nil {
			t.Error(errChmod)
		}
	})
	if file, errOpen := os.OpenFile(path, os.O_RDWR, 0); errOpen == nil {
		if errClose := file.Close(); errClose != nil {
			t.Fatal(errClose)
		}
		t.Skip("read-only file is writable by this user")
	}
	cfg, err := LoadConfig(path)
	if err != nil || cfg.ServerID == "" || cfg.SourceHash != ComputeSourceHash([]byte("port: 8317\n")) {
		t.Fatalf("read-only startup = %#v, %v", cfg, err)
	}
	cfg2, err := LoadConfig(path)
	if err != nil || cfg2.ServerID != cfg.ServerID || readUploadTestConfig(t, path) != "port: 8317\n" {
		t.Fatalf("read-only startup changed ID or bytes: %v", err)
	}
	if snapshot, errPrepare := PrepareConfigFileForUploadWithOptional(path, false); snapshot != nil || errPrepare == nil {
		t.Fatal("accepted an unpersisted identity for upload")
	}
	if err = os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg3, err := LoadConfig(path)
	if err != nil || cfg3.ServerID != cfg.ServerID || !strings.Contains(readUploadTestConfig(t, path), cfg.ServerID) {
		t.Fatalf("did not persist the same ID after file became writable: %v", err)
	}
	promoted, err := ReadConfigFileSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if lookupEphemeralServerID(promoted.info) != "" {
		t.Fatal("promotion did not retire the ephemeral ID")
	}
	const rotatedID = "8237fe4a-1419-4a20-8b39-77dbc44168cc"
	rotated, err := WriteConfigFileCAS(path, promoted, []byte("port: 8317\nserver: {server-id: "+rotatedID+"}\n"))
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(path)
	if err != nil || loaded.ServerID != rotatedID {
		t.Fatalf("startup did not accept the rotated ID: %v", err)
	}
	if _, err = WriteConfigFileCAS(path, rotated, []byte("port: 8317\n")); err != nil {
		t.Fatal(err)
	}
	regenerated, err := LoadConfig(path)
	if err != nil || regenerated.ServerID == "" || regenerated.ServerID == cfg.ServerID || regenerated.ServerID == rotatedID {
		t.Fatalf("deletion revived a retired server ID: %+v, %v", regenerated, err)
	}
}

func TestServerIDReadOnlyAcceptsExplicitIdentityAndRetiresEphemeral(t *testing.T) {
	path := writeUploadTestConfig(t, "port: 8317\n")
	chmod := func(mode os.FileMode) {
		t.Helper()
		if errChmod := os.Chmod(path, mode); errChmod != nil {
			t.Fatal(errChmod)
		}
	}
	chmod(0o400)
	initial, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	onDisk, err := ReadConfigFileSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk.Bytes()) != "port: 8317\n" {
		t.Skip("read-only file is writable by this user")
	}
	if initial.ServerID == "" || lookupEphemeralServerID(onDisk.info) != initial.ServerID {
		t.Fatal("read-only startup did not cache a temporary ID")
	}
	const explicitID = "8237fe4a-1419-4a20-8b39-77dbc44168cc"
	chmod(0o600)
	// An external editor adds an explicit ID to the same inode. Canonicalizing
	// its spelling requires a write that the next read-only load cannot do.
	if errWrite := os.WriteFile(path, []byte("port: 8317\nserver: {server-id: "+strings.ToUpper(explicitID)+"}\n"), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	chmod(0o400)
	accepted, err := LoadConfig(path)
	if err != nil || accepted.ServerID != explicitID || lookupEphemeralServerID(onDisk.info) != "" {
		t.Fatalf("accepting an explicit read-only ID did not retire the temporary ID: %v", err)
	}
}

func TestServerIDRejectsCrossDocumentAlias(t *testing.T) {
	raw := []byte("port: 8317\nextra: &marker value\n---\nextra: *marker\n")
	if _, _, err := EnsureServerIDInConfigBytes(raw); err == nil {
		t.Fatal("accepted a reference to an anchor from another document")
	}
}

func TestPrepareConfigUploadVerifiesEntireStream(t *testing.T) {
	t.Setenv("SERVER_ID_TEST_SECRET", uploadTestPlaintextSecret)
	for name, raw := range map[string]string{
		"later legacy secret": "port: 8317\n---\nremote-management: {secret-key: wrong-to-upload}\n",
		"later v8 secret":     "port: 8317\n---\nmanagement: {secret-key: wrong-to-upload}\n",
		"copy in later doc":   "management: {secret-key: " + uploadTestPlaintextSecret + "}\n---\nextra: " + uploadTestPlaintextSecret + "\n",
		"expanded later copy": "management: {secret-key: '${SERVER_ID_TEST_SECRET}'}\n---\nextra: " + uploadTestPlaintextSecret + "\n",
		"expression later":    "management: {secret-key: " + uploadTestPlaintextSecret + "}\n---\nextra: '${SERVER_ID_TEST_SECRET}'\n",
		"invalid later key":   "port: 8317\n---\nmanagement: {secret-key: [wrong-to-upload]}\n",
		"shadowed merge":      "default: &m {management: {secret-key: wrong-to-upload}}\n<<: *m\nmanagement: {secret-key: ''}\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := writeUploadTestConfig(t, raw)
			out, err := PrepareConfigFileForUploadWithOptional(path, false)
			if err == nil || out != nil || readUploadTestConfig(t, path) != raw {
				t.Fatalf("prepared unsafe config or changed source: %v", err)
			}
		})
	}
}

func TestPrepareConfigUploadReturnsVerifiedSnapshot(t *testing.T) {
	secret := strings.Repeat("密", 24)
	tail := "--- # retain\r\n{xyz: 'a    b', management: {secret-key: null}}\r\n"
	path := writeUploadTestConfig(t, "management: {secret-key: "+secret+"}\n"+tail)
	out, err := PrepareConfigFileForUploadWithOptional(path, false)
	if err != nil || string(out) != readUploadTestConfig(t, path) || !strings.HasSuffix(string(out), tail) || strings.Contains(string(out), secret) {
		t.Fatalf("prepared snapshot was not rewritten safely: %v", err)
	}
	cfg, err := LoadConfig(path)
	if err != nil || cfg.ServerID == "" || cfg.SourceHash != ComputeSourceHash(out) {
		t.Fatalf("startup does not match verified bytes: %v", err)
	}
}

func TestServerIDRetainsUTF16StreamSuffix(t *testing.T) {
	codec := textunicode.UTF16(textunicode.LittleEndian, textunicode.UseBOM)
	tail := "---\r\n{other: '🌍   keep'}\r\n"
	raw, err := codec.NewEncoder().String("# 🌎\r\nport: 8317\r\n" + tail)
	if err != nil {
		t.Fatal(err)
	}
	encodedTail, err := codec.NewEncoder().String(tail)
	if err != nil {
		t.Fatal(err)
	}
	updated, id, err := EnsureServerIDInConfigBytes([]byte(raw))
	if err != nil || id == "" || !bytes.HasSuffix(updated, []byte(encodedTail[2:])) {
		t.Fatalf("UTF-16 rewrite changed the second document: %v", err)
	}
}

func TestServerIDAliasKeysBindAcrossRedefinitions(t *testing.T) {
	raw := "first: &k server\n*k: {port: 8317}\nsecond: &k extra\n*k: keep\n"
	updated, id, err := EnsureServerIDInConfigBytes([]byte(raw))
	if err != nil || id == "" || strings.Count(string(updated), "*k") != 2 || strings.Count(string(updated), "&k") != 2 {
		t.Fatalf("could not preserve both alias bindings: %s, %v", updated, err)
	}
	parsed, err := ParseConfigBytes(updated)
	if err != nil || parsed.Port != 8317 || parsed.ServerID != id {
		t.Fatalf("rewritten config did not close over the input semantics: %v", err)
	}
}

func TestServerIDSaveDoesNotMigrateLegacyConfig(t *testing.T) {
	path := writeUploadTestConfig(t, "port: 8317\n")
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Port = 9443
	if err = SaveConfigPreserveComments(path, cfg); err != nil {
		t.Fatal(err)
	}
	data := readUploadTestConfig(t, path)
	if !strings.Contains(data, "\nport: 9443") && !strings.HasPrefix(data, "port: 9443\n") {
		t.Fatalf("legacy port was moved: %s", data)
	}
	if strings.Contains(data, "config-version:") || !strings.Contains(data, cfg.ServerID) {
		t.Fatalf("legacy save migrated the file or lost its identity: %s", data)
	}
}
