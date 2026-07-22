package codexintegration

import (
	"bytes"
	"fmt"
	"os"
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
	if plan.writeConfig {
		if err = atomicWriteFile(l.Paths.ConfigFile, plan.configData, plan.configMode); err != nil {
			return OperationResult{}, err
		}
	}
	if plan.restoreCatalog {
		if plan.journalExists && plan.journal.CatalogExisted && plan.journal.CatalogBackupFile != "" {
			backup, _, _, readErr := readRegularFile(plan.journal.CatalogBackupFile, 0o600)
			if readErr != nil {
				return OperationResult{}, fmt.Errorf("read original catalog backup: %w", readErr)
			}
			if err = atomicWriteFile(l.Paths.CatalogFile, backup, 0o600); err != nil {
				return OperationResult{}, err
			}
		} else if _, err = moveAside(l.Paths.CatalogFile, l.Paths.BackupDir, "catalog.restored"); err != nil {
			return OperationResult{}, err
		}
	}
	if plan.journalExists {
		if _, err = moveAside(l.Paths.JournalFile, l.Paths.BackupDir, "journal.restored.json"); err != nil {
			return OperationResult{}, err
		}
	}
	result.Applied = true
	result.RestartRequired = true
	return result, nil
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
				newConfig, _, _, err = readRegularFile(journal.ConfigBackupFile, 0o600)
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
