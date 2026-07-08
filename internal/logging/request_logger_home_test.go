package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type stubHomeRequestLogClient struct {
	heartbeatOK bool
	pushed      [][]byte
}

func (c *stubHomeRequestLogClient) HeartbeatOK() bool { return c.heartbeatOK }

func (c *stubHomeRequestLogClient) RPushRequestLog(_ context.Context, payload []byte) error {
	c.pushed = append(c.pushed, bytes.Clone(payload))
	return nil
}

func assertFileBodySourceCleaned(t *testing.T, partPaths []string) {
	t.Helper()

	dirs := make(map[string]struct{}, len(partPaths))
	for _, path := range partPaths {
		if _, errStat := os.Stat(path); !os.IsNotExist(errStat) {
			t.Fatalf("expected part %s to be removed, stat err=%v", path, errStat)
		}
		dirs[filepath.Dir(path)] = struct{}{}
	}
	for dir := range dirs {
		if _, errStat := os.Stat(dir); !os.IsNotExist(errStat) {
			t.Fatalf("expected part dir %s to be removed, stat err=%v", dir, errStat)
		}
	}
}

func TestFileBodySource_RecreatesPartDirAfterManualCleanup(t *testing.T) {
	logsDir := t.TempDir()
	source, errSource := NewFileBodySourceInDir(logsDir, "websocket-timeline-test")
	if errSource != nil {
		t.Fatalf("NewFileBodySourceInDir: %v", errSource)
	}
	if errAppend := source.AppendPart([]byte("before manual cleanup")); errAppend != nil {
		t.Fatalf("AppendPart before cleanup: %v", errAppend)
	}
	if errRemove := os.RemoveAll(logsDir); errRemove != nil {
		t.Fatalf("RemoveAll logs dir: %v", errRemove)
	}
	if errAppend := source.AppendPart([]byte("after manual cleanup")); errAppend != nil {
		t.Fatalf("AppendPart after cleanup: %v", errAppend)
	}

	raw, errBytes := source.Bytes()
	if errBytes != nil {
		t.Fatalf("Bytes after cleanup: %v", errBytes)
	}
	if bytes.Contains(raw, []byte("before manual cleanup")) {
		t.Fatalf("expected manually removed part to be skipped, got %q", string(raw))
	}
	if !bytes.Contains(raw, []byte("after manual cleanup")) {
		t.Fatalf("expected recreated part content, got %q", string(raw))
	}

	partPaths := source.Paths()
	if errCleanup := source.Cleanup(); errCleanup != nil {
		t.Fatalf("Cleanup: %v", errCleanup)
	}
	assertFileBodySourceCleaned(t, partPaths)
}

func TestFileRequestLogger_HomeEnabled_ForwardsWhenRequestLogEnabled(t *testing.T) {
	original := currentHomeRequestLogClient
	defer func() {
		currentHomeRequestLogClient = original
	}()

	stub := &stubHomeRequestLogClient{heartbeatOK: true}
	currentHomeRequestLogClient = func() homeRequestLogClient {
		return stub
	}

	logsDir := t.TempDir()
	logger := NewFileRequestLogger(true, logsDir, "", 0)
	logger.SetHomeEnabled(true)

	requestHeaders := map[string][]string{
		"Content-Type":  {"application/json"},
		"Authorization": {"Bearer secret"},
	}

	errLog := logger.LogRequest(
		"/v1/chat/completions",
		http.MethodPost,
		requestHeaders,
		[]byte(`{"input":"hello"}`),
		http.StatusOK,
		map[string][]string{"Content-Type": {"application/json"}},
		[]byte(`{"ok":true}`),
		nil,
		nil,
		nil,
		nil,
		nil,
		"req-1",
		time.Now(),
		time.Now(),
	)
	if errLog != nil {
		t.Fatalf("LogRequest error: %v", errLog)
	}

	entries, errRead := os.ReadDir(logsDir)
	if errRead != nil {
		t.Fatalf("failed to read logs dir: %v", errRead)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no local request log files, got entries: %+v", entries)
	}

	if len(stub.pushed) != 1 {
		t.Fatalf("home pushed records = %d, want 1", len(stub.pushed))
	}

	var got struct {
		Headers    map[string][]string `json:"headers"`
		RequestID  string              `json:"request_id"`
		RequestLog string              `json:"request_log"`
	}
	if errUnmarshal := json.Unmarshal(stub.pushed[0], &got); errUnmarshal != nil {
		t.Fatalf("unmarshal payload: %v payload=%s", errUnmarshal, string(stub.pushed[0]))
	}
	if got.Headers == nil || got.Headers["Content-Type"][0] != "application/json" {
		t.Fatalf("headers.content-type = %+v, want application/json", got.Headers["Content-Type"])
	}
	if got.Headers == nil || got.Headers["Authorization"][0] != "Bearer secret" {
		t.Fatalf("headers.authorization = %+v, want Bearer secret", got.Headers["Authorization"])
	}
	if got.RequestID != "req-1" {
		t.Fatalf("request_id = %q, want req-1", got.RequestID)
	}
	if got.RequestLog == "" {
		t.Fatalf("request_log empty, want non-empty")
	}
}

func TestFileRequestLogger_LogRequestWithSourcesWritesLocalLogAndCleansParts(t *testing.T) {
	logsDir := t.TempDir()
	logger := NewFileRequestLogger(true, logsDir, "", 0)

	timelineSource, errSource := logger.NewFileBodySource("websocket-timeline-test")
	if errSource != nil {
		t.Fatalf("logger.NewFileBodySource: %v", errSource)
	}
	if errAppend := timelineSource.AppendPart([]byte("Timestamp: 2026-05-25T12:00:00Z\nEvent: websocket.request\n{}")); errAppend != nil {
		t.Fatalf("AppendPart request: %v", errAppend)
	}
	if errAppend := timelineSource.AppendPart([]byte("Timestamp: 2026-05-25T12:00:01Z\nEvent: websocket.response\n{}")); errAppend != nil {
		t.Fatalf("AppendPart response: %v", errAppend)
	}
	partPaths := timelineSource.Paths()
	for _, path := range partPaths {
		if !strings.HasPrefix(path, logsDir+string(os.PathSeparator)) {
			t.Fatalf("part path %s is not under logs dir %s", path, logsDir)
		}
	}

	errLog := logger.LogRequestWithOptionsAndSources(
		"/v1/responses/ws",
		http.MethodGet,
		map[string][]string{"Upgrade": {"websocket"}},
		nil,
		http.StatusSwitchingProtocols,
		map[string][]string{"Upgrade": {"websocket"}},
		nil,
		nil,
		timelineSource,
		nil,
		nil,
		nil,
		nil,
		nil,
		false,
		"ws-req-1",
		time.Now(),
		time.Now(),
	)
	if errLog != nil {
		t.Fatalf("LogRequestWithOptionsAndSources error: %v", errLog)
	}

	assertFileBodySourceCleaned(t, partPaths)

	entries, errRead := os.ReadDir(logsDir)
	if errRead != nil {
		t.Fatalf("failed to read logs dir: %v", errRead)
	}
	var logPath string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		logPath = logsDir + string(os.PathSeparator) + entry.Name()
		break
	}
	if logPath == "" {
		t.Fatal("expected local request log file")
	}
	raw, errReadLog := os.ReadFile(logPath)
	if errReadLog != nil {
		t.Fatalf("read log file: %v", errReadLog)
	}
	if !bytes.Contains(raw, []byte("=== WEBSOCKET TIMELINE ===")) {
		t.Fatalf("websocket timeline section missing: %s", string(raw))
	}
	if !bytes.Contains(raw, []byte("Event: websocket.request")) || !bytes.Contains(raw, []byte("Event: websocket.response")) {
		t.Fatalf("merged websocket events missing: %s", string(raw))
	}
}

func TestFileRequestLogger_HomeEnabled_ForwardsSourceLogAndCleansParts(t *testing.T) {
	original := currentHomeRequestLogClient
	defer func() {
		currentHomeRequestLogClient = original
	}()

	stub := &stubHomeRequestLogClient{heartbeatOK: true}
	currentHomeRequestLogClient = func() homeRequestLogClient {
		return stub
	}

	logsDir := t.TempDir()
	logger := NewFileRequestLogger(true, logsDir, "", 0)
	logger.SetHomeEnabled(true)

	timelineSource, errSource := logger.NewFileBodySource("home-websocket-timeline-test")
	if errSource != nil {
		t.Fatalf("logger.NewFileBodySource: %v", errSource)
	}
	if errAppend := timelineSource.AppendPart([]byte("Timestamp: 2026-05-25T12:00:00Z\nEvent: websocket.request\n{}")); errAppend != nil {
		t.Fatalf("AppendPart request: %v", errAppend)
	}
	partPaths := timelineSource.Paths()
	for _, path := range partPaths {
		if !strings.HasPrefix(path, logsDir+string(os.PathSeparator)) {
			t.Fatalf("part path %s is not under logs dir %s", path, logsDir)
		}
	}

	errLog := logger.LogRequestWithOptionsAndSources(
		"/v1/responses/ws",
		http.MethodGet,
		map[string][]string{"Upgrade": {"websocket"}},
		nil,
		http.StatusSwitchingProtocols,
		map[string][]string{"Upgrade": {"websocket"}},
		nil,
		nil,
		timelineSource,
		nil,
		nil,
		nil,
		nil,
		nil,
		false,
		"home-ws-req-1",
		time.Now(),
		time.Now(),
	)
	if errLog != nil {
		t.Fatalf("LogRequestWithOptionsAndSources error: %v", errLog)
	}
	if len(stub.pushed) != 1 {
		t.Fatalf("home pushed records = %d, want 1", len(stub.pushed))
	}

	var got struct {
		RequestID  string `json:"request_id"`
		RequestLog string `json:"request_log"`
	}
	if errUnmarshal := json.Unmarshal(stub.pushed[0], &got); errUnmarshal != nil {
		t.Fatalf("unmarshal payload: %v payload=%s", errUnmarshal, string(stub.pushed[0]))
	}
	if got.RequestID != "home-ws-req-1" {
		t.Fatalf("request_id = %q, want home-ws-req-1", got.RequestID)
	}
	if !strings.Contains(got.RequestLog, "Event: websocket.request") {
		t.Fatalf("forwarded request_log missing websocket request: %s", got.RequestLog)
	}
	assertFileBodySourceCleaned(t, partPaths)
}

func TestFileRequestLogger_HomeEnabled_ForwardsStreamingRequestID(t *testing.T) {
	original := currentHomeRequestLogClient
	defer func() {
		currentHomeRequestLogClient = original
	}()

	stub := &stubHomeRequestLogClient{heartbeatOK: true}
	currentHomeRequestLogClient = func() homeRequestLogClient {
		return stub
	}

	logsDir := t.TempDir()
	logger := NewFileRequestLogger(true, logsDir, "", 0)
	logger.SetHomeEnabled(true)

	writer, errLog := logger.LogStreamingRequest(
		"/v1/responses",
		http.MethodPost,
		map[string][]string{"Content-Type": {"application/json"}},
		[]byte(`{"input":"hello"}`),
		"stream-req-1",
	)
	if errLog != nil {
		t.Fatalf("LogStreamingRequest error: %v", errLog)
	}

	if errStatus := writer.WriteStatus(http.StatusOK, map[string][]string{"Content-Type": {"text/event-stream"}}); errStatus != nil {
		t.Fatalf("WriteStatus error: %v", errStatus)
	}
	writer.WriteChunkAsync([]byte("data: ok\n\n"))
	if errClose := writer.Close(); errClose != nil {
		t.Fatalf("Close error: %v", errClose)
	}

	if len(stub.pushed) != 1 {
		t.Fatalf("home pushed records = %d, want 1", len(stub.pushed))
	}

	var got struct {
		RequestID  string `json:"request_id"`
		RequestLog string `json:"request_log"`
	}
	if errUnmarshal := json.Unmarshal(stub.pushed[0], &got); errUnmarshal != nil {
		t.Fatalf("unmarshal payload: %v payload=%s", errUnmarshal, string(stub.pushed[0]))
	}
	if got.RequestID != "stream-req-1" {
		t.Fatalf("request_id = %q, want stream-req-1", got.RequestID)
	}
	if got.RequestLog == "" {
		t.Fatalf("request_log empty, want non-empty")
	}
}

func TestFileRequestLogger_HomeEnabled_DoesNotForwardForcedErrorLogsWhenRequestLogDisabled(t *testing.T) {
	original := currentHomeRequestLogClient
	defer func() {
		currentHomeRequestLogClient = original
	}()

	stub := &stubHomeRequestLogClient{heartbeatOK: true}
	currentHomeRequestLogClient = func() homeRequestLogClient {
		return stub
	}

	logsDir := t.TempDir()
	logger := NewFileRequestLogger(false, logsDir, "", 0)
	logger.SetHomeEnabled(true)

	errLog := logger.LogRequestWithOptions(
		"/v1/chat/completions",
		http.MethodPost,
		map[string][]string{"Content-Type": {"application/json"}},
		[]byte(`{"input":"hello"}`),
		http.StatusBadGateway,
		map[string][]string{"Content-Type": {"application/json"}},
		[]byte(`{"error":"upstream failure"}`),
		nil,
		nil,
		nil,
		nil,
		nil,
		true,
		"req-2",
		time.Now(),
		time.Now(),
	)
	if errLog != nil {
		t.Fatalf("LogRequestWithOptions error: %v", errLog)
	}

	if len(stub.pushed) != 0 {
		t.Fatalf("home pushed records = %d, want 0", len(stub.pushed))
	}

	entries, errRead := os.ReadDir(logsDir)
	if errRead != nil {
		t.Fatalf("failed to read logs dir: %v", errRead)
	}
	found := false
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if entry.Name() != "" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected local forced error log file when request-log disabled")
	}
}
