//go:build windows

package pluginhost

import (
	"context"
	"runtime"
	"sync/atomic"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

var testReentrantHostCallback uintptr

var testWindowsCloseShutdownCalls atomic.Int64

func testWindowsCloseShutdown() uintptr {
	testWindowsCloseShutdownCalls.Add(1)
	return 0
}

func TestWindowsClosedClientKeepsResidentHostState(t *testing.T) {
	host := New()
	id := windowsHostCallbackID.Add(1)
	instance := &hostCallbackInstance{}
	hostCtx := new(uintptr)
	*hostCtx = id
	hostAPI := &windowsHostAPI{
		abiVersion: pluginHostABIVersion,
		hostCtx:    uintptr(unsafe.Pointer(hostCtx)),
		call:       windowsHostCallCallback,
		freeBuffer: windowsHostFreeCallback,
	}
	windowsResidentHostState.Store(id, windowsResidentHostBlocks{hostCtx: hostCtx, hostAPI: hostAPI})
	windowsHostCallbackEntries.Store(id, dynamicHostCallbackEntry{host: host, pluginID: "alpha", instance: instance})
	shutdownsBefore := testWindowsCloseShutdownCalls.Load()
	client := &dynamicLibraryClient{
		hostCtx:  hostCtx,
		hostAPI:  hostAPI,
		host:     host,
		pluginID: "alpha",
		instance: instance,
		api: windowsPluginAPI{
			call:       1,
			freeBuffer: 1,
			shutdown:   syscall.NewCallback(testWindowsCloseShutdown),
		},
	}
	ctxPtr := uintptr(unsafe.Pointer(hostCtx))

	client.close()

	if _, okEntry := windowsHostCallbackEntries.Load(id); okEntry {
		t.Fatal("callback registration retained after close; repeated reloads would pin the host")
	}
	rawBlocks, okBlocks := windowsResidentHostState.Load(id)
	if !okBlocks {
		t.Fatal("resident host state missing after close; late plugin callbacks could dereference freed memory")
	}
	blocks, okBlocks := rawBlocks.(windowsResidentHostBlocks)
	if !okBlocks || blocks.hostCtx != hostCtx || blocks.hostAPI != hostAPI {
		t.Fatalf("resident host blocks = %+v, want pinned hostCtx/hostAPI", rawBlocks)
	}
	if client.api.call != 0 || client.api.freeBuffer != 0 || client.api.shutdown != 0 {
		t.Fatalf("plugin API table not cleared by close: %+v", client.api)
	}
	if gotShutdowns := testWindowsCloseShutdownCalls.Load(); gotShutdowns != shutdownsBefore+1 {
		t.Fatalf("plugin shutdown calls = %d, want %d", gotShutdowns, shutdownsBefore+1)
	}

	// A late callback from the resident plugin runtime dereferences the
	// pinned context, finds no registration, and fails safely.
	methodBytes, errMethod := syscall.BytePtrFromString("host.log")
	if errMethod != nil {
		t.Fatalf("BytePtrFromString() error = %v", errMethod)
	}
	if rc := windowsHostCall(ctxPtr, uintptr(unsafe.Pointer(methodBytes)), 0, 0, 0); rc == 0 {
		t.Fatal("windowsHostCall() with detached context succeeded, want failure")
	}
	windowsResidentHostState.Delete(id)
}

func TestDynamicLibraryClientCallSurvivesReentrantCallbackStackGrowth(t *testing.T) {
	testReentrantHostCallback = syscall.NewCallback(testGrowHostCallbackStack)
	client := newGuardedPluginClient(&dynamicLibraryClient{api: windowsPluginAPI{
		call:       syscall.NewCallback(testReentrantPluginCall),
		freeBuffer: syscall.NewCallback(testReentrantPluginFree),
	}})
	t.Cleanup(client.Shutdown)

	got, errCall := client.Call(context.Background(), "model.route", []byte(`{}`))
	if errCall != nil {
		t.Fatalf("Call() error = %v", errCall)
	}
	want := `{"ok":true,"result":{"Handled":true}}`
	if string(got) != want {
		t.Fatalf("Call() response = %q, want %q", got, want)
	}
}

func testReentrantPluginCall(_, _, _, responsePtr uintptr) uintptr {
	if testReentrantHostCallback == 0 || responsePtr == 0 {
		return 1
	}
	_, _, _ = syscall.SyscallN(testReentrantHostCallback)

	raw := []byte(`{"ok":true,"result":{"Handled":true}}`)
	mem, errAlloc := windows.LocalAlloc(windows.LMEM_FIXED, uint32(len(raw)))
	if errAlloc != nil || mem == 0 {
		return 1
	}
	copy(unsafe.Slice((*byte)(unsafe.Pointer(mem)), len(raw)), raw)
	response := (*windowsBuffer)(unsafe.Pointer(responsePtr))
	response.ptr = mem
	response.len = uintptr(len(raw))
	return 0
}

func testReentrantPluginFree(ptr, _ uintptr) uintptr {
	if ptr != 0 {
		_, _ = windows.LocalFree(windows.Handle(ptr))
	}
	return 0
}

func testGrowHostCallbackStack() uintptr {
	return uintptr(testGrowStack(64))
}

//go:noinline
func testGrowStack(depth int) int {
	var padding [1024]byte
	for index := range padding {
		padding[index] = byte(index + depth)
	}
	if depth == 0 {
		return int(padding[0])
	}
	return testGrowStack(depth-1) + int(padding[depth%len(padding)])
}

var (
	testLivenessFinalizedDuringCallback atomic.Bool
	testLivenessCallbackReadCorrupted   atomic.Bool
	testLivenessCurrentOwnerID          atomic.Uint64
	testLivenessFinalizedOwnerID        atomic.Uint64
)

type testLivenessRequestBuffer [2 * 1024 * 1024]byte

func TestDynamicLibraryClientCallKeepsRequestMemoryAlive(t *testing.T) {
	testLivenessFinalizedDuringCallback.Store(false)
	testLivenessCallbackReadCorrupted.Store(false)

	client := newGuardedPluginClient(&dynamicLibraryClient{api: windowsPluginAPI{
		call:       syscall.NewCallback(testLivenessPluginCall),
		freeBuffer: syscall.NewCallback(testReentrantPluginFree),
	}})
	t.Cleanup(client.Shutdown)

	req := new(testLivenessRequestBuffer)
	req[0] = 0xAA
	req[len(req)-1] = 0x55
	ownerID := testLivenessCurrentOwnerID.Add(1)
	runtime.SetFinalizer(req, func(_ *testLivenessRequestBuffer) {
		testLivenessFinalizedOwnerID.Store(ownerID)
	})

	got, errCall := client.Call(context.Background(), "test.liveness", req[:])
	if errCall != nil {
		t.Fatalf("Call() error = %v", errCall)
	}
	want := `{"ok":true}`
	if string(got) != want {
		t.Fatalf("Call() response = %q, want %q", got, want)
	}
	if testLivenessFinalizedDuringCallback.Load() {
		t.Fatalf("request memory was finalized during native callback execution")
	}
	if testLivenessCallbackReadCorrupted.Load() {
		t.Fatalf("request buffer was corrupted during native callback execution")
	}
}

func testLivenessPluginCall(methodPtr, requestPtr, requestLen, responsePtr uintptr) uintptr {
	if methodPtr == 0 || requestPtr == 0 || requestLen == 0 || responsePtr == 0 {
		return 1
	}

	for i := 0; i < 5; i++ {
		runtime.GC()
		runtime.Gosched()
	}

	currentID := testLivenessCurrentOwnerID.Load()
	if testLivenessFinalizedOwnerID.Load() == currentID {
		testLivenessFinalizedDuringCallback.Store(true)
		return 2
	}

	buf := unsafe.Slice((*byte)(unsafe.Pointer(requestPtr)), requestLen)
	if buf[0] != 0xAA || buf[requestLen-1] != 0x55 {
		testLivenessCallbackReadCorrupted.Store(true)
		return 3
	}

	raw := []byte(`{"ok":true}`)
	mem, errAlloc := windows.LocalAlloc(windows.LMEM_FIXED, uint32(len(raw)))
	if errAlloc != nil || mem == 0 {
		return 1
	}
	copy(unsafe.Slice((*byte)(unsafe.Pointer(mem)), len(raw)), raw)
	response := (*windowsBuffer)(unsafe.Pointer(responsePtr))
	response.ptr = mem
	response.len = uintptr(len(raw))
	return 0
}
