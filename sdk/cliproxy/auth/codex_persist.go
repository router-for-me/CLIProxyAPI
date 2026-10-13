package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
)

// IsCodexAuthRecord reports whether auth represents a Codex credential record.
func IsCodexAuthRecord(auth *Auth) bool {
	if auth == nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return true
	}
	if auth.Metadata != nil {
		if t, ok := auth.Metadata["type"].(string); ok && strings.EqualFold(strings.TrimSpace(t), "codex") {
			return true
		}
	}
	if _, ok := auth.Storage.(*codex.CodexTokenStorage); ok {
		return true
	}
	return false
}

// PersistCodexAuthFile persists a Codex auth record to path using inter-process file locking,
// read-before-write merging, and scoped refresh patches when present.
// It returns handled=false when auth is not a Codex credential record.
func PersistCodexAuthFile(ctx context.Context, path string, auth *Auth) (bool, error) {
	if !IsCodexAuthRecord(auth) {
		return false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	type metadataSetter interface {
		SetMetadata(map[string]any)
	}

	switch {
	case auth.CodexRefreshPatch != nil:
		errRefresh := codex.WithFileLock(path, func() error {
			existing, errRead := os.ReadFile(path)
			if errRead != nil {
				if os.IsNotExist(errRead) {
					if auth.Metadata == nil {
						auth.Metadata = make(map[string]any)
					}
					auth.Metadata["disabled"] = auth.Disabled
					NormalizeCredentialMetadata(auth.Metadata)
					return codex.WriteJSONFileAtomic(path, auth.Metadata)
				}
				return fmt.Errorf("auth filestore: read refresh source failed: %w", errRead)
			}
			var diskMap map[string]any
			if errUnmarshal := json.Unmarshal(existing, &diskMap); errUnmarshal != nil || diskMap == nil {
				return fmt.Errorf("auth filestore: invalid refresh source JSON")
			}
			merged := auth.CodexRefreshPatch.Apply(diskMap)
			if mode, ok := ctx.Value(authUpdateModeContextKey{}).(updateAuthMode); ok && mode == updateModeReplace {
				// Update owns intentional configuration edits, even when a previous
				// rotation is pending. Keep the patch's partition/version arbitration
				// while using the existing configuration-save merge for other fields.
				merged = codex.MergeCodexCredentialsFromDisk(merged, auth.Metadata)
				merged["disabled"] = auth.Disabled
			}
			NormalizeCredentialMetadata(merged)
			if errWrite := codex.WriteJSONFileAtomic(path, merged); errWrite != nil {
				return errWrite
			}
			auth.Metadata = merged
			return nil
		})
		if errRefresh != nil {
			return true, errRefresh
		}
		auth.CodexRefreshPatch = nil
		auth.Storage = nil
		if disabled, ok := auth.Metadata["disabled"].(bool); ok {
			auth.Disabled = disabled
		}
		return true, nil

	case auth.Storage != nil:
		if auth.Metadata == nil {
			auth.Metadata = make(map[string]any)
		}
		auth.Metadata["disabled"] = auth.Disabled
		if mode, ok := ctx.Value(authUpdateModeContextKey{}).(updateAuthMode); ok && mode == updateModeReplace && !HasAuthCreationIntent(ctx) {
			// Storage snapshots carry login-time tokens and configuration. An
			// explicit Update owns configuration edits and deletions, while the
			// locked disk record remains authoritative for credential partitions.
			replaced, errReplace := persistCodexConfigurationReplace(path, auth)
			if errReplace != nil || replaced {
				return true, errReplace
			}
		}
		if storage, ok := auth.Storage.(*codex.CodexTokenStorage); ok && storage != nil {
			clonedStorage := *storage
			clonedStorage.ReplaceCredentials = HasAuthCreationIntent(ctx)
			auth.Storage = &clonedStorage
		}
		if setter, ok := auth.Storage.(metadataSetter); ok {
			setter.SetMetadata(auth.Metadata)
		}
		if errSave := auth.Storage.SaveTokenToFile(path); errSave != nil {
			return true, errSave
		}
		if persistedBytes, errReadPersisted := os.ReadFile(path); errReadPersisted == nil && len(persistedBytes) > 0 {
			var diskMap map[string]any
			if errUnmarshal := json.Unmarshal(persistedBytes, &diskMap); errUnmarshal == nil && len(diskMap) > 0 {
				auth.Metadata = codex.MergeCodexCredentialsFromDisk(diskMap, auth.Metadata)
				NormalizeCredentialMetadata(auth.Metadata)
			}
		}
		return true, nil

	case auth.Metadata != nil:
		auth.Metadata["disabled"] = auth.Disabled
		errLock := codex.WithFileLock(path, func() error {
			existing, errRead := os.ReadFile(path)
			if errRead == nil && len(existing) > 0 {
				var diskMap map[string]any
				if errUnmarshal := json.Unmarshal(existing, &diskMap); errUnmarshal == nil && len(diskMap) > 0 {
					if !HasAuthCreationIntent(ctx) {
						auth.Metadata = codex.MergeCodexCredentialsFromDisk(diskMap, auth.Metadata)
					}
					auth.Metadata["disabled"] = auth.Disabled
					NormalizeCredentialMetadata(auth.Metadata)
					if raw, errMarshal := json.Marshal(auth.Metadata); errMarshal == nil && string(existing) == string(raw) {
						return nil
					}
				}
			} else if errRead != nil && !os.IsNotExist(errRead) {
				return fmt.Errorf("auth filestore: read existing failed: %w", errRead)
			}
			return codex.WriteJSONFileAtomic(path, auth.Metadata)
		})
		if errLock != nil {
			return true, errLock
		}
		return true, nil

	default:
		return false, nil
	}
}

// persistCodexConfigurationReplace applies an explicit configuration update to
// an existing record. It returns replaced=false when no record exists, leaving
// first writes to the storage save path.
func persistCodexConfigurationReplace(path string, auth *Auth) (bool, error) {
	replaced := false
	errLock := codex.WithFileLock(path, func() error {
		existing, errRead := os.ReadFile(path)
		if errRead != nil {
			if os.IsNotExist(errRead) {
				return nil
			}
			return fmt.Errorf("auth filestore: read existing failed: %w", errRead)
		}
		var diskMap map[string]any
		if errUnmarshal := json.Unmarshal(existing, &diskMap); errUnmarshal != nil || diskMap == nil {
			return fmt.Errorf("auth filestore: invalid existing credential JSON")
		}
		merged := codex.MergeCodexCredentialsFromDisk(diskMap, auth.Metadata)
		if _, hasType := merged["type"]; !hasType {
			// Storage-backed metadata omits the record type that storage saves add.
			merged["type"] = "codex"
		}
		merged["disabled"] = auth.Disabled
		NormalizeCredentialMetadata(merged)
		if errWrite := codex.WriteJSONFileAtomic(path, merged); errWrite != nil {
			return errWrite
		}
		auth.Metadata = merged
		replaced = true
		return nil
	})
	return replaced, errLock
}
