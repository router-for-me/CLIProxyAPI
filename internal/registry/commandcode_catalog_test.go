package registry

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
// the resolver at pkgDir. Returns pkgDir.
func writeSampleCatalog(t *testing.T, content string) string {
	t.Helper()
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
// visible without recompilation.
func TestCommandCodeCatalog_RefreshAddsNewModel(t *testing.T) {
	writeSampleCatalog(t, sampleCommandCodeModelsMD)

	// Fresh state for this test.
	commandCodeCatalog = &commandCodeCatalogStore{}

	tryRefreshCommandCodeCatalog("test initial")
	models, ok := getCommandCodeCatalog()
	if !ok {
		t.Fatal("catalog not loaded after initial refresh")
	}
	if _, exists := findCommandCodeModel(models, "vendor/new-model-test"); !exists {
		t.Fatalf("initial catalog should contain vendor/new-model-test")
	}
	if _, exists := findCommandCodeModel(models, "deepseek/deepseek-v4-flash"); !exists {
		t.Fatalf("initial catalog should contain deepseek/deepseek-v4-flash")
	}
	initialCount := len(models)

	// Append a brand new model to the file (simulating upstream adding it).
	newMD := sampleCommandCodeModelsMD + "\n| `future/model-9` | Future Model 9 | 128K | — | $1/$2 · cache $0.1 | Go and above | released tomorrow |\n"
	dir, _ := commandCodeCLIResolvers[0]()
	realPath := filepath.Join(dir, filepath.FromSlash(commandCodeCLICatalogFileName))
	if err := os.WriteFile(realPath, []byte(newMD), 0600); err != nil {
		t.Fatalf("rewrite catalog: %v", err)
	}

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
	if _, exists := findCommandCodeModel(models2, "vendor/new-model-test"); !exists {
		t.Fatalf("refresh must keep vendor/new-model-test")
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
