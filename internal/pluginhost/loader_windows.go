//go:build windows

package pluginhost

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"golang.org/x/sys/windows"
)

type windowsBuffer struct {
	ptr uintptr
	len uintptr
}

type windowsResidentHostBlocks struct {
	hostCtx *uintptr
	hostAPI *windowsHostAPI
}

type windowsHostAPI struct {
	abiVersion uint32
	hostCtx    uintptr
	call       uintptr
	freeBuffer uintptr
}

type windowsPluginAPI struct {
	abiVersion uint32
	call       uintptr
	freeBuffer uintptr
	shutdown   uintptr
}

var (
	windowsHostCallbackID      atomic.Uintptr
	windowsHostCallbackEntries sync.Map
	windowsHostCallCallback    = syscall.NewCallback(windowsHostCall)
	windowsHostFreeCallback    = syscall.NewCallback(windowsHostFree)
	// windowsResidentHostState pins the host context and host API blocks of
	// every loaded plugin for the lifetime of the process. A resident
	// plugin runtime may pass the context back into windowsHostCall at any
	// time, long after its client was detached, and native pointers are
	// not GC roots. Only these small blocks are pinned, never the client
	// or the host.
	windowsResidentHostState sync.Map
)

type dynamicLibraryLoader struct{}

type dynamicLibraryClient struct {
	dll      *syscall.DLL
	tempPath string
	hostAPI  *windowsHostAPI
	hostCtx  *uintptr
	api      windowsPluginAPI
	host     *Host
	pluginID string
	instance *hostCallbackInstance
}

func defaultPluginLoader() pluginLoader {
	return dynamicLibraryLoader{}
}

func (dynamicLibraryLoader) Open(file pluginFile, host *Host) (pluginClient, error) {
	loadPath, errShadow := shadowCopyPlugin(file)
	if errShadow != nil {
		return nil, errShadow
	}
	dll, errLoad := syscall.LoadDLL(loadPath)
	if errLoad != nil {
		removeShadowPlugin(loadPath)
		return nil, errLoad
	}
	proc, errProc := dll.FindProc("cliproxy_plugin_init")
	if errProc != nil {
		// Do not release the module: loading it may already have started
		// the plugin runtime, which cannot be unloaded safely.
		return nil, errProc
	}
	id := windowsHostCallbackID.Add(1)
	instance := &hostCallbackInstance{}
	host.registerHostCallbackInstance(file.ID, instance)
	hostCtx := new(uintptr)
	*hostCtx = id
	hostAPI := &windowsHostAPI{
		abiVersion: pluginHostABIVersion,
		hostCtx:    uintptr(unsafe.Pointer(hostCtx)),
		call:       windowsHostCallCallback,
		freeBuffer: windowsHostFreeCallback,
	}
	windowsResidentHostState.Store(id, windowsResidentHostBlocks{hostCtx: hostCtx, hostAPI: hostAPI})
	windowsHostCallbackEntries.Store(id, dynamicHostCallbackEntry{host: host, pluginID: file.ID, instance: instance})
	client := &dynamicLibraryClient{
		dll:      dll,
		tempPath: loadPath,
		hostCtx:  hostCtx,
		host:     host,
		pluginID: file.ID,
		instance: instance,
		hostAPI:  hostAPI,
	}
	rc, _, errCall := proc.Call(uintptr(unsafe.Pointer(client.hostAPI)), uintptr(unsafe.Pointer(&client.api)))
	if rc != 0 {
		client.closeAfterOpenFailure()
		return nil, fmt.Errorf("cliproxy_plugin_init returned %d: %v", rc, errCall)
	}
	if client.api.abiVersion != pluginHostABIVersion {
		client.closeAfterOpenFailure()
		return nil, fmt.Errorf("plugin ABI version %d is not supported", client.api.abiVersion)
	}
	if client.api.call == 0 || client.api.freeBuffer == 0 {
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
	if c == nil || c.api.call == 0 {
		return nil, fmt.Errorf("plugin client is closed")
	}
	if ctx != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
	}
	methodBytes, errMethod := syscall.BytePtrFromString(method)
	if errMethod != nil {
		return nil, errMethod
	}
	var requestPtr uintptr
	if len(request) > 0 {
		requestPtr = uintptr(unsafe.Pointer(&request[0]))
	}
	responseMem, errAlloc := windows.LocalAlloc(
		windows.LMEM_FIXED|windows.LMEM_ZEROINIT,
		uint32(unsafe.Sizeof(windowsBuffer{})),
	)
	if errAlloc != nil {
		return nil, fmt.Errorf("allocate plugin response buffer: %w", errAlloc)
	}
	if responseMem == 0 {
		return nil, fmt.Errorf("allocate plugin response buffer")
	}
	defer func() {
		_, _ = windows.LocalFree(windows.Handle(responseMem))
	}()
	response := (*windowsBuffer)(unsafe.Pointer(responseMem))
	rc, _, _ := syscall.SyscallN(
		c.api.call,
		uintptr(unsafe.Pointer(methodBytes)),
		requestPtr,
		uintptr(len(request)),
		responseMem,
	)
	runtime.KeepAlive(methodBytes)
	runtime.KeepAlive(request)
	var out []byte
	if response.ptr != 0 && response.len > 0 {
		out = unsafe.Slice((*byte)(unsafe.Pointer(response.ptr)), response.len)
		out = append([]byte(nil), out...)
	}
	if response.ptr != 0 {
		_, _, _ = syscall.SyscallN(c.api.freeBuffer, response.ptr, response.len)
	}
	if rc != 0 {
		if isPluginErrorEnvelope(out) {
			return out, nil
		}
		return nil, fmt.Errorf("plugin call %s returned %d: %s", method, rc, string(out))
	}
	return out, nil
}

func (c *dynamicLibraryClient) Shutdown() {
	c.close()
}

func (c *dynamicLibraryClient) closeAfterOpenFailure() {
	c.close()
}

// close detaches the client logically without unloading the plugin module.
// Go plugin DLLs are not safe to hot-unload from the host process: the
// plugin runtime, its threads and its signal handlers stay resident after
// FreeLibrary. This also applies when initialization failed, because
// loading the module may already have started the runtime. The plugin was
// loaded from a shadow copy, so keeping the module mapped does not block
// deleting or replacing the source artifact.
func (c *dynamicLibraryClient) close() {
	if c == nil {
		return
	}
	if c.host != nil {
		c.host.closeHostHTTPCallbackInstance(c.pluginID, c.instance)
		c.host = nil
	}
	if c.api.shutdown != 0 {
		_, _, _ = syscall.SyscallN(c.api.shutdown)
		c.api.shutdown = 0
	}
	c.api.call = 0
	c.api.freeBuffer = 0
	if c.hostCtx != nil {
		// Drop the callback registration so repeated reloads do not pin
		// the host forever. The context and host API blocks stay pinned in
		// windowsResidentHostState because the resident plugin runtime may
		// still dereference them on late callbacks.
		windowsHostCallbackEntries.Delete(*c.hostCtx)
	}
}

func windowsHostCall(hostCtx uintptr, methodPtr uintptr, requestPtr uintptr, requestLen uintptr, responsePtr uintptr) uintptr {
	if responsePtr != 0 {
		response := (*windowsBuffer)(unsafe.Pointer(responsePtr))
		response.ptr = 0
		response.len = 0
	}
	if hostCtx == 0 || methodPtr == 0 {
		return 1
	}
	id := *(*uintptr)(unsafe.Pointer(hostCtx))
	rawHost, okHost := windowsHostCallbackEntries.Load(id)
	if !okHost {
		return 1
	}
	entry, okHost := rawHost.(dynamicHostCallbackEntry)
	if !okHost || entry.host == nil {
		return 1
	}
	var request []byte
	if requestPtr != 0 && requestLen > 0 {
		request = unsafe.Slice((*byte)(unsafe.Pointer(requestPtr)), requestLen)
		request = append([]byte(nil), request...)
	}
	ctx := withHostCallbackIdentity(context.Background(), entry.pluginID, entry.instance)
	resp, errCall := entry.host.callFromPlugin(ctx, windowsString(methodPtr), request)
	if errCall != nil {
		resp = marshalRPCError("host_call_failed", errCall.Error(), clienterror.HTTPStatusFromError(errCall))
	}
	if len(resp) == 0 || responsePtr == 0 {
		return 0
	}
	mem, errAlloc := windows.LocalAlloc(windows.LMEM_FIXED, uint32(len(resp)))
	if errAlloc != nil || mem == 0 {
		return 1
	}
	copy(unsafe.Slice((*byte)(unsafe.Pointer(mem)), len(resp)), resp)
	response := (*windowsBuffer)(unsafe.Pointer(responsePtr))
	response.ptr = mem
	response.len = uintptr(len(resp))
	return 0
}

func windowsHostFree(ptr uintptr, len uintptr) uintptr {
	if ptr != 0 {
		_, _ = windows.LocalFree(windows.Handle(ptr))
	}
	return 0
}

func windowsString(ptr uintptr) string {
	if ptr == 0 {
		return ""
	}
	bytes := make([]byte, 0)
	for offset := uintptr(0); ; offset++ {
		b := *(*byte)(unsafe.Pointer(ptr + offset))
		if b == 0 {
			break
		}
		bytes = append(bytes, b)
	}
	return string(bytes)
}
