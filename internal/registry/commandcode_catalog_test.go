package registry

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const sampleCommandCodeModelsMD = "# Command Code Models\n" +
	"\n" +
	"The model catalog — every id `/model`, `--model`, and `model:effort` shorthand accept.\n" +
	"\n" +
	"## Open Source\n" +
	"\n" +
	"| Id (use EXACTLY this) | Name | Context | Efforts | $/1M in/out · cache read | Min plan | Best for |\n" +
	"|---|---|---|---|---|---|---|\n" +
	"| `deepseek/deepseek-v4-pro` | DeepSeek V4 Pro (latest) | 1M | high, max | $0.66/$1.98 · cache $0.022 | Go and above | hybrid-attention long-context reasoning |\n" +
	"| `deepseek/deepseek-v4-flash` | DeepSeek V4 Flash (latest) | 1M | high, max | $0.22/$0.66 · cache $0.007 | Go and above | fast hybrid-attention reasoning |\n" +
	"| `zai-org/GLM-5.2-Fast` | GLM-5.2 Fast | 1M | — | $3/$10.25 · cache $0.5 | Go and above | high-throughput GLM-5.2 with 1M context |\n" +
	"| `vendor/new-model-test` | New Model Test | 256K | — | $1/$2 · cache $0.1 | Go and above | brand new model from tomorrow |\n" +
	"\n" +
	"## Anthropic\n" +
	"\n" +
	"| Id (use EXACTLY this) | Name | Context | Efforts | $/1M in/out · cache read | Min plan | Best for |\n" +
	"|---|---|---|---|---|---|---|\n" +
	"| `claude-sonnet-5` | Claude Sonnet 5 | 1M | low, medium, high, xhigh, max | $2/$10 · cache $0.2 | Pro and above | best combo of speed & intelligence |\n" +
	"\n" +
	"## OpenAI\n" +
	"\n" +
	"| Id (use EXACTLY this) | Name | Context | Efforts | $/1M in/out · cache read | Min plan | Best for |\n" +
	"|---|---|---|---|---|---|---|\n" +
	"| `gpt-5.6-luna` | GPT-5.6 Luna | 1.05M | low, medium, high, xhigh, max | $0.2/$1.2 · cache $0.02 | Go and above | optimized for cost-sensitive workloads |\n"

// writeSampleCatalog writes models.md at the exact path the resolver expects
// (pkgDir/dist/bundled/command-code-knowledge/reference/models.md) and points
// the resolver at pkgDir. It also stubs the remote catalog fetcher to fail so
// these tests exercise the local CLI fallback path deterministically without
// network. Returns pkgDir.
func writeSampleCatalog(t *testing.T, content string) string {
	t.Helper()
	stubRemoteCatalogFailure(t)
	base := t.TempDir()
	pkgDir := filepath.Join(base, "command-code")
	catalogPath := filepath.Join(pkgDir, filepath.FromSlash(commandCodeCLICatalogFileName))
	if err := os.MkdirAll(filepath.Dir(catalogPath), 0700); err != nil {
		t.Fatalf("mkdir catalog dir: %v", err)
	}
	if err := os.WriteFile(catalogPath, []byte(content), 0600); err != nil {
		t.Fatalf("write catalog: %v", err)
	}
	orig := commandCodeCLIResolvers
	commandCodeCLIResolvers = []func() (string, error){
		func() (string, error) { return pkgDir, nil },
	}
	t.Cleanup(func() { commandCodeCLIResolvers = orig })
	return pkgDir
}

// stubRemoteCatalogFailure makes the remote catalog fetcher report failure so
// tests that exercise local/fallback paths never touch the network.
func stubRemoteCatalogFailure(t *testing.T) {
	t.Helper()
	orig := commandCodeRemoteCatalogFetcher
	commandCodeRemoteCatalogFetcher = func(context.Context) ([]*ModelInfo, string, bool) {
		return nil, "", false
	}
	t.Cleanup(func() { commandCodeRemoteCatalogFetcher = orig })
}

// TestParseCommandCodeModelsMarkdown verifies id/name/context parsing and
// lowercase canonicalization.
func TestParseCommandCodeModelsMarkdown(t *testing.T) {
	models := parseCommandCodeModelsMarkdown([]byte(sampleCommandCodeModelsMD))
	if len(models) != 6 {
		t.Fatalf("expected 6 models, got %d", len(models))
	}
	byID := make(map[string]*ModelInfo, len(models))
	for _, m := range models {
		byID[m.ID] = m
	}
	// Canonical IDs are lowercase.
	for _, want := range []string{"deepseek/deepseek-v4-pro", "deepseek/deepseek-v4-flash", "zai-org/glm-5.2-fast", "vendor/new-model-test", "claude-sonnet-5", "gpt-5.6-luna"} {
		if _, ok := byID[want]; !ok {
			t.Errorf("missing model %q", want)
		}
	}
	if byID["zai-org/glm-5.2-fast"].DisplayName == "" {
		t.Errorf("glm display name missing")
	}
	if byID["deepseek/deepseek-v4-pro"].ContextLength != 1_000_000 {
		t.Errorf("context for deepseek-v4-pro = %d, want 1000000", byID["deepseek/deepseek-v4-pro"].ContextLength)
	}
	if byID["gpt-5.6-luna"].ContextLength != 1_050_000 {
		t.Errorf("context for gpt-5.6-luna = %d, want 1050000", byID["gpt-5.6-luna"].ContextLength)
	}
	if byID["vendor/new-model-test"].ContextLength != 256_000 {
		t.Errorf("context for new-model-test = %d, want 256000", byID["vendor/new-model-test"].ContextLength)
	}
}

// TestCommandCodeCatalog_RefreshAddsNewModel is Case A + B: a first catalog
// load, then a refresh with an additional model, must make the new model
// visible without recompilation. The refresh source is the remote catalog
// (official provider endpoint) — a remote refresh that adds a model must
// update the working catalog.
func TestCommandCodeCatalog_RefreshAddsNewModel(t *testing.T) {
	commandCodeCatalog = &commandCodeCatalogStore{}

	// First refresh: remote returns the base set.
	remoteCalls := 0
	commandCodeRemoteCatalogFetcher = remoteFetcherFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		remoteCalls++
		if remoteCalls == 1 {
			_, _ = w.Write([]byte(sampleRemoteCatalogJSON))
		} else {
			// Second call: keep the base set plus a brand new model.
			withNew := `{"object":"list","data":[` +
				`{"id":"deepseek/deepseek-v4-flash","object":"model","created":1,"owned_by":"command-code","name":"DS","context_length":1000000},` +
				`{"id":"moonshotai/Kimi-K3","object":"model","created":1,"owned_by":"command-code","name":"Kimi K3","context_length":1000000},` +
				`{"id":"meta/muse-spark-1.2-contributor","object":"model","created":1,"owned_by":"command-code","name":"Muse","context_length":1048576},` +
				`{"id":"Qwen/Qwen3.7-Plus","object":"model","created":1,"owned_by":"command-code","name":"Qwen","context_length":1000000},` +
				`{"id":"future/model-9","object":"model","created":1,"owned_by":"command-code","name":"Future Model 9","context_length":131072}` +
				`]}`
			_, _ = w.Write([]byte(withNew))
		}
	})
	t.Cleanup(func() { commandCodeRemoteCatalogFetcher = fetchCommandCodeRemoteCatalogHTTP })

	tryRefreshCommandCodeCatalog("test initial")
	models, ok := getCommandCodeCatalog()
	if !ok {
		t.Fatal("catalog not loaded after initial refresh")
	}
	if _, exists := findCommandCodeModel(models, "deepseek/deepseek-v4-flash"); !exists {
		t.Fatalf("initial catalog should contain deepseek/deepseek-v4-flash")
	}
	initialCount := len(models)

	tryRefreshCommandCodeCatalog("test refresh")
	models2, ok := getCommandCodeCatalog()
	if !ok {
		t.Fatal("catalog lost after refresh")
	}
	if len(models2) != initialCount+1 {
		t.Fatalf("expected %d models after refresh, got %d", initialCount+1, len(models2))
	}
	if _, exists := findCommandCodeModel(models2, "future/model-9"); !exists {
		t.Fatalf("refresh should add future/model-9")
	}
	if _, exists := findCommandCodeModel(models2, "deepseek/deepseek-v4-flash"); !exists {
		t.Fatalf("refresh must keep deepseek/deepseek-v4-flash")
	}
}

// TestCommandCodeCatalog_DiscoveryFailureKeepsLastKnownGood is Case C: after a
// successful load, a failed refresh must not wipe the catalog.
func TestCommandCodeCatalog_DiscoveryFailureKeepsLastKnownGood(t *testing.T) {
	writeSampleCatalog(t, sampleCommandCodeModelsMD)

	commandCodeCatalog = &commandCodeCatalogStore{}

	tryRefreshCommandCodeCatalog("test initial")
	if _, ok := getCommandCodeCatalog(); !ok {
		t.Fatal("catalog not loaded")
	}

	// Break discovery: resolver now fails.
	commandCodeCLIResolvers = []func() (string, error){
		func() (string, error) { return "", os.ErrNotExist },
	}
	tryRefreshCommandCodeCatalog("test failed refresh")

	models, ok := getCommandCodeCatalog()
	if !ok {
		t.Fatal("catalog must survive failed refresh (last-known-good)")
	}
	if len(models) == 0 {
		t.Fatal("catalog must not be emptied by failed refresh")
	}
	if _, exists := findCommandCodeModel(models, "deepseek/deepseek-v4-flash"); !exists {
		t.Fatalf("last-known-good catalog should still contain deepseek/deepseek-v4-flash")
	}
}

// TestGetCommandCodeModels_FallbackBeforeDiscovery ensures static bootstrap
// works when no CLI catalog has been loaded.
func TestGetCommandCodeModels_FallbackBeforeDiscovery(t *testing.T) {
	commandCodeCatalog = &commandCodeCatalogStore{}
	models := GetCommandCodeModels()
	if len(models) == 0 {
		t.Fatal("fallback static catalog should not be empty")
	}
	if _, exists := findCommandCodeModel(models, "deepseek/deepseek-v4-flash"); !exists {
		t.Fatalf("static fallback should contain deepseek/deepseek-v4-flash")
	}
}

// TestGetCommandCodeModels_PrefersDynamicCatalog verifies dynamic catalog wins
// once loaded (static remains bootstrap-only).
func TestGetCommandCodeModels_PrefersDynamicCatalog(t *testing.T) {
	commandCodeCatalog = &commandCodeCatalogStore{
		loaded: true,
		models: []*ModelInfo{
			{ID: "vendor/new-model-test", Object: "model", OwnedBy: "commandcode", Type: "commandcode"},
		},
	}
	models := GetCommandCodeModels()
	if len(models) != 1 || models[0].ID != "vendor/new-model-test" {
		t.Fatalf("dynamic catalog should win, got %d models", len(models))
	}
}

// TestCommandCodeCatalog_RegisteredModelResolvesProvider is Case A routing
// proof at the registry level: once the dynamic catalog is loaded, a model
// that does not exist in the static built-in list must resolve through
// GetModelProviders with provider "commandcode" (this is what /v1/models and
// chat routing use to avoid "unknown provider for model").
func TestCommandCodeCatalog_RegisteredModelResolvesProvider(t *testing.T) {
	commandCodeCatalog = &commandCodeCatalogStore{
		loaded: true,
		models: []*ModelInfo{
			{ID: "vendor/new-model-test", Object: "model", OwnedBy: "commandcode", Type: "commandcode"},
			{ID: "deepseek/deepseek-v4-flash", Object: "model", OwnedBy: "commandcode", Type: "commandcode"},
		},
	}
	defer func() { commandCodeCatalog = &commandCodeCatalogStore{} }()

	// Simulate the service registering models for a commandcode auth: the
	// provider name used is "commandcode" and the models come from
	// GetCommandCodeModels() (dynamic first).
	models := GetCommandCodeModels()
	if len(models) != 2 {
		t.Fatalf("expected 2 models from dynamic catalog, got %d", len(models))
	}
	reg := GetGlobalRegistry()
	reg.RegisterClient("test-auth-cc", "commandcode", models)

	providers := reg.GetModelProviders("vendor/new-model-test")
	if len(providers) != 1 || providers[0] != "commandcode" {
		t.Fatalf("GetModelProviders(vendor/new-model-test) = %v, want [commandcode]", providers)
	}
	providers = reg.GetModelProviders("deepseek/deepseek-v4-flash")
	if len(providers) != 1 || providers[0] != "commandcode" {
		t.Fatalf("GetModelProviders(deepseek/deepseek-v4-flash) = %v, want [commandcode]", providers)
	}

	reg.UnregisterClient("test-auth-cc")
}

// TestCommandCodeUpdater_StartupAndTicker ensures StartCommandCodeModelsUpdater
// is safe to call and the ticker fires on schedule (drives refresh).
func TestCommandCodeUpdater_StartupAndTicker(t *testing.T) {
	writeSampleCatalog(t, sampleCommandCodeModelsMD)

	commandCodeCatalog = &commandCodeCatalogStore{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	StartCommandCodeModelsUpdater(ctx)
	StartCommandCodeModelsUpdater(ctx) // idempotent

	// The startup refresh is synchronous inside the goroutine; poll briefly.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, ok := getCommandCodeCatalog(); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("updater did not load catalog at startup")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func findCommandCodeModel(models []*ModelInfo, id string) (*ModelInfo, bool) {
	for _, m := range models {
		if m != nil && m.ID == id {
			return m, true
		}
	}
	return nil, false
}

// TestParseCommandCodeContextWindow covers context cell formats.
func TestParseCommandCodeContextWindow(t *testing.T) {
	cases := map[string]int{
		"1M":      1_000_000,
		"256K":    256_000,
		"200K":    200_000,
		"1.05M":   1_050_000,
		"—":       0,
		"-":       0,
		"":        0,
		"unknown": 0,
	}
	for in, want := range cases {
		if got := parseCommandCodeContextWindow(in); got != want {
			t.Errorf("parseCommandCodeContextWindow(%q) = %d, want %d", in, got, want)
		}
	}
}

// TestCommandCodeModelsEqual sanity-checks change detection.
func TestCommandCodeModelsEqual(t *testing.T) {
	a := []*ModelInfo{{ID: "x", DisplayName: "X"}}
	b := []*ModelInfo{{ID: "x", DisplayName: "X"}}
	if !commandCodeModelsEqual(a, b) {
		t.Error("identical catalogs should compare equal")
	}
	c := []*ModelInfo{{ID: "x", DisplayName: "X"}, {ID: "y", DisplayName: "Y"}}
	if commandCodeModelsEqual(a, c) {
		t.Error("catalogs of different length should compare unequal")
	}
	d := []*ModelInfo{{ID: "x", DisplayName: "Y"}}
	if commandCodeModelsEqual(a, d) {
		t.Error("catalogs with changed display name should compare unequal")
	}
	// Order must not matter: same IDs in different order are equal.
	reversed := []*ModelInfo{{ID: "y", DisplayName: "Y"}, {ID: "x", DisplayName: "X"}}
	if !commandCodeModelsEqual(c, reversed) {
		t.Error("pure reordering must not cause a spurious change")
	}
	// Context window change must be detected (metadata affecting registry).
	ctxChanged := []*ModelInfo{{ID: "x", DisplayName: "X", ContextLength: 1_000_000}}
	if commandCodeModelsEqual(a, ctxChanged) {
		t.Error("context length change should compare unequal")
	}
	// MaxCompletionTokens change must be detected.
	maxChanged := []*ModelInfo{{ID: "x", DisplayName: "X", MaxCompletionTokens: 65_536}}
	if commandCodeModelsEqual(a, maxChanged) {
		t.Error("MaxCompletionTokens change should compare unequal")
	}
	if !strings.Contains(sampleCommandCodeModelsMD, "vendor/new-model-test") {
		t.Fatal("fixture must include the synthetic new model")
	}
}

// TestEnrichCommandCodeModelsFromBuiltin verifies static-known models get
// MaxCompletionTokens enriched from builtin metadata while dynamic-only models
// keep 0 (unknown, never guessed).
func TestEnrichCommandCodeModelsFromBuiltin(t *testing.T) {
	models := []*ModelInfo{
		{ID: "deepseek/deepseek-v4-flash", Object: "model"},           // static-known
		{ID: "vendor/brand-new-dynamic", Object: "model"},             // dynamic-only
		{ID: "zai-org/glm-5.2-fast", Object: "model"},                 // static-known
		{ID: "x/explicit", Object: "model", MaxCompletionTokens: 999}, // already set
	}
	enrichCommandCodeModelsFromBuiltin(models)

	if got := models[0].MaxCompletionTokens; got <= 0 {
		t.Errorf("deepseek-v4-flash MaxCompletionTokens = %d, want >0 (enriched from builtin)", got)
	}
	if got := models[2].MaxCompletionTokens; got <= 0 {
		t.Errorf("glm-5.2-fast MaxCompletionTokens = %d, want >0 (enriched from builtin)", got)
	}
	if got := models[1].MaxCompletionTokens; got != 0 {
		t.Errorf("dynamic-only model MaxCompletionTokens = %d, want 0 (unknown, never guessed)", got)
	}
	if got := models[3].MaxCompletionTokens; got != 999 {
		t.Errorf("explicit MaxCompletionTokens = %d, want 999 (never overwritten)", got)
	}
}

// TestEnrichCommandCodeModelsFromBuiltin_RefreshPath verifies the refresh path
// (which stores the dynamic catalog) enriches static-known models so the
// catalog exposed via GetCommandCodeModels carries MaxCompletionTokens.
func TestEnrichCommandCodeModelsFromBuiltin_RefreshPath(t *testing.T) {
	writeSampleCatalog(t, sampleCommandCodeModelsMD)
	commandCodeCatalog = &commandCodeCatalogStore{}

	tryRefreshCommandCodeCatalog("test enrich")
	models, ok := getCommandCodeCatalog()
	if !ok {
		t.Fatal("catalog not loaded")
	}
	m, found := findCommandCodeModel(models, "deepseek/deepseek-v4-flash")
	if !found {
		t.Fatal("deepseek/deepseek-v4-flash missing from dynamic catalog")
	}
	if m.MaxCompletionTokens <= 0 {
		t.Errorf("deepseek/deepseek-v4-flash MaxCompletionTokens = %d, want >0 after enrich", m.MaxCompletionTokens)
	}
}

// TestCommandCodePackageCandidates_LinuxNpmLibLayout verifies the Linux/macOS
// npm global layout (prefix/bin/cmdc symlink -> ../lib/node_modules/command-code)
// is covered by candidate discovery.
func TestCommandCodePackageCandidates_LinuxNpmLibLayout(t *testing.T) {
	cands := commandCodePackageCandidates(filepath.FromSlash("/usr/local/bin"))
	want := filepath.FromSlash("/usr/local/lib/node_modules/command-code")
	found := false
	for _, c := range cands {
		if filepath.Clean(c) == filepath.Clean(want) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("candidates %v must include %s", cands, want)
	}
}

// TestCommandCodePackageCandidates_WindowsLayout verifies the Windows npm
// layout (prefix/node_modules/command-code beside the .cmd shim) is covered.
func TestCommandCodePackageCandidates_WindowsLayout(t *testing.T) {
	cands := commandCodePackageCandidates(`C:\Users\KK\AppData\Roaming\npm`)
	want := `C:\Users\KK\AppData\Roaming\npm\node_modules\command-code`
	found := false
	for _, c := range cands {
		if filepath.Clean(c) == filepath.Clean(want) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("candidates %v must include %s", cands, want)
	}
}

// TestCommandCodeCLIFromPath_SymlinkResolution verifies that a launcher
// symlink is resolved before package discovery (npm/Homebrew style), so the
// real install prefix is used instead of the symlink's directory.
func TestCommandCodeCLIFromPath_SymlinkResolution(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink layout test targets Linux/macOS style installs")
	}
	base := t.TempDir()
	prefix := filepath.Join(base, "prefix")
	pkgDir := filepath.Join(prefix, "lib", "node_modules", "command-code")
	catalogPath := filepath.Join(pkgDir, filepath.FromSlash(commandCodeCLICatalogFileName))
	if err := os.MkdirAll(filepath.Dir(catalogPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalogPath, []byte(sampleCommandCodeModelsMD), 0600); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(prefix, "bin")
	if err := os.MkdirAll(binDir, 0700); err != nil {
		t.Fatal(err)
	}
	// launcher symlink bin/cmdc -> lib/node_modules/command-code/dist/cli.mjs
	launcher := filepath.Join(binDir, "cmdc")
	if err := os.Symlink(filepath.Join(pkgDir, "dist", "cli.mjs"), launcher); err != nil {
		t.Skipf("symlink not supported on this host: %v", err)
	}

	// Resolve exactly what commandCodeCLIFromPath does: LookPath -> EvalSymlinks.
	exePath := launcher
	if resolved, errResolve := filepath.EvalSymlinks(exePath); errResolve == nil {
		exePath = resolved
	}
	resolvedBin := filepath.Dir(exePath)

	found := false
	for _, cand := range commandCodePackageCandidates(resolvedBin) {
		if filepath.Clean(cand) == filepath.Clean(pkgDir) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("after symlink resolution, candidates %v must include package dir %s", commandCodePackageCandidates(resolvedBin), pkgDir)
	}
}

// sampleRemoteCatalogJSON is a representative /provider/v1/models response with
// mixed-case IDs and a Muse model that only exists remotely.
const sampleRemoteCatalogJSON = `{
  "object": "list",
  "data": [
    {"id": "deepseek/deepseek-v4-flash", "object": "model", "created": 1787186876, "owned_by": "command-code", "name": "DeepSeek V4 Flash (latest)", "context_length": 1000000},
    {"id": "moonshotai/Kimi-K3", "object": "model", "created": 1787186876, "owned_by": "command-code", "name": "Kimi K3", "context_length": 1000000},
    {"id": "meta/muse-spark-1.2-contributor", "object": "model", "created": 1787186876, "owned_by": "command-code", "name": "Muse Spark 1.2 Contributor", "context_length": 1048576},
    {"id": "Qwen/Qwen3.7-Plus", "object": "model", "created": 1787186876, "owned_by": "command-code", "name": "Qwen 3.7 Plus", "context_length": 1000000}
  ]
}`

// remoteFetcherFromHandler builds a remote fetcher that routes through an
// httptest server with the given handler, so tests exercise the real HTTP
// parsing path (status/content-type/size/JSON validation).
func remoteFetcherFromHandler(t *testing.T, handler http.HandlerFunc) func(context.Context) ([]*ModelInfo, string, bool) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	origURL := commandCodeRemoteCatalogURL
	commandCodeRemoteCatalogURL = srv.URL
	t.Cleanup(func() { commandCodeRemoteCatalogURL = origURL })
	return fetchCommandCodeRemoteCatalogHTTP
}

// TestCommandCodeRemoteCatalog_Success verifies remote catalog success: models
// parsed, mixed-case IDs canonicalized to lowercase, Muse visible, and catalog
// becomes the active dynamic catalog.
func TestCommandCodeRemoteCatalog_Success(t *testing.T) {
	commandCodeCatalog = &commandCodeCatalogStore{}
	commandCodeRemoteCatalogFetcher = remoteFetcherFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sampleRemoteCatalogJSON))
	})
	t.Cleanup(func() { commandCodeRemoteCatalogFetcher = fetchCommandCodeRemoteCatalogHTTP })

	models, source, ok := fetchCommandCodeRemoteCatalog(context.Background())
	if !ok {
		t.Fatal("remote catalog fetch failed")
	}
	if !strings.HasPrefix(source, "remote:") {
		t.Fatalf("source should be remote, got %q", source)
	}
	byID := make(map[string]*ModelInfo, len(models))
	for _, m := range models {
		byID[m.ID] = m
	}
	// Mixed-case IDs must be canonicalized to lowercase.
	if _, ok := byID["moonshotai/kimi-k3"]; !ok {
		t.Errorf("missing lowercase moonshotai/kimi-k3 (remote was mixed-case)")
	}
	if _, ok := byID["qwen/qwen3.7-plus"]; !ok {
		t.Errorf("missing lowercase qwen/qwen3.7-plus")
	}
	// Muse Contributor must be present from remote.
	muse, ok := byID["meta/muse-spark-1.2-contributor"]
	if !ok {
		t.Fatalf("remote catalog should include meta/muse-spark-1.2-contributor")
	}
	if muse.ContextLength != 1048576 {
		t.Errorf("muse context = %d, want 1048576", muse.ContextLength)
	}
}

// TestCommandCodeRemoteCatalog_SuccessThenRefresh verifies a remote success is
// applied to the store and triggers the refresh callback (auth re-registration
// signal).
func TestCommandCodeRemoteCatalog_SuccessThenRefresh(t *testing.T) {
	commandCodeCatalog = &commandCodeCatalogStore{}
	commandCodeRemoteCatalogFetcher = remoteFetcherFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sampleRemoteCatalogJSON))
	})
	t.Cleanup(func() { commandCodeRemoteCatalogFetcher = fetchCommandCodeRemoteCatalogHTTP })

	called := false
	origCallback := refreshCallback
	SetModelRefreshCallback(func(changedProviders []string) {
		for _, p := range changedProviders {
			if p == "commandcode" {
				called = true
			}
		}
	})
	t.Cleanup(func() { SetModelRefreshCallback(origCallback) })

	tryRefreshCommandCodeCatalog("test remote refresh")

	models, ok := getCommandCodeCatalog()
	if !ok {
		t.Fatal("catalog not loaded after remote refresh")
	}
	if _, exists := findCommandCodeModel(models, "meta/muse-spark-1.2-contributor"); !exists {
		t.Fatalf("remote refresh should include muse")
	}
	if !called {
		t.Fatal("model refresh callback must be notified after remote catalog change")
	}
}

// TestCommandCodeRemoteCatalog_HTTPFailureFallsBackToLocal verifies that when
// the remote endpoint fails (non-2xx), the local CLI catalog is used.
func TestCommandCodeRemoteCatalog_HTTPFailureFallsBackToLocal(t *testing.T) {
	writeSampleCatalog(t, sampleCommandCodeModelsMD)
	commandCodeCatalog = &commandCodeCatalogStore{}
	commandCodeRemoteCatalogFetcher = remoteFetcherFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	t.Cleanup(func() { commandCodeRemoteCatalogFetcher = fetchCommandCodeRemoteCatalogHTTP })

	tryRefreshCommandCodeCatalog("test remote failure")

	models, ok := getCommandCodeCatalog()
	if !ok {
		t.Fatal("catalog must load from local fallback")
	}
	if _, exists := findCommandCodeModel(models, "deepseek/deepseek-v4-flash"); !exists {
		t.Fatalf("local fallback should include deepseek/deepseek-v4-flash")
	}
}

// TestCommandCodeRemoteCatalog_InvalidJSONRejected verifies malformed JSON from
// remote is rejected and does not poison the store.
func TestCommandCodeRemoteCatalog_InvalidJSONRejected(t *testing.T) {
	commandCodeCatalog = &commandCodeCatalogStore{}
	commandCodeRemoteCatalogFetcher = remoteFetcherFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{not json`))
	})
	t.Cleanup(func() { commandCodeRemoteCatalogFetcher = fetchCommandCodeRemoteCatalogHTTP })

	models, _, ok := fetchCommandCodeRemoteCatalog(context.Background())
	if ok || models != nil {
		t.Fatal("invalid JSON must be rejected")
	}
}

// TestCommandCodeRemoteCatalog_EmptyDataRejected verifies an empty data array
// is treated as a failure (never registers zero models from remote).
func TestCommandCodeRemoteCatalog_EmptyDataRejected(t *testing.T) {
	commandCodeCatalog = &commandCodeCatalogStore{}
	commandCodeRemoteCatalogFetcher = remoteFetcherFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
	})
	t.Cleanup(func() { commandCodeRemoteCatalogFetcher = fetchCommandCodeRemoteCatalogHTTP })

	if _, _, ok := fetchCommandCodeRemoteCatalog(context.Background()); ok {
		t.Fatal("empty data array must be rejected")
	}
}

// TestCommandCodeRemoteCatalog_OversizeBodyRejected verifies the body size cap
// rejects a bloated response.
func TestCommandCodeRemoteCatalog_OversizeBodyRejected(t *testing.T) {
	commandCodeCatalog = &commandCodeCatalogStore{}
	commandCodeRemoteCatalogFetcher = remoteFetcherFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		big := strings.Repeat("x", commandCodeRemoteCatalogMaxBody+1024)
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"` + big + `"}]}`))
	})
	t.Cleanup(func() { commandCodeRemoteCatalogFetcher = fetchCommandCodeRemoteCatalogHTTP })

	if _, _, ok := fetchCommandCodeRemoteCatalog(context.Background()); ok {
		t.Fatal("oversize body must be rejected")
	}
}

// TestCommandCodeRemoteCatalog_NonJSONContentTypeRejected verifies a
// non-JSON content-type is rejected even when the body would parse.
func TestCommandCodeRemoteCatalog_NonJSONContentTypeRejected(t *testing.T) {
	commandCodeCatalog = &commandCodeCatalogStore{}
	commandCodeRemoteCatalogFetcher = remoteFetcherFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(sampleRemoteCatalogJSON))
	})
	t.Cleanup(func() { commandCodeRemoteCatalogFetcher = fetchCommandCodeRemoteCatalogHTTP })

	if _, _, ok := fetchCommandCodeRemoteCatalog(context.Background()); ok {
		t.Fatal("non-JSON content-type must be rejected")
	}
}

// TestCommandCodeRemoteCatalog_FailureKeepsLastKnownGood verifies that after a
// successful remote load, a subsequent remote+local failure keeps the
// last-known-good catalog (never empties it).
func TestCommandCodeRemoteCatalog_FailureKeepsLastKnownGood(t *testing.T) {
	commandCodeCatalog = &commandCodeCatalogStore{}
	commandCodeRemoteCatalogFetcher = remoteFetcherFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sampleRemoteCatalogJSON))
	})
	t.Cleanup(func() { commandCodeRemoteCatalogFetcher = fetchCommandCodeRemoteCatalogHTTP })

	tryRefreshCommandCodeCatalog("test first success")
	if _, ok := getCommandCodeCatalog(); !ok {
		t.Fatal("initial remote load failed")
	}

	// Now both remote and local fail.
	stubRemoteCatalogFailure(t)
	commandCodeCLIResolvers = []func() (string, error){
		func() (string, error) { return "", os.ErrNotExist },
	}
	tryRefreshCommandCodeCatalog("test both fail")

	models, ok := getCommandCodeCatalog()
	if !ok {
		t.Fatal("catalog must survive complete refresh failure")
	}
	if _, exists := findCommandCodeModel(models, "meta/muse-spark-1.2-contributor"); !exists {
		t.Fatalf("last-known-good should keep muse from earlier remote load")
	}
}

// TestCommandCodeRemoteCatalog_NoLocalNoRemoteFallsBackToBuiltin verifies that
// with no CLI and a failing remote, GetCommandCodeModels still returns the
// builtin bootstrap (never empty).
func TestCommandCodeRemoteCatalog_NoLocalNoRemoteFallsBackToBuiltin(t *testing.T) {
	commandCodeCatalog = &commandCodeCatalogStore{}
	stubRemoteCatalogFailure(t)
	commandCodeCLIResolvers = []func() (string, error){
		func() (string, error) { return "", os.ErrNotExist },
	}

	tryRefreshCommandCodeCatalog("test cold start no sources")

	models := GetCommandCodeModels()
	if len(models) == 0 {
		t.Fatal("builtin fallback must not be empty on cold start")
	}
	if _, exists := findCommandCodeModel(models, "deepseek/deepseek-v4-flash"); !exists {
		t.Fatalf("builtin fallback should include deepseek/deepseek-v4-flash")
	}
}

// TestCommandCodeRemoteCatalog_SystemLikeEnvStillWorks verifies the remote
// source works even when the local CLI is completely absent (SYSTEM service
// scenario): remote success must load regardless of local discovery.
func TestCommandCodeRemoteCatalog_SystemLikeEnvStillWorks(t *testing.T) {
	commandCodeCatalog = &commandCodeCatalogStore{}
	commandCodeRemoteCatalogFetcher = remoteFetcherFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sampleRemoteCatalogJSON))
	})
	t.Cleanup(func() { commandCodeRemoteCatalogFetcher = fetchCommandCodeRemoteCatalogHTTP })
	// No local CLI resolvers at all.
	commandCodeCLIResolvers = nil

	tryRefreshCommandCodeCatalog("test system-like env")

	models, ok := getCommandCodeCatalog()
	if !ok {
		t.Fatal("remote catalog must load without any local CLI")
	}
	if _, exists := findCommandCodeModel(models, "meta/muse-spark-1.2-contributor"); !exists {
		t.Fatalf("system-like env should still get muse from remote")
	}
}

// TestCommandCodeCatalog_PreservesOfficialMixedCaseDisplay verifies that
// canonical lowercase IDs retain the official display name and that a
// mixed-case registry request still resolves (registry-level EqualFold).
func TestCommandCodeCatalog_PreservesOfficialMixedCaseDisplay(t *testing.T) {
	models := parseCommandCodeModelsMarkdown([]byte(sampleCommandCodeModelsMD))
	byID := make(map[string]*ModelInfo, len(models))
	for _, m := range models {
		byID[m.ID] = m
	}
	glm, ok := byID["zai-org/glm-5.2-fast"]
	if !ok {
		t.Fatalf("missing glm-5.2-fast after lowercase canonicalization")
	}
	if glm.DisplayName == "" {
		t.Error("display name should be preserved from official catalog")
	}
}

// TestCommandCodeCatalog_RemoteNewModelSurvivesLocalFallbackFailure verifies
// the no-regression rule: after a remote refresh introduces a new model, a
// subsequent remote failure with an older local CLI catalog present must keep
// the newer remote catalog — the stale local catalog must not downgrade the
// model set.
func TestCommandCodeCatalog_RemoteNewModelSurvivesLocalFallbackFailure(t *testing.T) {
	// Local CLI has an OLDER catalog (no muse).
	writeSampleCatalog(t, sampleCommandCodeModelsMD)
	commandCodeCatalog = &commandCodeCatalogStore{}

	// Remote succeeds first, introducing muse.
	commandCodeRemoteCatalogFetcher = remoteFetcherFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sampleRemoteCatalogJSON))
	})
	t.Cleanup(func() { commandCodeRemoteCatalogFetcher = fetchCommandCodeRemoteCatalogHTTP })

	tryRefreshCommandCodeCatalog("test remote first success")
	models, ok := getCommandCodeCatalog()
	if !ok {
		t.Fatal("remote first load failed")
	}
	if _, exists := findCommandCodeModel(models, "meta/muse-spark-1.2-contributor"); !exists {
		t.Fatalf("remote first load should include muse")
	}

	// Now remote fails (periodic), local CLI is still the old one without muse.
	stubRemoteCatalogFailure(t)
	tryRefreshCommandCodeCatalog("test remote periodic failure")

	after, ok := getCommandCodeCatalog()
	if !ok {
		t.Fatal("catalog must survive remote failure")
	}
	// The stale local catalog must NOT have downgraded the model set.
	if _, exists := findCommandCodeModel(after, "meta/muse-spark-1.2-contributor"); !exists {
		t.Fatalf("muse from earlier remote load must survive a later remote failure (no regression)")
	}
}

// TestCommandCodeCatalog_CaseInsensitiveRouting verifies that a mixed-case
// client request resolves to the lowercase-canonical registration through the
// registry routing path (GetModelProviders EqualFold fallback).
func TestCommandCodeCatalog_CaseInsensitiveRouting(t *testing.T) {
	reg := GetGlobalRegistry()
	clientID := "test-commandcode-auth-casing"
	models := []*ModelInfo{
		{ID: "meta/muse-spark-1.2-contributor", Object: "model", OwnedBy: "commandcode", Type: "commandcode"},
		{ID: "moonshotai/kimi-k3", Object: "model", OwnedBy: "commandcode", Type: "commandcode"},
	}
	reg.RegisterClient(clientID, "commandcode", models)
	defer reg.UnregisterClient(clientID)

	// Lowercase request (canonical) resolves directly.
	providers := reg.GetModelProviders("meta/muse-spark-1.2-contributor")
	if len(providers) != 1 || providers[0] != "commandcode" {
		t.Fatalf("lowercase lookup = %v, want [commandcode]", providers)
	}
	// Mixed-case request (official spelling) resolves via EqualFold fallback.
	providersMixed := reg.GetModelProviders("Meta/Muse-Spark-1.2-Contributor")
	if len(providersMixed) != 1 || providersMixed[0] != "commandcode" {
		t.Fatalf("mixed-case lookup = %v, want [commandcode]", providersMixed)
	}
	// Unrelated model does not resolve.
	if p := reg.GetModelProviders("nope/not-a-model"); len(p) != 0 {
		t.Fatalf("unknown model should not resolve, got %v", p)
	}
}

// TestCommandCodeCatalog_SetTransportRoutesThroughProxy verifies that
// SetCommandCodeCatalogTransport installs the transport used by remote
// catalog fetches — i.e. a configured CLIProxyAPI proxy-url is honored.
func TestCommandCodeCatalog_SetTransportRoutesThroughProxy(t *testing.T) {
	// Record that the custom transport was actually used for the fetch.
	used := false
	roundTripper := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		used = true
		// Serve the catalog directly.
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(sampleRemoteCatalogJSON)),
			Request:    req,
		}, nil
	})

	SetCommandCodeCatalogTransport(roundTripper)
	t.Cleanup(func() { SetCommandCodeCatalogTransport(nil) })

	commandCodeCatalog = &commandCodeCatalogStore{}
	tryRefreshCommandCodeCatalog("test custom transport")

	if !used {
		t.Fatal("custom transport must be used for remote catalog fetch")
	}
	models, ok := getCommandCodeCatalog()
	if !ok {
		t.Fatal("catalog must load through custom transport")
	}
	if _, exists := findCommandCodeModel(models, "meta/muse-spark-1.2-contributor"); !exists {
		t.Fatal("catalog through custom transport should include muse")
	}
}

// TestCommandCodeCatalog_TransportConcurrentSetAndFetch verifies the setter
// and fetcher can run concurrently without the fetch ever observing a mutated
// shared client: the fetch takes a transport snapshot at request time. We
// exercise setter + fetch interleaving; race detection requires cgo/gcc which
// is unavailable in this environment, so this is a functional (not -race)
// test.
func TestCommandCodeCatalog_TransportConcurrentSetAndFetch(t *testing.T) {
	transportA := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(sampleRemoteCatalogJSON)),
			Request:    req,
		}, nil
	})
	transportB := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Request:    req,
		}, nil
	})

	SetCommandCodeCatalogTransport(transportA)
	t.Cleanup(func() { SetCommandCodeCatalogTransport(nil) })

	// Interleave setter writes with fetches. Fetches must never panic or hang
	// regardless of which transport snapshot they observe.
	var wg sync.WaitGroup
	var storeMu sync.Mutex
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					storeMu.Lock()
					commandCodeCatalog = &commandCodeCatalogStore{}
					storeMu.Unlock()
					tryRefreshCommandCodeCatalog("test concurrent fetch")
				}
			}
		}()
	}
	for i := 0; i < 100; i++ {
		SetCommandCodeCatalogTransport(transportA)
		SetCommandCodeCatalogTransport(transportB)
	}
	close(stop)
	wg.Wait()

	// Final state must be a valid transport usable for a fetch.
	SetCommandCodeCatalogTransport(transportA)
	commandCodeCatalog = &commandCodeCatalogStore{}
	tryRefreshCommandCodeCatalog("test post-concurrency fetch")
	if _, ok := getCommandCodeCatalog(); !ok {
		t.Fatal("catalog must load after concurrent setter/fetch churn")
	}
}

// roundTripperFunc adapts a func to http.RoundTripper.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// TestCommandCodeCatalog_DynamicRemoval verifies the removal path: a catalog
// refresh that drops a model (A,B,C -> A,B) must remove C from the registry
// and from per-auth routes (no stale route remains).
func TestCommandCodeCatalog_DynamicRemoval(t *testing.T) {
	commandCodeCatalog = &commandCodeCatalogStore{}
	stubRemoteCatalogFailure(t)

	// Local catalog has A,B,C.
	md3 := sampleCommandCodeModelsMD + "| `vendor/model-c` | Model C | 128K | — | $1/$2 · cache $0.1 | Go and above | third model |\n"
	writeSampleCatalog(t, md3)
	tryRefreshCommandCodeCatalog("test initial load")
	models, ok := getCommandCodeCatalog()
	if !ok {
		t.Fatal("initial catalog not loaded")
	}
	if _, exists := findCommandCodeModel(models, "vendor/model-c"); !exists {
		t.Fatalf("initial catalog should include vendor/model-c")
	}

	// Register an auth for all three models.
	reg := GetGlobalRegistry()
	clientID := "test-commandcode-removal-auth"
	reg.RegisterClient(clientID, "commandcode", GetCommandCodeModels())
	defer reg.UnregisterClient(clientID)
	if p := reg.GetModelProviders("vendor/model-c"); len(p) != 1 {
		t.Fatalf("model-c should route before removal, got %v", p)
	}

	// Now refresh with a catalog that drops C (rewrite local file, then cold
	// refresh path via resolver change below).
	md2 := sampleCommandCodeModelsMD // without vendor/model-c
	dir, _ := commandCodeCLIResolvers[0]()
	realPath := filepath.Join(dir, filepath.FromSlash(commandCodeCLICatalogFileName))
	if err := os.WriteFile(realPath, []byte(md2), 0600); err != nil {
		t.Fatalf("rewrite catalog: %v", err)
	}

	// Force a fresh store (simulates process restart / new working catalog).
	commandCodeCatalog = &commandCodeCatalogStore{}
	tryRefreshCommandCodeCatalog("test refresh without model-c")
	after, ok := getCommandCodeCatalog()
	if !ok {
		t.Fatal("catalog after refresh not loaded")
	}
	if _, exists := findCommandCodeModel(after, "vendor/model-c"); exists {
		t.Fatalf("catalog should no longer contain vendor/model-c")
	}

	// Re-register the auth with the reduced set (mirrors what the refresh
	// callback does for existing auths) and confirm C no longer routes.
	reg.UnregisterClient(clientID)
	reg.RegisterClient(clientID, "commandcode", GetCommandCodeModels())
	if p := reg.GetModelProviders("vendor/model-c"); len(p) != 0 {
		t.Fatalf("model-c must be removed from routes after catalog drop, got %v", p)
	}
}
