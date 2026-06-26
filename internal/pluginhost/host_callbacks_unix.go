//go:build cgo && (linux || darwin || freebsd)

package pluginhost

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;
*/
import "C"

import (
	"context"
	"unsafe"
)

//export cliproxyHostCall
func cliproxyHostCall(hostCtx unsafe.Pointer, method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if hostCtx == nil || method == nil {
		return 1
	}
	id := uintptr(*(*C.uintptr_t)(hostCtx))
	rawHost, okHost := hostCallbackEntries.Load(id)
	if !okHost {
		return 1
	}
	entry, okHost := rawHost.(dynamicHostCallbackEntry)
	if !okHost || entry.host == nil {
		return 1
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	ctx := withHostCallbackPluginID(context.Background(), entry.pluginID)
	resp, errCall := entry.host.callFromPlugin(ctx, C.GoString(method), requestBytes)
	if errCall != nil {
		resp = marshalRPCError("host_call_failed", errCall.Error())
	}
	if len(resp) == 0 || response == nil {
		return 0
	}
	ptr := C.CBytes(resp)
	if ptr == nil {
		return 1
	}
	response.ptr = ptr
	response.len = C.size_t(len(resp))
	return 0
}

//export cliproxyHostFree
func cliproxyHostFree(ptr unsafe.Pointer, len C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}
