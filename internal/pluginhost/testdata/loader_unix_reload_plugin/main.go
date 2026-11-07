package main

/*
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

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

// The marker lets the host test distinguish a freshly mapped image from a
// reused image after the plugin file is replaced under the same path.
#ifndef PLUGIN_VERSION
#define PLUGIN_VERSION 1
#endif

#if PLUGIN_VERSION == 4
#include <signal.h>
__attribute__((constructor)) static void cliproxyTestSigillOnDlopen(void) {
	raise(SIGILL);
}
#elif PLUGIN_VERSION == 5
#include <unistd.h>
__attribute__((constructor)) static void cliproxyTestPrematureExitZeroOnDlopen(void) {
	_exit(0);
}
#elif PLUGIN_VERSION == 6
#include <signal.h>
__attribute__((constructor)) static void cliproxyTestSigkillOnDlopen(void) {
	raise(SIGKILL);
}
#elif PLUGIN_VERSION == 7
#include <stdio.h>
#include <unistd.h>
__attribute__((constructor)) static void cliproxyTestInheritedStdioGrandchildOnDlopen(void) {
	pid_t pid = fork();
	if (pid == 0) {
		pause();
		_exit(0);
	}
	if (pid > 0) {
		const char* pid_path = getenv("CLIPROXY_TEST_GRANDCHILD_PID_FILE");
		if (pid_path != NULL && pid_path[0] != '\0') {
			FILE* fp = fopen(pid_path, "w");
			if (fp != NULL) {
				fprintf(fp, "%d\n", (int)pid);
				fclose(fp);
			}
		}
	}
}
#endif

static const char* cliproxyTestMarker(void) {
#if PLUGIN_VERSION == 2
	return "v2";
#else
	return "v1";
#endif
}

static int cliproxyTestFailInit(void) {
#if PLUGIN_VERSION == 3
	return 1;
#else
	return 0;
#endif
}

static const cliproxy_host_api* cliproxy_test_stored_host = NULL;

static void cliproxyTestStoreHost(const cliproxy_host_api* host) {
	cliproxy_test_stored_host = host;
}

static void cliproxyTestOnShutdown(void) {
#if PLUGIN_VERSION == 8
	if (cliproxy_test_stored_host != NULL && cliproxy_test_stored_host->call != NULL) {
		cliproxy_buffer resp = {0};
		(void)cliproxy_test_stored_host->call(cliproxy_test_stored_host->host_ctx, "host.log", NULL, 0, &resp);
	}
	char chunk[4096];
	for (size_t i = 0; i < sizeof(chunk); i++) {
		chunk[i] = 'E';
	}
	chunk[sizeof(chunk) - 1] = '\n';
	for (int i = 0; i < 32; i++) {
		(void)write(2, chunk, sizeof(chunk));
	}
	_exit(1);
#endif
}
*/
import "C"

import (
	"strconv"
	"sync/atomic"
	"unsafe"
)

var shutdownCalls atomic.Int64

var failInit = C.cliproxyTestFailInit() != 0

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	C.cliproxyTestStoreHost(host)
	if failInit {
		return 7
	}
	plugin.abi_version = 1
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response == nil {
		return 1
	}
	response.ptr = nil
	response.len = 0
	var body []byte
	if method != nil && C.GoString(method) == "test.shutdown_calls" {
		body = []byte(strconv.FormatInt(shutdownCalls.Load(), 10))
	} else {
		body = []byte(C.GoString(C.cliproxyTestMarker()))
	}
	ptr := C.CBytes(body)
	if ptr == nil {
		return 1
	}
	response.ptr = ptr
	response.len = C.size_t(len(body))
	_ = request
	_ = requestLen
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, _ C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	shutdownCalls.Add(1)
	C.cliproxyTestOnShutdown()
}

func main() {}
