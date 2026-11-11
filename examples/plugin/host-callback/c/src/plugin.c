#include <stdint.h>
#include <stdlib.h>
#include <string.h>

#if defined(_WIN32)
#define CLIPROXY_EXPORT __declspec(dllexport)
#else
#define CLIPROXY_EXPORT __attribute__((visibility("default")))
#endif

#define ABI_VERSION 1

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

static const cliproxy_host_api* stored_host = NULL;

static void write_response(cliproxy_buffer* response, const char* text) {
	if (response == NULL || text == NULL) {
		return;
	}
	size_t len = strlen(text);
	void* ptr = malloc(len);
	if (ptr == NULL) {
		response->ptr = NULL;
		response->len = 0;
		return;
	}
	memcpy(ptr, text, len);
	response->ptr = ptr;
	response->len = len;
}

static void call_host(const char* method, const char* payload) {
	if (stored_host == NULL || stored_host->call == NULL || method == NULL) {
		return;
	}
	cliproxy_buffer response = {0};
	const uint8_t* request = (const uint8_t*)payload;
	size_t request_len = payload == NULL ? 0 : strlen(payload);
	if (stored_host->call(stored_host->host_ctx, method, request, request_len, &response) == 0 && response.ptr != NULL && stored_host->free_buffer != NULL) {
		stored_host->free_buffer(response.ptr, response.len);
	}
}

static int plugin_call(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (response != NULL) {
		response->ptr = NULL;
		response->len = 0;
	}
	if (method == NULL) {
		write_response(response, "{\"ok\":false,\"error\":{\"code\":\"invalid_method\",\"message\":\"method is required\"}}");
		return 1;
	}
	if (strcmp(method, "plugin.register") == 0) {
		write_response(response, "{\"ok\":true,\"result\":{\"schema_version\":1,\"metadata\":{\"Name\":\"example-host-callback-c\",\"Version\":\"0.1.0\",\"Author\":\"router-for-me\",\"GitHubRepository\":\"https://github.com/router-for-me/CLIProxyAPI\",\"Logo\":\"https://example.invalid/example-host-callback-c.png\",\"ConfigFields\":[]},\"capabilities\":{\"management_api\":true}}}");
		return 0;
	}
	if (strcmp(method, "plugin.reconfigure") == 0) {
		write_response(response, "{\"ok\":true,\"result\":{\"schema_version\":1,\"metadata\":{\"Name\":\"example-host-callback-c\",\"Version\":\"0.1.0\",\"Author\":\"router-for-me\",\"GitHubRepository\":\"https://github.com/router-for-me/CLIProxyAPI\",\"Logo\":\"https://example.invalid/example-host-callback-c.png\",\"ConfigFields\":[]},\"capabilities\":{\"management_api\":true}}}");
		return 0;
	}
	if (strcmp(method, "management.register") == 0) {
		write_response(response, "{\"ok\":true,\"result\":{\"resources\":[{\"Path\":\"/status\",\"Menu\":\"Host Callback\",\"Description\":\"CPA exposes this menu resource under /v0/resource/plugins/example-host-callback-c/status.\"}]}}");
		return 0;
	}
	if (strcmp(method, "management.handle") == 0) {
		call_host("host.log", "{\"level\":\"info\",\"message\":\"example-host-callback-c host callback log\",\"fields\":{\"plugin\":\"example-host-callback-c\"}}");
		call_host("host.http.do", "{\"method\":\"GET\",\"url\":\"https://example.com\",\"headers\":{\"user-agent\":[\"example-host-callback-c\"]}}");

		write_response(response, "{\"ok\":true,\"result\":{\"StatusCode\":200,\"Headers\":{\"content-type\":[\"text/html; charset=utf-8\"]},\"Body\":\"PCFkb2N0eXBlIGh0bWw+PHRpdGxlPkhvc3QgQ2FsbGJhY2s8L3RpdGxlPjxtYWluPkhvc3QgQ2FsbGJhY2sgcmVzb3VyY2U8L21haW4+\"}}");
		return 0;
	}
	write_response(response, "{\"ok\":false,\"error\":{\"code\":\"unknown_method\",\"message\":\"unknown method\"}}");
	(void)request;
	(void)request_len;
	return 0;
}

static void plugin_free(void* ptr, size_t len) {
	(void)len;
	free(ptr);
}

static void plugin_shutdown(void) {}

CLIPROXY_EXPORT int cliproxy_plugin_init(const cliproxy_host_api* host, cliproxy_plugin_api* plugin) {
	if (plugin == NULL) {
		return 1;
	}
	stored_host = host;
	plugin->abi_version = ABI_VERSION;
	plugin->call = plugin_call;
	plugin->free_buffer = plugin_free;
	plugin->shutdown = plugin_shutdown;
	return 0;
}
