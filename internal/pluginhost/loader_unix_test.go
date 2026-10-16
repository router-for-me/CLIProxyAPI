//go:build cgo && (linux || darwin || freebsd)

package pluginhost

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
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
	switch version {
	case 2:
		cflags = "-DPLUGIN_VERSION=2"
	case 3:
		cflags = "-DPLUGIN_VERSION=3"
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
