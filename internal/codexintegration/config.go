package codexintegration

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

const (
	ManagedConfigBegin = "# BEGIN CLIProxyAPI Codex Integration"
	ManagedConfigEnd   = "# END CLIProxyAPI Codex Integration"
	openCodexMarker    = "# Auto-injected by opencodex"
)

// OperationResult describes a lifecycle preview or applied transaction.
type OperationResult struct {
	Changed          bool
	Applied          bool
	RestartRequired  bool
	CatalogRevision  string
	SourceRevision   uint64
	MappingRevision  string
	ConfigBackupFile string
}

// Lifecycle manages the Codex config and model catalog as one transaction.
type Lifecycle struct {
	Config *config.Config
	Paths  Paths
}

type setupPlan struct {
	catalog       Catalog
	catalogData   []byte
	configData    []byte
	configMode    os.FileMode
	configOld     []byte
	configExists  bool
	catalogOld    []byte
	catalogExists bool
	journal       Journal
	journalExists bool
	changed       bool
}

// NewLifecycle validates configuration and resolves all managed paths without writing them.
func NewLifecycle(cfg *config.Config, explicitHome string) (*Lifecycle, error) {
	if cfg == nil {
		return nil, errors.New("Codex integration requires a non-nil config")
	}
	if err := cfg.NormalizeCodexIntegration(); err != nil {
		return nil, err
	}
	if !cfg.CodexIntegration.Enabled {
		return nil, errors.New("Codex integration is disabled")
	}
	paths, err := ResolvePaths(explicitHome, cfg.CodexIntegration)
	if err != nil {
		return nil, err
	}
	return &Lifecycle{Config: cfg, Paths: paths}, nil
}

// Setup compiles and previews or applies the Codex integration transaction.
func (l *Lifecycle) Setup(models []map[string]any, providers ModelProvidersFunc, apply, migrateOpenCodex bool) (OperationResult, error) {
	plan, err := l.prepareSetup(models, providers, migrateOpenCodex)
	if err != nil {
		return OperationResult{}, err
	}
	result := operationResultForPlan(plan)
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

	plan, err = l.prepareSetup(models, providers, migrateOpenCodex)
	if err != nil {
		return OperationResult{}, err
	}
	result = operationResultForPlan(plan)
	if !plan.changed {
		return result, nil
	}
	if err = l.applySetup(plan, &result); err != nil {
		return OperationResult{}, err
	}
	result.Applied = true
	result.RestartRequired = true
	return result, nil
}

// Sync refreshes an integration previously claimed by Setup.
func (l *Lifecycle) Sync(models []map[string]any, providers ModelProvidersFunc, apply bool) (OperationResult, error) {
	if _, exists, err := readJournal(l.Paths.JournalFile); err != nil {
		return OperationResult{}, err
	} else if !exists {
		return OperationResult{}, errors.New("Codex integration is not set up; run setup before sync")
	}
	return l.Setup(models, providers, apply, false)
}

func (l *Lifecycle) prepareSetup(models []map[string]any, providers ModelProvidersFunc, migrateOpenCodex bool) (setupPlan, error) {
	catalog, err := CompileCatalog(models, providers, l.Config.CodexIntegration)
	if err != nil {
		return setupPlan{}, err
	}
	catalogData, err := catalog.Marshal()
	if err != nil {
		return setupPlan{}, err
	}
	oldConfig, configMode, configExists, err := readRegularFile(l.Paths.ConfigFile, 0o600)
	if err != nil {
		return setupPlan{}, err
	}
	newConfig, err := injectManagedConfig(oldConfig, l.baseURL(), l.Paths.CatalogFile, migrateOpenCodex)
	if err != nil {
		return setupPlan{}, err
	}
	oldCatalog, _, catalogExists, err := readRegularFile(l.Paths.CatalogFile, 0o600)
	if err != nil {
		return setupPlan{}, err
	}
	journal, journalExists, err := readJournal(l.Paths.JournalFile)
	if err != nil {
		return setupPlan{}, err
	}
	journalStale := journalExists && (journal.CatalogRevision != catalog.Revision ||
		journal.CatalogSourceVersion != catalog.SourceRevision ||
		journal.MappingRevision != catalog.MappingRevision)
	changed := !bytes.Equal(oldConfig, newConfig) || !bytes.Equal(oldCatalog, catalogData) || !journalExists || journalStale
	return setupPlan{
		catalog: catalog, catalogData: catalogData, configData: newConfig, configMode: configMode,
		configOld: oldConfig, configExists: configExists, catalogOld: oldCatalog,
		catalogExists: catalogExists, journal: journal, journalExists: journalExists, changed: changed,
	}, nil
}

func operationResultForPlan(plan setupPlan) OperationResult {
	return OperationResult{
		Changed: plan.changed, CatalogRevision: plan.catalog.Revision,
		SourceRevision: plan.catalog.SourceRevision, MappingRevision: plan.catalog.MappingRevision,
	}
}

func (l *Lifecycle) applySetup(plan setupPlan, result *OperationResult) (err error) {
	journalSnapshot, err := snapshotRegularFile(l.Paths.JournalFile, 0o600)
	if err != nil {
		return err
	}
	configSnapshot := fileSnapshot{data: plan.configOld, mode: plan.configMode, exists: plan.configExists}
	catalogSnapshot := fileSnapshot{data: plan.catalogOld, mode: 0o600, exists: plan.catalogExists}
	var configMutated, catalogMutated, journalMutated bool
	var cacheBackup string
	defer func() {
		if err == nil {
			return
		}
		rollbackErrors := make([]error, 0, 4)
		if journalMutated {
			rollbackErrors = append(rollbackErrors, restoreFileSnapshot(l.Paths.JournalFile, journalSnapshot))
		}
		if configMutated {
			rollbackErrors = append(rollbackErrors, restoreFileSnapshot(l.Paths.ConfigFile, configSnapshot))
		}
		if catalogMutated {
			rollbackErrors = append(rollbackErrors, restoreFileSnapshot(l.Paths.CatalogFile, catalogSnapshot))
		}
		if cacheBackup != "" {
			rollbackErrors = append(rollbackErrors, restoreMovedAside(l.Paths.CacheFile, cacheBackup))
		}
		err = withRollbackError(err, rollbackErrors...)
	}()

	journal := plan.journal
	if !plan.journalExists {
		journal = Journal{
			Version: journalVersion, ConfigExisted: plan.configExists,
			OriginalConfigHash: contentHash(plan.configOld), OriginalConfigMode: uint32(plan.configMode.Perm()),
			CatalogExisted: plan.catalogExists, OriginalCatalogHash: contentHash(plan.catalogOld),
		}
		if plan.configExists {
			journal.ConfigBackupFile = uniqueBackupPath(l.Paths.BackupDir, "config.toml")
			if err := copyFile(l.Paths.ConfigFile, journal.ConfigBackupFile, 0o600); err != nil {
				return err
			}
			result.ConfigBackupFile = journal.ConfigBackupFile
		}
		if plan.catalogExists {
			journal.CatalogBackupFile = uniqueBackupPath(l.Paths.BackupDir, filepath.Base(l.Paths.CatalogFile))
			if err := copyFile(l.Paths.CatalogFile, journal.CatalogBackupFile, 0o600); err != nil {
				return err
			}
		}
	}
	if !bytes.Equal(plan.catalogOld, plan.catalogData) {
		catalogMutated = true
		if err = atomicWriteFile(l.Paths.CatalogFile, plan.catalogData, 0o600); err != nil {
			return err
		}
	}
	if !bytes.Equal(plan.configOld, plan.configData) {
		configMutated = true
		if err = atomicWriteFile(l.Paths.ConfigFile, plan.configData, plan.configMode); err != nil {
			return err
		}
	}
	if cacheBackup, err = moveAside(l.Paths.CacheFile, l.Paths.BackupDir, "models_cache.json.stale"); err != nil {
		return err
	}
	journal.UpdatedAt = time.Now().UTC()
	journal.ManagedConfigHash = contentHash(plan.configData)
	journal.ManagedCatalogHash = contentHash(plan.catalogData)
	journal.CatalogRevision = plan.catalog.Revision
	journal.CatalogSourceVersion = plan.catalog.SourceRevision
	journal.MappingRevision = plan.catalog.MappingRevision
	journalData, err := marshalJournal(journal)
	if err != nil {
		return err
	}
	journalMutated = true
	return atomicWriteFile(l.Paths.JournalFile, journalData, 0o600)
}

func (l *Lifecycle) ensureWritePaths() error {
	if err := rejectSymlink(l.Paths.Home); err != nil {
		return err
	}
	if err := os.MkdirAll(l.Paths.Home, 0o700); err != nil {
		return fmt.Errorf("create Codex home: %w", err)
	}
	for _, path := range []string{l.Paths.ConfigFile, l.Paths.CatalogFile, l.Paths.CacheFile, l.Paths.StateDir, l.Paths.JournalFile, l.Paths.LockFile, l.Paths.BackupDir} {
		if err := rejectSymlink(path); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(l.Paths.BackupDir, 0o700); err != nil {
		return fmt.Errorf("create Codex integration state: %w", err)
	}
	return nil
}

func (l *Lifecycle) baseURL() string {
	host := strings.TrimSpace(l.Config.Host)
	if strings.EqualFold(host, "localhost") {
		return "http://localhost:" + strconv.Itoa(l.Config.Port) + "/v1"
	}
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	return "http://" + net.JoinHostPort(host, strconv.Itoa(l.Config.Port)) + "/v1"
}

func injectManagedConfig(original []byte, baseURL, catalogPath string, migrateOpenCodex bool) ([]byte, error) {
	eol := "\n"
	if bytes.Contains(original, []byte("\r\n")) {
		eol = "\r\n"
	}
	normalized := strings.ReplaceAll(string(original), "\r\n", "\n")
	withoutManaged, _, err := removeManagedBlock(normalized)
	if err != nil {
		return nil, err
	}
	if migrateOpenCodex {
		withoutManaged = removeOpenCodexEntries(withoutManaged)
	}
	if err = validateConfigOwnership(withoutManaged); err != nil {
		return nil, err
	}
	block := strings.Join([]string{
		ManagedConfigBegin,
		"openai_base_url = " + strconv.Quote(baseURL),
		"model_catalog_json = " + strconv.Quote(catalogPath),
		ManagedConfigEnd,
	}, "\n") + "\n"
	configured := insertRootBlock(withoutManaged, block)
	if err = validateTOML([]byte(configured)); err != nil {
		return nil, err
	}
	if eol != "\n" {
		configured = strings.ReplaceAll(configured, "\n", eol)
	}
	return []byte(configured), nil
}

func removeManagedBlock(input string) (string, bool, error) {
	begin := strings.Index(input, ManagedConfigBegin)
	end := strings.Index(input, ManagedConfigEnd)
	if begin < 0 && end < 0 {
		return input, false, nil
	}
	if begin < 0 || end < begin {
		return "", false, errors.New("malformed CLIProxyAPI Codex Integration marker block")
	}
	if strings.Contains(input[begin+len(ManagedConfigBegin):end], ManagedConfigBegin) || strings.Contains(input[end+len(ManagedConfigEnd):], ManagedConfigEnd) {
		return "", false, errors.New("multiple CLIProxyAPI Codex Integration marker blocks")
	}
	end += len(ManagedConfigEnd)
	if end < len(input) && input[end] == '\n' {
		end++
	}
	// The blank separator before the first TOML table belongs to the managed block.
	if end < len(input) && input[end] == '\n' {
		end++
	}
	return input[:begin] + input[end:], true, nil
}

func removeOpenCodexEntries(input string) string {
	lines := strings.SplitAfter(input, "\n")
	out := make([]string, 0, len(lines))
	for index := 0; index < len(lines); index++ {
		if strings.TrimSpace(lines[index]) != openCodexMarker {
			out = append(out, lines[index])
			continue
		}
		if index+1 < len(lines) {
			key := rootAssignmentKey(lines[index+1])
			if key == "openai_base_url" || key == "model_catalog_json" {
				if key == "openai_base_url" && len(out) > 0 && isKnownOpenCodexCatalogAssignment(out[len(out)-1]) {
					out = out[:len(out)-1]
				}
				index++
				continue
			}
		}
		out = append(out, lines[index])
	}
	return strings.Join(out, "")
}

func isKnownOpenCodexCatalogAssignment(line string) bool {
	if rootAssignmentKey(line) != "model_catalog_json" {
		return false
	}
	_, rawValue, found := strings.Cut(strings.TrimSpace(line), "=")
	if !found {
		return false
	}
	value, err := strconv.Unquote(strings.TrimSpace(rawValue))
	if err != nil {
		return false
	}
	return filepath.Base(value) == "opencodex-catalog.json"
}

func validateConfigOwnership(input string) error {
	if err := validateTOML([]byte(input)); err != nil {
		return err
	}
	var document map[string]any
	if strings.TrimSpace(input) != "" {
		if err := toml.Unmarshal([]byte(input), &document); err != nil {
			return fmt.Errorf("parse Codex config: %w", err)
		}
	}
	for _, key := range []string{"openai_base_url", "model_catalog_json"} {
		if _, exists := document[key]; exists {
			return fmt.Errorf("Codex config root key %q is user-owned; no files were changed", key)
		}
	}
	return nil
}

func validateTOML(data []byte) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	var document map[string]any
	if err := toml.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("parse Codex config TOML: %w", err)
	}
	return nil
}

func insertRootBlock(input, block string) string {
	lines := strings.SplitAfter(input, "\n")
	offset := len(input)
	consumed := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			offset = consumed
			break
		}
		consumed += len(line)
	}
	prefix, suffix := input[:offset], input[offset:]
	if prefix != "" && !strings.HasSuffix(prefix, "\n") {
		prefix += "\n"
	}
	if prefix != "" && !strings.HasSuffix(prefix, "\n\n") {
		prefix += "\n"
	}
	result := prefix + block
	if suffix != "" && !strings.HasPrefix(suffix, "\n") {
		result += "\n"
	}
	return result + suffix
}

func rootAssignmentKey(line string) string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "[") {
		return ""
	}
	key, _, found := strings.Cut(trimmed, "=")
	if !found {
		return ""
	}
	return strings.TrimSpace(key)
}

func readRegularFile(path string, defaultMode os.FileMode) ([]byte, os.FileMode, bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, defaultMode, false, nil
	}
	if err != nil {
		return nil, 0, false, fmt.Errorf("inspect %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, 0, false, fmt.Errorf("refusing symbolic link at %s", path)
	}
	if !info.Mode().IsRegular() {
		return nil, 0, false, fmt.Errorf("refusing non-regular file at %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, false, fmt.Errorf("read %s: %w", path, err)
	}
	return data, info.Mode().Perm(), true, nil
}

func rejectSymlink(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing symbolic link at %s", path)
	}
	return nil
}

func acquireLifecycleLock(path string) (func(), error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("Codex integration lock is already held at %s", path)
		}
		return nil, fmt.Errorf("acquire Codex integration lock: %w", err)
	}
	_, _ = fmt.Fprintf(file, "%d\n", os.Getpid())
	_ = file.Sync()
	return func() {
		_ = file.Close()
		_ = os.Remove(path)
	}, nil
}
