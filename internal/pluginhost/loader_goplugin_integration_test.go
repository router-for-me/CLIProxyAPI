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

	got := string(output)
	if !strings.Contains(got, "panic during plugin initialization") {
		t.Fatalf("the helper did not report the recovered initializer panic:\n%s", got)
	}
	if !strings.Contains(got, "cannot be retried") {
		t.Fatalf("the helper did not refuse to reopen the poisoned path:\n%s", got)
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
	if !strings.Contains(err.Error(), "panic during plugin initialization") {
		t.Fatalf("error %q does not mention the recovered initializer panic", err)
	}
	fmt.Printf("FIRST_OPEN_ERROR=%s\n", err)

	// The second open of the same path has to be refused right away. Go's
	// plugin cache keeps a placeholder for the path whose channel is closed
	// only after a fully loaded plugin, so a second plugin.Open on the path
	// would block forever.
	_, err = loader.Open(file, host)
	if err == nil {
		t.Fatal("expected the retry of the poisoned path to fail")
	}
	if !strings.Contains(err.Error(), "cannot be retried") {
		t.Fatalf("error %q does not explain that the path cannot be retried", err)
	}
	fmt.Printf("SECOND_OPEN_ERROR=%s\n", err)
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
	args := []string{"build", "-buildmode=plugin"}
	if goPluginRaceEnabled() {
		// A race-enabled host rejects a plugin that was built without the
		// race detector, so the plugin has to be built with the same flags.
		args = append(args, "-race")
	}
	args = append(args, "-o", outPath, ".")
	cmd := exec.Command(goTool, args...)
	cmd.Dir = filepath.Join("testdata", "loader_goplugin_panic")
	if output, errBuild := cmd.CombinedOutput(); errBuild != nil {
		t.Fatalf("build go test plugin: %v\n%s", errBuild, output)
	}
}

// goPluginRaceEnabled reports whether the test binary was built with the
// race detector.
func goPluginRaceEnabled() bool {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return false
	}
	for _, setting := range info.Settings {
		if setting.Key == "-race" {
			return setting.Value == "true"
		}
	}
	return false
}
