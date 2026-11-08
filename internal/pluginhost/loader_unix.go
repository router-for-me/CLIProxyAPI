//go:build cgo && (linux || darwin || freebsd)

package pluginhost

/*
#cgo linux LDFLAGS: -ldl
#cgo freebsd LDFLAGS: -ldl
#include <dlfcn.h>
#include <stdint.h>
#include <stdlib.h>
#include <unistd.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

typedef int (*cliproxy_plugin_init_fn)(const cliproxy_host_api*, cliproxy_plugin_api*);

extern int cliproxyHostCall(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyHostFree(void*, size_t);

static void* cliproxy_dlopen(const char* path) {
	return dlopen(path, RTLD_NOW | RTLD_LOCAL);
}

static void* cliproxy_dlsym(void* handle, const char* name) {
	return dlsym(handle, name);
}

static const char* cliproxy_dlerror(void) {
	return dlerror();
}

static int cliproxy_call_init(void* fn, const cliproxy_host_api* host, cliproxy_plugin_api* plugin) {
	return ((cliproxy_plugin_init_fn)fn)(host, plugin);
}

static int cliproxy_call_plugin(cliproxy_plugin_call_fn fn, const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	return fn(method, request, request_len, response);
}

static void cliproxy_free_plugin_buffer(cliproxy_plugin_free_fn fn, void* ptr, size_t len) {
	fn(ptr, len);
}

static void cliproxy_shutdown_plugin(cliproxy_plugin_shutdown_fn fn) {
	fn();
}

static void cliproxy_set_host_api(cliproxy_host_api* api, uint32_t abi_version, void* host_ctx) {
	api->abi_version = abi_version;
	api->host_ctx = host_ctx;
	api->call = cliproxyHostCall;
	api->free_buffer = cliproxyHostFree;
}

static int cliproxy_probe_host_call(void* host_ctx, const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	(void)host_ctx;
	(void)method;
	(void)request;
	(void)request_len;
	if (response != NULL) {
		response->ptr = NULL;
		response->len = 0;
	}
	return 1;
}

static void cliproxy_probe_host_free(void* ptr, size_t len) {
	(void)ptr;
	(void)len;
}

static int cliproxy_probe_init_and_shutdown(void* fn, uint32_t abi_version) {
	static cliproxy_host_api host_api;
	cliproxy_plugin_api plugin_api = {0};
	host_api.abi_version = abi_version;
	host_api.host_ctx = NULL;
	host_api.call = cliproxy_probe_host_call;
	host_api.free_buffer = cliproxy_probe_host_free;
	int rc = ((cliproxy_plugin_init_fn)fn)(&host_api, &plugin_api);
	if (rc == 0 && plugin_api.shutdown != NULL) {
		plugin_api.shutdown();
	}
	return rc;
}

static void cliproxy_probe_exit(int code) {
	_exit(code);
}

*/
import "C"

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"
)

const (
	pluginDlopenProbeEnv       = "CLIPROXY_PLUGIN_DLOPEN_PROBE"
	pluginDlopenProbeStatusEnv = "CLIPROXY_PLUGIN_DLOPEN_PROBE_STATUS"
	probeStatusStarted         = "started"
	probeStatusLoaded          = "loaded"
	probeStatusDlopenNull      = "dlopen_null"
	maxPluginProbeOutputBytes  = 64 * 1024
)

var (
	hostCallbackID        atomic.Uintptr
	hostCallbackEntries   sync.Map
	probedPluginShadows   sync.Map
	pluginProbeExecutable = os.Executable
	unixDlopen            = defaultUnixDlopen
)

func defaultUnixDlopen(path string) unsafe.Pointer {
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))
	return C.cliproxy_dlopen(cPath)
}

func init() {
	if probePath := os.Getenv(pluginDlopenProbeEnv); probePath != "" {
		runPluginDlopenProbe(probePath)
	}
}

type dynamicLibraryLoader struct{}

type dynamicLibraryClient struct {
	handle   unsafe.Pointer
	tempPath string
	hostAPI  *C.cliproxy_host_api
	hostCtx  unsafe.Pointer
	api      C.cliproxy_plugin_api
	host     *Host
	pluginID string
	instance *hostCallbackInstance
}

func defaultPluginLoader() pluginLoader {
	return dynamicLibraryLoader{}
}

func (dynamicLibraryLoader) Open(file pluginFile, host *Host) (pluginClient, error) {
	loadPath, releaseGuard, errShadow := acquireShadowPluginLoad(file)
	if errShadow != nil {
		return nil, errShadow
	}
	handle, loadPath, errOpen := openUnixShadowPluginWithFallback(file, loadPath, releaseGuard)
	if errOpen != nil {
		return nil, errOpen
	}
	// From here on the image must never be unloaded: dlopen may already
	// have started the plugin runtime, and a Go c-shared runtime cannot be
	// removed from the host process safely. Failure paths below only detach
	// the client logically and deliberately leak the mapped image together
	// with any host callback state the runtime may reference.

	cSymbol := C.CString("cliproxy_plugin_init")
	initSymbol := C.cliproxy_dlsym(handle, cSymbol)
	C.free(unsafe.Pointer(cSymbol))
	if initSymbol == nil {
		return nil, fmt.Errorf("missing cliproxy_plugin_init: %s", dlerrorString())
	}

	hostAPI := (*C.cliproxy_host_api)(C.malloc(C.size_t(unsafe.Sizeof(C.cliproxy_host_api{}))))
	if hostAPI == nil {
		return nil, fmt.Errorf("allocate host api")
	}
	hostCtx := C.malloc(C.size_t(unsafe.Sizeof(C.uintptr_t(0))))
	if hostCtx == nil {
		return nil, fmt.Errorf("allocate host context")
	}
	id := hostCallbackID.Add(1)
	instance := &hostCallbackInstance{}
	host.registerHostCallbackInstance(file.ID, instance)
	*(*C.uintptr_t)(hostCtx) = C.uintptr_t(id)
	hostCallbackEntries.Store(id, dynamicHostCallbackEntry{host: host, pluginID: file.ID, instance: instance})
	C.cliproxy_set_host_api(hostAPI, C.uint32_t(pluginHostABIVersion), hostCtx)

	client := &dynamicLibraryClient{
		handle:   handle,
		tempPath: loadPath,
		hostAPI:  hostAPI,
		hostCtx:  hostCtx,
		host:     host,
		pluginID: file.ID,
		instance: instance,
	}
	rc := C.cliproxy_call_init(initSymbol, hostAPI, &client.api)
	if rc != 0 {
		client.closeAfterOpenFailure()
		return nil, fmt.Errorf("cliproxy_plugin_init returned %d", int(rc))
	}
	if uint32(client.api.abi_version) != pluginHostABIVersion {
		client.closeAfterOpenFailure()
		return nil, fmt.Errorf("plugin ABI version %d is not supported", uint32(client.api.abi_version))
	}
	if client.api.call == nil || client.api.free_buffer == nil {
		client.closeAfterOpenFailure()
		return nil, fmt.Errorf("plugin function table is incomplete")
	}
	return client, nil
}

func (c *dynamicLibraryClient) callbackInstance() *hostCallbackInstance {
	if c == nil {
		return nil
	}
	return c.instance
}

func (c *dynamicLibraryClient) Call(ctx context.Context, method string, request []byte) ([]byte, error) {
	if c == nil || c.api.call == nil {
		return nil, fmt.Errorf("plugin client is closed")
	}
	if ctx != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
	}

	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	var cRequest unsafe.Pointer
	if len(request) > 0 {
		cRequest = C.CBytes(request)
		defer C.free(cRequest)
	}
	var response C.cliproxy_buffer
	rc := C.cliproxy_call_plugin(c.api.call, cMethod, (*C.uint8_t)(cRequest), C.size_t(len(request)), &response)
	var out []byte
	if response.ptr != nil && response.len > 0 {
		out = C.GoBytes(response.ptr, C.int(response.len))
	}
	if response.ptr != nil {
		C.cliproxy_free_plugin_buffer(c.api.free_buffer, response.ptr, response.len)
	}
	if rc != 0 {
		if isPluginErrorEnvelope(out) {
			return out, nil
		}
		return nil, fmt.Errorf("plugin call %s returned %d: %s", method, int(rc), string(out))
	}
	return out, nil
}

func (c *dynamicLibraryClient) Shutdown() {
	c.close()
}

func (c *dynamicLibraryClient) closeAfterOpenFailure() {
	c.close()
}

// close detaches the client logically without unloading the plugin image.
// Go plugin libraries are not safe to hot-unload from the host process: the
// plugin runtime, its threads and its signal handlers stay resident after
// dlclose, so a later load in the same process forwards signals into
// unmapped memory and spins. This also applies when initialization failed,
// because dlopen may already have started the runtime. The plugin was
// loaded from a shadow copy, so keeping the image mapped does not block
// deleting or replacing the source artifact.
func (c *dynamicLibraryClient) close() {
	if c == nil {
		return
	}
	if c.host != nil {
		c.host.closeHostHTTPCallbackInstance(c.pluginID, c.instance)
		c.host = nil
	}
	if c.api.shutdown != nil {
		C.cliproxy_shutdown_plugin(c.api.shutdown)
		c.api.shutdown = nil
	}
	c.api.call = nil
	c.api.free_buffer = nil
	if c.hostCtx != nil {
		// Drop the callback registration so repeated reloads do not pin the
		// host forever. The C host context and host API block themselves are
		// deliberately leaked: the resident plugin runtime may still pass
		// the context back into cliproxyHostCall, which reads the callback
		// id from it before failing the lookup.
		id := uintptr(*(*C.uintptr_t)(c.hostCtx))
		hostCallbackEntries.Delete(id)
	}
}

func openUnixShadowPluginWithFallback(file pluginFile, loadPath string, releaseGuard func()) (unsafe.Pointer, string, error) {
	errProbe := probePluginLibrary(file.Path, loadPath)
	if errProbe != nil {
		releaseGuard()
		if _, cached := probedPluginShadows.Load(filepath.Base(loadPath)); cached {
			return nil, "", errProbe
		}
		if fallbackHandle, fallbackPath, okFallback := tryOpenUnixFallbackShadowPlugin(file, loadPath); okFallback {
			return fallbackHandle, fallbackPath, nil
		}
		return nil, "", errProbe
	}
	handle := unixDlopen(loadPath)
	firstErr := dlerrorString()
	releaseGuard()
	if handle != nil {
		return handle, loadPath, nil
	}
	probedPluginShadows.Delete(filepath.Base(loadPath))

	if fallbackHandle, fallbackPath, okFallback := tryOpenUnixFallbackShadowPlugin(file, loadPath); okFallback {
		return fallbackHandle, fallbackPath, nil
	}
	return nil, "", fmt.Errorf("dlopen %s: %s", file.Path, firstErr)
}

func tryOpenUnixFallbackShadowPlugin(file pluginFile, failedLoadPath string) (unsafe.Pointer, string, bool) {
	fallbackPath, releaseFallbackGuard, errFallback := acquireShadowPluginFallbackLoad(file, failedLoadPath)
	if errFallback != nil {
		return nil, "", false
	}
	if errFallbackProbe := probePluginLibrary(file.Path, fallbackPath); errFallbackProbe != nil {
		releaseFallbackGuard()
		return nil, "", false
	}
	fallbackHandle := unixDlopen(fallbackPath)
	releaseFallbackGuard()
	if fallbackHandle != nil {
		return fallbackHandle, fallbackPath, true
	}
	probedPluginShadows.Delete(filepath.Base(fallbackPath))
	return nil, "", false
}

func dlerrorString() string {
	errText := C.cliproxy_dlerror()
	if errText == nil {
		return ""
	}
	return C.GoString(errText)
}

func runPluginDlopenProbe(probePath string) {
	statusPath := os.Getenv(pluginDlopenProbeStatusEnv)
	_ = os.Unsetenv(pluginDlopenProbeEnv)
	_ = os.Unsetenv(pluginDlopenProbeStatusEnv)
	writeProbeStatus(statusPath, probeStatusStarted)
	cPath := C.CString(probePath)
	handle := C.cliproxy_dlopen(cPath)
	C.free(unsafe.Pointer(cPath))
	if handle == nil {
		writeProbeStatus(statusPath, probeStatusDlopenNull)
		C.cliproxy_probe_exit(0)
		return
	}
	cSymbol := C.CString("cliproxy_plugin_init")
	initSymbol := C.cliproxy_dlsym(handle, cSymbol)
	C.free(unsafe.Pointer(cSymbol))
	if initSymbol != nil {
		_ = C.cliproxy_probe_init_and_shutdown(initSymbol, C.uint32_t(pluginHostABIVersion))
	}
	writeProbeStatus(statusPath, probeStatusLoaded)
	C.cliproxy_probe_exit(0)
}

func writeProbeStatus(statusPath, status string) {
	if statusPath == "" {
		return
	}
	_ = os.WriteFile(statusPath, []byte(status), 0o600)
}

type limitedProbeBuffer struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	limit int
}

func newLimitedProbeBuffer(limit int) *limitedProbeBuffer {
	if limit <= 0 {
		limit = maxPluginProbeOutputBytes
	}
	return &limitedProbeBuffer{limit: limit}
}

func (b *limitedProbeBuffer) Write(p []byte) (int, error) {
	total := len(p)
	if b == nil || b.limit <= 0 {
		return total, nil
	}
	b.mu.Lock()
	remaining := b.limit - b.buf.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.buf.Write(p)
	}
	b.mu.Unlock()
	return total, nil
}

func (b *limitedProbeBuffer) String() string {
	if b == nil {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func probePluginLibrary(sourcePath, loadPath string) error {
	cacheKey := filepath.Base(loadPath)
	if cached, ok := probedPluginShadows.Load(cacheKey); ok {
		if detail, _ := cached.(string); detail != "" {
			return fmt.Errorf("dlopen %s: %s", sourcePath, detail)
		}
		return nil
	}

	execPath, errExec := pluginProbeExecutable()
	if errExec != nil {
		return fmt.Errorf("dlopen %s: resolve preflight probe executable: %w", sourcePath, errExec)
	}
	if strings.TrimSpace(execPath) == "" {
		execPath = os.Args[0]
	}

	statusDir, errStatusDir := shadowPluginDir()
	if errStatusDir != nil {
		statusDir = os.TempDir()
	}
	statusFile, errStatus := os.CreateTemp(statusDir, ".cliproxy-probe-*.status")
	if errStatus != nil {
		return fmt.Errorf("dlopen %s: create preflight probe status file: %w", sourcePath, errStatus)
	}
	statusPath := statusFile.Name()
	_ = statusFile.Close()
	defer func() {
		_ = os.Remove(statusPath)
	}()

	devNull, errDevNull := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if errDevNull != nil {
		return fmt.Errorf("dlopen %s: open %s: %w", sourcePath, os.DevNull, errDevNull)
	}
	defer func() {
		_ = devNull.Close()
	}()

	// Use an explicit os.Pipe (*os.File) for Stderr so os/exec does not spawn
	// its own uncancelable pipe-drain goroutine. Closing stderrReader as soon
	// as cmd.Wait() returns ensures a grandchild process that inherited Stderr
	// neither blocks Wait() nor writes unbounded data to disk.
	stderrReader, stderrWriter, errPipe := os.Pipe()
	if errPipe != nil {
		return fmt.Errorf("dlopen %s: create preflight probe pipe: %w", sourcePath, errPipe)
	}
	stderrBuf := newLimitedProbeBuffer(maxPluginProbeOutputBytes)
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		_, _ = io.Copy(stderrBuf, stderrReader)
	}()

	cmd := exec.Command(execPath)
	cmd.Env = append(
		os.Environ(),
		pluginDlopenProbeEnv+"="+loadPath,
		pluginDlopenProbeStatusEnv+"="+statusPath,
	)
	cmd.Stdout = devNull
	cmd.Stderr = stderrWriter
	errStart := cmd.Start()
	_ = stderrWriter.Close()
	if errStart != nil {
		_ = stderrReader.Close()
		<-stderrDone
		return fmt.Errorf("dlopen %s: preflight probe failed: %w", sourcePath, errStart)
	}
	errRun := cmd.Wait()
	_ = stderrReader.Close()
	<-stderrDone

	statusBytes, _ := os.ReadFile(statusPath)
	statusText := strings.TrimSpace(string(statusBytes))
	stderrText := stderrBuf.String()

	if errRun == nil {
		switch statusText {
		case probeStatusLoaded:
			probedPluginShadows.Store(cacheKey, "")
			return nil
		case probeStatusDlopenNull:
			return nil
		case probeStatusStarted:
			detail := "preflight probe exited prematurely before completing initialization"
			if firstLine := firstNonEmptyLine(stderrText); firstLine != "" {
				detail = fmt.Sprintf("%s: %s", detail, firstLine)
			}
			probedPluginShadows.Store(cacheKey, detail)
			return fmt.Errorf("dlopen %s: %s", sourcePath, detail)
		default:
			return fmt.Errorf("dlopen %s: preflight probe did not complete", sourcePath)
		}
	}

	detail := describePluginProbeFailure(errRun, stderrText)
	if statusText == probeStatusStarted && isCacheablePluginProbeFailure(errRun, stderrText) {
		probedPluginShadows.Store(cacheKey, detail)
	}
	return fmt.Errorf("dlopen %s: %s", sourcePath, detail)
}

func isCacheablePluginProbeFailure(errRun error, stderrText string) bool {
	var exitErr *exec.ExitError
	if errors.As(errRun, &exitErr) {
		if status, okStatus := exitErr.Sys().(syscall.WaitStatus); okStatus && status.Signaled() {
			switch status.Signal() {
			case syscall.SIGILL, syscall.SIGSEGV, syscall.SIGBUS, syscall.SIGFPE, syscall.SIGABRT, syscall.SIGTRAP, syscall.SIGSYS:
				return true
			default:
				return false
			}
		}
	}
	lowerStderr := strings.ToLower(stderrText)
	for _, token := range []string{
		"sigill",
		"illegal instruction",
		"sigsegv",
		"segmentation violation",
		"sigbus",
		"bus error",
		"sigfpe",
		"floating-point exception",
		"sigabrt",
		"sigtrap",
		"panic:",
		"fatal error:",
	} {
		if strings.Contains(lowerStderr, token) {
			return true
		}
	}
	return false
}

func describePluginProbeFailure(errRun error, stderrText string) string {
	firstLine := firstNonEmptyLine(stderrText)
	lowerStderr := strings.ToLower(stderrText)
	var exitErr *exec.ExitError
	if errors.As(errRun, &exitErr) {
		if status, okStatus := exitErr.Sys().(syscall.WaitStatus); okStatus && status.Signaled() {
			sig := status.Signal()
			if sig == syscall.SIGILL {
				return "preflight probe terminated by SIGILL: illegal instruction (possible CPU instruction set mismatch)"
			}
			if firstLine != "" {
				return fmt.Sprintf("preflight probe terminated by signal %s: %s", sig, firstLine)
			}
			return fmt.Sprintf("preflight probe terminated by signal %s", sig)
		}
	}
	if strings.Contains(stderrText, "SIGILL") || strings.Contains(lowerStderr, "illegal instruction") {
		return "preflight probe terminated by SIGILL: illegal instruction (possible CPU instruction set mismatch)"
	}
	if firstLine != "" {
		return fmt.Sprintf("preflight probe failed (%v): %s", errRun, firstLine)
	}
	return fmt.Sprintf("preflight probe failed: %v", errRun)
}

func firstNonEmptyLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
