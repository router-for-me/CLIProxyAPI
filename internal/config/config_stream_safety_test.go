package config

import (
	"fmt"
	"strings"
	"testing"

	"github.com/MoeForge/moego/base64"
	"golang.org/x/crypto/bcrypt"
)

func TestConfigUploadRecognizesBinaryKeyNames(t *testing.T) {
	binary := func(name string) string {
		return "!!binary " + base64.StdEncoding.EncodeToString([]byte(name))
	}
	for _, section := range []string{"remote-management", "management"} {
		t.Run(section, func(t *testing.T) {
			for name, raw := range map[string]string{
				"direct": binary(section) + ": {" + binary("secret-key") + ": " + uploadTestPlaintextSecret + "}\n",
				"alias":  "section: &section " + binary(section) + "\nfield: &field " + binary("secret-key") + "\n*section: {*field: " + uploadTestPlaintextSecret + "}\n",
			} {
				t.Run(name, func(t *testing.T) {
					if errVerify := VerifyConfigBytesForUpload([]byte(raw)); errVerify == nil {
						t.Fatal("immutable YAML with a binary plaintext key was accepted")
					}
					path := writeUploadTestConfig(t, raw)
					prepared, err := PrepareConfigFileForUploadWithOptional(path, false)
					if err != nil || strings.Contains(string(prepared), uploadTestPlaintextSecret) {
						t.Fatalf("did not hash the binary-named management secret: %v", err)
					}
					if errVerify := VerifyConfigBytesForUpload(prepared); errVerify != nil {
						t.Fatal(errVerify)
					}
					parsed, errParse := ParseConfigBytes(prepared)
					if errParse != nil {
						t.Fatal(errParse)
					}
					if errHash := bcrypt.CompareHashAndPassword([]byte(parsed.RemoteManagement.SecretKey), []byte(uploadTestPlaintextSecret)); errHash != nil {
						t.Fatal(errHash)
					}
					tail := []byte("port: 8317\n---\n" + raw)
					if errVerify := VerifyConfigBytesForUpload(tail); errVerify == nil {
						t.Fatal("immutable YAML with a binary secret in a trailing document was accepted")
					}
					if prepared, errPrepare := PrepareConfigBytesForUploadWithOptional(tail, false); errPrepare == nil || prepared != nil {
						t.Fatal("upload preparation accepted a binary secret in a trailing document")
					}
				})
			}
		})
	}
	raw := "port: 8317\n---\nmanagement: {}\n" + binary("management") + ": {}\n"
	if _, err := DecodeFirstConfigDocument([]byte(raw)); err == nil {
		t.Fatal("plain and binary spellings of the same key were accepted as distinct keys")
	}
}

func TestConfigStreamChecksShadowedAliasGraphBeforeExpansion(t *testing.T) {
	const cycle = "<<: [{shadowed: &loop [*loop]}]\nshadowed: safe\n"
	for name, raw := range map[string]string{"first": cycle, "trailing": "port: 8317\n---\n" + cycle} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeFirstConfigDocument([]byte(raw)); err == nil || !strings.Contains(err.Error(), "cyclic YAML alias") {
				t.Fatalf("shadowed alias cycle was not caught: %v", err)
			}
			if _, _, err := NormalizeConfigLayout([]byte(raw), true); err == nil {
				t.Fatal("v8 migration admitted the cycle")
			}
		})
	}

	for _, tc := range []struct {
		name, value string
		depth       int
	}{
		{"nodes", "small", 17},
		{"bytes", strings.Repeat("x", 256<<10), 9},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var raw strings.Builder
			fmt.Fprintf(&raw, "port: 8317\n---\n<<:\n  a0: &a0 %s\n", tc.value)
			for i := 1; i <= tc.depth; i++ {
				fmt.Fprintf(&raw, "  a%d: &a%d [*a%d, *a%d]\n", i, i, i-1, i-1)
			}
			for i := 0; i <= tc.depth; i++ {
				fmt.Fprintf(&raw, "a%d: shadowed\n", i)
			}
			if _, err := DecodeFirstConfigDocument([]byte(raw.String())); err == nil || !strings.Contains(err.Error(), "maximum alias expansion size") {
				t.Fatalf("expansion amplification was not caught: %v", err)
			}
		})
	}
}

func TestConfigStreamChecksDuplicateKeysInsideShadowedMappings(t *testing.T) {
	for name, tail := range map[string]string{
		"scalar":       "<<: {nested: {x: one, x: two}}\nnested: ok\n",
		"scalar alias": "<<: {nested: {x: &k x, *k : two}}\nnested: ok\n",
		"binary":       "<<: [{nested: [{x: one, !!binary eA== : two}]}]\nnested: ok\n",
		"binary alias": "<<: {nested: {x: &k !!binary eA==, *k : two}}\nnested: ok\n",
	} {
		t.Run(name, func(t *testing.T) {
			raw := "port: 8317\n---\n" + tail
			path := writeUploadTestConfig(t, raw)
			if prepared, err := PrepareConfigFileForUploadWithOptional(path, false); prepared != nil || err == nil || !strings.Contains(err.Error(), "document 2: mapping key") {
				t.Fatalf("local preparation did not reject a hidden duplicate: %v", err)
			}
			if prepared, err := PrepareConfigBytesForUploadWithOptional([]byte(raw), false); prepared != nil || err == nil || !strings.Contains(err.Error(), "document 2: mapping key") {
				t.Fatalf("remote preparation did not reject a hidden duplicate: %v", err)
			}
			if err := VerifyConfigBytesForUpload([]byte(raw)); err == nil || !strings.Contains(err.Error(), "document 2: mapping key") {
				t.Fatalf("immutable verification did not reject a hidden duplicate: %v", err)
			}
			if got := readUploadTestConfig(t, path); got != raw {
				t.Fatal("failed preparation modified the config")
			}
		})
	}

	const tail = "---\n<<: {nested: {first: &k alpha, *k : one, second: &k !!binary YmV0YQ==, *k : two}}\nnested: ok\n"
	if prepared, err := PrepareConfigBytesForUploadWithOptional([]byte("port: 8317\n"+tail), false); err != nil || !strings.HasSuffix(string(prepared), tail) {
		t.Fatalf("valid merge override or alias redefinitions were not preserved: %v", err)
	}
}

func TestConfigRepairPreservesIdentifiablePlaintextInValidPrefix(t *testing.T) {
	const management = "management: {secret-key: " + uploadTestPlaintextSecret + "}\n"
	const copy = "copy: " + uploadTestPlaintextSecret + "\n"
	broken := []byte(management + copy + "---\ninvalid: [\n")
	if err := ValidateConfigSave(broken, []byte(copy)); err == nil {
		t.Fatal("repair of a later document orphaned plaintext from a valid document")
	}
	if err := ValidateConfigSave(broken, []byte("port: 8317\n")); err != nil {
		t.Fatalf("repair removing both the setting and the plaintext copy was rejected: %v", err)
	}
}

func TestConfigSaveAndUploadAccountForPlaintextComments(t *testing.T) {
	const key = "management: {secret-key: " + uploadTestPlaintextSecret + "}\n"
	for name, extra := range map[string]string{
		"inline":     "port: 8317 # " + uploadTestPlaintextSecret + "\n",
		"end marker": "... # " + uploadTestPlaintextSecret + "\n",
		"postamble":  "...\n# archive: " + uploadTestPlaintextSecret + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := writeUploadTestConfig(t, key+extra)
			if snapshot, err := PrepareConfigFileForUploadWithOptional(path, false); err == nil || snapshot != nil {
				t.Fatal("upload preparation accepted plaintext in a comment")
			}
			cfg, err := LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			before := readUploadTestConfig(t, path)
			if errSave := SaveConfigPreserveCommentsUpdateNestedScalar(path, []string{"management", "secret-key"}, cfg.RemoteManagement.SecretKey); errSave == nil {
				t.Fatal("a scalar save orphaned plaintext in a comment")
			}
			if got := readUploadTestConfig(t, path); got != before {
				t.Fatal("rejected save changed the file")
			}
		})
	}
}
