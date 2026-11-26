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
	"time"
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

	// Reopening without a content change must map a fresh image: native Go
	// images stay mapped after shutdown, and reusing one would also reuse
	// its shut-down package globals (see TestAcquireShadowPluginLoadDoesNotReuseResidentImage).
	thirdClient, errOpenThird := dynamicLibraryLoader{}.Open(file, host)
	if errOpenThird != nil {
		t.Fatalf("Open() third error = %v", errOpenThird)
	}
	third, okThird := thirdClient.(*dynamicLibraryClient)
	if !okThird {
		t.Fatalf("Open() third client type = %T, want *dynamicLibraryClient", thirdClient)
	}
	if third.handle == nil {
		t.Fatal("third client handle is nil")
	}
	if third.handle == second.handle || third.handle == firstHandle {
		t.Fatalf("third handle = %v, must not reuse prior handles (first=%v, second=%v)", third.handle, firstHandle, second.handle)
	}
	if third.tempPath == second.tempPath {
		t.Fatalf("third tempPath = %q, want distinct reload shadow copy", third.tempPath)
	}
	respThird, errCallThird := thirdClient.Call(context.Background(), "test.marker", nil)
	if errCallThird != nil {
		t.Fatalf("Call() third error = %v", errCallThird)
	}
	if string(respThird) != "v2" {
		t.Fatalf("Call() third response = %q, want v2", respThird)
	}
	if gotCalls := testShutdownCallCount(t, thirdClient); gotCalls != baselineCalls {
		t.Fatalf("plugin shutdown calls = %d, want fresh image baseline %d", gotCalls, baselineCalls)
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

func splitCCCommand(raw string) ([]string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("empty compiler command")
	}
	if _, errLookup := exec.LookPath(trimmed); errLookup == nil {
		return []string{trimmed}, nil
	}
	var args []string
	var cur strings.Builder
	inToken := false
	inSingle := false
	inDouble := false
	escaped := false
	for i := 0; i < len(trimmed); i++ {
		c := trimmed[i]
		if escaped {
			cur.WriteByte(c)
			escaped = false
			inToken = true
			continue
		}
		if inSingle {
			if c == '\'' {
				inSingle = false
			} else {
				cur.WriteByte(c)
			}
			inToken = true
			continue
		}
		if inDouble {
			if c == '"' {
				inDouble = false
			} else if c == '\\' {
				escaped = true
			} else {
				cur.WriteByte(c)
			}
			inToken = true
			continue
		}
		switch c {
		case '\\':
			escaped = true
			inToken = true
		case '\'':
			inSingle = true
			inToken = true
		case '"':
			inDouble = true
			inToken = true
		case ' ', '\t', '\n', '\r':
			if inToken {
				args = append(args, cur.String())
				cur.Reset()
				inToken = false
			}
		default:
			cur.WriteByte(c)
			inToken = true
		}
	}
	if escaped || inSingle || inDouble {
		return nil, fmt.Errorf("unterminated quote or escape in CC command %q", raw)
	}
	if inToken {
		args = append(args, cur.String())
	}
	if len(args) == 0 {
		return nil, fmt.Errorf("empty compiler command %q", raw)
	}
	return args, nil
}

func runCPreprocessor(t *testing.T, ccFields []string, defineFlag, objDir, unitPath string) []byte {
	t.Helper()
	args := make([]string, 0, len(ccFields)-1+5)
	args = append(args, ccFields[1:]...)
	args = append(args, "-E", defineFlag, "-I", objDir, unitPath)
	cmd := exec.Command(ccFields[0], args...)
	out, errRun := cmd.CombinedOutput()
	if errRun != nil {
		t.Fatalf("%s %v error = %v\n%s", ccFields[0], args, errRun, out)
	}
	return out
}

func TestUnixReloadTestPluginConstructorsNotDuplicatedByCgo(t *testing.T) {
	goTool, errGo := exec.LookPath("go")
	if errGo != nil {
		t.Skipf("go toolchain unavailable: %v", errGo)
	}
	mainPath, errAbs := filepath.Abs(filepath.Join("testdata", "loader_unix_reload_plugin", "main.go"))
	if errAbs != nil {
		t.Fatalf("Abs(main.go) error = %v", errAbs)
	}
	ccCmd := exec.Command(goTool, "env", "CC")
	ccOut, errCC := ccOutAndTrim(ccCmd)
	if errCC != nil {
		t.Fatalf("go env CC error = %v", errCC)
	}
	ccFields, errSplit := splitCCCommand(ccOut)
	if errSplit != nil {
		t.Fatalf("splitCCCommand(%q) error = %v", ccOut, errSplit)
	}

	for _, version := range []int{4, 5, 6, 7} {
		version := version
		t.Run(fmt.Sprintf("PLUGIN_VERSION_%d", version), func(t *testing.T) {
			objDir := t.TempDir()
			defineFlag := fmt.Sprintf("-DPLUGIN_VERSION=%d", version)
			cgoCmd := exec.Command(goTool, "tool", "cgo", "-objdir", objDir, "--", defineFlag, mainPath)
			if output, errCgo := cgoCmd.CombinedOutput(); errCgo != nil {
				t.Fatalf("go tool cgo (%s) error = %v\n%s", defineFlag, errCgo, output)
			}

			exportHeader, errHeader := os.ReadFile(filepath.Join(objDir, "_cgo_export.h"))
			if errHeader != nil {
				t.Fatalf("ReadFile(_cgo_export.h) error = %v", errHeader)
			}
			if strings.Contains(string(exportHeader), "__attribute__((constructor))") {
				t.Fatalf("_cgo_export.h contains __attribute__((constructor)) for %s; cgo duplicates constructors into both _cgo_export.c and main.cgo2.c", defineFlag)
			}

			for _, unit := range []string{"_cgo_export.c", "main.cgo2.c"} {
				ppOut := runCPreprocessor(t, ccFields, defineFlag, objDir, filepath.Join(objDir, unit))
				if strings.Contains(string(ppOut), "__attribute__((constructor))") {
					t.Fatalf("preprocessed %s contains __attribute__((constructor)) for %s; side-effecting constructors must live in a separate .c file", unit, defineFlag)
				}
			}
		})
	}
}

func ccOutAndTrim(cmd *exec.Cmd) (string, error) {
	out, errRun := cmd.Output()
	if errRun != nil {
		return "", errRun
	}
	return strings.TrimSpace(string(out)), nil
}

func parseGrandchildPIDs(raw string) ([]int, error) {
	lines := strings.Fields(raw)
	if len(lines) == 0 {
		return nil, fmt.Errorf("grandchild pid file is empty")
	}
	pids := make([]int, 0, len(lines))
	for _, line := range lines {
		pid, errParse := strconv.Atoi(line)
		if errParse != nil || pid <= 0 {
			return nil, fmt.Errorf("invalid grandchild pid %q: %w", line, errParse)
		}
		pids = append(pids, pid)
	}
	return pids, nil
}

func cleanupGrandchildPIDs(t *testing.T, pidFile, lockFile string, requireRecorded bool) {
	t.Helper()
	raw, errRead := os.ReadFile(pidFile)
	if errRead != nil {
		if !requireRecorded && errors.Is(errRead, os.ErrNotExist) {
			return
		}
		t.Errorf("ReadFile(%s) cleanup error = %v", pidFile, errRead)
		return
	}
	pids, errParse := parseGrandchildPIDs(string(raw))
	if errParse != nil {
		t.Errorf("parseGrandchildPIDs(%s) cleanup error = %v", pidFile, errParse)
		return
	}
	for _, pid := range pids {
		if errKill := syscall.Kill(pid, syscall.SIGKILL); errKill != nil && !errors.Is(errKill, syscall.ESRCH) {
			t.Errorf("SIGKILL grandchild pid %d failed: %v", pid, errKill)
		}
	}

	// Verify that every grandchild process has actually exited and closed its
	// open file descriptors (releasing LOCK_SH), even if PID 1 does not reap
	// zombies promptly inside a container.
	lockF, errOpen := os.OpenFile(lockFile, os.O_CREATE|os.O_RDWR, 0o600)
	if errOpen != nil {
		t.Errorf("OpenFile(%s) cleanup error = %v", lockFile, errOpen)
		return
	}
	defer func() {
		if errClose := lockF.Close(); errClose != nil {
			t.Errorf("Close(%s) cleanup error = %v", lockFile, errClose)
		}
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		errFlock := syscall.Flock(int(lockF.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if errFlock == nil {
			return
		}
		if !errors.Is(errFlock, syscall.EWOULDBLOCK) && !errors.Is(errFlock, syscall.EAGAIN) {
			t.Errorf("Flock(%s, LOCK_EX|LOCK_NB) cleanup error = %v", lockFile, errFlock)
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("grandchild process still holds lock %s after SIGKILL", lockFile)
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestUnixPluginProbeDoesNotBlockOnInheritedStdioGrandchild(t *testing.T) {
	dir := t.TempDir()
	pluginPath := filepath.Join(dir, "grandchild"+testPluginLibraryExtension())
	buildReloadTestPlugin(t, 7, pluginPath)

	pidFile := filepath.Join(dir, "grandchild.pid")
	lockFile := filepath.Join(dir, "grandchild.lock")
	t.Setenv("CLIPROXY_TEST_GRANDCHILD_PID_FILE", pidFile)
	t.Setenv("CLIPROXY_TEST_GRANDCHILD_LOCK_FILE", lockFile)
	probeAttempted := false
	t.Cleanup(func() {
		cleanupGrandchildPIDs(t, pidFile, lockFile, probeAttempted)
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

	probeAttempted = true
	if errProbe := probePluginLibrary(file.Path, loadPath); errProbe != nil {
		t.Fatalf("probePluginLibrary() error = %v, want nil without blocking on grandchild stdio", errProbe)
	}
	raw, errRead := os.ReadFile(pidFile)
	if errRead != nil {
		t.Fatalf("ReadFile(%s) error = %v, want grandchild pid recorded", pidFile, errRead)
	}
	pids, errParse := parseGrandchildPIDs(string(raw))
	if errParse != nil {
		t.Fatalf("parseGrandchildPIDs(%q) error = %v, want positive pids", string(raw), errParse)
	}
	if len(pids) != 1 {
		t.Fatalf("grandchild pid count = %d (%v), want exactly 1 (constructor must run once)", len(pids), pids)
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
