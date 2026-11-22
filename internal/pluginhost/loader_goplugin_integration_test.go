//go:build cgo && (linux || darwin || freebsd)

package pluginhost

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"plugin"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// goPluginHelperTimeout bounds the helper process of the real plugin test.
//
// The helper has to finish quickly: a second open of a poisoned path either
// returns an error right away or blocks forever inside Go's plugin cache.
const goPluginHelperTimeout = 45 * time.Second

// The helper prints the outcome of both opens behind these labels so that
// the parent process asserts on the exact output lines instead of on a bare
// substring of the whole helper output.
const (
	goPluginFirstOpenLabel  = "FIRST_OPEN_ERROR="
	goPluginSecondOpenLabel = "SECOND_OPEN_ERROR="
)

// TestGoPluginLoaderRealPluginInitPanic checks the two Go runtime facts the
// Go-native loader relies on against a real plugin.Open:
//
//  1. a plugin whose package initializer panics is reported as an ordinary
//     load error instead of tearing the host process down, and
//  2. the same path is never opened again.
//
// The mocked loader tests only prove the host's own state handling. Here the
// assertions run in a helper process because Go's plugin cache is process
// wide: the panicking initializer leaves a placeholder behind that blocks
// every later open of the same path, so the test binary must not be the one
// that runs the poisoned path.
func TestGoPluginLoaderRealPluginInitPanic(t *testing.T) {
	pluginPath := filepath.Join(t.TempDir(), "loader_goplugin_panic.so")
	buildGoPluginTestPlugin(t, pluginPath)

	ctx, cancel := context.WithTimeout(context.Background(), 2*goPluginHelperTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestGoPluginHelperRealOpen$", fmt.Sprintf("-test.timeout=%s", goPluginHelperTimeout))
	cmd.Env = append(os.Environ(),
		"GO_WANT_GOPLUGIN_HELPER=1",
		"CLIPROXY_GOPLUGIN_HELPER_PLUGIN="+pluginPath,
	)
	output, errRun := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("the go plugin helper process did not finish within %s: %v\n%s", 2*goPluginHelperTimeout, ctx.Err(), output)
	}
	if errRun != nil {
		t.Fatalf("go plugin helper process error = %v\n%s", errRun, output)
	}

	first := goPluginHelperResult(t, string(output), goPluginFirstOpenLabel)
	if !strings.Contains(first, "panic during plugin initialization") {
		t.Fatalf("the helper did not report the recovered initializer panic: %s\n%s", first, output)
	}
	second := goPluginHelperResult(t, string(output), goPluginSecondOpenLabel)
	if !strings.Contains(second, "cannot be retried") {
		t.Fatalf("the helper did not refuse to reopen the poisoned path: %s\n%s", second, output)
	}
}

// TestGoPluginHelperRealOpen is the helper process of
// TestGoPluginLoaderRealPluginInitPanic. It opens the real Go plugin whose
// initializer panics and checks that the host recovers the panic and refuses
// to open the same path again.
//
// The helper skips itself in a normal test run.
func TestGoPluginHelperRealOpen(t *testing.T) {
	if os.Getenv("GO_WANT_GOPLUGIN_HELPER") != "1" {
		t.Skip("go plugin helper entry point")
	}
	pluginPath := os.Getenv("CLIPROXY_GOPLUGIN_HELPER_PLUGIN")
	if pluginPath == "" {
		t.Fatal("CLIPROXY_GOPLUGIN_HELPER_PLUGIN is not set")
	}

	loader := goPluginLoader{
		shadow: passthroughShadow,
		open:   plugin.Open,
		lookup: (*plugin.Plugin).Lookup,
	}
	file := pluginFile{ID: "goplugin-panic", Path: pluginPath}
	host := &Host{loading: map[string]*pluginLoadRequest{}}

	_, err := loader.Open(file, host)
	if err == nil {
		t.Fatal("expected the initializer panic to surface as an error")
	}
	// Print the raw error before the assertions below so that the
	// original Go error is part of the helper output.
	fmt.Printf("%s%s\n", goPluginFirstOpenLabel, err)
	if goPluginBuildConfigMismatch(err) {
		// The runtime rejected the plugin before the initializer ran
		// because the plugin was not built with the configuration of the
		// host binary. That is a build configuration problem, not a
		// plugin problem, so it is reported as such.
		t.Fatalf("the plugin was built with a different configuration than the host binary: %v\nmirrored build configuration: %s", err, goPluginHostBuildConfig().Describe())
	}
	if !strings.Contains(err.Error(), "panic during plugin initialization") {
		t.Fatalf("error %q does not mention the recovered initializer panic", err)
	}

	// The second open of the same path has to be refused right away. Go's
	// plugin cache keeps a placeholder for the path whose channel is closed
	// only after a fully loaded plugin, so a second plugin.Open on the path
	// would block forever.
	_, err = loader.Open(file, host)
	if err == nil {
		t.Fatal("expected the retry of the poisoned path to fail")
	}
	fmt.Printf("%s%s\n", goPluginSecondOpenLabel, err)
	if !strings.Contains(err.Error(), "cannot be retried") {
		t.Fatalf("error %q does not explain that the path cannot be retried", err)
	}
}

// goPluginHelperResult returns the value the helper printed behind label.
//
// The helper labels its results so that the parent process cannot mistake a
// failure message of the helper itself for a result line.
func goPluginHelperResult(t *testing.T, output, label string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, label) {
			return strings.TrimSpace(strings.TrimPrefix(line, label))
		}
	}
	t.Fatalf("the go plugin helper did not print a %s line:\n%s", label, output)
	return ""
}

// buildGoPluginTestPlugin builds the testdata Go plugin into outPath.
//
// The plugin is built from its own module so it does not depend on the host
// module: a Go plugin has to be built with the same toolchain and an
// identical dependency graph as the host binary.
func buildGoPluginTestPlugin(t *testing.T, outPath string) {
	t.Helper()
	goTool, errGo := exec.LookPath("go")
	if errGo != nil {
		t.Skipf("go toolchain unavailable: %v", errGo)
	}
	config := goPluginHostBuildConfig()
	args := append([]string{"build", "-buildmode=plugin"}, config.Flags...)
	args = append(args, "-o", outPath, ".")
	cmd := exec.Command(goTool, args...)
	cmd.Dir = filepath.Join("testdata", "loader_goplugin_panic")
	cmd.Env = config.Env
	if output, errBuild := cmd.CombinedOutput(); errBuild != nil {
		t.Fatalf("build go test plugin: %v\nbuild configuration: %s\n%s", errBuild, config.Describe(), output)
	}
}

// goPluginBuildConfig describes the host build configuration that the plugin
// build has to mirror.
//
// A Go plugin is only accepted by a host binary that compiled the shared
// packages with the same configuration: the runtime compares the package
// fingerprints of both and refuses the plugin with "plugin was built with a
// different version of package ..." otherwise.
type goPluginBuildConfig struct {
	// Flags are the go build flags that reproduce the host configuration.
	Flags []string
	// Env is the environment of the plugin build.
	Env []string
	// Overrides are the environment entries the plugin build sets on top of
	// the inherited environment.
	Overrides []string
}

// Describe renders the configuration for diagnostics.
func (c goPluginBuildConfig) Describe() string {
	return fmt.Sprintf("flags=%v env overrides=%v", c.Flags, c.Overrides)
}

// goPluginHostBuildConfig collects the build configuration of the host test
// binary and turns it into the flags and the environment of the plugin
// build.
//
// The settings are read from the build info of the running binary, which
// records the flags the binary was built with. GOFLAGS is not part of the
// recorded settings but changes how the plugin is compiled as well, so it is
// taken from the environment. GOEXPERIMENT is pinned to the value the host
// binary was built with: the running environment may differ from it, and the
// plugin has to match the binary it is loaded into.
func goPluginHostBuildConfig() goPluginBuildConfig {
	settings := goPluginBuildSettings()
	config := goPluginBuildConfig{}
	// The instrumentation modes change the generated code of every shared
	// package, so the plugin has to be built the same way.
	for _, mode := range []string{"race", "msan", "asan"} {
		if settings["-"+mode] == "true" {
			config.Flags = append(config.Flags, "-"+mode)
		}
	}
	// Build tags select the source files of a package.
	if tags := settings["-tags"]; tags != "" {
		config.Flags = append(config.Flags, "-tags="+tags)
	}
	// -trimpath rewrites the recorded source paths and therefore changes
	// the fingerprint of the shared packages.
	if settings["-trimpath"] == "true" {
		config.Flags = append(config.Flags, "-trimpath")
	}
	env := os.Environ()
	if experiment := settings["GOEXPERIMENT"]; experiment != "" {
		// The host binary was built with an explicit experiment set: the
		// plugin build has to use the very same one.
		env = append(env, "GOEXPERIMENT="+experiment)
		config.Overrides = append(config.Overrides, "GOEXPERIMENT="+experiment)
	} else {
		// The host binary was built with the default experiment set of the
		// toolchain: drop an inherited GOEXPERIMENT so that the plugin
		// build uses the same default.
		env = goPluginEnvWithout(env, "GOEXPERIMENT")
	}
	if flags := os.Getenv("GOFLAGS"); flags != "" {
		env = append(env, "GOFLAGS="+flags)
		config.Overrides = append(config.Overrides, "GOFLAGS="+flags)
	}
	// The testdata plugin is a module of its own: it must not be resolved
	// through a go.work file that covers the repository.
	env = append(env, "GOWORK=off")
	config.Overrides = append(config.Overrides, "GOWORK=off")
	config.Env = env
	return config
}

// goPluginBuildSettings returns the build settings of the running test
// binary.
func goPluginBuildSettings() map[string]string {
	settings := map[string]string{}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return settings
	}
	for _, setting := range info.Settings {
		settings[setting.Key] = setting.Value
	}
	return settings
}

// goPluginEnvWithout returns env without the variable named key.
func goPluginEnvWithout(env []string, key string) []string {
	prefix := key + "="
	filtered := make([]string, 0, len(env))
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

// goPluginBuildConfigMismatch reports whether the Go runtime rejected the
// plugin because it was built with a different configuration than the host
// binary.
func goPluginBuildConfigMismatch(err error) bool {
	return err != nil && strings.Contains(err.Error(), "different version of package")
}

// The helpers of the build identity tests print their results behind these
// labels, so the parent process asserts on the exact output lines.
const (
	goPluginReplacementFirstLabel  = "REPLACEMENT_FIRST_ERROR="
	goPluginReplacementSecondLabel = "REPLACEMENT_SECOND_ERROR="
	goPluginSharedOpenLabel        = "SHARED_OPEN_CALLS="
	goPluginSharedFactoryLabel     = "SHARED_FACTORY_CALLS="
)

// runGoPluginHelper runs the named helper entry point in a subprocess and
// returns its combined output.
//
// The helper has to run in a subprocess because Go's plugin cache is process
// wide: a build identity that was loaded once can never be opened again in
// the same process, so the parent test binary must not load the plugins
// itself.
func runGoPluginHelper(t *testing.T, helper string, env map[string]string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*goPluginHelperTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^"+helper+"$", fmt.Sprintf("-test.timeout=%s", goPluginHelperTimeout))
	cmd.Env = append(os.Environ(), "GO_WANT_GOPLUGIN_HELPER=1")
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	output, errRun := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("the go plugin helper process did not finish within %s: %v\n%s", 2*goPluginHelperTimeout, ctx.Err(), output)
	}
	if errRun != nil {
		t.Fatalf("go plugin helper process error = %v\n%s", errRun, output)
	}
	return string(output)
}

// buildGoPluginOKTestPlugin builds the well-behaved testdata Go plugin into
// outPath. It mirrors the host build configuration the same way the panicking
// plugin build does, so plugin.Open accepts the result.
func buildGoPluginOKTestPlugin(t *testing.T, outPath string) {
	t.Helper()
	goTool, errGo := exec.LookPath("go")
	if errGo != nil {
		t.Skipf("go toolchain unavailable: %v", errGo)
	}
	config := goPluginHostBuildConfig()
	args := append([]string{"build", "-buildmode=plugin"}, config.Flags...)
	args = append(args, "-o", outPath, ".")
	cmd := exec.Command(goTool, args...)
	cmd.Dir = filepath.Join("testdata", "loader_goplugin_ok")
	cmd.Env = config.Env
	if output, errBuild := cmd.CombinedOutput(); errBuild != nil {
		t.Fatalf("build go test plugin: %v\nbuild configuration: %s\n%s", errBuild, config.Describe(), output)
	}
}

// buildGoPluginOKTestPluginVariant builds a second, content-different build
// of the well-behaved test plugin: the source is copied to a scratch
// directory and the marker value is changed. The module path is kept, so the
// build carries the same package identity as the first build while having a
// different content digest.
func buildGoPluginOKTestPluginVariant(t *testing.T, outPath string) {
	t.Helper()
	goTool, errGo := exec.LookPath("go")
	if errGo != nil {
		t.Skipf("go toolchain unavailable: %v", errGo)
	}
	sourceDir := filepath.Join("testdata", "loader_goplugin_ok")
	scratchDir := filepath.Join(t.TempDir(), "loader_goplugin_ok")
	if errMkdir := os.MkdirAll(scratchDir, 0o700); errMkdir != nil {
		t.Fatalf("create the scratch module directory: %v", errMkdir)
	}
	for _, name := range []string{"go.mod", "main.go"} {
		content, errRead := os.ReadFile(filepath.Join(sourceDir, name))
		if errRead != nil {
			t.Fatalf("read %s: %v", name, errRead)
		}
		if name == "main.go" {
			content = bytes.Replace(content, []byte(`return "ok"`), []byte(`return "ok-two"`), 1)
			if bytes.Contains(content, []byte(`return "ok"`)) || !bytes.Contains(content, []byte(`return "ok-two"`)) {
				t.Fatal("the marker value of the variant build was not changed")
			}
		}
		if errWrite := os.WriteFile(filepath.Join(scratchDir, name), content, 0o600); errWrite != nil {
			t.Fatalf("write %s: %v", name, errWrite)
		}
	}
	config := goPluginHostBuildConfig()
	args := append([]string{"build", "-buildmode=plugin"}, config.Flags...)
	args = append(args, "-o", outPath, ".")
	cmd := exec.Command(goTool, args...)
	cmd.Dir = scratchDir
	cmd.Env = config.Env
	if output, errBuild := cmd.CombinedOutput(); errBuild != nil {
		t.Fatalf("build go test plugin variant: %v\nbuild configuration: %s\n%s", errBuild, config.Describe(), output)
	}
}

// goPluginHelperFactory is the loader's lookup seam for the well-behaved
// test plugin: the plugin does not export New, so the factory is injected and
// counted instead.
func goPluginHelperFactory(calls *int32) func(*plugin.Plugin, string) (plugin.Symbol, error) {
	return func(*plugin.Plugin, string) (plugin.Symbol, error) {
		return plugin.Symbol(func(pluginapi.GoPluginCaller) (pluginapi.GoPlugin, error) {
			if calls != nil {
				atomic.AddInt32(calls, 1)
			}
			return &fakeGoPlugin{}, nil
		}), nil
	}
}

// TestGoPluginLoaderRealPluginReplacementDiagnosis checks the loader's
// behavior for a rebuilt plugin against a real plugin.Open: the same package
// built twice has two different content digests, so the second build is not
// covered by the digest sharing, and Go's runtime refuses it with "plugin
// already loaded" because it identifies plugins by their build identity. The
// loader has to report that condition as "requires restarting the host"
// instead of the generic open failure hint.
func TestGoPluginLoaderRealPluginReplacementDiagnosis(t *testing.T) {
	dir := t.TempDir()
	firstPath := filepath.Join(dir, "loader_goplugin_ok_v1.so")
	secondPath := filepath.Join(dir, "loader_goplugin_ok_v2.so")
	buildGoPluginOKTestPlugin(t, firstPath)
	buildGoPluginOKTestPluginVariant(t, secondPath)

	// The variant build differs in content but keeps the same module path,
	// so both builds carry the same package identity: exactly the situation
	// of a plugin that was fixed and rebuilt.
	firstBytes, errRead := os.ReadFile(firstPath)
	if errRead != nil {
		t.Fatalf("read the first build: %v", errRead)
	}
	secondBytes, errRead := os.ReadFile(secondPath)
	if errRead != nil {
		t.Fatalf("read the second build: %v", errRead)
	}
	if bytes.Equal(firstBytes, secondBytes) {
		t.Fatal("the variant build is byte identical to the first build, so the second build would not exercise plugin.Open")
	}

	output := runGoPluginHelper(t, "TestGoPluginHelperReplacementDiagnosis", map[string]string{
		"CLIPROXY_GOPLUGIN_HELPER_PLUGIN_V1": firstPath,
		"CLIPROXY_GOPLUGIN_HELPER_PLUGIN_V2": secondPath,
	})

	first := goPluginHelperResult(t, output, goPluginReplacementFirstLabel)
	if first != "" {
		t.Fatalf("the first build must load, got error: %s\n%s", first, output)
	}
	second := goPluginHelperResult(t, output, goPluginReplacementSecondLabel)
	if !strings.Contains(second, "requires restarting the host") {
		t.Fatalf("the second build must be reported as requiring a host restart: %s\n%s", second, output)
	}
}

// TestGoPluginHelperReplacementDiagnosis is the helper process of
// TestGoPluginLoaderRealPluginReplacementDiagnosis. It loads the first build
// of a plugin through a real plugin.Open and then tries the second build,
// which Go's runtime refuses because the build identity is already loaded.
func TestGoPluginHelperReplacementDiagnosis(t *testing.T) {
	if os.Getenv("GO_WANT_GOPLUGIN_HELPER") != "1" {
		t.Skip("go plugin helper entry point")
	}
	firstPath := os.Getenv("CLIPROXY_GOPLUGIN_HELPER_PLUGIN_V1")
	secondPath := os.Getenv("CLIPROXY_GOPLUGIN_HELPER_PLUGIN_V2")
	if firstPath == "" || secondPath == "" {
		t.Fatal("the helper plugin paths are not set")
	}

	loader := goPluginLoader{
		shadow: passthroughShadow,
		open:   plugin.Open,
		lookup: goPluginHelperFactory(nil),
	}
	host := &Host{loading: map[string]*pluginLoadRequest{}}

	if _, err := loader.Open(pluginFile{ID: "ok-v1", Path: firstPath}, host); err != nil {
		if goPluginBuildConfigMismatch(err) {
			t.Fatalf("the plugin was built with a different configuration than the host binary: %v\nmirrored build configuration: %s", err, goPluginHostBuildConfig().Describe())
		}
		fmt.Printf("%s%s\n", goPluginReplacementFirstLabel, err)
		t.Fatalf("the first build must load: %v", err)
	}
	fmt.Printf("%s\n", goPluginReplacementFirstLabel)

	_, err := loader.Open(pluginFile{ID: "ok-v2", Path: secondPath}, host)
	if err == nil {
		fmt.Printf("%s\n", goPluginReplacementSecondLabel)
		t.Fatal("the second build of the same plugin identity must not load in the same process")
	}
	fmt.Printf("%s%s\n", goPluginReplacementSecondLabel, err)
	if !strings.Contains(err.Error(), "requires restarting the host") {
		t.Fatalf("error %q does not explain that replacing the plugin requires restarting the host", err)
	}
}

// TestGoPluginLoaderRealPluginSameBuildShared checks the digest sharing
// against a real plugin.Open: the same build copied under a second file name
// loads successfully, plugin.Open is called exactly once for the image, and
// each of the two hosts gets its own plugin instance through its own New
// call.
func TestGoPluginLoaderRealPluginSameBuildShared(t *testing.T) {
	dir := t.TempDir()
	firstPath := filepath.Join(dir, "loader_goplugin_ok_v1.so")
	copiedPath := filepath.Join(dir, "loader_goplugin_ok_copy.so")
	buildGoPluginOKTestPlugin(t, firstPath)

	firstBytes, errRead := os.ReadFile(firstPath)
	if errRead != nil {
		t.Fatalf("read the build: %v", errRead)
	}
	if errCopy := os.WriteFile(copiedPath, firstBytes, 0o600); errCopy != nil {
		t.Fatalf("copy the build: %v", errCopy)
	}

	output := runGoPluginHelper(t, "TestGoPluginHelperSameBuildShared", map[string]string{
		"CLIPROXY_GOPLUGIN_HELPER_PLUGIN_V1": firstPath,
		"CLIPROXY_GOPLUGIN_HELPER_PLUGIN_V2": copiedPath,
	})

	if got := goPluginHelperResult(t, output, goPluginSharedOpenLabel); got != "1" {
		t.Fatalf("plugin.Open was called %s times, want 1: the second host must reuse the opened image\n%s", got, output)
	}
	if got := goPluginHelperResult(t, output, goPluginSharedFactoryLabel); got != "2" {
		t.Fatalf("the New factory was called %s times, want 2: each host must get its own instance\n%s", got, output)
	}
}

// TestGoPluginHelperSameBuildShared is the helper process of
// TestGoPluginLoaderRealPluginSameBuildShared. It loads the same plugin build
// from two different paths through a real plugin.Open and counts both the
// plugin.Open calls and the New factory calls.
func TestGoPluginHelperSameBuildShared(t *testing.T) {
	if os.Getenv("GO_WANT_GOPLUGIN_HELPER") != "1" {
		t.Skip("go plugin helper entry point")
	}
	firstPath := os.Getenv("CLIPROXY_GOPLUGIN_HELPER_PLUGIN_V1")
	secondPath := os.Getenv("CLIPROXY_GOPLUGIN_HELPER_PLUGIN_V2")
	if firstPath == "" || secondPath == "" {
		t.Fatal("the helper plugin paths are not set")
	}

	var openCalls, factoryCalls int32
	loader := goPluginLoader{
		shadow: passthroughShadow,
		open: func(path string) (*plugin.Plugin, error) {
			atomic.AddInt32(&openCalls, 1)
			return plugin.Open(path)
		},
		lookup: goPluginHelperFactory(&factoryCalls),
	}
	host := &Host{loading: map[string]*pluginLoadRequest{}}

	for _, path := range []string{firstPath, secondPath} {
		if _, err := loader.Open(pluginFile{ID: filepath.Base(path), Path: path}, host); err != nil {
			if goPluginBuildConfigMismatch(err) {
				t.Fatalf("the plugin was built with a different configuration than the host binary: %v\nmirrored build configuration: %s", err, goPluginHostBuildConfig().Describe())
			}
			t.Fatalf("the build must load from %s: %v", path, err)
		}
	}
	fmt.Printf("%s%d\n", goPluginSharedOpenLabel, atomic.LoadInt32(&openCalls))
	fmt.Printf("%s%d\n", goPluginSharedFactoryLabel, atomic.LoadInt32(&factoryCalls))
}
