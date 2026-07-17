package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

var replaceAuthMutationFile = publishAuthMutationFile

// ValidateSource checks that an already-persisted auth snapshot still matches disk.
func (s *FileTokenStore) ValidateSource(ctx context.Context, auth *cliproxyauth.Auth) error {
	if auth == nil || auth.Metadata == nil {
		return nil
	}
	pathHint := ""
	if auth.Attributes != nil {
		pathHint = strings.TrimSpace(auth.Attributes[cliproxyauth.AttributePath])
		if pathHint == "" {
			pathHint = strings.TrimSpace(auth.Attributes[cliproxyauth.AttributeSource])
		}
	}
	if pathHint == "" && strings.TrimSpace(auth.FileName) == "" {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	path, err := s.resolveAuthPath(auth)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	currentRaw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("auth filestore: read validation source: %w", err)
	}
	currentMetadata, err := decodeMutationMetadata(currentRaw)
	if err != nil {
		return fmt.Errorf("auth filestore: decode validation source: %w", err)
	}
	expected := comparableMutationMetadata(auth.Metadata, auth.Disabled)
	if !reflect.DeepEqual(comparableMutationMetadata(currentMetadata, false), expected) {
		return cliproxyauth.ErrAuthSourceConflict
	}
	return nil
}

// PersistMutation conditionally replaces one file-backed auth snapshot.
// The destination is published only after a complete same-directory temp write.
func (s *FileTokenStore) PersistMutation(ctx context.Context, before, after *cliproxyauth.Auth) (resultPath string, resultErr error) {
	if before == nil || after == nil || strings.TrimSpace(before.ID) == "" || before.ID != after.ID {
		return "", fmt.Errorf("auth filestore: invalid conditional mutation")
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}
	path, err := s.resolveAuthPath(after)
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", fmt.Errorf("auth filestore: missing file path attribute for %s", after.ID)
	}
	if before.Metadata == nil || after.Metadata == nil {
		return "", cliproxyauth.ErrPriorityMutationUnsupported
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	currentRaw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("auth filestore: read current mutation source: %w", err)
	}
	currentMetadata, err := decodeMutationMetadata(currentRaw)
	if err != nil {
		return "", fmt.Errorf("auth filestore: decode current mutation source: %w", err)
	}
	preservedMetadata, err := decodePreservedMutationMetadata(currentRaw)
	if err != nil {
		return "", fmt.Errorf("auth filestore: preserve current mutation source: %w", err)
	}
	beforeMetadata := comparableMutationMetadata(before.Metadata, before.Disabled)
	if !reflect.DeepEqual(comparableMutationMetadata(currentMetadata, false), beforeMetadata) {
		return "", cliproxyauth.ErrAuthSourceConflict
	}

	afterMetadata := mergeMutationMetadata(preservedMetadata, before.Metadata, after.Metadata)
	_, disabledPresent := currentMetadata["disabled"]
	if disabledPresent || after.Disabled {
		afterMetadata["disabled"] = after.Disabled
	} else {
		delete(afterMetadata, "disabled")
	}
	raw, err := json.Marshal(afterMetadata)
	if err != nil {
		return "", fmt.Errorf("auth filestore: marshal conditional mutation: %w", err)
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("auth filestore: create mutation dir: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".cliproxy-auth-mutation-*")
	if err != nil {
		return "", fmt.Errorf("auth filestore: create mutation temp: %w", err)
	}
	tempPath := temp.Name()
	removeTemp := true
	tempClosed := false
	defer func() {
		if !tempClosed {
			if errClose := temp.Close(); errClose != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("auth filestore: close mutation temp during cleanup: %w", errClose))
			}
		}
		if removeTemp {
			if errRemove := os.Remove(tempPath); errRemove != nil && !os.IsNotExist(errRemove) {
				resultErr = errors.Join(resultErr, fmt.Errorf("auth filestore: remove mutation temp during cleanup: %w", errRemove))
			}
		}
	}()
	if err = temp.Chmod(0o600); err != nil {
		return "", fmt.Errorf("auth filestore: chmod mutation temp: %w", err)
	}
	if _, err = temp.Write(raw); err != nil {
		return "", fmt.Errorf("auth filestore: write mutation temp: %w", err)
	}
	if err = temp.Sync(); err != nil {
		return "", fmt.Errorf("auth filestore: sync mutation temp: %w", err)
	}
	if err = temp.Close(); err != nil {
		return "", fmt.Errorf("auth filestore: close mutation temp: %w", err)
	}
	tempClosed = true
	latestRaw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("auth filestore: re-read mutation source: %w", err)
	}
	if !reflect.DeepEqual(latestRaw, currentRaw) {
		return "", cliproxyauth.ErrAuthSourceConflict
	}
	if err = replaceAuthMutationFile(tempPath, path, currentRaw); err != nil {
		return "", fmt.Errorf("auth filestore: publish conditional mutation: %w", err)
	}

	if after.Attributes == nil {
		after.Attributes = make(map[string]string)
	}
	after.Attributes[cliproxyauth.AttributePath] = path
	after.Attributes[cliproxyauth.AttributeSource] = path
	after.Attributes[cliproxyauth.AttributeSourceBackend] = cliproxyauth.AuthSourceFile
	return path, nil
}

func publishAuthMutationFile(tempPath, destinationPath string, expectedRaw []byte) (resultErr error) {
	dir := filepath.Dir(destinationPath)
	backup, err := os.CreateTemp(dir, ".cliproxy-auth-previous-*")
	if err != nil {
		return fmt.Errorf("create mutation backup path: %w", err)
	}
	backupPath := backup.Name()
	if err = backup.Close(); err != nil {
		errClose := fmt.Errorf("close mutation backup path: %w", err)
		if errRemove := os.Remove(backupPath); errRemove != nil && !os.IsNotExist(errRemove) {
			return errors.Join(errClose, fmt.Errorf("remove mutation backup after close failure: %w", errRemove))
		}
		return errClose
	}
	if err = os.Remove(backupPath); err != nil {
		return fmt.Errorf("prepare mutation backup path: %w", err)
	}
	cleanupBackup := true
	defer func() {
		if cleanupBackup {
			if errRemove := os.Remove(backupPath); errRemove != nil && !os.IsNotExist(errRemove) {
				resultErr = errors.Join(resultErr, fmt.Errorf("remove mutation backup during cleanup: %w", errRemove))
			}
		}
	}()

	if err = os.Rename(destinationPath, backupPath); err != nil {
		return fmt.Errorf("displace mutation source: %w", err)
	}
	displacedRaw, err := os.ReadFile(backupPath)
	if err != nil {
		if errRestore := restoreDisplacedAuthMutationFile(backupPath, destinationPath); errRestore != nil {
			cleanupBackup = false
			return fmt.Errorf("read displaced mutation source: %w (restore failed: %v)", err, errRestore)
		}
		cleanupBackup = false
		return fmt.Errorf("read displaced mutation source: %w", err)
	}
	if !reflect.DeepEqual(displacedRaw, expectedRaw) {
		if errRestore := restoreDisplacedAuthMutationFile(backupPath, destinationPath); errRestore != nil {
			cleanupBackup = false
			return fmt.Errorf("%w: restore displaced source: %v", cliproxyauth.ErrAuthSourceConflict, errRestore)
		}
		cleanupBackup = false
		return cliproxyauth.ErrAuthSourceConflict
	}

	if err = os.Link(tempPath, destinationPath); err != nil {
		errRestore := restoreDisplacedAuthMutationFile(backupPath, destinationPath)
		cleanupBackup = false
		if os.IsExist(err) {
			if errRestore != nil {
				return fmt.Errorf("%w: restore displaced source: %v", cliproxyauth.ErrAuthSourceConflict, errRestore)
			}
			return cliproxyauth.ErrAuthSourceConflict
		}
		if errRestore != nil {
			return fmt.Errorf("publish mutation source: %w (restore failed: %v)", err, errRestore)
		}
		return fmt.Errorf("publish mutation source: %w", err)
	}
	if err = os.Remove(tempPath); err != nil {
		if errRestore := rollbackPublishedAuthMutationFile(tempPath, backupPath, destinationPath); errRestore != nil {
			cleanupBackup = false
			return fmt.Errorf("remove mutation temp link: %w (rollback failed: %v)", err, errRestore)
		}
		cleanupBackup = false
		return fmt.Errorf("remove mutation temp link: %w", err)
	}
	if err = os.Remove(backupPath); err != nil {
		if errRestore := rollbackPublishedAuthMutationFile(destinationPath, backupPath, destinationPath); errRestore != nil {
			cleanupBackup = false
			return fmt.Errorf("remove mutation backup: %w (rollback failed: %v)", err, errRestore)
		}
		cleanupBackup = false
		return fmt.Errorf("remove mutation backup: %w", err)
	}
	cleanupBackup = false
	return nil
}

func restoreDisplacedAuthMutationFile(backupPath, destinationPath string) error {
	if _, err := os.Lstat(destinationPath); err == nil {
		return os.Remove(backupPath)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Link(backupPath, destinationPath); err != nil {
		if os.IsExist(err) {
			return os.Remove(backupPath)
		}
		return err
	}
	return os.Remove(backupPath)
}

func rollbackPublishedAuthMutationFile(publishedPath, backupPath, destinationPath string) error {
	publishedInfo, errPublished := os.Stat(publishedPath)
	destinationInfo, errDestination := os.Stat(destinationPath)
	if errPublished == nil && errDestination == nil && os.SameFile(publishedInfo, destinationInfo) {
		if errRemove := os.Remove(destinationPath); errRemove != nil {
			return errRemove
		}
	}
	return restoreDisplacedAuthMutationFile(backupPath, destinationPath)
}

func decodeMutationMetadata(raw []byte) (map[string]any, error) {
	var metadata map[string]any
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return nil, err
	}
	if metadata == nil {
		metadata = make(map[string]any)
	}
	return metadata, nil
}

func decodePreservedMutationMetadata(raw []byte) (map[string]any, error) {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var metadata map[string]any
	if err := decoder.Decode(&metadata); err != nil {
		return nil, err
	}
	if metadata == nil {
		metadata = make(map[string]any)
	}
	return metadata, nil
}

func mergeMutationMetadata(current, before, after map[string]any) map[string]any {
	merged := cloneMutationMetadata(current)
	for key := range before {
		if _, present := after[key]; !present {
			delete(merged, key)
		}
	}
	for key, value := range after {
		beforeValue, present := before[key]
		if !present || !reflect.DeepEqual(beforeValue, value) {
			merged[key] = value
		}
	}
	return merged
}

func comparableMutationMetadata(metadata map[string]any, disabled bool) map[string]any {
	comparable := cloneMutationMetadata(metadata)
	if _, present := comparable["disabled"]; !present {
		comparable["disabled"] = disabled
	}
	return comparable
}

func cloneMutationMetadata(metadata map[string]any) map[string]any {
	clone := make(map[string]any, len(metadata)+1)
	for key, value := range metadata {
		clone[key] = value
	}
	return clone
}
