package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestGenerateFilenameRejectsPathSeparators covers the request-log filename
// traversal (report nine, F-23): the file name is built from the client-supplied
// request path, and on Windows a backslash in that path used to survive
// sanitisation so filepath.Join could climb out of the log directory.
func TestGenerateFilenameRejectsPathSeparators(t *testing.T) {
	logger := &FileRequestLogger{}
	cases := []string{
		`/v1beta/models/x\..\..\..\..\probe:generateContent`,
		`/v1/realtime/calls/x\..\..\..\probe/hangup`,
		`/v1/models/../../etc/passwd`,
		`/v1/models/..\..\..\..\windows\win.ini`,
		`/v1/\..\..\..\root`,
		`/v1/models/a//b//c`,
		`/.`,
		`/..`,
		`/...`,
	}
	for _, raw := range cases {
		for _, got := range []string{
			logger.generateFilename(raw, "deadbeef"),
			logger.generateErrorFilename(raw, "deadbeef"),
		} {
			if strings.ContainsAny(got, `/\`) {
				t.Fatalf("generate filename(%q) = %q, want no path separators", raw, got)
			}
			if strings.Contains(got, "..") {
				t.Fatalf("generate filename(%q) = %q, want no parent-directory sequence", raw, got)
			}
			if filepath.Base(got) != got {
				t.Fatalf("generate filename(%q) = %q, want a single path component", raw, got)
			}
			if !strings.HasSuffix(got, ".log") {
				t.Fatalf("generate filename(%q) = %q, want a .log suffix", raw, got)
			}
		}
	}
}

// TestGenerateFilenameKeepsOrdinaryPaths guards against over-sanitising: a normal
// request path must still produce the historical "<path>-<timestamp>-<id>.log" shape.
func TestGenerateFilenameKeepsOrdinaryPaths(t *testing.T) {
	logger := &FileRequestLogger{}
	got := logger.generateFilename("/v1/chat/completions", "deadbeef")
	if !strings.HasPrefix(got, "v1-chat-completions-") {
		t.Fatalf("generateFilename() = %q, want the v1-chat-completions- prefix", got)
	}
	if !strings.HasSuffix(got, "-deadbeef.log") {
		t.Fatalf("generateFilename() = %q, want the -deadbeef.log suffix", got)
	}

	errGot := logger.generateErrorFilename("/v1/chat/completions", "deadbeef")
	if !strings.HasPrefix(errGot, "error-v1-chat-completions-") {
		t.Fatalf("generateErrorFilename() = %q, want the error-v1-chat-completions- prefix", errGot)
	}
	if !strings.HasSuffix(errGot, "-deadbeef.log") {
		t.Fatalf("generateErrorFilename() = %q, want the -deadbeef.log suffix", errGot)
	}
}

func TestCreateUniqueLogFileRejectsTraversalNames(t *testing.T) {
	dir := t.TempDir()
	for _, bad := range []string{
		"",
		".",
		"..",
		"../outside.log",
		`..\outside.log`,
		"sub/inside.log",
		`sub\inside.log`,
	} {
		if f, path, err := createUniqueLogFile(dir, bad); err == nil {
			_ = f.Close()
			t.Fatalf("createUniqueLogFile(%q) succeeded with path %q, want error", bad, path)
		}
	}
}

func TestLogRequestWithTraversalPathStaysInsideLogsDir(t *testing.T) {
	rootDir := t.TempDir()
	logsDir := filepath.Join(rootDir, "a", "b", "c", "logs")

	for _, tc := range []struct {
		name    string
		enabled bool
		force   bool
	}{
		{name: "forced_error_log_when_disabled", enabled: false, force: true},
		{name: "normal_request_log_when_enabled", enabled: true, force: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logger := NewFileRequestLogger(tc.enabled, logsDir, rootDir, 10)
			now := time.Now()
			errLog := logger.LogRequestWithOptions(
				`/v1beta/models/x\..\..\..\..\traversal-probe:generateContent`,
				"POST",
				map[string][]string{"Content-Type": {"application/json"}},
				[]byte(`{"contents":[]}`),
				400,
				map[string][]string{"Content-Type": {"application/json"}},
				[]byte(`{"error":"unknown model"}`),
				nil,
				nil,
				nil,
				nil,
				nil,
				tc.force,
				"deadbeef",
				now,
				now,
			)
			if errLog != nil {
				t.Fatalf("LogRequestWithOptions error: %v", errLog)
			}

			errWalk := filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if info.IsDir() {
					return nil
				}
				if filepath.Dir(path) != logsDir {
					t.Fatalf("file written outside logsDir: %s (logsDir=%s)", path, logsDir)
				}
				return nil
			})
			if errWalk != nil {
				t.Fatalf("filepath.Walk error: %v", errWalk)
			}
		})
	}
}
