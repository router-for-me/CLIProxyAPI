package authfile

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func testKey(t *testing.T, fill byte) []byte {
	t.Helper()
	return bytes.Repeat([]byte{fill}, KeySize)
}

func useCodec(t *testing.T, key []byte, encrypt bool) {
	t.Helper()
	codec, errCodec := NewAESGCMCodec(key)
	if errCodec != nil {
		t.Fatalf("NewAESGCMCodec() error = %v", errCodec)
	}
	Configure(codec, encrypt)
	t.Cleanup(func() { Configure(nil, false) })
}

type fakeKeyring struct {
	values map[string]string
	getErr error
	setErr error
	gets   int
}

func (f *fakeKeyring) Get(service, user string) (string, error) {
	f.gets++
	if f.getErr != nil {
		return "", f.getErr
	}
	value, ok := f.values[service+"/"+user]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return value, nil
}

func (f *fakeKeyring) Set(service, user, password string) error {
	if f.setErr != nil {
		return f.setErr
	}
	if f.values == nil {
		f.values = map[string]string{}
	}
	f.values[service+"/"+user] = password
	return nil
}

func useKeyring(t *testing.T, backend *fakeKeyring) {
	t.Helper()
	previous := keyringBackend
	keyringBackend = backend
	t.Cleanup(func() { keyringBackend = previous })
	t.Setenv(EnvKey, "")
	t.Setenv(EnvKeyFile, "")
}

const sampleAuth = `{"access_token":"secret-token","disabled":false,"email":"a@example.com","expired":"2026-11-01T00:00:00Z","refresh_token":"secret-refresh","token":{"access_token":"nested-secret"},"type":"claude","usage_ceiling":{"weekly_percent":70}}`

func fieldsOf(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var fields map[string]any
	if errUnmarshal := json.Unmarshal(data, &fields); errUnmarshal != nil {
		t.Fatalf("stored file is not a JSON object: %v\n%s", errUnmarshal, data)
	}
	return fields
}

func TestIsSecretField(t *testing.T) {
	for _, name := range []string{
		"access_token", "refresh_token", "id_token", "api_key", "session_token", "dca_token", "token", "client_secret", "password", "cookie", "private_key", "service_account",
		"api-key", "apiKey", "privateKey", "serviceAccount", "accessKey", "sessionKey", "jwt", "bearer", "device_code", "accessToken",
	} {
		if !IsSecretField(name) {
			t.Errorf("IsSecretField(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"type", "email", "usage_ceiling", "priority", "prefix", "proxy_url", "disabled", "token_type", "expired", "expires_at", "last_refresh", "account_uuid"} {
		if IsSecretField(name) {
			t.Errorf("IsSecretField(%q) = true, want false", name)
		}
	}
}

func TestEncodeSealsOnlySecretFields(t *testing.T) {
	useCodec(t, testKey(t, 1), true)
	stored, errEncode := Encode([]byte(sampleAuth))
	if errEncode != nil {
		t.Fatalf("Encode() error = %v", errEncode)
	}
	for _, secret := range []string{"secret-token", "secret-refresh", "nested-secret"} {
		if bytes.Contains(stored, []byte(secret)) {
			t.Fatalf("stored file leaks %q: %s", secret, stored)
		}
	}
	fields := fieldsOf(t, stored)
	for _, name := range []string{"access_token", "refresh_token", "token"} {
		if text, _ := fields[name].(string); !strings.HasPrefix(text, sealedPrefix) {
			t.Fatalf("%s = %v, want sealed", name, fields[name])
		}
	}
	if fields["email"] != "a@example.com" || fields["type"] != "claude" || fields["expired"] != "2026-11-01T00:00:00Z" {
		t.Fatalf("plain fields changed: %v", fields)
	}
	if ceiling, _ := fields["usage_ceiling"].(map[string]any); ceiling["weekly_percent"] != float64(70) {
		t.Fatalf("usage_ceiling = %v, want plain", fields["usage_ceiling"])
	}

	plain, errDecode := Decode(stored)
	if errDecode != nil {
		t.Fatalf("Decode() error = %v", errDecode)
	}
	if string(plain) != sampleAuth {
		t.Fatalf("Decode() = %s, want %s", plain, sampleAuth)
	}
}

func TestEncodeSealsObjectsHoldingNestedSecrets(t *testing.T) {
	useCodec(t, testKey(t, 1), true)
	input := []byte(`{"type":"codex","use_siwc":true,"siwc":{"access_token":"siwc-secret","scope":"x"},"headers":{"X-Plain":"ok"},"list":[{"refresh_token":"list-secret"}]}`)
	stored, errEncode := Encode(input)
	if errEncode != nil {
		t.Fatalf("Encode() error = %v", errEncode)
	}
	for _, secret := range []string{"siwc-secret", "list-secret"} {
		if bytes.Contains(stored, []byte(secret)) {
			t.Fatalf("nested secret %q leaks: %s", secret, stored)
		}
	}
	fields := fieldsOf(t, stored)
	if fields["use_siwc"] != true {
		t.Fatalf("use_siwc = %v, want plain true", fields["use_siwc"])
	}
	if headers, _ := fields["headers"].(map[string]any); headers["X-Plain"] != "ok" {
		t.Fatalf("headers = %v, want plain object without secrets", fields["headers"])
	}
	plain, errDecode := Decode(stored)
	if errDecode != nil || !bytes.Equal(plain, mustCompact(t, input)) {
		t.Fatalf("Decode() = %s, %v; want original", plain, errDecode)
	}
	if !isCurrent(stored) || isCurrent(input) {
		t.Fatal("isCurrent() does not track nested secrets")
	}
}

func mustCompact(t *testing.T, data []byte) []byte {
	t.Helper()
	obj, ok := parseObject(data)
	if !ok {
		t.Fatal("parseObject() failed")
	}
	out, errMarshal := obj.marshal()
	if errMarshal != nil {
		t.Fatalf("marshal() error = %v", errMarshal)
	}
	return out
}

func TestEncodeKeepsSealedFieldsAndRandomNonce(t *testing.T) {
	useCodec(t, testKey(t, 1), true)
	first, _ := Encode([]byte(sampleAuth))
	again, errEncode := Encode(first)
	if errEncode != nil {
		t.Fatalf("Encode(sealed) error = %v", errEncode)
	}
	if !bytes.Equal(first, again) {
		t.Fatal("Encode() resealed fields that were already sealed")
	}
	second, _ := Encode([]byte(sampleAuth))
	if bytes.Equal(first, second) {
		t.Fatal("two seals of the same input are equal; nonce is not random")
	}
}

func TestHandEditOfPlainFieldSurvives(t *testing.T) {
	useCodec(t, testKey(t, 1), true)
	stored, _ := Encode([]byte(sampleAuth))
	edited := bytes.Replace(stored, []byte(`"weekly_percent":70`), []byte(`"weekly_percent":55`), 1)
	if bytes.Equal(edited, stored) {
		t.Fatal("test setup: usage_ceiling not found in stored file")
	}
	plain, errDecode := Decode(edited)
	if errDecode != nil {
		t.Fatalf("Decode() after hand edit error = %v", errDecode)
	}
	fields := fieldsOf(t, plain)
	if ceiling, _ := fields["usage_ceiling"].(map[string]any); ceiling["weekly_percent"] != float64(55) {
		t.Fatalf("usage_ceiling = %v, want edited value 55", fields["usage_ceiling"])
	}
	if fields["access_token"] != "secret-token" {
		t.Fatalf("access_token = %v, want opened", fields["access_token"])
	}
}

func TestEncodeKeepsLayout(t *testing.T) {
	useCodec(t, testKey(t, 1), true)
	indented := []byte("{\n  \"access_token\": \"x\",\n  \"type\": \"kimi\"\n}\n")
	stored, _ := Encode(indented)
	if !bytes.HasPrefix(stored, []byte("{\n  \"")) || !bytes.HasSuffix(stored, []byte("}\n")) {
		t.Fatalf("indented layout lost: %q", stored)
	}
	compact := []byte(`{"access_token":"x","type":"claude"}`)
	stored, _ = Encode(compact)
	if bytes.Contains(stored, []byte("\n")) {
		t.Fatalf("compact layout lost: %q", stored)
	}
}

func TestPlainModeLeavesFilesUnchanged(t *testing.T) {
	Configure(nil, false)
	odd := []byte("{\"type\":\"claude\",  \"access_token\":\"x\"}")
	stored, errEncode := Encode(odd)
	if errEncode != nil || !bytes.Equal(stored, odd) {
		t.Fatalf("Encode() with encryption off = %q, %v; want input unchanged", stored, errEncode)
	}
	plain, errDecode := Decode(odd)
	if errDecode != nil || !bytes.Equal(plain, odd) {
		t.Fatalf("Decode(plain) = %q, %v", plain, errDecode)
	}
}

func TestDecodeErrors(t *testing.T) {
	useCodec(t, testKey(t, 1), true)
	stored, _ := Encode([]byte(sampleAuth))

	Configure(nil, false)
	if _, errDecode := Decode(stored); !errors.Is(errDecode, ErrNoKey) {
		t.Fatalf("Decode() without key error = %v, want ErrNoKey", errDecode)
	}

	useCodec(t, testKey(t, 2), true)
	if _, errDecode := Decode(stored); errDecode == nil || !strings.Contains(errDecode.Error(), "encrypted with key") {
		t.Fatalf("Decode() with other key error = %v, want key mismatch", errDecode)
	}

	useCodec(t, testKey(t, 1), true)
	fields := fieldsOf(t, stored)
	// A sealed value moved to another field must not open.
	fields["refresh_token"] = fields["access_token"]
	swapped, _ := json.Marshal(fields)
	if _, errDecode := Decode(swapped); errDecode == nil {
		t.Fatal("Decode() of value moved to another field error = nil")
	}
}

func TestWriteAndReadFile(t *testing.T) {
	useCodec(t, testKey(t, 1), true)
	dir := t.TempDir()
	for name, write := range map[string]func(string, []byte, os.FileMode) error{
		"in-place.json": WriteFile,
		"atomic.json":   WriteFileAtomic,
	} {
		path := filepath.Join(dir, name)
		if errWrite := write(path, []byte(sampleAuth), 0o600); errWrite != nil {
			t.Fatalf("%s: write error = %v", name, errWrite)
		}
		raw, _ := os.ReadFile(path)
		if !IsSealed(raw) || bytes.Contains(raw, []byte("secret-token")) {
			t.Fatalf("%s: secrets not sealed on disk: %s", name, raw)
		}
		plain, upToDate, errRead := ReadFileCurrent(path)
		if errRead != nil || !upToDate || string(plain) != sampleAuth {
			t.Fatalf("%s: ReadFileCurrent() = %s, %v, %v", name, plain, upToDate, errRead)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatalf("directory has %d entries, want only the two auth files", len(entries))
	}
}

func TestReadFileCurrentFlagsPlainSecrets(t *testing.T) {
	useCodec(t, testKey(t, 1), true)
	path := filepath.Join(t.TempDir(), "claude-a.json")
	_ = os.WriteFile(path, []byte(sampleAuth), 0o600)
	if _, upToDate, _ := ReadFileCurrent(path); upToDate {
		t.Fatal("plain secrets reported as up to date with encryption on")
	}
	noSecrets := filepath.Join(filepath.Dir(path), "other.json")
	_ = os.WriteFile(noSecrets, []byte(`{"type":"claude","email":"a@example.com"}`), 0o600)
	if _, upToDate, _ := ReadFileCurrent(noSecrets); !upToDate {
		t.Fatal("file without secrets reported as needing a rewrite")
	}
}

func TestLoadKeySources(t *testing.T) {
	t.Run("env wins", func(t *testing.T) {
		backend := &fakeKeyring{}
		useKeyring(t, backend)
		t.Setenv(EnvKey, base64.StdEncoding.EncodeToString(testKey(t, 3)))
		key, source, errKey := LoadKey("", true)
		if errKey != nil || !bytes.Equal(key, testKey(t, 3)) || !strings.Contains(source, "env") {
			t.Fatalf("LoadKey() = %x, %q, %v", key, source, errKey)
		}
		if backend.gets != 0 {
			t.Fatal("keyring queried although env key is set")
		}
	})
	t.Run("key file", func(t *testing.T) {
		useKeyring(t, &fakeKeyring{})
		path := filepath.Join(t.TempDir(), "key")
		_ = os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(testKey(t, 4))+"\n"), 0o600)
		key, source, errKey := LoadKey(path, true)
		if errKey != nil || !bytes.Equal(key, testKey(t, 4)) || !strings.Contains(source, "file") {
			t.Fatalf("LoadKey() = %x, %q, %v", key, source, errKey)
		}
	})
	t.Run("keyring creates once", func(t *testing.T) {
		backend := &fakeKeyring{}
		useKeyring(t, backend)
		first, _, errFirst := LoadKey("", true)
		second, _, errSecond := LoadKey("", true)
		if errFirst != nil || errSecond != nil || !bytes.Equal(first, second) {
			t.Fatalf("LoadKey() twice = %x, %v / %x, %v; want same key", first, errFirst, second, errSecond)
		}
	})
	t.Run("keyring missing without create", func(t *testing.T) {
		useKeyring(t, &fakeKeyring{})
		if _, _, errKey := LoadKey("", false); errKey == nil {
			t.Fatal("LoadKey(create=false) error = nil")
		}
	})
	t.Run("keyring unavailable", func(t *testing.T) {
		useKeyring(t, &fakeKeyring{getErr: errors.New("no dbus")})
		_, _, errKey := LoadKey("", true)
		if errKey == nil || !strings.Contains(errKey.Error(), EnvKey) {
			t.Fatalf("LoadKey() error = %v, want hint to set %s", errKey, EnvKey)
		}
	})
	t.Run("bad key", func(t *testing.T) {
		useKeyring(t, &fakeKeyring{})
		t.Setenv(EnvKey, "short")
		if _, _, errKey := LoadKey("", true); errKey == nil {
			t.Fatal("LoadKey() with short key error = nil")
		}
	})
}

func TestSetupConvertsDirectory(t *testing.T) {
	useKeyring(t, &fakeKeyring{})
	t.Cleanup(func() { Configure(nil, false) })
	dir := t.TempDir()
	authPath := filepath.Join(dir, "claude-a.json")
	otherPath := filepath.Join(dir, "notes.json")
	nestedPath := filepath.Join(dir, "team", "codex-b.json")
	_ = os.MkdirAll(filepath.Dir(nestedPath), 0o700)
	_ = os.WriteFile(authPath, []byte(sampleAuth), 0o600)
	_ = os.WriteFile(otherPath, []byte(`{"api_key":"not-an-auth-file"}`), 0o600)
	_ = os.WriteFile(nestedPath, []byte(`{"type":"codex","access_token":"x"}`), 0o600)

	if errSetup := Setup(true, "", dir); errSetup != nil {
		t.Fatalf("Setup(true) error = %v", errSetup)
	}
	for _, path := range []string{authPath, nestedPath} {
		raw, _ := os.ReadFile(path)
		if !IsSealed(raw) {
			t.Fatalf("%s not encrypted: %s", path, raw)
		}
	}
	if raw, _ := os.ReadFile(otherPath); IsSealed(raw) {
		t.Fatal("non-auth JSON was encrypted")
	}
	if plain, errRead := ReadFile(authPath); errRead != nil || string(plain) != sampleAuth {
		t.Fatalf("ReadFile() after setup = %s, %v", plain, errRead)
	}

	// Turning encryption off never decrypts: the file stays sealed and can no
	// longer be read until the account logs in again.
	before, _ := os.ReadFile(authPath)
	if errSetup := Setup(false, "", dir); errSetup != nil {
		t.Fatalf("Setup(false) error = %v", errSetup)
	}
	if after, _ := os.ReadFile(authPath); !bytes.Equal(before, after) {
		t.Fatalf("Setup(false) changed a sealed file:\n%s", after)
	}
	if Encrypting() {
		t.Fatal("Encrypting() = true after Setup(false)")
	}
	if _, errRead := ReadFile(authPath); !errors.Is(errRead, ErrUnreadable) {
		t.Fatalf("ReadFile() with encryption off error = %v, want ErrUnreadable", errRead)
	}
}

func TestSetupEnabledNeverCreatesKeyForSealedFiles(t *testing.T) {
	dir := t.TempDir()
	useCodec(t, testKey(t, 1), true)
	_ = WriteFile(filepath.Join(dir, "claude-a.json"), []byte(sampleAuth), 0o600)
	Configure(nil, false)

	backend := &fakeKeyring{}
	useKeyring(t, backend)
	if errSetup := Setup(true, "", dir); errSetup == nil {
		t.Fatal("Setup(true) error = nil, want missing key error")
	}
	if len(backend.values) != 0 {
		t.Fatal("Setup(true) created a new key although encrypted files exist")
	}
	if Encrypting() {
		t.Fatal("Encrypting() = true after failed Setup")
	}
}

func TestSetupEnabledWithWrongKeyFails(t *testing.T) {
	dir := t.TempDir()
	useCodec(t, testKey(t, 1), true)
	sealedPath := filepath.Join(dir, "claude-a.json")
	_ = WriteFile(sealedPath, []byte(sampleAuth), 0o600)
	plainPath := filepath.Join(dir, "claude-b.json")
	_ = os.WriteFile(plainPath, []byte(sampleAuth), 0o600)
	Configure(nil, false)

	useKeyring(t, &fakeKeyring{})
	t.Setenv(EnvKey, base64.StdEncoding.EncodeToString(testKey(t, 2)))
	if errSetup := Setup(true, "", dir); errSetup == nil || !strings.Contains(errSetup.Error(), "claude-a.json") {
		t.Fatalf("Setup(true) with wrong key error = %v, want error naming the file", errSetup)
	}
	if Encrypting() {
		t.Fatal("Encrypting() = true after failed Setup")
	}
}

func TestSetupDisabledWithPlainFilesSkipsKeyring(t *testing.T) {
	backend := &fakeKeyring{getErr: errors.New("must not be called")}
	useKeyring(t, backend)
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "claude-a.json"), []byte(sampleAuth), 0o600)
	if errSetup := Setup(false, "", dir); errSetup != nil {
		t.Fatalf("Setup(false) error = %v", errSetup)
	}
	if backend.gets != 0 {
		t.Fatal("keyring queried with encryption off and no encrypted files")
	}
}

func TestSetupDisabledWithSealedFilesSkipsKeyring(t *testing.T) {
	dir := t.TempDir()
	useCodec(t, testKey(t, 1), true)
	_ = WriteFile(filepath.Join(dir, "claude-a.json"), []byte(sampleAuth), 0o600)
	Configure(nil, false)

	backend := &fakeKeyring{getErr: errors.New("must not be called")}
	useKeyring(t, backend)
	if errSetup := Setup(false, "", dir); errSetup != nil {
		t.Fatalf("Setup(false) error = %v, want startup to continue", errSetup)
	}
	if backend.gets != 0 {
		t.Fatal("keyring queried with encryption off")
	}
}
