package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
)

// commandCodeCatalogRefreshInterval controls how often the Command Code CLI
// catalog is re-read from disk. The catalog ships inside the cmdc CLI package,
// so a re-read picks up new models after `npm update`/`cmdc update` without
// recompiling CLIProxyAPI.
const commandCodeCatalogRefreshInterval = 3 * time.Hour

// commandCodeRemoteCatalogURL is the official public Command Code provider
// model catalog. It is anonymously readable and reflects the current global
// model pool. Catalog visibility does NOT imply plan entitlement; entitlement
// is enforced by the upstream /alpha/generate endpoint (402 when a model is
// not included in the active plan). A variable so tests can redirect it.
var commandCodeRemoteCatalogURL = "https://api.commandcode.ai/provider/v1/models"

// commandCodeRemoteCatalogTimeout bounds a single remote catalog fetch.
const commandCodeRemoteCatalogTimeout = 15 * time.Second

// commandCodeRemoteCatalogMaxBody caps the accepted remote catalog body size
// so a misbehaving/compromised endpoint cannot exhaust memory.
const commandCodeRemoteCatalogMaxBody = 2 << 20 // 2 MiB

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

// tryRefreshCommandCodeCatalog refreshes the Command Code catalog.
//
// Source precedence:
//   - Cold start (no valid working catalog yet): remote -> local CLI -> builtin.
//   - Already have a working catalog: only a successful remote refresh may
//     replace it. A remote failure must keep the current catalog — the local
//     CLI catalog may be stale/older and must never regress the model set.
//
// On change it notifies the global model refresh callback so per-auth
// registrations update. A failure never clears an existing catalog.
func tryRefreshCommandCodeCatalog(label string) {
	_, hasWorking := getCommandCodeCatalog()

	// Preferred source: official remote catalog (anonymous GET, no token).
	if models, source, ok := fetchCommandCodeRemoteCatalog(context.Background()); ok {
		enrichCommandCodeModelsFromBuiltin(models)
		applyCommandCodeCatalog(models, source)
		return
	}
	log.Debugf("%s: remote Command Code catalog unavailable", label)

	// Local CLI catalog is only consulted on cold start (no working catalog).
	// Once a working catalog exists, a stale local CLI must not downgrade it.
	if !hasWorking {
		if path := resolveCommandCodeCLICatalogPath(); path != "" {
			data, errRead := os.ReadFile(path)
			if errRead == nil {
				models := parseCommandCodeModelsMarkdown(data)
				if len(models) > 0 {
					enrichCommandCodeModelsFromBuiltin(models)
					applyCommandCodeCatalog(models, path)
					return
				}
				log.Warnf("%s: Command Code CLI catalog %s parsed to zero models; keeping current catalog", label, path)
			} else {
				log.Warnf("%s: failed to read Command Code CLI catalog %s: %v", label, path, errRead)
			}
		} else {
			log.Debugf("%s: Command Code CLI catalog not found", label)
		}
	} else {
		log.Debugf("%s: working catalog exists; skipping local CLI fallback to avoid regression", label)
	}

	// All applicable sources failed. Keep last-known-good and warn loudly so
	// operators know the model pool may be stale (e.g. SYSTEM service without
	// local CLI).
	if hasWorking {
		log.Warnf("%s: remote Command Code catalog unavailable; keeping current catalog (%d models)", label, commandCodeCatalogLen())
	} else {
		log.Warnf("%s: remote and local Command Code catalogs unavailable; falling back to builtin (%d models)", label, len(BuiltinCommandCodeModels))
	}
}

// applyCommandCodeCatalog stores a parsed catalog and, on change, notifies the
// model refresh callback so existing auths re-register their models.
func applyCommandCodeCatalog(models []*ModelInfo, source string) {
	commandCodeCatalog.mu.Lock()
	changed := !commandCodeCatalog.loaded || !commandCodeModelsEqual(commandCodeCatalog.models, models)
	if changed {
		commandCodeCatalog.models = models
		commandCodeCatalog.source = source
		commandCodeCatalog.loaded = true
	}
	commandCodeCatalog.mu.Unlock()

	if changed {
		log.Infof("Command Code catalog updated from %s (%d models)", source, len(models))
		notifyModelRefresh([]string{"commandcode"})
	} else {
		log.Debugf("Command Code catalog unchanged from %s (%d models)", source, len(models))
	}
}

func commandCodeCatalogLen() int {
	commandCodeCatalog.mu.RLock()
	defer commandCodeCatalog.mu.RUnlock()
	return len(commandCodeCatalog.models)
}

// commandCodeRemoteCatalogResponse mirrors the OpenAI-style /provider/v1/models
// envelope returned by api.commandcode.ai.
type commandCodeRemoteCatalogResponse struct {
	Object string                          `json:"object"`
	Data   []commandCodeRemoteCatalogModel `json:"data"`
}

type commandCodeRemoteCatalogModel struct {
	ID            string `json:"id"`
	Object        string `json:"object"`
	Created       int64  `json:"created"`
	OwnedBy       string `json:"owned_by"`
	Name          string `json:"name"`
	ContextLength int    `json:"context_length"`
}

// commandCodeRemoteCatalogFetcher performs the remote catalog GET. It is a
// variable so tests can stub it without hitting the network. The default
// implementation is proxy-aware (respects the configured global proxy and
// environment proxies), enforces a body size cap, and validates status and
// JSON shape.
var commandCodeRemoteCatalogFetcher = fetchCommandCodeRemoteCatalogHTTP

// fetchCommandCodeRemoteCatalog fetches and parses the official Command Code
// provider catalog. Returns the parsed models plus a human-readable source
// label.
func fetchCommandCodeRemoteCatalog(ctx context.Context) ([]*ModelInfo, string, bool) {
	return commandCodeRemoteCatalogFetcher(ctx)
}

// fetchCommandCodeRemoteCatalogHTTP is the default remote catalog fetcher.
func fetchCommandCodeRemoteCatalogHTTP(ctx context.Context) ([]*ModelInfo, string, bool) {
	reqCtx, cancel := context.WithTimeout(ctx, commandCodeRemoteCatalogTimeout)
	defer cancel()

	req, errNew := http.NewRequestWithContext(reqCtx, http.MethodGet, commandCodeRemoteCatalogURL, nil)
	if errNew != nil {
		log.Debugf("commandcode remote catalog: create request: %v", errNew)
		return nil, "", false
	}
	req.Header.Set("Accept", "application/json")

	// Take a snapshot of the configured transport and build a local client so
	// a concurrent SetCommandCodeCatalogTransport never mutates the client
	// mid-request.
	client := &http.Client{Transport: commandCodeCatalogTransportSnapshot()}
	resp, errDo := client.Do(req)
	if errDo != nil {
		log.Debugf("commandcode remote catalog: fetch %s: %v", commandCodeRemoteCatalogURL, errDo)
		return nil, "", false
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		log.Debugf("commandcode remote catalog: fetch %s returned status %d", commandCodeRemoteCatalogURL, resp.StatusCode)
		return nil, "", false
	}
	contentType := resp.Header.Get("Content-Type")
	if contentType != "" && !strings.Contains(strings.ToLower(contentType), "json") {
		log.Debugf("commandcode remote catalog: unexpected content-type %q", contentType)
		return nil, "", false
	}

	limited := io.LimitReader(resp.Body, commandCodeRemoteCatalogMaxBody+1)
	data, errRead := io.ReadAll(limited)
	if errRead != nil {
		log.Debugf("commandcode remote catalog: read body: %v", errRead)
		return nil, "", false
	}
	if len(data) > commandCodeRemoteCatalogMaxBody {
		log.Debugf("commandcode remote catalog: body exceeds %d bytes limit", commandCodeRemoteCatalogMaxBody)
		return nil, "", false
	}

	var parsed commandCodeRemoteCatalogResponse
	if errUnmarshal := json.Unmarshal(data, &parsed); errUnmarshal != nil {
		log.Debugf("commandcode remote catalog: parse JSON: %v", errUnmarshal)
		return nil, "", false
	}
	if len(parsed.Data) == 0 {
		log.Debugf("commandcode remote catalog: empty data array")
		return nil, "", false
	}

	models := make([]*ModelInfo, 0, len(parsed.Data))
	seen := make(map[string]struct{}, len(parsed.Data))
	for _, m := range parsed.Data {
		id := strings.TrimSpace(m.ID)
		if id == "" || strings.Contains(id, " ") {
			continue
		}
		// Normalize to the same lowercase canonical form the CLI accepts; the
		// upstream CLI itself canonicalizes model ids case-insensitively.
		id = strings.ToLower(id)
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		displayName := strings.TrimSpace(m.Name)
		if displayName == "" {
			displayName = id
		}
		models = append(models, &ModelInfo{
			ID:                        id,
			Object:                    "model",
			OwnedBy:                   "commandcode",
			Type:                      "commandcode",
			DisplayName:               displayName,
			ContextLength:             m.ContextLength,
			SupportedInputModalities:  []string{"text", "image"},
			SupportedOutputModalities: []string{"text"},
		})
	}
	if len(models) == 0 {
		log.Debugf("commandcode remote catalog: parsed zero models")
		return nil, "", false
	}
	return models, "remote:" + commandCodeRemoteCatalogURL, true
}

// commandCodeCatalogTransportBox wraps a transport so atomic.Value always
// stores the same concrete type (atomic.Value rejects stores of differing
// dynamic types).
type commandCodeCatalogTransportBox struct {
	transport http.RoundTripper
}

// commandCodeCatalogTransport is the transport used for remote catalog
// fetches, stored in an atomic.Value so SetCommandCodeCatalogTransport can be
// called at any time without racing in-flight fetches. The default honors
// environment proxies (http.ProxyFromEnvironment).
var commandCodeCatalogTransport atomic.Value

func defaultCommandCodeCatalogTransport() http.RoundTripper {
	return &http.Transport{Proxy: http.ProxyFromEnvironment}
}

func init() {
	commandCodeCatalogTransport.Store(&commandCodeCatalogTransportBox{transport: defaultCommandCodeCatalogTransport()})
}

// SetCommandCodeCatalogTransport installs the transport used for remote
// catalog fetches. Pass nil to restore the default environment-proxy
// transport. This lets the service layer route catalog fetches through the
// configured CLIProxyAPI proxy-url so catalog traffic follows the same proxy
// policy as inference traffic. Safe to call at any time: fetches take a
// snapshot, so an in-flight fetch never observes a mutated shared client.
func SetCommandCodeCatalogTransport(transport http.RoundTripper) {
	if transport == nil {
		transport = defaultCommandCodeCatalogTransport()
	}
	commandCodeCatalogTransport.Store(&commandCodeCatalogTransportBox{transport: transport})
}

// commandCodeCatalogTransportSnapshot returns the currently configured
// transport.
func commandCodeCatalogTransportSnapshot() http.RoundTripper {
	if box, ok := commandCodeCatalogTransport.Load().(*commandCodeCatalogTransportBox); ok && box != nil {
		return box.transport
	}
	return defaultCommandCodeCatalogTransport()
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
	// Resolve launcher symlinks (Linux/macOS npm, Homebrew, nvm) to the real
	// package location so binDir points at the actual install prefix.
	if resolved, errResolve := filepath.EvalSymlinks(exePath); errResolve == nil {
		exePath = resolved
	}
	binDir := filepath.Dir(exePath)
	for _, pkgDir := range commandCodePackageCandidates(binDir) {
		if fi, errStat := os.Stat(pkgDir); errStat == nil && fi.IsDir() {
			return pkgDir, nil
		}
	}
	return "", fmt.Errorf("could not locate command-code package from launcher %s", exePath)
}

// commandCodePackageCandidates returns plausible package dirs relative to a
// launcher's bin directory. It covers npm prefix/bin, prefix/lib/node_modules
// (Linux/macOS global installs), prefix/node_modules and the hermes-managed
// node dir. Order matters: most common first.
func commandCodePackageCandidates(binDir string) []string {
	if binDir == "" {
		return nil
	}
	base := []string{
		filepath.Join(binDir, "node_modules", "command-code"),
		filepath.Join(binDir, "..", "lib", "node_modules", "command-code"),
		filepath.Join(binDir, "..", "node_modules", "command-code"),
		filepath.Join(binDir, "..", "command-code"),
	}
	// The hermes-managed npm global root keeps packages directly in the node
	// dir; cmdc launcher sits beside node_modules/command-code.
	if strings.Contains(strings.ToLower(binDir), "hermes") {
		base = append(base, filepath.Join(binDir, "node_modules", "command-code"))
	}
	return base
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

// commandCodeModelsEqual reports whether two catalogs are semantically equal.
// Comparison is order-insensitive by model ID so pure reordering never causes
// a spurious refresh, and it includes the metadata that actually affects the
// registry (ContextLength, MaxCompletionTokens) so real metadata changes are
// not missed.
func commandCodeModelsEqual(a, b []*ModelInfo) bool {
	if len(a) != len(b) {
		return false
	}
	index := make(map[string]*ModelInfo, len(b))
	for _, m := range b {
		if m == nil {
			continue
		}
		index[m.ID] = m
	}
	for _, ma := range a {
		if ma == nil {
			return false
		}
		mb, ok := index[ma.ID]
		if !ok || mb == nil {
			return false
		}
		if ma.DisplayName != mb.DisplayName ||
			ma.ContextLength != mb.ContextLength ||
			ma.MaxCompletionTokens != mb.MaxCompletionTokens {
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

// enrichCommandCodeModelsFromBuiltin fills MaxCompletionTokens for dynamic
// catalog models whose ID exists in the trusted built-in metadata. Models that
// are dynamic-only (no reliable max-output source) keep MaxCompletionTokens=0
// (unknown); we never guess values for them.
func enrichCommandCodeModelsFromBuiltin(models []*ModelInfo) {
	if len(models) == 0 {
		return
	}
	for _, m := range models {
		if m == nil {
			continue
		}
		// Already has an explicit value; never overwrite.
		if m.MaxCompletionTokens > 0 {
			continue
		}
		for _, b := range BuiltinCommandCodeModels {
			if b.ID == m.ID {
				m.MaxCompletionTokens = b.MaxTokens
				break
			}
		}
	}
}
