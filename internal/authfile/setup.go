package authfile

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	log "github.com/sirupsen/logrus"
)

// Setup configures auth file encryption for dir.
//
// With enabled, secret fields of plain auth files are sealed. If dir already
// holds sealed files, an existing key is required: no new key is created, and
// startup fails when any sealed file cannot be opened, so credentials are never
// split across two keys.
//
// Without enabled, nothing is ever decrypted back to plain text and no key is
// looked up. Sealed files cannot be read, so their accounts are skipped until
// the user logs in again, which replaces the file with a plain one.
func Setup(enabled bool, keyFile, dir string) error {
	sealedFiles := countSealedFiles(dir)
	if !enabled {
		Configure(nil, false)
		if sealedFiles > 0 {
			log.Warnf("auth file encryption is off; %d encrypted auth file(s) in %s are skipped until you log in to those accounts again", sealedFiles, dir)
		}
		return nil
	}

	key, source, errKey := LoadKey(keyFile, sealedFiles == 0)
	if errKey != nil {
		if sealedFiles > 0 {
			return fmt.Errorf("auth file encryption: %d auth file(s) are already encrypted, but their key is not available (no new key is created): %w", sealedFiles, errKey)
		}
		return fmt.Errorf("auth file encryption: %w", errKey)
	}
	codec, errCodec := NewAESGCMCodec(key)
	if errCodec != nil {
		return errCodec
	}
	Configure(codec, true)
	log.Infof("auth file encryption on (AES-256-GCM, key %s from %s)", KeyID(key), source)
	if errConvert := convertDir(dir); errConvert != nil {
		Configure(nil, false)
		return errConvert
	}
	return nil
}

// walkAuthFiles calls fn for each .json file under dir, the same set the file
// token store loads.
func walkAuthFiles(dir string, fn func(path string) error) error {
	if strings.TrimSpace(dir) == "" {
		return nil
	}
	errWalk := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, errEntry error) error {
		if errEntry != nil {
			return errEntry
		}
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			return nil
		}
		return fn(path)
	})
	if os.IsNotExist(errWalk) {
		return nil
	}
	return errWalk
}

func countSealedFiles(dir string) int {
	count := 0
	_ = walkAuthFiles(dir, func(path string) error {
		if raw, errRead := os.ReadFile(path); errRead == nil && IsSealed(raw) {
			count++
		}
		return nil
	})
	return count
}

// convertDir seals the secret fields of each plain auth file. Only JSON
// objects with a "type" field count as auth files; anything else in the
// directory is left alone. A sealed file that cannot be opened stops the
// conversion with an error, because it means the configured key is wrong.
func convertDir(dir string) error {
	converted := 0
	var unreadable []string
	errWalk := walkAuthFiles(dir, func(path string) error {
		plain, upToDate, errRead := ReadFileCurrent(path)
		if errRead != nil {
			if raw, errRaw := os.ReadFile(path); errRaw == nil && IsSealed(raw) {
				unreadable = append(unreadable, fmt.Sprintf("%s (%v)", filepath.Base(path), errRead))
				return nil
			}
			log.Warnf("auth file encryption: skip %s: %v", filepath.Base(path), errRead)
			return nil
		}
		if upToDate || !isAuthJSON(plain) {
			return nil
		}
		if errWrite := WriteFileAtomic(path, plain, 0o600); errWrite != nil {
			log.Warnf("auth file encryption: rewrite %s: %v", filepath.Base(path), errWrite)
			return nil
		}
		converted++
		return nil
	})
	if errWalk != nil {
		return errWalk
	}
	if len(unreadable) > 0 {
		return fmt.Errorf("auth file encryption: %d encrypted auth file(s) cannot be opened with the configured key: %s", len(unreadable), strings.Join(unreadable, "; "))
	}
	if converted > 0 {
		log.Infof("auth file encryption: encrypted secrets in %d existing file(s)", converted)
	}
	return nil
}

func isAuthJSON(data []byte) bool {
	var object map[string]any
	if errUnmarshal := json.Unmarshal(data, &object); errUnmarshal != nil {
		return false
	}
	provider, ok := object["type"].(string)
	return ok && strings.TrimSpace(provider) != ""
}
