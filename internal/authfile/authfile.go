// Package authfile reads and writes auth files. When encryption is on, it
// seals the secret fields of an auth file (tokens, keys, passwords, cookies)
// before they reach disk and opens them on read. All other fields stay plain,
// so settings such as priority, prefix or proxy_url can still be edited by
// hand. Plain files are always readable, so existing auth directories keep
// working.
//
// Every reader and writer of auth files should go through this package so the
// on-disk format is decided in one place.
package authfile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

// Codec seals one auth file field value for storage and opens it again. The
// field name is bound to the sealed value.
type Codec interface {
	// Seal returns the stored string for the JSON encoding of a field value.
	Seal(plain []byte, field string) (string, error)
	// Open returns the JSON encoding of a sealed field value.
	Open(sealed string, field string) ([]byte, error)
}

var (
	// ErrUnreadable reports an auth file whose sealed fields cannot be opened.
	// Writers that create a credential treat such a file like a missing one,
	// so logging in again replaces it.
	ErrUnreadable = errors.New("authfile: encrypted fields cannot be decrypted")
	// ErrNoKey reports a sealed auth file read while no key is configured.
	ErrNoKey = fmt.Errorf("%w: no encryption key is configured", ErrUnreadable)
)

type state struct {
	codec   Codec
	encrypt bool
}

var current atomic.Pointer[state]

// Configure sets the codec used to open sealed fields. When encrypt is true,
// writes seal secret fields with it. A nil codec means plain files only.
func Configure(codec Codec, encrypt bool) {
	if codec == nil {
		current.Store(nil)
		return
	}
	current.Store(&state{codec: codec, encrypt: encrypt})
}

// Encrypting reports whether writes seal secret fields.
func Encrypting() bool {
	s := current.Load()
	return s != nil && s.encrypt
}

// secretMarkers are matched against field names with case, "-" and "_"
// removed, so "api_key", "api-key" and "apiKey" all match "apikey".
var secretMarkers = []string{
	"token", "secret", "password", "cookie", "apikey", "privatekey", "serviceaccount",
	"credential", "accesskey", "sessionkey", "jwt", "bearer", "devicecode",
}

func normalizeFieldName(name string) string {
	return strings.NewReplacer("_", "", "-", "", " ", "").Replace(strings.ToLower(strings.TrimSpace(name)))
}

// IsSecretField reports whether an auth file field name holds secret material
// that is sealed when encryption is on. Matching is by name so that provider
// and plugin fields are covered without a per-provider list. Sealing a field
// that is not secret only costs hand-editability, never function, because
// every reader opens sealed values.
func IsSecretField(name string) bool {
	normalized := normalizeFieldName(name)
	switch normalized {
	case "tokentype", "tokenendpoint", "tokenuri":
		return false
	}
	lower := strings.ToLower(strings.TrimSpace(name))
	if strings.Contains(normalized, "expire") || strings.HasSuffix(lower, "_at") || strings.HasSuffix(lower, "-at") || strings.HasSuffix(name, "At") {
		return false
	}
	for _, marker := range secretMarkers {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

// containsSecret reports whether a nested object or array holds a field with a
// secret name at any depth, such as the Codex "siwc" object.
func containsSecret(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for name, child := range typed {
			if IsSecretField(name) && !isEmptyValue(child) {
				return true
			}
			if containsSecret(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsSecret(child) {
				return true
			}
		}
	}
	return false
}

func isEmptyValue(value any) bool {
	if value == nil {
		return true
	}
	text, isText := value.(string)
	return isText && text == ""
}

// shouldSeal reports whether a top-level field must be stored sealed: a
// non-empty value under a secret name, or an object or array that holds a
// secret anywhere inside it.
func shouldSeal(name string, value any) bool {
	if isEmptyValue(value) || isSealedValue(value) {
		return false
	}
	return IsSecretField(name) || containsSecret(value)
}

// object is a parsed auth file plus the layout needed to write it back.
type object struct {
	fields          map[string]any
	indent          bool
	trailingNewline bool
}

func parseObject(data []byte) (object, bool) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return object{}, false
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	var fields map[string]any
	if errDecode := decoder.Decode(&fields); errDecode != nil || fields == nil {
		return object{}, false
	}
	return object{
		fields:          fields,
		indent:          bytes.HasPrefix(trimmed, []byte("{\n")) || bytes.HasPrefix(trimmed, []byte("{\r\n")),
		trailingNewline: bytes.HasSuffix(data, []byte("\n")),
	}, true
}

func (o object) marshal() ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	if o.indent {
		encoder.SetIndent("", "  ")
	}
	if errEncode := encoder.Encode(o.fields); errEncode != nil {
		return nil, errEncode
	}
	out := buf.Bytes()
	if !o.trailingNewline {
		out = bytes.TrimSuffix(out, []byte("\n"))
	}
	return out, nil
}

func isSealedValue(value any) bool {
	text, ok := value.(string)
	return ok && strings.HasPrefix(text, sealedPrefix)
}

// IsSealed reports whether data has at least one sealed field.
func IsSealed(data []byte) bool {
	if !bytes.Contains(data, []byte(sealedPrefix)) {
		return false
	}
	obj, ok := parseObject(data)
	if !ok {
		return false
	}
	for _, value := range obj.fields {
		if isSealedValue(value) {
			return true
		}
	}
	return false
}

// Decode returns data with every sealed field opened. Data without sealed
// fields is returned unchanged.
func Decode(data []byte) ([]byte, error) {
	if !bytes.Contains(data, []byte(sealedPrefix)) {
		return data, nil
	}
	obj, ok := parseObject(data)
	if !ok {
		return data, nil
	}
	s := current.Load()
	opened := false
	for name, value := range obj.fields {
		if !isSealedValue(value) {
			continue
		}
		if s == nil {
			return nil, ErrNoKey
		}
		raw, errOpen := s.codec.Open(value.(string), name)
		if errOpen != nil {
			return nil, fmt.Errorf("%w: field %q: %v", ErrUnreadable, name, errOpen)
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var plain any
		if errDecode := decoder.Decode(&plain); errDecode != nil {
			return nil, fmt.Errorf("field %q: decode opened value: %w", name, errDecode)
		}
		obj.fields[name] = plain
		opened = true
	}
	if !opened {
		return data, nil
	}
	return obj.marshal()
}

// Encode returns the stored form of plain. With encryption on, non-empty
// secret fields are sealed and the rest is kept. With it off, sealed fields
// are opened and plain input is returned unchanged.
func Encode(plain []byte) ([]byte, error) {
	s := current.Load()
	if s == nil || !s.encrypt {
		return Decode(plain)
	}
	obj, ok := parseObject(plain)
	if !ok {
		return plain, nil
	}
	sealedAny := false
	for name, value := range obj.fields {
		if !shouldSeal(name, value) {
			continue
		}
		raw, errMarshal := json.Marshal(value)
		if errMarshal != nil {
			return nil, fmt.Errorf("field %q: %w", name, errMarshal)
		}
		sealed, errSeal := s.codec.Seal(raw, name)
		if errSeal != nil {
			return nil, fmt.Errorf("field %q: %w", name, errSeal)
		}
		obj.fields[name] = sealed
		sealedAny = true
	}
	if !sealedAny {
		return plain, nil
	}
	return obj.marshal()
}

// isCurrent reports whether raw is stored the way Encode would store it now:
// with encryption on, no secret field is left plain; with it off, no field is
// sealed.
func isCurrent(raw []byte) bool {
	if !Encrypting() {
		return !IsSealed(raw)
	}
	obj, ok := parseObject(raw)
	if !ok {
		return true
	}
	for name, value := range obj.fields {
		if shouldSeal(name, value) {
			return false
		}
	}
	return true
}

// ReadFile reads path and returns its contents with sealed fields opened.
func ReadFile(path string) ([]byte, error) {
	data, _, err := ReadFileCurrent(path)
	return data, err
}

// ReadFileCurrent reads path, returns its contents with sealed fields opened,
// and reports whether the file is already stored in the current mode. Callers
// that skip unchanged writes use the flag to still rewrite a file that needs
// sealing or opening.
func ReadFileCurrent(path string) ([]byte, bool, error) {
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return nil, false, errRead
	}
	plain, errDecode := Decode(raw)
	if errDecode != nil {
		return nil, false, fmt.Errorf("%s: %w", filepath.Base(path), errDecode)
	}
	return plain, isCurrent(raw), nil
}

// WriteFile stores plain at path, with secret fields sealed when encryption is
// on. Like os.WriteFile, it overwrites an existing file in place, so file
// watchers see a write event and no remove.
func WriteFile(path string, plain []byte, perm os.FileMode) error {
	data, errEncode := Encode(plain)
	if errEncode != nil {
		return errEncode
	}
	return os.WriteFile(path, data, perm)
}

// WriteFileAtomic stores plain at path like WriteFile, but writes a temporary
// file in the same directory and renames it into place, so a crash never
// leaves a half-written credential.
func WriteFileAtomic(path string, plain []byte, perm os.FileMode) error {
	data, errEncode := Encode(plain)
	if errEncode != nil {
		return errEncode
	}
	dir := filepath.Dir(path)
	// The temporary name does not end in .json, so auth watchers ignore it.
	tmp, errCreate := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if errCreate != nil {
		return errCreate
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, errWrite := tmp.Write(data); errWrite != nil {
		_ = tmp.Close()
		cleanup()
		return errWrite
	}
	if errSync := tmp.Sync(); errSync != nil {
		_ = tmp.Close()
		cleanup()
		return errSync
	}
	if errClose := tmp.Close(); errClose != nil {
		cleanup()
		return errClose
	}
	if errChmod := os.Chmod(tmpName, perm); errChmod != nil {
		cleanup()
		return errChmod
	}
	if errRename := os.Rename(tmpName, path); errRename != nil {
		cleanup()
		return errRename
	}
	return nil
}

// WriteJSON encodes v the way json.Encoder does (HTML escaped, trailing
// newline), indented by two spaces when indent is true, and stores it with
// WriteFile.
func WriteJSON(path string, v any, indent bool) error {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	if indent {
		encoder.SetIndent("", "  ")
	}
	if errEncode := encoder.Encode(v); errEncode != nil {
		return errEncode
	}
	return WriteFile(path, buf.Bytes(), 0o600)
}
