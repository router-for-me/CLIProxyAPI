package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCleanupStaleRequestLogTempFiles(t *testing.T) {
	tempDir := t.TempDir()

	oldRequest := filepath.Join(tempDir, requestBodyTempPrefix+"old"+requestLoggerTempFileSuffix)
	oldResponse := filepath.Join(tempDir, responseBodyTempPrefix+"old"+requestLoggerTempFileSuffix)
	freshResponse := filepath.Join(tempDir, responseBodyTempPrefix+"fresh"+requestLoggerTempFileSuffix)
	ignored := filepath.Join(tempDir, "main.log")

	for _, path := range []string{oldRequest, oldResponse, freshResponse, ignored} {
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	oldTime := time.Now().Add(-2 * requestLoggerTempMaxAge)
	if err := os.Chtimes(oldRequest, oldTime, oldTime); err != nil {
		t.Fatalf("chtimes old request: %v", err)
	}
	if err := os.Chtimes(oldResponse, oldTime, oldTime); err != nil {
		t.Fatalf("chtimes old response: %v", err)
	}

	removed, err := cleanupStaleRequestLogTempFiles(tempDir, requestLoggerTempMaxAge)
	if err != nil {
		t.Fatalf("cleanup temp files: %v", err)
	}
	if removed != 2 {
		t.Fatalf("expected 2 files removed, got %d", removed)
	}

	if _, err := os.Stat(oldRequest); !os.IsNotExist(err) {
		t.Fatalf("expected old request temp file removed, got stat err=%v", err)
	}
	if _, err := os.Stat(oldResponse); !os.IsNotExist(err) {
		t.Fatalf("expected old response temp file removed, got stat err=%v", err)
	}
	if _, err := os.Stat(freshResponse); err != nil {
		t.Fatalf("expected fresh response temp file kept, got stat err=%v", err)
	}
	if _, err := os.Stat(ignored); err != nil {
		t.Fatalf("expected non-temp file kept, got stat err=%v", err)
	}
}

func TestLogRequestDoesNotCreateTempFilesInLogsDir(t *testing.T) {
	logsDir := t.TempDir()
	logger := NewFileRequestLogger(true, logsDir, "", 0)

	if err := logger.LogRequest(
		"/v1/responses",
		"POST",
		map[string][]string{"Content-Type": {"application/json"}},
		[]byte(`{"hello":"world"}`),
		200,
		map[string][]string{"Content-Type": {"application/json"}},
		[]byte(`{"ok":true}`),
		nil,
		nil,
		nil,
		nil,
		nil,
		"req-nonstream",
		time.Unix(1, 0),
		time.Unix(2, 0),
	); err != nil {
		t.Fatalf("log request: %v", err)
	}

	matches, err := filepath.Glob(filepath.Join(logsDir, "*"+requestLoggerTempFileSuffix))
	if err != nil {
		t.Fatalf("glob temp files: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("expected no temp files in logs dir, got %v", matches)
	}
}

func TestLogStreamingRequestUsesDedicatedTempDirAndCleansUp(t *testing.T) {
	logsDir := t.TempDir()
	logger := NewFileRequestLogger(true, logsDir, "", 0)

	streamWriter, err := logger.LogStreamingRequest(
		"/v1/responses",
		"POST",
		map[string][]string{"Content-Type": {"application/json"}},
		[]byte(`{"stream":true}`),
		"req-stream",
	)
	if err != nil {
		t.Fatalf("LogStreamingRequest: %v", err)
	}

	writer, ok := streamWriter.(*FileStreamingLogWriter)
	if !ok {
		t.Fatalf("expected *FileStreamingLogWriter, got %T", streamWriter)
	}

	responseTempPath := writer.responseBodyPath
	if responseTempPath == "" {
		t.Fatal("expected response temp path to be set")
	}
	if strings.HasPrefix(responseTempPath, logsDir) {
		t.Fatalf("expected response temp file outside logs dir, got %s", responseTempPath)
	}

	if err := writer.WriteStatus(200, map[string][]string{"Content-Type": {"text/event-stream"}}); err != nil {
		t.Fatalf("WriteStatus: %v", err)
	}
	writer.WriteChunkAsync([]byte("data: hello\n\n"))
	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := os.Stat(responseTempPath); !os.IsNotExist(err) {
		t.Fatalf("expected response temp file removed, got stat err=%v", err)
	}

	logMatches, err := filepath.Glob(filepath.Join(logsDir, "*req-stream.log"))
	if err != nil {
		t.Fatalf("glob final log: %v", err)
	}
	if len(logMatches) != 1 {
		t.Fatalf("expected one final log file, got %v", logMatches)
	}

	content, err := os.ReadFile(logMatches[0])
	if err != nil {
		t.Fatalf("read final log: %v", err)
	}
	if !strings.Contains(string(content), "data: hello") {
		t.Fatalf("expected streamed response in final log, got %q", string(content))
	}

	matches, err := filepath.Glob(filepath.Join(logsDir, "*"+requestLoggerTempFileSuffix))
	if err != nil {
		t.Fatalf("glob temp files: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("expected no temp files left in logs dir, got %v", matches)
	}
}

func TestLogStreamingRequestFallsBackWhenPrimaryTempDirUnavailable(t *testing.T) {
	logsDir := t.TempDir()
	logger := NewFileRequestLogger(true, logsDir, "", 0)

	blockedPath := filepath.Join(logsDir, "blocked-temp-dir")
	if err := os.WriteFile(blockedPath, []byte("blocked"), 0o644); err != nil {
		t.Fatalf("write blocked temp file: %v", err)
	}

	fallbackDir := filepath.Join(logsDir, "fallback-temp")
	logger.tempDir = blockedPath
	logger.fallbackTempDir = fallbackDir

	streamWriter, err := logger.LogStreamingRequest(
		"/v1/responses",
		"POST",
		map[string][]string{"Content-Type": {"application/json"}},
		[]byte(`{"stream":true}`),
		"req-stream-fallback",
	)
	if err != nil {
		t.Fatalf("LogStreamingRequest fallback: %v", err)
	}

	writer, ok := streamWriter.(*FileStreamingLogWriter)
	if !ok {
		t.Fatalf("expected *FileStreamingLogWriter, got %T", streamWriter)
	}

	if !strings.HasPrefix(writer.responseBodyPath, fallbackDir) {
		t.Fatalf("expected response temp file under fallback dir %s, got %s", fallbackDir, writer.responseBodyPath)
	}

	if err := writer.WriteStatus(200, map[string][]string{"Content-Type": {"text/event-stream"}}); err != nil {
		t.Fatalf("WriteStatus: %v", err)
	}
	writer.WriteChunkAsync([]byte("data: fallback\n\n"))
	if err := writer.Close(); err != nil {
		t.Fatalf("Close fallback writer: %v", err)
	}
}
