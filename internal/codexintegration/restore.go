package codexintegration

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
)

type restorePlan struct {
	changed        bool
	configData     []byte
	configMode     os.FileMode
	writeConfig    bool
	catalogExists  bool
	restoreCatalog bool
	journal        Journal
	journalExists  bool
}

// Restore removes only integration-owned state and preserves subsequent user edits.
func (l *Lifecycle) Restore(apply bool) (OperationResult, error) {
	plan, err := l.prepareRestore()
	if err != nil {
		return OperationResult{}, err
	}
	result := OperationResult{Changed: plan.changed, RestartRequired: false}
	if !apply || !plan.changed {
		return result, nil
	}
	if err = l.ensureWritePaths(); err != nil {
		return OperationResult{}, err
	}
	release, err := acquireLifecycleLock(l.Paths.LockFile)
	if err != nil {
		return OperationResult{}, err
	}
	defer release()
	plan, err = l.prepareRestore()
	if err != nil {
		return OperationResult{}, err
	}
	if !plan.changed {
		return OperationResult{}, nil
	}
	if err = l.applyRestore(plan); err != nil {
		return OperationResult{}, err
	}
	result.Applied = true
	result.RestartRequired = true
	return result, nil
}

func (l *Lifecycle) applyRestore(plan restorePlan) (err error) {
	configSnapshot, err := snapshotRegularFile(l.Paths.ConfigFile, 0o600)
	if err != nil {
		return err
	}
	catalogSnapshot, err := snapshotRegularFile(l.Paths.CatalogFile, 0o600)
	if err != nil {
		return err
	}
	journalSnapshot, err := snapshotRegularFile(l.Paths.JournalFile, 0o600)
	if err != nil {
		return err
	}
	var configMutated, catalogMutated bool
	var catalogBackup, journalBackup string
	defer func() {
		if err == nil {
			return
		}
		rollbackErrors := make([]error, 0, 3)
		if journalBackup != "" {
			rollbackErrors = append(rollbackErrors, restoreMovedAside(l.Paths.JournalFile, journalBackup))
		} else if !journalSnapshot.exists {
			rollbackErrors = append(rollbackErrors, restoreFileSnapshot(l.Paths.JournalFile, journalSnapshot))
		}
		if catalogBackup != "" {
			rollbackErrors = append(rollbackErrors, restoreMovedAside(l.Paths.CatalogFile, catalogBackup))
		} else if catalogMutated {
			rollbackErrors = append(rollbackErrors, restoreFileSnapshot(l.Paths.CatalogFile, catalogSnapshot))
		}
		if configMutated {
			rollbackErrors = append(rollbackErrors, restoreFileSnapshot(l.Paths.ConfigFile, configSnapshot))
		}
		err = withRollbackError(err, rollbackErrors...)
	}()

	if plan.writeConfig {
		configMutated = true
		if err = atomicWriteFile(l.Paths.ConfigFile, plan.configData, plan.configMode); err != nil {
			return err
		}
	}
	if plan.restoreCatalog {
		if plan.journalExists && plan.journal.CatalogExisted && plan.journal.CatalogBackupFile != "" {
			backup, readErr := l.readBackup(plan.journal.CatalogBackupFile)
			if readErr != nil {
				return fmt.Errorf("read original catalog backup: %w", readErr)
			}
			catalogMutated = true
			if err = atomicWriteFile(l.Paths.CatalogFile, backup, 0o600); err != nil {
				return err
			}
		} else if catalogBackup, err = moveAside(l.Paths.CatalogFile, l.Paths.BackupDir, "catalog.restored"); err != nil {
			return err
		}
	}
	if plan.journalExists {
		if journalBackup, err = moveAside(l.Paths.JournalFile, l.Paths.BackupDir, "journal.restored.json"); err != nil {
			return err
		}
	}
	return nil
}

func (l *Lifecycle) prepareRestore() (restorePlan, error) {
	configData, configMode, configExists, err := readRegularFile(l.Paths.ConfigFile, 0o600)
	if err != nil {
		return restorePlan{}, err
	}
	journal, journalExists, err := readJournal(l.Paths.JournalFile)
	if err != nil {
		return restorePlan{}, err
	}
	_, _, catalogExists, err := readRegularFile(l.Paths.CatalogFile, 0o600)
	if err != nil {
		return restorePlan{}, err
	}
	newConfig := configData
	writeConfig := false
	managedConfigFound := false
	if configExists {
		normalized := bytes.ReplaceAll(configData, []byte("\r\n"), []byte("\n"))
		stripped, found, removeErr := removeManagedBlock(string(normalized))
		if removeErr != nil {
			return restorePlan{}, removeErr
		}
		if found {
			managedConfigFound = true
			writeConfig = true
			if journalExists && contentHash(configData) == journal.ManagedConfigHash && journal.ConfigExisted && journal.ConfigBackupFile != "" {
				newConfig, err = l.readBackup(journal.ConfigBackupFile)
				if err != nil {
					return restorePlan{}, fmt.Errorf("read original config backup: %w", err)
				}
				if journal.OriginalConfigMode != 0 {
					configMode = os.FileMode(journal.OriginalConfigMode)
				}
			} else {
				newConfig = []byte(stripped)
				if bytes.Contains(configData, []byte("\r\n")) {
					newConfig = bytes.ReplaceAll(newConfig, []byte("\n"), []byte("\r\n"))
				}
			}
			if err = validateTOML(newConfig); err != nil {
				return restorePlan{}, err
			}
		}
	}
	restoreCatalog := (journalExists && (catalogExists || journal.CatalogExisted)) || (managedConfigFound && catalogExists)
	changed := writeConfig || restoreCatalog || journalExists
	return restorePlan{
		changed: changed, configData: newConfig, configMode: configMode, writeConfig: writeConfig,
		catalogExists: catalogExists, restoreCatalog: restoreCatalog, journal: journal, journalExists: journalExists,
	}, nil
}

func (l *Lifecycle) readBackup(path string) ([]byte, error) {
	cleanBackupDir := filepath.Clean(l.Paths.BackupDir)
	cleanPath := filepath.Clean(path)
	relative, err := filepath.Rel(cleanBackupDir, cleanPath)
	if err != nil || relative == "." || relative == ".." || filepath.IsAbs(relative) || len(relative) >= 3 && relative[:3] == ".."+string(filepath.Separator) {
		return nil, fmt.Errorf("backup path %q is outside the Codex integration backup directory", path)
	}
	data, _, exists, err := readRegularFile(cleanPath, 0o600)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("backup file %q is missing", cleanPath)
	}
	return data, nil
}
