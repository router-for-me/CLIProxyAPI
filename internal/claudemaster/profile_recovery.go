package claudemaster

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	log "github.com/sirupsen/logrus"
)

// recoverStagedRefresh finishes an interrupted atomic credential save. Callers
// must hold the profile lock (and the store mutex when an active store exists).
// Never select a partial snapshot or discard a complete, potentially rotated token.
func recoverStagedRefresh(dir, authID, provider string) error {
	if err := ownedDirectory(dir, false, true); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return errors.New("cannot inspect interrupted credential refresh")
	}
	var stages []string
	for _, entry := range entries {
		if entry.Name() == authID {
			continue
		}
		if !strings.HasPrefix(entry.Name(), ".refresh-") {
			return errors.New("profile auth directory contains an unexpected file")
		}
		stages = append(stages, filepath.Join(dir, entry.Name()))
	}
	if len(stages) == 0 {
		return nil
	}
	currentPath := filepath.Join(dir, authID)
	current, err := readRefreshSnapshot(currentPath)
	if err != nil || !validRefreshSnapshot(current, provider) {
		return errors.New("interrupted refresh requires a valid selected credential; no staged token was removed")
	}
	currentAccount, currentAccountOK := refreshSnapshotAccountUUID(current)
	if !currentAccountOK {
		return errors.New("selected credential has ambiguous account identity; no staged token was removed")
	}
	var complete string
	var incomplete []string
	for _, path := range stages {
		snapshot, errRead := readRefreshSnapshot(path)
		if errors.Is(errRead, errIncompleteRefreshSnapshot) {
			incomplete = append(incomplete, path)
			continue
		}
		if errRead != nil || !validRefreshSnapshot(snapshot, provider) {
			return errors.New("interrupted refresh contains an unsafe snapshot; no staged token was removed")
		}
		account, accountOK := refreshSnapshotAccountUUID(snapshot)
		if !accountOK || currentAccount != "" && account != currentAccount {
			return errors.New("interrupted refresh does not match the selected account; no staged token was removed")
		}
		for _, key := range []string{"organization_uuid", "email"} {
			if identity, _ := current[key].(string); identity != "" && snapshot[key] != identity {
				return errors.New("interrupted refresh does not match the selected account; no staged token was removed")
			}
		}
		if complete != "" {
			return errors.New("interrupted refresh contains multiple complete snapshots; no staged token was removed")
		}
		complete = path
	}
	if complete != "" {
		// A complete snapshot may contain the only valid rotated refresh token.
		if err := os.Rename(complete, currentPath); err != nil {
			return errors.New("cannot recover the completed credential refresh")
		}
	}
	for _, path := range incomplete {
		// Only syntactically incomplete JSON is discarded, and only after a
		// valid selected credential was established above.
		if err := os.Remove(path); err != nil {
			return errors.New("cannot remove an incomplete credential refresh")
		}
	}
	if err := syncDirectory(dir); err != nil {
		return errors.New("credential refresh recovered but durability confirmation failed")
	}
	return nil
}

// Accept both supported metadata spellings, but refuse contradictory aliases
// instead of letting one spelling silently override a different account.
func refreshSnapshotAccountUUID(snapshot map[string]any) (string, bool) {
	account := ""
	for _, key := range []string{"account_uuid", "accountUuid"} {
		raw, exists := snapshot[key]
		if !exists || raw == nil {
			continue
		}
		value, ok := raw.(string)
		if !ok {
			return "", false
		}
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if account != "" && account != value {
			return "", false
		}
		account = value
	}
	return account, true
}

var errIncompleteRefreshSnapshot = errors.New("incomplete credential refresh snapshot")

func readRefreshSnapshot(path string) (map[string]any, error) {
	f, err := openPrivateFile(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if errClose := f.Close(); errClose != nil {
			log.Warn("claude-master could not close a private credential refresh snapshot")
		}
	}()
	info, err := f.Stat()
	if err != nil || info.Size() > 1024*1024 {
		return nil, errors.New("credential refresh snapshot is not bounded")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 1024*1024+1))
	if err != nil || len(raw) > 1024*1024 {
		return nil, errors.New("cannot read credential refresh snapshot")
	}
	if !json.Valid(raw) {
		return nil, errIncompleteRefreshSnapshot
	}
	var snapshot map[string]any
	if err := json.Unmarshal(raw, &snapshot); err != nil || snapshot == nil {
		return nil, errors.New("credential refresh snapshot is not an object")
	}
	// Ensure a recovered complete stage is durable before replacing the old file.
	if err := f.Sync(); err != nil {
		return nil, errors.New("cannot sync credential refresh snapshot")
	}
	return snapshot, nil
}

func validRefreshSnapshot(snapshot map[string]any, provider string) bool {
	if snapshot == nil || snapshot["type"] != provider {
		return false
	}
	for _, key := range []string{"access_token", "refresh_token"} {
		value, ok := snapshot[key].(string)
		if !ok || strings.TrimSpace(value) == "" {
			return false
		}
	}
	return true
}
