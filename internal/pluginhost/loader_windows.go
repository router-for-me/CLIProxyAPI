//go:build windows

package pluginhost

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsBuffer struct {
	ptr uintptr
	len uintptr
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
)

type dynamicLibraryLoader struct{}

type dynamicLibraryClient struct {
	dll     *syscall.DLL
	hostAPI *windowsHostAPI
	hostCtx *uintptr
	api     windowsPluginAPI
}

func defaultPluginLoader() pluginLoader {
	return dynamicLibraryLoader{}
}

func (dynamicLibraryLoader) Open(file pluginFile, host *Host) (pluginClient, error) {
	dll, errLoad := syscall.LoadDLL(file.Path)
	if errLoad != nil {
		return nil, errLoad
	}
	proc, errProc := dll.FindProc("cliproxy_plugin_init")
	if errProc != nil {
		_ = dll.Release()
		return nil, errProc
	}
	id := windowsHostCallbackID.Add(1)
	hostCtx := new(uintptr)
	*hostCtx = id
	windowsHostCallbackEntries.Store(id, dynamicHostCallbackEntry{host: host, pluginID: file.ID})
	client := &dynamicLibraryClient{
		dll:     dll,
		hostCtx: hostCtx,
		hostAPI: &windowsHostAPI{
			abiVersion: pluginHostABIVersion,
			hostCtx:    uintptr(unsafe.Pointer(hostCtx)),
			call:       windowsHostCallCallback,
			freeBuffer: windowsHostFreeCallback,
		},
	}
	rc, _, errCall := proc.Call(uintptr(unsafe.Pointer(client.hostAPI)), uintptr(unsafe.Pointer(&client.api)))
	if rc != 0 {
		client.Shutdown()
		return nil, fmt.Errorf("cliproxy_plugin_init returned %d: %v", rc, errCall)
	}
	if client.api.abiVersion != pluginHostABIVersion {
		client.Shutdown()
		return nil, fmt.Errorf("plugin ABI version %d is not supported", client.api.abiVersion)
	}
	if client.api.call == 0 || client.api.freeBuffer == 0 {
		client.Shutdown()
		return nil, fmt.Errorf("plugin function table is incomplete")
	}
	return client, nil
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
	var response windowsBuffer
	rc, _, _ := syscall.SyscallN(
		c.api.call,
		uintptr(unsafe.Pointer(methodBytes)),
		requestPtr,
		uintptr(len(request)),
		uintptr(unsafe.Pointer(&response)),
	)
	var out []byte
	if response.ptr != 0 && response.len > 0 {
		out = unsafe.Slice((*byte)(unsafe.Pointer(response.ptr)), response.len)
		out = append([]byte(nil), out...)
	}
	if response.ptr != 0 {
		_, _, _ = syscall.SyscallN(c.api.freeBuffer, response.ptr, response.len)
	}
	if rc != 0 {
		return nil, fmt.Errorf("plugin call %s returned %d: %s", method, rc, string(out))
	}
	return out, nil
}

func (c *dynamicLibraryClient) Shutdown() {
	if c == nil {
		return
	}
	if c.api.shutdown != 0 {
		_, _, _ = syscall.SyscallN(c.api.shutdown)
		c.api.shutdown = 0
	}
	if c.hostCtx != nil {
		windowsHostCallbackEntries.Delete(*c.hostCtx)
		c.hostCtx = nil
	}
	if c.dll != nil {
		_ = c.dll.Release()
		c.dll = nil
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
	ctx := withHostCallbackPluginID(context.Background(), entry.pluginID)
	resp, errCall := entry.host.callFromPlugin(ctx, windowsString(methodPtr), request)
	if errCall != nil {
		resp = marshalRPCError("host_call_failed", errCall.Error())
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
