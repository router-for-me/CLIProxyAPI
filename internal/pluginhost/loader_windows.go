//go:build windows

package pluginhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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
	shadowPluginCleanupOnce    sync.Once
)

const (
	shadowPluginPrefix           = "cliproxy-plugin-"
	shadowPluginTempPrefix       = ".cliproxy-plugin-"
	shadowPluginProcessDirPrefix = "pid-"
	shadowPluginDigestLength     = 32
)

type dynamicLibraryLoader struct{}

type dynamicLibraryClient struct {
	dll      *syscall.DLL
	tempPath string
	hostAPI  *windowsHostAPI
	hostCtx  *uintptr
	api      windowsPluginAPI
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
		_ = dll.Release()
		removeShadowPlugin(loadPath)
		return nil, errProc
	}
	id := windowsHostCallbackID.Add(1)
	hostCtx := new(uintptr)
	*hostCtx = id
	windowsHostCallbackEntries.Store(id, dynamicHostCallbackEntry{host: host, pluginID: file.ID})
	client := &dynamicLibraryClient{
		dll:      dll,
		tempPath: loadPath,
		hostCtx:  hostCtx,
		hostAPI: &windowsHostAPI{
			abiVersion: pluginHostABIVersion,
			hostCtx:    uintptr(unsafe.Pointer(hostCtx)),
			call:       windowsHostCallCallback,
			freeBuffer: windowsHostFreeCallback,
		},
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

func shadowCopyPlugin(file pluginFile) (string, error) {
	dir, errDir := shadowPluginDir()
	if errDir != nil {
		return "", errDir
	}
	shadowPluginCleanupOnce.Do(func() {
		removeStaleShadowPlugins(dir)
	})
	return shadowCopyPluginToDir(file, dir)
}

func shadowCopyPluginToDir(file pluginFile, dir string) (string, error) {
	source := filepath.Clean(file.Path)
	tmp, errTemp := os.CreateTemp(dir, shadowPluginTempPrefix+file.ID+"-*"+filepath.Ext(source))
	if errTemp != nil {
		return "", errTemp
	}
	tmpName := tmp.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			removeShadowPlugin(tmpName)
		}
	}()

	in, errOpen := os.Open(source)
	if errOpen != nil {
		_ = tmp.Close()
		return "", errOpen
	}
	defer func() {
		_ = in.Close()
	}()
	hasher := sha256.New()
	size, errCopy := io.Copy(io.MultiWriter(tmp, hasher), in)
	if errCopy != nil {
		_ = tmp.Close()
		return "", errCopy
	}
	if errClose := tmp.Close(); errClose != nil {
		return "", errClose
	}
	digest := hex.EncodeToString(hasher.Sum(nil))
	target := shadowPluginPath(dir, file.ID, digest, filepath.Ext(source))
	if shadowPluginMatches(target, size, digest) {
		return target, nil
	}
	if errRemove := os.Remove(target); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
		if shadowPluginMatches(target, size, digest) {
			return target, nil
		}
		removeShadowPlugin(target)
		return "", fmt.Errorf("remove stale shadow plugin: %w", errRemove)
	}
	if errRename := os.Rename(tmpName, target); errRename != nil {
		if shadowPluginMatches(target, size, digest) {
			return target, nil
		}
		return "", fmt.Errorf("move shadow plugin: %w", errRename)
	}
	removeTemp = false
	return target, nil
}

func shadowPluginDir() (string, error) {
	dir := filepath.Join(os.TempDir(), "cliproxy-pluginhost", shadowPluginProcessDirName(os.Getpid()))
	if errMkdir := os.MkdirAll(dir, 0o700); errMkdir != nil {
		return "", errMkdir
	}
	return dir, nil
}

func shadowPluginProcessDirName(pid int) string {
	return fmt.Sprintf("%s%d", shadowPluginProcessDirPrefix, pid)
}

func removeShadowPlugin(path string) {
	if path == "" {
		return
	}
	if errRemove := os.Remove(path); errRemove == nil {
		return
	}
	pathPtr, errPath := windows.UTF16PtrFromString(path)
	if errPath != nil {
		return
	}
	_ = windows.MoveFileEx(pathPtr, nil, windows.MOVEFILE_DELAY_UNTIL_REBOOT)
}

func removeStaleShadowPlugins(dir string) {
	entries, errRead := os.ReadDir(dir)
	if errRead != nil {
		return
	}
	for _, entry := range entries {
		if entry == nil || entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, shadowPluginPrefix) || strings.HasPrefix(name, shadowPluginTempPrefix) {
			removeShadowPlugin(filepath.Join(dir, name))
		}
	}
}

func shadowPluginPath(dir string, id string, digest string, extension string) string {
	if len(digest) > shadowPluginDigestLength {
		digest = digest[:shadowPluginDigestLength]
	}
	return filepath.Join(dir, shadowPluginPrefix+id+"-"+digest+extension)
}

func shadowPluginMatches(path string, size int64, digest string) bool {
	info, errStat := os.Stat(path)
	if errStat != nil {
		return false
	}
	if !info.Mode().IsRegular() || info.Size() != size {
		return false
	}
	file, errOpen := os.Open(path)
	if errOpen != nil {
		return false
	}
	defer func() {
		_ = file.Close()
	}()
	hasher := sha256.New()
	if _, errCopy := io.Copy(hasher, file); errCopy != nil {
		return false
	}
	return hex.EncodeToString(hasher.Sum(nil)) == digest
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
		if isPluginErrorEnvelope(out) {
			return out, nil
		}
		return nil, fmt.Errorf("plugin call %s returned %d: %s", method, rc, string(out))
	}
	return out, nil
}

func (c *dynamicLibraryClient) Shutdown() {
	// Windows Go DLLs are not safe to hot-unload from the host process.
	// The plugin was loaded from a shadow copy, so keeping the module mapped
	// does not block deleting or replacing the source artifact.
	c.close(false)
}

func (c *dynamicLibraryClient) closeAfterOpenFailure() {
	c.close(true)
}

func (c *dynamicLibraryClient) close(releaseDLL bool) {
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
		if releaseDLL {
			_ = c.dll.Release()
		}
		c.dll = nil
	}
	removeShadowPlugin(c.tempPath)
	c.tempPath = ""
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
