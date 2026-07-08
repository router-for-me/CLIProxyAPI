//go:build cgo && (linux || darwin || freebsd)

package pluginhost

/*
#cgo linux LDFLAGS: -ldl
#cgo freebsd LDFLAGS: -ldl
#include <dlfcn.h>
#include <stdint.h>
#include <stdlib.h>

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

static int cliproxy_dlclose(void* handle) {
	return dlclose(handle);
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

*/
import "C"

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"unsafe"
)

var (
	hostCallbackID      atomic.Uintptr
	hostCallbackEntries sync.Map
)

type dynamicLibraryLoader struct{}

type dynamicLibraryClient struct {
	handle  unsafe.Pointer
	hostAPI *C.cliproxy_host_api
	hostCtx unsafe.Pointer
	api     C.cliproxy_plugin_api
}

func defaultPluginLoader() pluginLoader {
	return dynamicLibraryLoader{}
}

func (dynamicLibraryLoader) Open(file pluginFile, host *Host) (pluginClient, error) {
	cPath := C.CString(file.Path)
	defer C.free(unsafe.Pointer(cPath))

	handle := C.cliproxy_dlopen(cPath)
	if handle == nil {
		return nil, fmt.Errorf("dlopen %s: %s", file.Path, dlerrorString())
	}

	cSymbol := C.CString("cliproxy_plugin_init")
	initSymbol := C.cliproxy_dlsym(handle, cSymbol)
	C.free(unsafe.Pointer(cSymbol))
	if initSymbol == nil {
		C.cliproxy_dlclose(handle)
		return nil, fmt.Errorf("missing cliproxy_plugin_init: %s", dlerrorString())
	}

	hostAPI := (*C.cliproxy_host_api)(C.malloc(C.size_t(unsafe.Sizeof(C.cliproxy_host_api{}))))
	if hostAPI == nil {
		C.cliproxy_dlclose(handle)
		return nil, fmt.Errorf("allocate host api")
	}
	hostCtx := C.malloc(C.size_t(unsafe.Sizeof(C.uintptr_t(0))))
	if hostCtx == nil {
		C.free(unsafe.Pointer(hostAPI))
		C.cliproxy_dlclose(handle)
		return nil, fmt.Errorf("allocate host context")
	}
	id := hostCallbackID.Add(1)
	*(*C.uintptr_t)(hostCtx) = C.uintptr_t(id)
	hostCallbackEntries.Store(id, dynamicHostCallbackEntry{host: host, pluginID: file.ID})
	C.cliproxy_set_host_api(hostAPI, C.uint32_t(pluginHostABIVersion), hostCtx)

	client := &dynamicLibraryClient{
		handle:  handle,
		hostAPI: hostAPI,
		hostCtx: hostCtx,
	}
	rc := C.cliproxy_call_init(initSymbol, hostAPI, &client.api)
	if rc != 0 {
		client.Shutdown()
		return nil, fmt.Errorf("cliproxy_plugin_init returned %d", int(rc))
	}
	if uint32(client.api.abi_version) != pluginHostABIVersion {
		client.Shutdown()
		return nil, fmt.Errorf("plugin ABI version %d is not supported", uint32(client.api.abi_version))
	}
	if client.api.call == nil || client.api.free_buffer == nil {
		client.Shutdown()
		return nil, fmt.Errorf("plugin function table is incomplete")
	}
	return client, nil
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
	if c == nil {
		return
	}
	if c.api.shutdown != nil {
		C.cliproxy_shutdown_plugin(c.api.shutdown)
		c.api.shutdown = nil
	}
	if c.hostCtx != nil {
		id := uintptr(*(*C.uintptr_t)(c.hostCtx))
		hostCallbackEntries.Delete(id)
		C.free(c.hostCtx)
		c.hostCtx = nil
	}
	if c.hostAPI != nil {
		C.free(unsafe.Pointer(c.hostAPI))
		c.hostAPI = nil
	}
	if c.handle != nil {
		C.cliproxy_dlclose(c.handle)
		c.handle = nil
	}
}

func dlerrorString() string {
	errText := C.cliproxy_dlerror()
	if errText == nil {
		return ""
	}
	return C.GoString(errText)
}
