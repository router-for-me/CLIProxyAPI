package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"

	log "github.com/sirupsen/logrus"
	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"
)

// LoadConfig loads the configuration and applies startup file rewrites.
func LoadConfig(configFile string) (*Config, error) {
	return LoadConfigOptional(configFile, false)
}

// ComputeSourceHash returns the lowercase hex SHA-256 of the exact file bytes.
func ComputeSourceHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// PrepareConfigFileForUpload applies startup rewrites and verifies that no
// plaintext management secret would be uploaded. Prefer the variant returning
// the verified bytes to avoid racing another write.
func PrepareConfigFileForUpload(configFile string) error {
	_, err := PrepareConfigFileForUploadWithOptional(configFile, false)
	return err
}

// PrepareConfigFileForUploadWithOptional returns the verified bytes from the
// same locked transaction that performed startup rewrites. Upload these bytes
// directly, not a later read. A nil slice with no error means optional standby
// and must not be uploaded. A required rewrite on a read-only file is an error.
func PrepareConfigFileForUploadWithOptional(configFile string, optional bool) ([]byte, error) {
	_, snapshot, err := loadConfigFile(configFile, optional, true)
	return snapshot, err
}

// LoadConfigOptional loads the config or returns the optional standby defaults
// for missing files, directories, empty input and YAML decoding failures. A
// valid read-only config can use a process-local generated server identity.
func LoadConfigOptional(configFile string, optional bool) (*Config, error) {
	cfg, _, err := loadConfigFile(configFile, optional, false)
	return cfg, err
}

func loadConfigFile(configFile string, optional, forUpload bool) (cfg *Config, snapshot []byte, err error) {
	err = withConfigFile(configFile, configWriteIfPossible, func(file *lockedConfigFile) error {
		data := file.snapshot.data
		parsed, errParse := validateStartupConfig(data, optional)
		if errParse != nil {
			return errParse
		}
		cfg = parsed.cfg
		if len(parsed.docs) == 0 {
			return nil
		}
		generated := cfg.ServerID == ""
		if generated {
			cfg.ServerID = lookupEphemeralServerID(file.snapshot.info)
			if cfg.ServerID == "" {
				cfg.ServerID, errParse = newServerID()
				if errParse != nil {
					return errParse
				}
			}
		}
		prepared, errPrepare := prepareStartupConfig(data, parsed, forUpload)
		if errPrepare != nil {
			return errPrepare
		}
		committed, errWrite := file.replace(prepared)
		if errWrite != nil {
			if forUpload || file.writeError == nil || !isConfigReadOnly(errWrite) {
				return fmt.Errorf("persist startup config: %w", errWrite)
			}
			if generated && lookupEphemeralServerID(file.snapshot.info) == "" {
				rememberEphemeralServerID(file.snapshot.info, cfg.ServerID)
			}
			log.Debug("configuration is read-only; startup rewrites were not persisted")
			committed = file.snapshot
		}
		if !generated {
			forgetEphemeralServerID(file.snapshot.info)
		}
		if !forUpload {
			cfg.sanitizeStartupConfig()
		}
		cfg.SourceHash = committed.SourceHash()
		snapshot = committed.data
		return nil
	})
	if err != nil {
		if optional && !errors.Is(err, ErrConfigFileChanged) && (errors.Is(err, os.ErrNotExist) || errors.Is(err, ErrConfigFileDirectory)) {
			return standbyConfig(), nil, nil
		}
		return nil, nil, err
	}
	return cfg, snapshot, nil
}

// PrepareConfigBytesForUploadWithOptional validates and rewrites remote or template
// bytes without changing any file. A nil result and no error means optional standby.
func PrepareConfigBytesForUploadWithOptional(data []byte, optional bool) ([]byte, error) {
	parsed, errParse := validateStartupConfig(data, optional)
	if errParse != nil || len(parsed.docs) == 0 {
		return nil, errParse
	}
	if parsed.cfg.ServerID == "" {
		parsed.cfg.ServerID, errParse = newServerID()
		if errParse != nil {
			return nil, errParse
		}
	}
	return prepareStartupConfig(data, parsed, true)
}

// IsPreparedConfigFromSeed reports whether prepared differs from seed only by
// the automatic startup rewrites, including a generated ID and salted hash.
func IsPreparedConfigFromSeed(seed, prepared []byte) bool {
	original, errSeed := validateStartupConfig(seed, false)
	winner, errWinner := validateStartupConfig(prepared, false)
	if errSeed != nil || errWinner != nil || len(original.docs) == 0 || len(winner.docs) == 0 || winner.cfg.ServerID == "" {
		return false
	}
	id := original.cfg.ServerID
	if id == "" {
		id = winner.cfg.ServerID
	}
	var hashed string
	secret := original.cfg.RemoteManagement.SecretKey
	if secret != "" && !looksLikeBcrypt(secret) {
		hashed = winner.cfg.RemoteManagement.SecretKey
		cost, errCost := bcrypt.Cost([]byte(hashed))
		if errCost != nil || cost != bcrypt.DefaultCost || bcrypt.CompareHashAndPassword([]byte(hashed), []byte(secret)) != nil {
			return false
		}
	}
	rendered, errRender := renderStartupConfig(seed, original.docs, id, hashed, true)
	return errRender == nil && bytes.Equal(rendered, prepared) && verifyConfigUpload(original.docs, prepared) == nil
}

// VerifyConfigBytesForUpload verifies immutable bytes without rewriting them.
func VerifyConfigBytesForUpload(data []byte) error {
	parsed, errParse := validateStartupConfig(data, false)
	if errParse != nil {
		return errParse
	}
	return verifyConfigUpload(parsed.docs, data)
}

func prepareStartupConfig(data []byte, parsed *startupConfig, forUpload bool) ([]byte, error) {
	secret := parsed.cfg.RemoteManagement.SecretKey
	var hashed string
	if secret != "" && !looksLikeBcrypt(secret) {
		if errHash := parsed.hashManagementSecret(); errHash != nil {
			return nil, errHash
		}
		hashed = parsed.cfg.RemoteManagement.SecretKey
	}
	prepared, errPrepare := renderStartupConfig(data, parsed.docs, parsed.cfg.ServerID, hashed, true)
	if errPrepare != nil {
		return nil, errPrepare
	}
	if errSecret := verifyConfigUpload(parsed.docs, prepared); errSecret != nil {
		if forUpload {
			return nil, errSecret
		}
		// Do not erase the evidence identifying a retained plaintext value.
		// Otherwise a subsequent upload could mistake it for an unrelated value.
		prepared, errPrepare = renderStartupConfig(data, parsed.docs, parsed.cfg.ServerID, "", false)
		if errPrepare != nil {
			return nil, errPrepare
		}
	}
	return prepared, nil
}

var managementSecretPaths = [][]string{{"management", "secret-key"}, {"remote-management", "secret-key"}}

func renderStartupConfig(data []byte, docs []*yaml.Node, id, hashed string, clean bool) ([]byte, error) {
	doc := deepCopyNode(docs[0])
	root := doc.Content[0]
	previous := effectiveConfigValue(root, serverIDPath...)
	changed := previous == nil || previous.Value != id
	if changed {
		setConfigScalar(root, serverIDPath, id)
	}
	if hashed != "" {
		path := managementSecretPaths[0]
		if effectiveConfigValue(root, path...) == nil {
			path = managementSecretPaths[1]
		}
		setConfigScalar(root, path, hashed)
		changed = true
	}
	if clean {
		changed = removeConflictingConfigPaths(root) || changed
	}
	if !changed {
		return data, nil
	}
	updated, errEncode := encodeConfigDocument(doc, data, docs)
	if errEncode != nil {
		return nil, errEncode
	}
	closed, errParse := validateStartupConfig(updated, false)
	if errParse != nil {
		return nil, fmt.Errorf("validate rewritten config: %w", errParse)
	}
	if len(closed.docs) != len(docs) || closed.cfg.ServerID != id || (hashed != "" && closed.cfg.RemoteManagement.SecretKey != hashed) {
		return nil, errors.New("rewritten config does not contain the expected startup settings")
	}
	return updated, nil
}
