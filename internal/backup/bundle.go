package backup

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// backupKeyPrefix is the deterministic object key prefix for backup snapshots.
// Snapshot keys look like: nixllm_backup_2026-08-16T02_30_00.123Z.json
const backupKeyPrefix = "nixllm_backup"

// snapshotKey returns a deterministic, sortable object key for a backup taken
// at time t. Millisecond precision is included so two backups created within
// the same second (e.g. a double-clicked "Backup now") never collide.
func snapshotKey(t time.Time) string {
	return backupKeyPrefix + "_" + t.UTC().Format("2006-01-02T15_04_05.000Z") + ".json"
}

// WriteBundle serializes a BackupBundle v2 to w as JSON. HTML-escaping is
// disabled so embedded credentials survive a round-trip byte-identically.
func WriteBundle(w io.Writer, b store.BackupBundle) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(b); err != nil {
		return fmt.Errorf("backup bundle: encode: %w", err)
	}
	return nil
}

// ReadBundle parses a BackupBundle from r.
func ReadBundle(r io.Reader) (store.BackupBundle, error) {
	var b store.BackupBundle
	if err := json.NewDecoder(r).Decode(&b); err != nil {
		return b, fmt.Errorf("backup bundle: decode: %w", err)
	}
	return b, nil
}
