package logging

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFileRequestLoggerRuntimeEnabledSharedAcrossScopes(t *testing.T) {
	logger := NewFileRequestLogger(true, t.TempDir(), "", 10)
	scoped, ok := logger.ForAPIKey("runtime-enabled-key").(*FileRequestLogger)
	if !ok {
		t.Fatal("ForAPIKey did not return a FileRequestLogger")
	}

	logger.SetEnabled(false)
	if scoped.IsEnabled() {
		t.Fatal("scoped logger did not observe parent SetEnabled(false)")
	}

	scoped.SetEnabled(true)
	if !logger.IsEnabled() {
		t.Fatal("parent logger did not observe scoped SetEnabled(true)")
	}
}

func TestFileRequestLoggerRuntimeHomeSettingSharedAcrossScopes(t *testing.T) {
	original := currentHomeRequestLogClient
	defer func() {
		currentHomeRequestLogClient = original
	}()

	stub := &stubHomeRequestLogClient{heartbeatOK: true}
	currentHomeRequestLogClient = func() homeRequestLogClient {
		return stub
	}

	logsDir := t.TempDir()
	logger := NewFileRequestLogger(true, logsDir, "", 10)
	scoped, ok := logger.ForKeyLabel("runtime-home").(*FileRequestLogger)
	if !ok {
		t.Fatal("ForKeyLabel did not return a FileRequestLogger")
	}

	logger.SetHomeEnabled(true)
	if errLog := logRuntimeTestRequest(scoped, "scoped-home"); errLog != nil {
		t.Fatalf("scoped home LogRequest: %v", errLog)
	}
	if len(stub.pushed) != 1 {
		t.Fatalf("home pushed records = %d, want 1", len(stub.pushed))
	}
	if _, errStat := os.Stat(filepath.Join(logsDir, "keys", "runtime-home")); !os.IsNotExist(errStat) {
		t.Fatalf("scoped logger wrote local artifacts in home mode: %v", errStat)
	}

	scoped.SetHomeEnabled(false)
	if errLog := logRuntimeTestRequest(logger, "parent-local"); errLog != nil {
		t.Fatalf("parent local LogRequest: %v", errLog)
	}
	if len(stub.pushed) != 1 {
		t.Fatalf("home pushed records after disabling via scope = %d, want 1", len(stub.pushed))
	}
	if got := countRuntimeErrorOrRequestLogs(t, logsDir, false); got != 1 {
		t.Fatalf("parent local request logs = %d, want 1", got)
	}
}

func TestFileRequestLoggerRuntimeErrorRetentionSharedAcrossScopes(t *testing.T) {
	logsDir := t.TempDir()
	logger := NewFileRequestLogger(false, logsDir, "", 10)
	scoped, ok := logger.ForKeyLabel("runtime-retention").(*FileRequestLogger)
	if !ok {
		t.Fatal("ForKeyLabel did not return a FileRequestLogger")
	}

	logger.SetErrorLogsMaxFiles(1)
	for index := 0; index < 2; index++ {
		if errLog := logRuntimeTestForcedError(scoped, fmt.Sprintf("scoped-%d", index)); errLog != nil {
			t.Fatalf("scoped forced error %d: %v", index, errLog)
		}
	}
	scopedDir := filepath.Join(logsDir, "keys", "runtime-retention")
	if got := countRuntimeErrorOrRequestLogs(t, scopedDir, true); got != 1 {
		t.Fatalf("scoped retained error logs = %d, want 1", got)
	}

	scoped.SetErrorLogsMaxFiles(2)
	for index := 0; index < 3; index++ {
		if errLog := logRuntimeTestForcedError(logger, fmt.Sprintf("parent-%d", index)); errLog != nil {
			t.Fatalf("parent forced error %d: %v", index, errLog)
		}
	}
	if got := countRuntimeErrorOrRequestLogs(t, logsDir, true); got != 2 {
		t.Fatalf("parent retained error logs = %d, want 2", got)
	}
}

func TestFileRequestLoggerRuntimeSettingsConcurrentScopes(t *testing.T) {
	original := currentHomeRequestLogClient
	defer func() {
		currentHomeRequestLogClient = original
	}()
	currentHomeRequestLogClient = func() homeRequestLogClient { return nil }

	logger := NewFileRequestLogger(true, t.TempDir(), "", 3)
	start := make(chan struct{})
	errCh := make(chan error, 32)
	var waitGroup sync.WaitGroup

	waitGroup.Add(1)
	go func() {
		defer waitGroup.Done()
		<-start
		for index := 0; index < 64; index++ {
			logger.SetEnabled(index%2 == 0)
			logger.SetHomeEnabled(index%3 == 0)
			logger.SetErrorLogsMaxFiles(index%4 + 1)
		}
	}()

	waitGroup.Add(1)
	go func() {
		defer waitGroup.Done()
		<-start
		for index := 0; index < 64; index++ {
			scoped := logger.ForAPIKey(fmt.Sprintf("runtime-key-%d", index%4))
			_ = scoped.IsEnabled()
			if fileLogger, ok := scoped.(*FileRequestLogger); ok {
				fileLogger.SetEnabled(index%2 != 0)
			}
		}
	}()

	waitGroup.Add(1)
	go func() {
		defer waitGroup.Done()
		<-start
		for index := 0; index < 32; index++ {
			scoped := logger.ForKeyLabel(fmt.Sprintf("runtime-label-%d", index%2))
			if errLog := logRuntimeTestRequest(scoped, fmt.Sprintf("race-%d", index)); errLog != nil {
				errCh <- errLog
			}
		}
	}()

	close(start)
	waitGroup.Wait()
	close(errCh)
	for errLog := range errCh {
		t.Errorf("concurrent LogRequest: %v", errLog)
	}
}

func TestZeroValueFileRequestLoggerConcurrentFirstUse(t *testing.T) {
	var logger FileRequestLogger
	start := make(chan struct{})
	var waitGroup sync.WaitGroup

	for worker := 0; worker < 32; worker++ {
		worker := worker
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			for iteration := 0; iteration < 32; iteration++ {
				switch worker % 4 {
				case 0:
					logger.SetAPIKeyNames([]string{"zero-key"}, []string{"zero-name"})
				case 1:
					logger.SetNoLogAPIKeys([]string{"zero-skip"})
				case 2:
					_ = logger.ForAPIKey("zero-key").IsEnabled()
				default:
					_ = logger.ShouldSkipLog("zero-skip")
				}
			}
		}()
	}

	close(start)
	waitGroup.Wait()
	logger.SetAPIKeyNames([]string{"zero-key"}, []string{"zero-name"})
	if scoped := logger.ForAPIKey("zero-key"); scoped == nil {
		t.Fatal("zero-value logger returned a nil scoped logger")
	}
}

func logRuntimeTestRequest(logger RequestLogger, requestID string) error {
	return logger.LogRequest(
		"/v1/chat/completions",
		http.MethodPost,
		map[string][]string{"Content-Type": {"application/json"}},
		[]byte(`{"input":"hello"}`),
		http.StatusOK,
		map[string][]string{"Content-Type": {"application/json"}},
		[]byte(`{"ok":true}`),
		nil,
		nil,
		nil,
		nil,
		nil,
		requestID,
		time.Now(),
		time.Now(),
	)
}

func logRuntimeTestForcedError(logger *FileRequestLogger, requestID string) error {
	return logger.LogRequestWithOptions(
		"/v1/chat/completions",
		http.MethodPost,
		map[string][]string{"Content-Type": {"application/json"}},
		[]byte(`{"input":"hello"}`),
		http.StatusBadGateway,
		map[string][]string{"Content-Type": {"application/json"}},
		[]byte(`{"error":"upstream"}`),
		nil,
		nil,
		nil,
		nil,
		nil,
		true,
		requestID,
		time.Now(),
		time.Now(),
	)
}

func countRuntimeErrorOrRequestLogs(t *testing.T, logsDir string, errorsOnly bool) int {
	t.Helper()
	entries, errRead := os.ReadDir(logsDir)
	if errRead != nil {
		t.Fatalf("read logs directory %s: %v", logsDir, errRead)
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}
		if errorsOnly && !strings.HasPrefix(entry.Name(), "error-") {
			continue
		}
		count++
	}
	return count
}
