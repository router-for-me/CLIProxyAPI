package codexintegration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

const journalVersion = 1

// Journal records enough state to audit and safely undo lifecycle writes.
type Journal struct {
	Version              int       `json:"version"`
	UpdatedAt            time.Time `json:"updated_at"`
	ConfigExisted        bool      `json:"config_existed"`
	OriginalConfigHash   string    `json:"original_config_hash,omitempty"`
	ManagedConfigHash    string    `json:"managed_config_hash"`
	OriginalConfigMode   uint32    `json:"original_config_mode,omitempty"`
	ConfigBackupFile     string    `json:"config_backup_file,omitempty"`
	CatalogExisted       bool      `json:"catalog_existed"`
	OriginalCatalogHash  string    `json:"original_catalog_hash,omitempty"`
	CatalogBackupFile    string    `json:"catalog_backup_file,omitempty"`
	ManagedCatalogHash   string    `json:"managed_catalog_hash"`
	CatalogRevision      string    `json:"catalog_revision"`
	CatalogSourceVersion uint64    `json:"catalog_source_revision"`
	MappingRevision      string    `json:"mapping_revision"`
}

func readJournal(path string) (Journal, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Journal{}, false, nil
	}
	if err != nil {
		return Journal{}, false, fmt.Errorf("read Codex integration journal: %w", err)
	}
	var journal Journal
	if err = json.Unmarshal(data, &journal); err != nil {
		return Journal{}, false, fmt.Errorf("decode Codex integration journal: %w", err)
	}
	if journal.Version != journalVersion {
		return Journal{}, false, fmt.Errorf("unsupported Codex integration journal version %d", journal.Version)
	}
	return journal, true, nil
}

func marshalJournal(journal Journal) ([]byte, error) {
	data, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode Codex integration journal: %w", err)
	}
	return append(data, '\n'), nil
}

func contentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
