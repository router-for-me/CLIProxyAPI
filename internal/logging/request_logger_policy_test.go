package logging

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
)

func TestFileRequestLoggerSuccessSummaryOmitsBodies(t *testing.T) {
	logsDir := t.TempDir()
	logger := NewFileRequestLogger(true, logsDir, "", 10)
	logger.SetSuccessSummaryPolicy(true, 5, 48)
	startedAt := time.Date(2026, 7, 13, 10, 15, 0, 0, time.UTC)
	logger.now = func() time.Time { return startedAt.Add(1250 * time.Millisecond) }
	apiRequestSource, errSource := logger.NewFileBodySource("success-api-request")
	if errSource != nil {
		t.Fatalf("NewFileBodySource(request) error = %v", errSource)
	}
	if errAppend := apiRequestSource.AppendPart([]byte("spooled upstream request secret")); errAppend != nil {
		t.Fatalf("AppendPart(request) error = %v", errAppend)
	}
	apiResponseSource, errSource := logger.NewFileBodySource("success-api-response")
	if errSource != nil {
		t.Fatalf("NewFileBodySource(response) error = %v", errSource)
	}
	if errAppend := apiResponseSource.AppendPart([]byte("spooled upstream response secret")); errAppend != nil {
		t.Fatalf("AppendPart(response) error = %v", errAppend)
	}
	tempPaths := append(apiRequestSource.Paths(), apiResponseSource.Paths()...)

	errLog := logger.LogRequestWithOptionsAndAllSources(
		"/v1/messages?key=masked",
		"POST",
		map[string][]string{"Authorization": {"secret-header"}},
		[]byte(`{"secret_request":"request payload"}`),
		200,
		map[string][]string{"Content-Type": {"application/json"}},
		[]byte(`{"secret_response":"response payload"}`),
		nil,
		nil,
		[]byte("upstream request secret"),
		apiRequestSource,
		[]byte("upstream response secret"),
		apiResponseSource,
		nil,
		nil,
		nil,
		false,
		"req-success",
		startedAt,
		startedAt.Add(time.Second),
	)
	if errLog != nil {
		t.Fatalf("LogRequest() error = %v", errLog)
	}

	entries, errRead := os.ReadDir(logsDir)
	if errRead != nil {
		t.Fatalf("ReadDir() error = %v", errRead)
	}
	if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), requestSummaryPrefix) {
		t.Fatalf("entries = %v, want one rolling request summary", entryNames(entries))
	}
	raw, errRead := os.ReadFile(filepath.Join(logsDir, entries[0].Name()))
	if errRead != nil {
		t.Fatalf("ReadFile() error = %v", errRead)
	}
	for _, secret := range []string{"secret-header", "secret_request", "secret_response", "upstream request secret", "upstream response secret", "spooled upstream request secret", "spooled upstream response secret"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("summary persisted full detail %q: %s", secret, raw)
		}
	}
	for _, path := range tempPaths {
		if _, errStat := os.Stat(path); !os.IsNotExist(errStat) {
			t.Fatalf("temporary full-detail part still exists at %q: %v", path, errStat)
		}
	}

	var record requestSummaryRecord
	if errUnmarshal := json.Unmarshal(raw, &record); errUnmarshal != nil {
		t.Fatalf("Unmarshal() error = %v; raw = %s", errUnmarshal, raw)
	}
	if record.RequestID != "req-success" || record.Status != 200 || record.DurationMS != 1250 {
		t.Fatalf("record = %+v", record)
	}
	if record.RequestBytes == 0 || record.ResponseBytes == 0 || record.UpstreamRequestBytes == 0 || record.UpstreamResponseBytes == 0 {
		t.Fatalf("summary byte counts missing: %+v", record)
	}
	if record.Path != "/v1/messages" {
		t.Fatalf("summary path = %q, want query-free request path", record.Path)
	}
	if strings.Contains(string(raw), "key=masked") {
		t.Fatalf("summary persisted query parameters: %s", raw)
	}
}

func TestNormalizeRequestSummaryPathRemovesQueryAndFragment(t *testing.T) {
	tests := map[string]string{
		"/v1/messages?api_key=secret#fragment":             "/v1/messages",
		"https://proxy.example/v1/responses?prompt=secret": "/v1/responses",
		"?token=secret":    "/",
		"%%%?token=secret": "%%%",
	}
	for input, want := range tests {
		if got := normalizeRequestSummaryPath(input); got != want {
			t.Errorf("normalizeRequestSummaryPath(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNewFileRequestLoggerCleansOnlyStaleTempArtifacts(t *testing.T) {
	logsDir := t.TempDir()
	now := time.Now()
	staleTime := now.Add(-staleRequestLogTempAge - time.Hour)
	recentTime := now.Add(-staleRequestLogTempAge + time.Hour)

	staleRequest := filepath.Join(logsDir, "request-body-stale.tmp")
	staleResponse := filepath.Join(logsDir, "response-body-stale.tmp")
	staleParts := filepath.Join(logsDir, "request-log-parts-stale-1")
	stalePart := filepath.Join(staleParts, "part-stale.tmp")
	recentRequest := filepath.Join(logsDir, "request-body-recent.tmp")
	recentParts := filepath.Join(logsDir, "request-log-parts-live-1")
	recentPart := filepath.Join(recentParts, "part-live.tmp")
	unrelated := filepath.Join(logsDir, "unrelated-stale.tmp")

	for _, dir := range []string{staleParts, recentParts} {
		if errMkdir := os.Mkdir(dir, 0o755); errMkdir != nil {
			t.Fatalf("Mkdir(%q) error = %v", dir, errMkdir)
		}
	}
	for _, path := range []string{staleRequest, staleResponse, stalePart, recentRequest, recentPart, unrelated} {
		if errWrite := os.WriteFile(path, []byte("spooled detail"), 0o600); errWrite != nil {
			t.Fatalf("WriteFile(%q) error = %v", path, errWrite)
		}
	}
	for _, path := range []string{staleRequest, staleResponse, stalePart, staleParts, unrelated} {
		if errTimes := os.Chtimes(path, staleTime, staleTime); errTimes != nil {
			t.Fatalf("Chtimes(%q) error = %v", path, errTimes)
		}
	}
	if errTimes := os.Chtimes(recentRequest, recentTime, recentTime); errTimes != nil {
		t.Fatalf("Chtimes(%q) error = %v", recentRequest, errTimes)
	}
	// The directory itself is old, but its live part is recent. Startup cleanup
	// must inspect the newest child before deciding that the source is stale.
	if errTimes := os.Chtimes(recentPart, recentTime, recentTime); errTimes != nil {
		t.Fatalf("Chtimes(%q) error = %v", recentPart, errTimes)
	}
	if errTimes := os.Chtimes(recentParts, staleTime, staleTime); errTimes != nil {
		t.Fatalf("Chtimes(%q) error = %v", recentParts, errTimes)
	}

	_ = NewFileRequestLogger(false, logsDir, "", 10)

	for _, path := range []string{staleRequest, staleResponse, staleParts} {
		if _, errStat := os.Stat(path); !os.IsNotExist(errStat) {
			t.Errorf("stale temp artifact %q still exists: %v", path, errStat)
		}
	}
	for _, path := range []string{recentRequest, recentParts, recentPart, unrelated} {
		if _, errStat := os.Stat(path); errStat != nil {
			t.Errorf("safe temp artifact %q was removed: %v", path, errStat)
		}
	}
}

func TestErrorRetentionCleanupWaitsForActiveErrorPublication(t *testing.T) {
	logsDir := t.TempDir()
	logger := NewFileRequestLogger(true, logsDir, "", 1)
	for index, name := range []string{"error-old.log", "error-active.log"} {
		path := filepath.Join(logsDir, name)
		if errWrite := os.WriteFile(path, []byte(name), 0o600); errWrite != nil {
			t.Fatalf("WriteFile(%q) error = %v", path, errWrite)
		}
		modified := time.Now().Add(time.Duration(index-2) * time.Hour)
		if errTimes := os.Chtimes(path, modified, modified); errTimes != nil {
			t.Fatalf("Chtimes(%q) error = %v", path, errTimes)
		}
	}

	logger.errorCleanupMu.Lock()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		done <- logger.cleanupOldErrorLogs()
	}()
	<-started
	select {
	case errCleanup := <-done:
		logger.errorCleanupMu.Unlock()
		t.Fatalf("cleanup completed during active publication: %v", errCleanup)
	case <-time.After(50 * time.Millisecond):
	}
	if entries, errRead := os.ReadDir(logsDir); errRead != nil || len(entries) != 2 {
		logger.errorCleanupMu.Unlock()
		t.Fatalf("files changed during active publication: entries=%v err=%v", entryNames(entries), errRead)
	}
	logger.errorCleanupMu.Unlock()

	select {
	case errCleanup := <-done:
		if errCleanup != nil {
			t.Fatalf("cleanup error = %v", errCleanup)
		}
	case <-time.After(time.Second):
		t.Fatal("cleanup did not resume after error publication completed")
	}
	entries, errRead := os.ReadDir(logsDir)
	if errRead != nil {
		t.Fatalf("ReadDir() error = %v", errRead)
	}
	if len(entries) != 1 || entries[0].Name() != "error-active.log" {
		t.Fatalf("retained error files = %v, want newest completed error", entryNames(entries))
	}
}

func TestFileRequestLoggerFailureRetainsFullDetail(t *testing.T) {
	logsDir := t.TempDir()
	logger := NewFileRequestLogger(true, logsDir, "", 10)
	logger.SetSuccessSummaryPolicy(true, 5, 48)
	startedAt := time.Date(2026, 7, 13, 10, 15, 0, 0, time.UTC)
	logger.now = func() time.Time { return startedAt.Add(time.Second) }

	errLog := logger.LogRequest(
		"/v1/messages",
		"POST",
		map[string][]string{"Authorization": {"secret-header"}},
		[]byte("failed request body"),
		200,
		map[string][]string{"Content-Type": {"application/json"}},
		[]byte("failed downstream body"),
		nil,
		[]byte("failed upstream request"),
		[]byte("failed upstream response"),
		nil,
		[]*interfaces.ErrorMessage{{StatusCode: 520, Error: errors.New("upstream disconnected")}},
		"req-failure",
		startedAt,
		startedAt.Add(time.Second),
	)
	if errLog != nil {
		t.Fatalf("LogRequest() error = %v", errLog)
	}

	entries, errRead := os.ReadDir(logsDir)
	if errRead != nil {
		t.Fatalf("ReadDir() error = %v", errRead)
	}
	if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "error-") {
		t.Fatalf("entries = %v, want one full error log", entryNames(entries))
	}
	raw, errRead := os.ReadFile(filepath.Join(logsDir, entries[0].Name()))
	if errRead != nil {
		t.Fatalf("ReadFile() error = %v", errRead)
	}
	for _, detail := range []string{"failed request body", "failed downstream body", "failed upstream request", "failed upstream response", "upstream disconnected"} {
		if !strings.Contains(string(raw), detail) {
			t.Fatalf("full error log missing %q: %s", detail, raw)
		}
	}
	if strings.Contains(string(raw), "secret-header") {
		t.Fatalf("sensitive header was not masked: %s", raw)
	}
}

func TestFileRequestLoggerSummaryRotationAndRetention(t *testing.T) {
	logsDir := t.TempDir()
	logger := NewFileRequestLogger(true, logsDir, "", 10)
	logger.SetSuccessSummaryPolicy(true, 5, 2)
	now := time.Date(2026, 7, 13, 0, 30, 0, 0, time.UTC)
	logger.now = func() time.Time { return now.Add(time.Second) }

	for i := 0; i < 3; i++ {
		requestID := "req-" + string(rune('a'+i))
		if errLog := logger.LogRequest("/v1/messages", "POST", nil, []byte("body"), 200, nil, []byte("response"), nil, nil, nil, nil, nil, requestID, now, time.Time{}); errLog != nil {
			t.Fatalf("LogRequest(%d) error = %v", i, errLog)
		}
		now = now.Add(5 * time.Hour)
	}

	entries, errRead := os.ReadDir(logsDir)
	if errRead != nil {
		t.Fatalf("ReadDir() error = %v", errRead)
	}
	if len(entries) != 2 {
		t.Fatalf("summary files = %v, want two retained windows", entryNames(entries))
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), requestSummaryPrefix) {
			t.Fatalf("unexpected retained file %q", entry.Name())
		}
	}
}

func TestFileRequestLoggerSummaryCleanupRunsOncePerWindowAndPolicyChange(t *testing.T) {
	logsDir := t.TempDir()
	logger := NewFileRequestLogger(true, logsDir, "", 10)
	logger.SetSuccessSummaryPolicy(true, 5, 1)

	windowStart := time.Unix(10*int64(time.Hour/time.Second), 0).UTC()
	logSuccess := func(requestID string, timestamp time.Time) {
		t.Helper()
		if errLog := logger.LogRequest("/v1/messages", "POST", nil, []byte("body"), 200, nil, []byte("response"), nil, nil, nil, nil, nil, requestID, timestamp, time.Time{}); errLog != nil {
			t.Fatalf("LogRequest(%s) error = %v", requestID, errLog)
		}
	}
	writeOldSummary := func(name string) string {
		t.Helper()
		path := filepath.Join(logsDir, name)
		if errWrite := os.WriteFile(path, []byte("{}\n"), 0o644); errWrite != nil {
			t.Fatalf("WriteFile(%s) error = %v", name, errWrite)
		}
		return path
	}

	logSuccess("first", windowStart)
	oldWithinWindow := writeOldSummary("request-summary-19691231T190000Z.log")
	logSuccess("second", windowStart.Add(time.Hour))
	if _, errStat := os.Stat(oldWithinWindow); errStat != nil {
		t.Fatalf("same-window request unexpectedly reran cleanup: %v", errStat)
	}

	// Ten-hour and five-hour rotations have the same filename at this exact
	// boundary. Cleanup must still rerun because the policy changed.
	logger.SetSuccessSummaryPolicy(true, 10, 1)
	logSuccess("after-policy-change", windowStart.Add(2*time.Hour))
	if _, errStat := os.Stat(oldWithinWindow); !os.IsNotExist(errStat) {
		t.Fatalf("policy change did not rerun cleanup; Stat error = %v", errStat)
	}

	oldBeforeRotation := writeOldSummary("request-summary-19691231T140000Z.log")
	logSuccess("same-new-window", windowStart.Add(3*time.Hour))
	if _, errStat := os.Stat(oldBeforeRotation); errStat != nil {
		t.Fatalf("same-window request unexpectedly reran cleanup after policy change: %v", errStat)
	}
	logSuccess("next-window", windowStart.Add(10*time.Hour))
	if _, errStat := os.Stat(oldBeforeRotation); !os.IsNotExist(errStat) {
		t.Fatalf("new window did not rerun cleanup; Stat error = %v", errStat)
	}
}

func TestSummaryWindowFilenameUsesFiveHourBoundaries(t *testing.T) {
	windowStart := time.Unix(0, 0).UTC().Add(500000 * time.Hour)
	withinWindow := windowStart.Add(5*time.Hour - time.Nanosecond)
	nextWindow := windowStart.Add(5 * time.Hour)

	first := summaryWindowFilename(windowStart, 5*time.Hour)
	if got := summaryWindowFilename(withinWindow, 5*time.Hour); got != first {
		t.Fatalf("within-window filename = %q, want %q", got, first)
	}
	if got := summaryWindowFilename(nextWindow, 5*time.Hour); got == first {
		t.Fatalf("next-window filename = %q, want a rotated file", got)
	}
}

func TestFileStreamingLogWriterSummaryAndErrorPolicy(t *testing.T) {
	logsDir := t.TempDir()
	logger := NewFileRequestLogger(true, logsDir, "", 10)
	logger.SetSuccessSummaryPolicy(true, 5, 48)
	startedAt := time.Date(2026, 7, 13, 10, 15, 0, 0, time.UTC)
	logger.now = func() time.Time { return startedAt }

	successWriter, errStart := logger.LogStreamingRequest("/v1/messages", "POST", nil, []byte("stream request secret"), "stream-success")
	if errStart != nil {
		t.Fatalf("LogStreamingRequest(success) error = %v", errStart)
	}
	_ = successWriter.WriteStatus(200, map[string][]string{"Content-Type": {"text/event-stream"}})
	successWriter.WriteChunkAsync([]byte("stream response secret"))
	if errClose := successWriter.Close(); errClose != nil {
		t.Fatalf("success Close() error = %v", errClose)
	}

	errorWriter, errStart := logger.LogStreamingRequest("/v1/messages", "POST", nil, []byte("failed stream request"), "stream-failure")
	if errStart != nil {
		t.Fatalf("LogStreamingRequest(error) error = %v", errStart)
	}
	_ = errorWriter.WriteStatus(200, map[string][]string{"Content-Type": {"text/event-stream"}})
	errorWriter.WriteChunkAsync([]byte("failed stream response"))
	errorRecorder, ok := errorWriter.(interface {
		WriteAPIResponseErrors([]*interfaces.ErrorMessage)
	})
	if !ok {
		t.Fatal("stream writer does not support API error retention")
	}
	errorRecorder.WriteAPIResponseErrors([]*interfaces.ErrorMessage{{StatusCode: 520, Error: errors.New("stream upstream disconnected")}})
	if errClose := errorWriter.Close(); errClose != nil {
		t.Fatalf("error Close() error = %v", errClose)
	}

	entries, errRead := os.ReadDir(logsDir)
	if errRead != nil {
		t.Fatalf("ReadDir() error = %v", errRead)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %v, want summary plus full error", entryNames(entries))
	}
	for _, entry := range entries {
		raw, errFile := os.ReadFile(filepath.Join(logsDir, entry.Name()))
		if errFile != nil {
			t.Fatalf("ReadFile(%s) error = %v", entry.Name(), errFile)
		}
		if strings.HasPrefix(entry.Name(), requestSummaryPrefix) {
			if strings.Contains(string(raw), "stream response secret") || strings.Contains(string(raw), "stream request secret") {
				t.Fatalf("stream success summary persisted body: %s", raw)
			}
			continue
		}
		if !strings.HasPrefix(entry.Name(), "error-") || !strings.Contains(string(raw), "failed stream request") || !strings.Contains(string(raw), "failed stream response") || !strings.Contains(string(raw), "stream upstream disconnected") {
			t.Fatalf("unexpected stream error log %q: %s", entry.Name(), raw)
		}
	}
}

func entryNames(entries []os.DirEntry) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}
