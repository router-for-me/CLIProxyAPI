//go:build cgo && (linux || darwin || freebsd)

package pluginhost

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func testHostCallbackID(t *testing.T, client *dynamicLibraryClient) uintptr {
	t.Helper()
	if client == nil || client.hostCtx == nil {
		t.Fatal("plugin client host context is nil")
	}
	return *(*uintptr)(client.hostCtx)
}

func buildReloadTestPlugin(t *testing.T, version int, outPath string) {
	t.Helper()
	goTool, errGo := exec.LookPath("go")
	if errGo != nil {
		t.Skipf("go toolchain unavailable: %v", errGo)
	}
	sourceDir := filepath.Join("testdata", "loader_unix_reload_plugin")
	cmd := exec.Command(goTool, "build", "-buildmode=c-shared", "-o", outPath, ".")
	cmd.Dir = sourceDir
	cflags := "-DPLUGIN_VERSION=1"
	if version >= 1 {
		cflags = fmt.Sprintf("-DPLUGIN_VERSION=%d", version)
	}
	cmd.Env = append(os.Environ(), "CGO_ENABLED=1", "CGO_CFLAGS="+cflags)
	if output, errBuild := cmd.CombinedOutput(); errBuild != nil {
		t.Fatalf("build test plugin (v%d): %v\n%s", version, errBuild, output)
	}
}

func TestUnixPluginReloadAfterShutdownUsesReplacedFile(t *testing.T) {
	dir := t.TempDir()
	ext := ".so"
	if runtime.GOOS == "darwin" {
		ext = ".dylib"
	}
	pluginPath := filepath.Join(dir, "alpha"+ext)
	buildReloadTestPlugin(t, 1, pluginPath)

	host := New()
	file := pluginFile{ID: "alpha", Path: pluginPath, Version: "1"}
	client, errOpen := dynamicLibraryLoader{}.Open(file, host)
	if errOpen != nil {
		t.Fatalf("Open() v1 error = %v", errOpen)
	}
	first, okFirst := client.(*dynamicLibraryClient)
	if !okFirst {
		t.Fatalf("Open() client type = %T, want *dynamicLibraryClient", client)
	}
	firstHandle := first.handle
	if firstHandle == nil {
		t.Fatal("first client handle is nil")
	}
	resp, errCall := client.Call(context.Background(), "test.marker", nil)
	if errCall != nil {
		t.Fatalf("Call() v1 error = %v", errCall)
	}
	if string(resp) != "v1" {
		t.Fatalf("Call() v1 response = %q, want v1", resp)
	}
	firstHostCtx := first.hostCtx
	firstCallbackID := testHostCallbackID(t, first)

	client.Shutdown()

	// The Go runtime of a loaded plugin stays resident in the host process
	// even after the logical shutdown: unloading a Go c-shared image is not
	// safe, so the handle and host callback state must be retained while the
	// callback instance is closed for further work.
	if first.handle == nil || first.handle != firstHandle {
		t.Fatalf("handle after Shutdown() = %v, want retained handle %v (library must stay mapped)", first.handle, firstHandle)
	}
	if first.api.call != nil || first.api.free_buffer != nil || first.api.shutdown != nil {
		t.Fatal("plugin API table not cleared by Shutdown(); detached client could still be called")
	}
	if first.hostCtx != firstHostCtx {
		t.Fatalf("hostCtx after Shutdown() = %v, want retained %v for the still-resident runtime", first.hostCtx, firstHostCtx)
	}
	if _, okEntry := hostCallbackEntries.Load(firstCallbackID); okEntry {
		t.Fatal("host callback entry retained by Shutdown(); repeated reloads would pin the host forever")
	}
	if first.instance == nil || !first.instance.closed.Load() {
		t.Fatal("callback instance not closed by Shutdown(); stale runtime could still perform work")
	}

	// Replace the plugin binary under the same path (update) and load again
	// without restarting the process. It must map the new image, not reuse
	// the previously loaded one.
	buildReloadTestPlugin(t, 2, pluginPath)
	file.Version = "2"
	secondClient, errOpenSecond := dynamicLibraryLoader{}.Open(file, host)
	if errOpenSecond != nil {
		t.Fatalf("Open() v2 error = %v", errOpenSecond)
	}
	second, okSecond := secondClient.(*dynamicLibraryClient)
	if !okSecond {
		t.Fatalf("Open() second client type = %T, want *dynamicLibraryClient", secondClient)
	}
	respSecond, errCallSecond := secondClient.Call(context.Background(), "test.marker", nil)
	if errCallSecond != nil {
		t.Fatalf("Call() v2 error = %v", errCallSecond)
	}
	if string(respSecond) != "v2" {
		t.Fatalf("Call() v2 response = %q, want v2 (replaced plugin file was not freshly mapped)", respSecond)
	}
	if second.handle == firstHandle {
		t.Fatal("second handle reuses the first image after the plugin file changed")
	}
	baselineCalls := testShutdownCallCount(t, secondClient)

	secondClient.Shutdown()

	// Reopening without a content change must reuse the already resident
	// image instead of mapping a second copy of the same runtime, and the
	// shutdown above must have reached that image exactly once.
	thirdClient, errOpenThird := dynamicLibraryLoader{}.Open(file, host)
	if errOpenThird != nil {
		t.Fatalf("Open() third error = %v", errOpenThird)
	}
	third, okThird := thirdClient.(*dynamicLibraryClient)
	if !okThird {
		t.Fatalf("Open() third client type = %T, want *dynamicLibraryClient", thirdClient)
	}
	if third.handle != second.handle {
		t.Fatalf("third handle = %v, want reused resident handle %v", third.handle, second.handle)
	}
	respThird, errCallThird := thirdClient.Call(context.Background(), "test.marker", nil)
	if errCallThird != nil {
		t.Fatalf("Call() third error = %v", errCallThird)
	}
	if string(respThird) != "v2" {
		t.Fatalf("Call() third response = %q, want v2", respThird)
	}
	if gotCalls := testShutdownCallCount(t, thirdClient); gotCalls != baselineCalls+1 {
		t.Fatalf("plugin shutdown calls = %d, want baseline %d + 1", gotCalls, baselineCalls)
	}
	thirdClient.Shutdown()
}

func testShutdownCallCount(t *testing.T, client pluginClient) int {
	t.Helper()
	calls, errCalls := client.Call(context.Background(), "test.shutdown_calls", nil)
	if errCalls != nil {
		t.Fatalf("Call() shutdown_calls error = %v", errCalls)
	}
	count, errParse := strconv.Atoi(string(calls))
	if errParse != nil {
		t.Fatalf("parse shutdown calls %q: %v", calls, errParse)
	}
	return count
}

func TestUnixPluginLoaderFallsBackToSharedCacheWhenLocalShadowDirNoexec(t *testing.T) {
	dir := t.TempDir()
	pluginPath := filepath.Join(dir, "noexec-fallback"+testPluginLibraryExtension())
	buildReloadTestPlugin(t, 1, pluginPath)

	localShadowDir := filepath.Clean(filepath.Join(dir, shadowPluginLocalDirName))
	origDlopen := unixDlopen
	t.Cleanup(func() {
		unixDlopen = origDlopen
	})
	unixDlopen = func(path string) unsafe.Pointer {
		if filepath.Clean(filepath.Dir(path)) == localShadowDir {
			return nil
		}
		return origDlopen(path)
	}

	host := New()
	file := pluginFile{ID: "noexec-fallback", Path: pluginPath, Version: "1"}
	client, errOpen := dynamicLibraryLoader{}.Open(file, host)
	if errOpen != nil {
		t.Fatalf("Open() with simulated noexec local .shadow error = %v, want fallback success", errOpen)
	}
	defer client.Shutdown()
	dlc, okType := client.(*dynamicLibraryClient)
	if !okType {
		t.Fatalf("Open() client type = %T, want *dynamicLibraryClient", client)
	}
	t.Cleanup(func() {
		removeShadowPluginDir(filepath.Dir(dlc.tempPath))
	})
	wantFallbackDir, errFallbackDir := shadowPluginFallbackDir(pluginPath)
	if errFallbackDir != nil {
		t.Fatalf("shadowPluginFallbackDir() error = %v", errFallbackDir)
	}
	if filepath.Clean(filepath.Dir(dlc.tempPath)) != filepath.Clean(wantFallbackDir) {
		t.Fatalf("loaded shadow dir = %q, want fallback dir %q", filepath.Dir(dlc.tempPath), wantFallbackDir)
	}
	resp, errCall := client.Call(context.Background(), "test.marker", nil)
	if errCall != nil {
		t.Fatalf("Call() fallback plugin error = %v", errCall)
	}
	if string(resp) != "v1" {
		t.Fatalf("Call() fallback plugin response = %q, want v1", resp)
	}
}

func TestUnixPluginLoaderLoadsFromReadOnlyPopulatedLocalShadowDir(t *testing.T) {
	dir := t.TempDir()
	pluginPath := filepath.Join(dir, "readonly-shadow"+testPluginLibraryExtension())
	buildReloadTestPlugin(t, 1, pluginPath)
	file := pluginFile{ID: "readonly-shadow", Path: pluginPath, Version: "1"}

	shadowPath, errShadow := shadowCopyPlugin(file)
	if errShadow != nil {
		t.Fatalf("shadowCopyPlugin() pre-populate error = %v", errShadow)
	}
	localShadowDir := filepath.Dir(shadowPath)
	probedPluginShadows.Delete(filepath.Base(shadowPath))

	if errChmod := os.Chmod(localShadowDir, 0o500); errChmod != nil {
		t.Fatalf("Chmod(localShadowDir) error = %v", errChmod)
	}
	t.Cleanup(func() {
		_ = os.Chmod(localShadowDir, 0o700)
	})

	host := New()
	client, errOpen := dynamicLibraryLoader{}.Open(file, host)
	if errOpen != nil {
		t.Fatalf("Open() with read-only populated .shadow error = %v, want success", errOpen)
	}
	defer client.Shutdown()
	resp, errCall := client.Call(context.Background(), "test.marker", nil)
	if errCall != nil {
		t.Fatalf("Call() plugin error = %v", errCall)
	}
	if string(resp) != "v1" {
		t.Fatalf("Call() plugin response = %q, want v1", resp)
	}
}

func TestUnixPluginOpenFailureKeepsImageUsableOnRetry(t *testing.T) {
	dir := t.TempDir()
	pluginPath := filepath.Join(dir, "failing"+testPluginLibraryExtension())
	buildReloadTestPlugin(t, 3, pluginPath)

	host := New()
	file := pluginFile{ID: "failing", Path: pluginPath, Version: "3"}
	loader := dynamicLibraryLoader{}
	if _, errOpen := loader.Open(file, host); errOpen == nil {
		t.Fatal("Open() with failing plugin init succeeded, want error")
	}

	// The failed load must not unload the image: retrying the same file
	// must fail the same way instead of crashing or spinning, and loading
	// a fixed build under the same path must succeed afterwards.
	if _, errOpen := loader.Open(file, host); errOpen == nil {
		t.Fatal("Open() retry with failing plugin init succeeded, want error")
	}

	buildReloadTestPlugin(t, 1, pluginPath)
	file.Version = "1"
	client, errOpen := loader.Open(file, host)
	if errOpen != nil {
		t.Fatalf("Open() fixed plugin error = %v", errOpen)
	}
	resp, errCall := client.Call(context.Background(), "test.marker", nil)
	if errCall != nil {
		t.Fatalf("Call() fixed plugin error = %v", errCall)
	}
	if string(resp) != "v1" {
		t.Fatalf("Call() fixed plugin response = %q, want v1", resp)
	}
	client.Shutdown()
}

func TestUnixPluginLoaderIsolatesSigillOnDlopen(t *testing.T) {
	dir := t.TempDir()
	pluginPath := filepath.Join(dir, "broken"+testPluginLibraryExtension())
	buildReloadTestPlugin(t, 4, pluginPath)

	host := New()
	file := pluginFile{ID: "broken", Path: pluginPath, Version: "4"}
	loader := dynamicLibraryLoader{}
	_, errOpen := loader.Open(file, host)
	if errOpen == nil {
		t.Fatal("Open() with SIGILL plugin succeeded, want error")
	}
	if !strings.Contains(errOpen.Error(), "preflight probe terminated by SIGILL: illegal instruction") {
		t.Fatalf("Open() error = %v, want preflight SIGILL diagnostic", errOpen)
	}

	// Retrying the same crashing plugin binary must fail cleanly without
	// crashing the host process.
	_, errRetry := loader.Open(file, host)
	if errRetry == nil {
		t.Fatal("Open() retry with SIGILL plugin succeeded, want error")
	}
	if !strings.Contains(errRetry.Error(), "preflight probe terminated by SIGILL: illegal instruction") {
		t.Fatalf("Open() retry error = %v, want cached preflight SIGILL diagnostic", errRetry)
	}

	// Applying host config with the crashing plugin enabled must isolate
	// the failure and keep the host process alive.
	host.ApplyConfig(context.Background(), &config.Config{
		Plugins: config.PluginsConfig{
			Enabled: true,
			Dir:     dir,
			Configs: enabledPluginConfigs("broken"),
		},
	})
	if host.PluginLoaded("broken") {
		t.Fatal("PluginLoaded(broken) = true, want false after preflight SIGILL failure")
	}

	// Replacing the broken binary with a valid build under the same path
	// must succeed without needing to restart the host process.
	buildReloadTestPlugin(t, 1, pluginPath)
	file.Version = "1"
	client, errFixed := loader.Open(file, host)
	if errFixed != nil {
		t.Fatalf("Open() fixed plugin after SIGILL error = %v", errFixed)
	}
	defer client.Shutdown()
	resp, errCall := client.Call(context.Background(), "test.marker", nil)
	if errCall != nil {
		t.Fatalf("Call() fixed plugin error = %v", errCall)
	}
	if string(resp) != "v1" {
		t.Fatalf("Call() fixed plugin response = %q, want v1", resp)
	}
}

func TestUnixPluginLoaderDoesNotCacheTransientProbeStartFailure(t *testing.T) {
	dir := t.TempDir()
	pluginPath := filepath.Join(dir, "transient"+testPluginLibraryExtension())
	buildReloadTestPlugin(t, 1, pluginPath)
	probedPluginShadows.Range(func(key, _ any) bool {
		probedPluginShadows.Delete(key)
		return true
	})

	origProbeExec := pluginProbeExecutable
	t.Cleanup(func() {
		pluginProbeExecutable = origProbeExec
	})
	pluginProbeExecutable = func() (string, error) {
		return "", errors.New("transient executable lookup failure")
	}

	host := New()
	file := pluginFile{ID: "transient", Path: pluginPath, Version: "1"}
	loader := dynamicLibraryLoader{}
	if _, errOpen := loader.Open(file, host); errOpen == nil {
		t.Fatal("Open() with transient probe executable failure succeeded, want error")
	}

	// Also verify a spawn failure (non-existent executable) is not cached.
	pluginProbeExecutable = func() (string, error) {
		return filepath.Join(dir, "nonexistent-probe-binary"), nil
	}
	if _, errOpen := loader.Open(file, host); errOpen == nil {
		t.Fatal("Open() with missing probe binary succeeded, want error")
	}

	// Once the transient failure resolves, the same plugin binary must load
	// without needing a content change or process restart.
	pluginProbeExecutable = origProbeExec
	client, errOpen := loader.Open(file, host)
	if errOpen != nil {
		t.Fatalf("Open() after transient probe failure recovered = %v, want nil", errOpen)
	}
	defer client.Shutdown()
	resp, errCall := client.Call(context.Background(), "test.marker", nil)
	if errCall != nil {
		t.Fatalf("Call() recovered plugin error = %v", errCall)
	}
	if string(resp) != "v1" {
		t.Fatalf("Call() recovered plugin response = %q, want v1", resp)
	}
}

func TestUnixPluginLoaderDoesNotCacheUnloadedProbeSuccess(t *testing.T) {
	dir := t.TempDir()
	pluginPath := filepath.Join(dir, "unloaded"+testPluginLibraryExtension())
	if errWrite := os.WriteFile(pluginPath, []byte("not-a-valid-shared-library"), 0o644); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}
	file := pluginFile{ID: "unloaded", Path: pluginPath, Version: "1"}
	loadPath, errShadow := shadowCopyPlugin(file)
	if errShadow != nil {
		t.Fatalf("shadowCopyPlugin() error = %v", errShadow)
	}
	cacheKey := filepath.Base(loadPath)
	removeShadowPlugin(loadPath)
	probedPluginShadows.Delete(cacheKey)

	host := New()
	loader := dynamicLibraryLoader{}
	if _, errOpen := loader.Open(file, host); errOpen == nil {
		t.Fatal("Open() with invalid shared library succeeded, want error")
	}
	if _, cached := probedPluginShadows.Load(cacheKey); cached {
		t.Fatalf("probedPluginShadows cached %q after dlopen returned NULL; subsequent retry would skip preflight probe", cacheKey)
	}
}

func TestUnixPluginLoaderRejectsPrematureExitZeroInConstructor(t *testing.T) {
	dir := t.TempDir()
	pluginPath := filepath.Join(dir, "early-exit"+testPluginLibraryExtension())
	buildReloadTestPlugin(t, 5, pluginPath)

	host := New()
	file := pluginFile{ID: "early-exit", Path: pluginPath, Version: "5"}
	loader := dynamicLibraryLoader{}
	_, errOpen := loader.Open(file, host)
	if errOpen == nil {
		t.Fatal("Open() with premature _exit(0) plugin succeeded, want error")
	}
	if !strings.Contains(errOpen.Error(), "preflight probe exited prematurely before completing initialization") {
		t.Fatalf("Open() error = %v, want premature exit diagnostic", errOpen)
	}
}

func TestUnixPluginLoaderDoesNotCacheSigkillDuringProbe(t *testing.T) {
	dir := t.TempDir()
	pluginPath := filepath.Join(dir, "killed"+testPluginLibraryExtension())
	buildReloadTestPlugin(t, 6, pluginPath)

	file := pluginFile{ID: "killed", Path: pluginPath, Version: "6"}
	loadPath, errShadow := shadowCopyPlugin(file)
	if errShadow != nil {
		t.Fatalf("shadowCopyPlugin() error = %v", errShadow)
	}
	cacheKey := filepath.Base(loadPath)
	removeShadowPlugin(loadPath)
	probedPluginShadows.Delete(cacheKey)

	host := New()
	loader := dynamicLibraryLoader{}
	if _, errOpen := loader.Open(file, host); errOpen == nil {
		t.Fatal("Open() with SIGKILL plugin succeeded, want error")
	}
	if _, cached := probedPluginShadows.Load(cacheKey); cached {
		t.Fatalf("probedPluginShadows cached %q after SIGKILL; transient external kill must not be permanently cached", cacheKey)
	}
}

func TestUnixPluginProbeDoesNotBlockOnInheritedStdioGrandchild(t *testing.T) {
	dir := t.TempDir()
	pluginPath := filepath.Join(dir, "grandchild"+testPluginLibraryExtension())
	buildReloadTestPlugin(t, 7, pluginPath)

	pidFile := filepath.Join(dir, "grandchild.pid")
	t.Setenv("CLIPROXY_TEST_GRANDCHILD_PID_FILE", pidFile)
	t.Cleanup(func() {
		raw, errRead := os.ReadFile(pidFile)
		if errRead != nil {
			return
		}
		pid, errParse := strconv.Atoi(strings.TrimSpace(string(raw)))
		if errParse == nil && pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})

	file := pluginFile{ID: "grandchild", Path: pluginPath, Version: "7"}
	loadPath, errShadow := shadowCopyPlugin(file)
	if errShadow != nil {
		t.Fatalf("shadowCopyPlugin() error = %v", errShadow)
	}
	t.Cleanup(func() {
		removeShadowPlugin(loadPath)
	})
	cacheKey := filepath.Base(loadPath)
	probedPluginShadows.Delete(cacheKey)

	if errProbe := probePluginLibrary(file.Path, loadPath); errProbe != nil {
		t.Fatalf("probePluginLibrary() error = %v, want nil without blocking on grandchild stdio", errProbe)
	}
	raw, errRead := os.ReadFile(pidFile)
	if errRead != nil {
		t.Fatalf("ReadFile(%s) error = %v, want grandchild pid recorded", pidFile, errRead)
	}
	pid, errParse := strconv.Atoi(strings.TrimSpace(string(raw)))
	if errParse != nil || pid <= 0 {
		t.Fatalf("grandchild pid = %q (%v), want positive pid", string(raw), errParse)
	}
}

func TestUnixPluginProbeStoredHostCallbackAndLargeStderrSubprocess(t *testing.T) {
	dir := t.TempDir()
	pluginPath := filepath.Join(dir, "noisy-shutdown"+testPluginLibraryExtension())
	buildReloadTestPlugin(t, 8, pluginPath)

	file := pluginFile{ID: "noisy-shutdown", Path: pluginPath, Version: "8"}
	loadPath, errShadow := shadowCopyPlugin(file)
	if errShadow != nil {
		t.Fatalf("shadowCopyPlugin() error = %v", errShadow)
	}
	t.Cleanup(func() {
		removeShadowPlugin(loadPath)
	})
	cacheKey := filepath.Base(loadPath)
	probedPluginShadows.Delete(cacheKey)

	errProbe := probePluginLibrary(file.Path, loadPath)
	if errProbe == nil {
		t.Fatal("probePluginLibrary() with noisy _exit(1) shutdown succeeded, want error")
	}
	if len(errProbe.Error()) > maxPluginProbeOutputBytes+512 {
		t.Fatalf("probePluginLibrary() error length = %d, exceeds bounded limit", len(errProbe.Error()))
	}
}

func TestLimitedProbeBufferCapsRetainedOutputWithoutBlocking(t *testing.T) {
	buf := newLimitedProbeBuffer(16)
	n1, err1 := buf.Write([]byte("0123456789"))
	if err1 != nil || n1 != 10 {
		t.Fatalf("first Write() = (%d, %v), want (10, nil)", n1, err1)
	}
	n2, err2 := buf.Write([]byte("abcdefghijklmnop"))
	if err2 != nil || n2 != 16 {
		t.Fatalf("second Write() = (%d, %v), want (16, nil)", n2, err2)
	}
	if got := buf.String(); got != "0123456789abcdef" {
		t.Fatalf("String() = %q, want %q", got, "0123456789abcdef")
	}
}
