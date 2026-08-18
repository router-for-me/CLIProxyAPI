package registry

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// commandCodeCatalogRefreshInterval controls how often the Command Code CLI
// catalog is re-read from disk. The catalog ships inside the cmdc CLI package,
// so a re-read picks up new models after `npm update`/`cmdc update` without
// recompiling CLIProxyAPI.
const commandCodeCatalogRefreshInterval = 3 * time.Hour

// commandCodeCLICatalogFileName is the structured markdown catalog bundled
// with the Command Code CLI. It lists every model id, context window, plan
// gate and description in a stable table format.
const commandCodeCLICatalogFileName = "dist/bundled/command-code-knowledge/reference/models.md"

// commandCodeCLIResolvers resolves the path to the installed cmdc CLI package
// directory. Order matters: env override first, then PATH lookup, then
// well-known install roots. A variable so tests can stub discovery.
var commandCodeCLIResolvers = []func() (string, error){
	commandCodeCLIFromEnv,
	commandCodeCLIFromPath,
	commandCodeCLIFromWellKnownRoots,
}

// commandCodeCatalogStore holds the last-known-good parsed catalog. Discovery
// failures keep the previous data so existing registrations never vanish.
type commandCodeCatalogStore struct {
	mu     sync.RWMutex
	loaded bool
	models []*ModelInfo
	source string
}

var commandCodeCatalog = &commandCodeCatalogStore{}

var commandCodeUpdaterOnce sync.Once

// StartCommandCodeModelsUpdater starts a background updater that reads the
// Command Code CLI catalog immediately and then refreshes it periodically.
// Safe to call multiple times; only one updater will run. Discovery failure
// keeps the previous catalog (last-known-good).
func StartCommandCodeModelsUpdater(ctx context.Context) {
	commandCodeUpdaterOnce.Do(func() {
		go runCommandCodeCatalogUpdater(ctx)
	})
}

func runCommandCodeCatalogUpdater(ctx context.Context) {
	tryRefreshCommandCodeCatalog("startup Command Code catalog load")

	ticker := time.NewTicker(commandCodeCatalogRefreshInterval)
	defer ticker.Stop()
	log.Infof("periodic Command Code catalog refresh started (interval=%s)", commandCodeCatalogRefreshInterval)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tryRefreshCommandCodeCatalog("periodic Command Code catalog refresh")
		}
	}
}

// tryRefreshCommandCodeCatalog re-reads the CLI catalog and, on change,
// notifies the global model refresh callback so per-auth registrations update.
func tryRefreshCommandCodeCatalog(label string) {
	path := resolveCommandCodeCLICatalogPath()
	if path == "" {
		log.Debugf("%s: Command Code CLI catalog not found; keeping current catalog", label)
		return
	}

	data, errRead := os.ReadFile(path)
	if errRead != nil {
		log.Warnf("%s: failed to read Command Code CLI catalog %s: %v", label, path, errRead)
		return
	}

	models := parseCommandCodeModelsMarkdown(data)
	if len(models) == 0 {
		log.Warnf("%s: Command Code CLI catalog %s parsed to zero models; keeping current catalog", label, path)
		return
	}

	commandCodeCatalog.mu.Lock()
	changed := !commandCodeCatalog.loaded || !commandCodeModelsEqual(commandCodeCatalog.models, models)
	if changed {
		commandCodeCatalog.models = models
		commandCodeCatalog.source = path
		commandCodeCatalog.loaded = true
	}
	commandCodeCatalog.mu.Unlock()

	if changed {
		log.Infof("%s: Command Code catalog updated from %s (%d models)", label, path, len(models))
		notifyModelRefresh([]string{"commandcode"})
	} else {
		log.Debugf("%s: Command Code catalog unchanged from %s (%d models)", label, path, len(models))
	}
}

// resolveCommandCodeCLICatalogPath locates the models.md bundled with cmdc.
// Returns "" when the CLI cannot be found.
func resolveCommandCodeCLICatalogPath() string {
	for _, resolve := range commandCodeCLIResolvers {
		dir, err := resolve()
		if err != nil || dir == "" {
			continue
		}
		candidate := filepath.Join(dir, filepath.FromSlash(commandCodeCLICatalogFileName))
		if fi, errStat := os.Stat(candidate); errStat == nil && !fi.IsDir() {
			return candidate
		}
	}
	return ""
}

// commandCodeCLIFromEnv checks COMMANDCODE_CLI_DIR for the CLI package dir.
func commandCodeCLIFromEnv() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("COMMANDCODE_CLI_DIR")); dir != "" {
		return dir, nil
	}
	return "", fmt.Errorf("COMMANDCODE_CLI_DIR not set")
}

// commandCodeCLIFromPath resolves the cmdc launcher via PATH and walks up to
// the npm package directory (node_modules/command-code).
func commandCodeCLIFromPath() (string, error) {
	name := "cmdc"
	if runtime.GOOS == "windows" {
		name = "cmdc.cmd"
	}
	exePath, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("cmdc not found on PATH: %w", err)
	}
	// exec.LookPath on Windows may return the .cmd shim; its parent is the npm
	// bin dir. The package dir is ../node_modules/command-code relative to it.
	binDir := filepath.Dir(exePath)
	for _, pkgDir := range []string{
		filepath.Join(binDir, "node_modules", "command-code"),
		filepath.Join(binDir, "..", "node_modules", "command-code"),
		filepath.Join(binDir, "..", "command-code"),
	} {
		if fi, errStat := os.Stat(pkgDir); errStat == nil && fi.IsDir() {
			return pkgDir, nil
		}
	}
	// The hermes-managed npm global root keeps packages directly in the node
	// dir; cmdc launcher sits beside node_modules/command-code.
	if strings.Contains(strings.ToLower(binDir), "hermes") {
		if fi, errStat := os.Stat(filepath.Join(binDir, "node_modules", "command-code")); errStat == nil && fi.IsDir() {
			return filepath.Join(binDir, "node_modules", "command-code"), nil
		}
	}
	return "", fmt.Errorf("could not locate command-code package from launcher %s", exePath)
}

// commandCodeCLIFromWellKnownRoots checks common npm global install roots.
func commandCodeCLIFromWellKnownRoots() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	roots := []string{
		filepath.Join(home, "AppData", "Roaming", "npm"),
		filepath.Join(home, "AppData", "Local", "hermes", "node"),
		filepath.Join(home, ".npm-global"),
		filepath.Join(home, "npm"),
	}
	for _, root := range roots {
		pkgDir := filepath.Join(root, "node_modules", "command-code")
		if fi, errStat := os.Stat(pkgDir); errStat == nil && fi.IsDir() {
			return pkgDir, nil
		}
	}
	return "", fmt.Errorf("command-code package not found in well-known roots")
}

// parseCommandCodeModelsMarkdown parses the structured table in models.md.
// Expected row format (pipe-separated, id in backticks as first cell):
//
//	| `deepseek/deepseek-v4-flash` | DeepSeek V4 Flash (latest) | 1M | ... | Go and above | ... |
//
// Rows outside the table or with a missing id cell are skipped.
func parseCommandCodeModelsMarkdown(data []byte) []*ModelInfo {
	var out []*ModelInfo
	seen := make(map[string]struct{})
	for _, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(rawLine)
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(line, "|")
		// Table rows have a leading empty cell before the first `| column`.
		if len(cells) < 2 {
			continue
		}
		idCell := strings.TrimSpace(cells[1])
		id := strings.TrimSpace(strings.Trim(idCell, "`"))
		if id == "" || strings.Contains(id, " ") || strings.Contains(id, "Id") {
			continue
		}
		// Skip markdown table separator rows like |---|---|.
		if strings.Trim(id, "-: ") == "" {
			continue
		}
		// Normalize to the lowercase canonical form the CLI accepts.
		id = strings.ToLower(id)
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}

		displayName := ""
		if len(cells) >= 3 {
			displayName = strings.TrimSpace(cells[2])
		}
		if displayName == "" || strings.EqualFold(displayName, "Name") {
			displayName = id
		}

		contextLength := 0
		if len(cells) >= 4 {
			contextLength = parseCommandCodeContextWindow(cells[3])
		}

		out = append(out, &ModelInfo{
			ID:                        id,
			Object:                    "model",
			OwnedBy:                   "commandcode",
			Type:                      "commandcode",
			DisplayName:               displayName,
			ContextLength:             contextLength,
			SupportedInputModalities:  []string{"text", "image"},
			SupportedOutputModalities: []string{"text"},
		})
	}
	return out
}

// parseCommandCodeContextWindow converts a context cell like "1M", "256K",
// "200K" or "—" into an integer. Unknown/empty cells yield 0.
func parseCommandCodeContextWindow(cell string) int {
	s := strings.TrimSpace(strings.ToLower(cell))
	s = strings.TrimSpace(strings.Trim(s, "—–-"))
	if s == "" {
		return 0
	}
	mult := 1
	switch {
	case strings.HasSuffix(s, "m"):
		mult = 1_000_000
		s = strings.TrimSuffix(s, "m")
	case strings.HasSuffix(s, "k"):
		mult = 1_000
		s = strings.TrimSuffix(s, "k")
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	var num float64
	if _, err := fmt.Sscanf(s, "%g", &num); err != nil {
		return 0
	}
	return int(num * float64(mult))
}

func commandCodeModelsEqual(a, b []*ModelInfo) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] == nil || b[i] == nil {
			if a[i] != b[i] {
				return false
			}
			continue
		}
		if a[i].ID != b[i].ID || a[i].DisplayName != b[i].DisplayName {
			return false
		}
	}
	return true
}

// getCommandCodeCatalog returns the last-known-good dynamic catalog.
func getCommandCodeCatalog() ([]*ModelInfo, bool) {
	commandCodeCatalog.mu.RLock()
	defer commandCodeCatalog.mu.RUnlock()
	if !commandCodeCatalog.loaded || len(commandCodeCatalog.models) == 0 {
		return nil, false
	}
	return cloneModelInfos(commandCodeCatalog.models), true
}
