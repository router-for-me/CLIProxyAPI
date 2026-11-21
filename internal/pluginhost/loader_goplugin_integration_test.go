//go:build cgo && (linux || darwin || freebsd)

package pluginhost

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"plugin"
	"runtime/debug"
	"strings"
	"testing"
	"time"
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
