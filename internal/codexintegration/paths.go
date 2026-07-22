package codexintegration

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

const (
	defaultCodexConfigFile = "config.toml"
	defaultCodexCacheFile  = "models_cache.json"
	integrationStateDir    = ".cliproxyapi-codex"
)

// Paths contains every Codex path managed by the integration lifecycle.
type Paths struct {
	Home        string
	ConfigFile  string
	CatalogFile string
	CacheFile   string
	StateDir    string
	JournalFile string
	LockFile    string
	BackupDir   string
}

// ResolvePaths applies the documented Codex home precedence without touching disk.
func ResolvePaths(explicitHome string, integration config.CodexIntegrationConfig) (Paths, error) {
	home := strings.TrimSpace(explicitHome)
	if home == "" {
		home = strings.TrimSpace(integration.CodexHome)
	}
	if home == "" {
		home = strings.TrimSpace(os.Getenv("CODEX_HOME"))
	}
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return Paths{}, fmt.Errorf("resolve user home: %w", err)
		}
		home = filepath.Join(userHome, ".codex")
	}
	absHome, err := filepath.Abs(home)
	if err != nil {
		return Paths{}, fmt.Errorf("resolve Codex home %q: %w", home, err)
	}
	absHome = filepath.Clean(absHome)
	if absHome == string(filepath.Separator) {
		return Paths{}, fmt.Errorf("Codex home cannot be the filesystem root")
	}

	catalogName := strings.TrimSpace(integration.CatalogFile)
	if catalogName == "" {
		catalogName = config.DefaultCodexCatalogFile
	}
	if filepath.IsAbs(catalogName) || filepath.Base(filepath.Clean(catalogName)) != filepath.Clean(catalogName) || catalogName == "." || catalogName == ".." {
		return Paths{}, fmt.Errorf("Codex catalog %q must be a file name inside Codex home", catalogName)
	}
	stateDir := filepath.Join(absHome, integrationStateDir)
	return Paths{
		Home:        absHome,
		ConfigFile:  filepath.Join(absHome, defaultCodexConfigFile),
		CatalogFile: filepath.Join(absHome, catalogName),
		CacheFile:   filepath.Join(absHome, defaultCodexCacheFile),
		StateDir:    stateDir,
		JournalFile: filepath.Join(stateDir, "journal.json"),
		LockFile:    filepath.Join(absHome, ".cliproxyapi-codex.lock"),
		BackupDir:   filepath.Join(stateDir, "backups"),
	}, nil
}
