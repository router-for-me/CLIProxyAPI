#include <stddef.h>
#include <stdint.h>
#include <stdlib.h>
#include <unistd.h>

#ifndef PLUGIN_VERSION
#define PLUGIN_VERSION 1
#endif

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_test_buffer;

typedef int (*cliproxy_test_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_test_buffer*);
typedef void (*cliproxy_test_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_test_host_call_fn call;
	cliproxy_test_host_free_fn free_buffer;
} cliproxy_test_host_api;

#if PLUGIN_VERSION == 4
#include <signal.h>
__attribute__((constructor)) static void cliproxyTestSigillOnDlopen(void) {
	raise(SIGILL);
}
#elif PLUGIN_VERSION == 5
__attribute__((constructor)) static void cliproxyTestPrematureExitZeroOnDlopen(void) {
	_exit(0);
}
#elif PLUGIN_VERSION == 6
#include <signal.h>
__attribute__((constructor)) static void cliproxyTestSigkillOnDlopen(void) {
	raise(SIGKILL);
}
#elif PLUGIN_VERSION == 7
#include <errno.h>
#include <fcntl.h>
#include <signal.h>
#include <stdio.h>
#include <sys/file.h>
#include <sys/wait.h>
__attribute__((constructor)) static void cliproxyTestInheritedStdioGrandchildOnDlopen(void) {
	const char* pid_path = getenv("CLIPROXY_TEST_GRANDCHILD_PID_FILE");
	if (pid_path == NULL || pid_path[0] == '\0') {
		return;
	}
	int pid_fd = open(pid_path, O_WRONLY | O_CREAT | O_APPEND, 0600);
	if (pid_fd < 0) {
		_exit(1);
	}
	int lock_fd = -1;
	const char* lock_path = getenv("CLIPROXY_TEST_GRANDCHILD_LOCK_FILE");
	if (lock_path != NULL && lock_path[0] != '\0') {
		lock_fd = open(lock_path, O_CREAT | O_RDWR, 0600);
		if (lock_fd < 0 || flock(lock_fd, LOCK_SH) != 0) {
			if (lock_fd >= 0) {
				close(lock_fd);
			}
			close(pid_fd);
			_exit(1);
		}
	}
	pid_t pid = fork();
	if (pid < 0) {
		if (lock_fd >= 0) {
			close(lock_fd);
		}
		close(pid_fd);
		_exit(1);
	}
	if (pid == 0) {
		// Only perform async-signal-safe calls in the forked child. The
		// inherited lock_fd keeps LOCK_SH on the shared open file description
		// until this child exits and the kernel closes its descriptors.
		close(pid_fd);
		pause();
		_exit(0);
	}
	if (lock_fd >= 0) {
		close(lock_fd);
	}
	FILE* fp = fdopen(pid_fd, "a");
	if (fp == NULL) {
		close(pid_fd);
		(void)kill(pid, SIGKILL);
		while (waitpid(pid, NULL, 0) < 0 && errno == EINTR) {
		}
		_exit(1);
	}
	int wrote = fprintf(fp, "%d\n", (int)pid);
	int flushed = fflush(fp);
	int closed = fclose(fp);
	if (wrote < 0 || flushed != 0 || closed != 0) {
		(void)kill(pid, SIGKILL);
		while (waitpid(pid, NULL, 0) < 0 && errno == EINTR) {
		}
		_exit(1);
	}
}
#endif

const char* cliproxyTestMarker(void) {
#if PLUGIN_VERSION == 2
	return "v2";
#else
	return "v1";
#endif
}

int cliproxyTestFailInit(void) {
#if PLUGIN_VERSION == 3
	return 1;
#else
	return 0;
#endif
}

static const cliproxy_test_host_api* cliproxy_test_stored_host = NULL;

void cliproxyTestStoreHost(const void* host) {
	cliproxy_test_stored_host = (const cliproxy_test_host_api*)host;
}

void cliproxyTestOnShutdown(void) {
#if PLUGIN_VERSION == 8
	if (cliproxy_test_stored_host != NULL && cliproxy_test_stored_host->call != NULL) {
		cliproxy_test_buffer resp = {0};
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
