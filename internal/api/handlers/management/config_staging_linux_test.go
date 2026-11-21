package management

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

func TestFullDocumentSaveRejectsStagedHashWriteFailure(t *testing.T) {
	const childEnv = "CLIPROXY_TEST_STAGED_HASH_WRITE_FAILURE"
	if os.Getenv(childEnv) != "1" {
		// File-size limits are process-wide. Isolate the real I/O failure from
		// other tests, without relying on chmod or running as an unprivileged user.
		cmd := exec.Command(os.Args[0], "-test.run=^TestFullDocumentSaveRejectsStagedHashWriteFailure$")
		cmd.Env = append(os.Environ(), childEnv+"=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("staging I/O regression: %v\n%s", err, output)
		}
		return
	}
	h, path, router := newFullDocumentHandler(t)
	before := h.cfg.CloneForRuntime()
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	const body = "port: 8320\nremote-management:\n  secret-key: staged-save-test-only\n"
	var limit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &limit); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limit); err != nil {
			t.Error(err)
		}
	})
	signal.Ignore(syscall.SIGXFSZ)
	// The submitted document fits. Replacing the fixture with a bcrypt hash
	// grows it, so the staged hash write fails with EFBIG after a partial write.
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{Cur: uint64(len(body)), Max: limit.Max}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/v0/management/config.yaml", strings.NewReader(body)))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "failed to persist remote management key hash") {
		t.Errorf("staged hash failure status=%d, want rejection of the hash write", rec.Code)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) || !reflect.DeepEqual(h.cfg, before) || h.reloadGeneration != 0 {
		t.Error("failed staging changed disk, handler, or reload generation")
	}
	staged, err := filepath.Glob(filepath.Join(filepath.Dir(path), "config-validate-*.yaml"))
	if err != nil || len(staged) != 0 {
		t.Errorf("failed save left staging files: count=%d error=%v", len(staged), err)
	}
}
