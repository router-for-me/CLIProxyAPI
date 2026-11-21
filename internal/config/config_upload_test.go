package config

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/MoeForge/moego/base64"
	textunicode "golang.org/x/text/encoding/unicode"
)

func TestConfigUploadRejectsEmbeddedPlaintextAcrossDocuments(t *testing.T) {
	t.Setenv("CONFIG_TEST_MGMT_KEY", uploadTestPlaintextSecret)
	const setting = "management: {secret-key: " + uploadTestPlaintextSecret + "}\n"
	const header = "Authorization: Bearer " + uploadTestPlaintextSecret + "\n"
	for _, tc := range []struct{ name, setting, tail string }{
		{"verbatim", setting, header},
		{"escaped", setting, "Authorization: \"Bearer \\x75pload-plaintext-secret\"\n"},
		{"binary", setting, "Authorization: !!binary " + base64.StdEncoding.EncodeToString([]byte("Bearer "+uploadTestPlaintextSecret)) + "\n"},
		{"expanded alias", setting, "header: &header 'Bearer ${CONFIG_TEST_MGMT_KEY}'\nAuthorization: *header\n"},
		{"expanded candidate", "management: {secret-key: '${CONFIG_TEST_MGMT_KEY}'}\n", header},
		{"escaped key", setting, "\"prefix-\\x75pload-plaintext-secret\": true\n"},
		{"raw anchor name", setting, "extra: &prefix-" + uploadTestPlaintextSecret + " safe\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := tc.setting + "--- # retained document\r\n" + tc.tail
			path := writeUploadTestConfig(t, raw)
			if prepared, err := PrepareConfigFileForUploadWithOptional(path, false); prepared != nil || err == nil || !strings.Contains(err.Error(), "present elsewhere") {
				t.Fatalf("local preparation did not detect an embedded plaintext copy: %v", err)
			}
			codec := textunicode.UTF16(textunicode.BigEndian, textunicode.UseBOM)
			utf16, err := codec.NewEncoder().Bytes([]byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			for _, data := range [][]byte{[]byte(raw), utf16} {
				if prepared, err := PrepareConfigBytesForUploadWithOptional(data, false); prepared != nil || err == nil || !strings.Contains(err.Error(), "present elsewhere") {
					t.Fatalf("remote preparation did not detect an embedded plaintext copy: %v", err)
				}
			}
			if got := readUploadTestConfig(t, path); got != raw {
				t.Fatal("failed preparation modified the config")
			}
		})
	}
}

func TestConfigUploadChecksNullValuesHiddenByMerge(t *testing.T) {
	const setting = "server: {server-id: aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa}\nmanagement: {secret-key: " + uploadTestPlaintextSecret + "}\n"
	for _, tc := range []struct{ name, tail string }{
		{"plain", "copy: safe\n<<: {copy: !!null prefix-" + uploadTestPlaintextSecret + "-suffix}\n"},
		{"quoted", "copy: safe\n<<: {copy: !!null \"prefix-\\x75pload-plaintext-secret-suffix\"}\n"},
		{"management value", "management: {secret-key: ''}\n<<: {management: {secret-key: !!null " + uploadTestPlaintextSecret + "}}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := setting + "---\n" + tc.tail
			path := writeUploadTestConfig(t, raw)
			if prepared, err := PrepareConfigFileForUploadWithOptional(path, false); prepared != nil || err == nil {
				t.Fatal("upload accepted an invalid null containing the known plaintext key")
			}
			if got := readUploadTestConfig(t, path); got != raw {
				t.Fatal("failed preparation modified the config")
			}
		})
	}
	raw := []byte("management: {secret-key: 'null'}\n---\ncopy: null\n")
	if prepared, err := PrepareConfigBytesForUploadWithOptional(raw, false); prepared == nil || err != nil {
		t.Fatalf("upload rejected a valid null: %v", err)
	}
}

func TestConfigSaveRetainsCandidateForSubsequentUpload(t *testing.T) {
	const tail = "---\nAuthorization: \"Bearer \\x75pload-plaintext-secret\"\n"
	path := writeUploadTestConfig(t, "management: {secret-key: "+uploadTestPlaintextSecret+"}\n"+tail)
	loaded, err := LoadConfig(path)
	if err != nil || !looksLikeBcrypt(loaded.RemoteManagement.SecretKey) {
		t.Fatalf("expected a runtime-only hashed secret: %v", err)
	}
	if err := SaveConfigPreserveCommentsUpdateNestedScalar(path, []string{"server", "host"}, "localhost"); err != nil {
		t.Fatalf("unrelated save failed: %v", err)
	}
	saved := readUploadTestConfig(t, path)
	if !strings.Contains(saved, uploadTestPlaintextSecret) || !strings.Contains(saved, "host: localhost") || !strings.HasSuffix(saved, tail) {
		t.Fatal("save removed the identifying setting, change, or retained document")
	}
	if err := SaveConfigPreserveCommentsUpdateNestedScalar(path, []string{"management", "secret-key"}, loaded.RemoteManagement.SecretKey); err == nil {
		t.Fatal("replacing the identifying setting orphaned the plaintext copy")
	}
	if prepared, err := PrepareConfigFileForUploadWithOptional(path, false); prepared != nil || err == nil || !strings.Contains(err.Error(), "present elsewhere") {
		t.Fatalf("upload forgot the candidate after saving: %v", err)
	}
	if got := readUploadTestConfig(t, path); got != saved {
		t.Fatal("failed save or upload modified the config")
	}
}

func TestConfigSaveDoesNotOrphanBinaryArchive(t *testing.T) {
	const suffix = "--- # retained document\r\nunchanged: true\r\n"
	encoded := base64.StdEncoding.EncodeToString([]byte("Bearer " + uploadTestPlaintextSecret))
	binaryField := "obsolete: !!binary " + encoded + "\n"
	path := writeUploadTestConfig(t, "config-version: 8\nmanagement: {secret-key: "+uploadTestPlaintextSecret+"}\n"+binaryField+suffix)
	loaded, err := LoadConfig(path)
	if err != nil || !looksLikeBcrypt(loaded.RemoteManagement.SecretKey) {
		t.Fatalf("expected a runtime-only hashed key: %v", err)
	}
	before := readUploadTestConfig(t, path)
	if !strings.Contains(before, uploadTestPlaintextSecret) || !strings.Contains(before, binaryField) {
		t.Fatal("startup did not retain the identifying setting and binary copy")
	}
	loaded.Port = 9443
	if errSave := SaveConfigPreserveComments(path, loaded); errSave != errConfigSaveOrphansSecret {
		t.Errorf("save did not refuse to archive the encoded copy: %v", errSave)
	}
	if saved := readUploadTestConfig(t, path); saved != before {
		t.Error("refused save changed the source file")
	}
	if prepared, errPrepare := PrepareConfigFileForUploadWithOptional(path, false); prepared != nil || errPrepare == nil || !strings.Contains(errPrepare.Error(), "present elsewhere") {
		t.Errorf("upload did not reject the retained copy after the attempted save: %v", errPrepare)
	}

	if errWrite := os.WriteFile(path, []byte(strings.Replace(before, binaryField, "", 1)), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	if errSave := SaveConfigPreserveComments(path, loaded); errSave != nil {
		t.Fatalf("save after removal of the copy: %v", errSave)
	}
	prepared, errPrepare := PrepareConfigFileForUploadWithOptional(path, false)
	if errPrepare != nil || bytes.Contains(prepared, []byte(encoded)) || bytes.Contains(prepared, []byte(uploadTestPlaintextSecret)) || !bytes.Contains(prepared, []byte("port: 9443")) || !bytes.HasSuffix(prepared, []byte(suffix)) {
		t.Fatalf("upload after removal failed, retained the key, or lost the edit or suffix: %v", errPrepare)
	}
}

func TestConfigPreparedSeedUsesOriginalPlaintextCandidates(t *testing.T) {
	seed := []byte("management: {secret-key: " + uploadTestPlaintextSecret + "}\n")
	prepared, err := PrepareConfigBytesForUploadWithOptional(seed, false)
	if err != nil || !IsPreparedConfigFromSeed(seed, prepared) {
		t.Fatalf("safe startup rewrite was not recognized: %v", err)
	}
	const tail = "---\nAuthorization: Bearer " + uploadTestPlaintextSecret + "\n"
	if IsPreparedConfigFromSeed(append(seed, tail...), append(prepared, tail...)) {
		t.Fatal("seeding accepted a retained plaintext copy after the identifying key was hashed")
	}
}

func TestConfigUploadDoesNotExemptEncodedHashSpelling(t *testing.T) {
	const hash = "$2a$10$" + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	encoded := base64.StdEncoding.EncodeToString([]byte(hash))
	secret := encoded[:12]
	raw := "management: {secret-key: " + secret + "}\n---\nmanagement: {secret-key: !!binary " + encoded + "}\n"
	if prepared, err := PrepareConfigBytesForUploadWithOptional([]byte(raw), false); prepared != nil || err == nil || !strings.Contains(err.Error(), "present elsewhere") {
		t.Fatalf("upload exempted plaintext in the encoded spelling of the hash: %v", err)
	}
}

func TestConfigUploadRecognizesSafeHashEvenWithShortKey(t *testing.T) {
	// The default bcrypt cost always contains this key. The generated hash
	// must not trigger the check, but occurrences elsewhere must still count.
	const raw = "server: {server-id: aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa}\nmanagement: {secret-key: '10'}\n"
	prepared, err := PrepareConfigBytesForUploadWithOptional([]byte(raw), false)
	if err != nil || !bytes.Contains(prepared, []byte("$2a$10$")) {
		t.Fatalf("bcrypt cost was mistaken for a retained short key: %v", err)
	}
	for _, tail := range []string{"---\nAuthorization: Bearer 10\n", "---\ncoincidental: version 2104\n"} {
		if prepared, err := PrepareConfigBytesForUploadWithOptional([]byte(raw+tail), false); prepared != nil || err == nil || !strings.Contains(err.Error(), "present elsewhere") {
			t.Fatalf("short candidate did not fail closed: %v", err)
		}
	}
}
