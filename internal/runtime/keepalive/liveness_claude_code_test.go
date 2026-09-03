package keepalive

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTaskState(t *testing.T, root, sessionDir, name, status string) {
	t.Helper()
	dir := filepath.Join(root, sessionDir)
	if errMkdir := os.MkdirAll(dir, 0o755); errMkdir != nil {
		t.Fatalf("MkdirAll: %v", errMkdir)
	}
	body := `{"id":"1","subject":"probe","status":"` + status + `"}`
	if errWrite := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); errWrite != nil {
		t.Fatalf("WriteFile: %v", errWrite)
	}
}

func writeTaskOutput(t *testing.T, root, project, sessionDir, name string, modTime time.Time) {
	t.Helper()
	dir := filepath.Join(root, project, sessionDir, "tasks")
	if errMkdir := os.MkdirAll(dir, 0o755); errMkdir != nil {
		t.Fatalf("MkdirAll: %v", errMkdir)
	}
	path := filepath.Join(dir, name)
	if errWrite := os.WriteFile(path, []byte("output"), 0o644); errWrite != nil {
		t.Fatalf("WriteFile: %v", errWrite)
	}
	if errChtimes := os.Chtimes(path, modTime, modTime); errChtimes != nil {
		t.Fatalf("Chtimes: %v", errChtimes)
	}
}

const testSession = "4463ede6-1111-2222-3333-444455556666"

func TestClaudeCodeTasksLivenessRunningTask(t *testing.T) {
	stateRoot := t.TempDir()
	writeTaskState(t, stateRoot, testSession, "1.json", "in_progress")

	liveness := &ClaudeCodeTasksLiveness{StateDirs: []string{stateRoot}}
	if !liveness.Live(testSession, time.Hour) {
		t.Fatalf("Live() = false, want true for an in_progress task")
	}
}

func TestClaudeCodeTasksLivenessPendingTaskCountsAsLive(t *testing.T) {
	stateRoot := t.TempDir()
	writeTaskState(t, stateRoot, testSession, "1.json", "pending")

	liveness := &ClaudeCodeTasksLiveness{StateDirs: []string{stateRoot}}
	if !liveness.Live(testSession, time.Hour) {
		t.Fatalf("Live() = false, want true for a pending task")
	}
}

func TestClaudeCodeTasksLivenessAllTerminal(t *testing.T) {
	stateRoot := t.TempDir()
	writeTaskState(t, stateRoot, testSession, "1.json", "completed")
	writeTaskState(t, stateRoot, testSession, "2.json", "failed")
	writeTaskState(t, stateRoot, testSession, "3.json", "cancelled")
	writeTaskState(t, stateRoot, testSession, "4.json", "killed")

	liveness := &ClaudeCodeTasksLiveness{StateDirs: []string{stateRoot}}
	if liveness.Live(testSession, time.Hour) {
		t.Fatalf("Live() = true, want false when every task reached a terminal status")
	}
}

func TestClaudeCodeTasksLivenessPrefixedSessionDirectory(t *testing.T) {
	stateRoot := t.TempDir()
	writeTaskState(t, stateRoot, "session-"+testSession[:8], "1.json", "in_progress")

	liveness := &ClaudeCodeTasksLiveness{StateDirs: []string{stateRoot}}
	if !liveness.Live(testSession, time.Hour) {
		t.Fatalf("Live() = false, want true for the session-<prefix> directory spelling")
	}
}

func TestClaudeCodeTasksLivenessOtherSessionIgnored(t *testing.T) {
	stateRoot := t.TempDir()
	writeTaskState(t, stateRoot, "99999999-0000-0000-0000-000000000000", "1.json", "in_progress")

	liveness := &ClaudeCodeTasksLiveness{StateDirs: []string{stateRoot}}
	if liveness.Live(testSession, time.Hour) {
		t.Fatalf("Live() = true, want false when only another session has running tasks")
	}
}

func TestClaudeCodeTasksLivenessRecentOutput(t *testing.T) {
	outputRoot := t.TempDir()
	now := time.Now()
	writeTaskOutput(t, outputRoot, "-Users-someone-project", testSession, "abc.output", now.Add(-10*time.Minute))

	liveness := &ClaudeCodeTasksLiveness{OutputDirs: []string{outputRoot}, now: func() time.Time { return now }}
	if !liveness.Live(testSession, time.Hour) {
		t.Fatalf("Live() = false, want true for an output file modified inside the window")
	}
}

func TestClaudeCodeTasksLivenessStaleOutput(t *testing.T) {
	outputRoot := t.TempDir()
	now := time.Now()
	writeTaskOutput(t, outputRoot, "-Users-someone-project", testSession, "abc.output", now.Add(-3*time.Hour))

	liveness := &ClaudeCodeTasksLiveness{OutputDirs: []string{outputRoot}, now: func() time.Time { return now }}
	if liveness.Live(testSession, time.Hour) {
		t.Fatalf("Live() = true, want false for an output file older than the window")
	}
}

func TestClaudeCodeTasksLivenessNoSignal(t *testing.T) {
	liveness := NewClaudeCodeTasksLiveness([]string{t.TempDir()}, []string{t.TempDir()})
	if liveness.Live(testSession, time.Hour) {
		t.Fatalf("Live() = true, want false with no task state at all")
	}
	if liveness.Live("", time.Hour) {
		t.Fatalf("Live() = true for an empty session id")
	}
}

func TestAlwaysLive(t *testing.T) {
	if !(AlwaysLive{}).Live("anything", time.Hour) {
		t.Fatalf("AlwaysLive must never block a probe")
	}
}

func TestExpandPath(t *testing.T) {
	home, errHome := os.UserHomeDir()
	if errHome != nil {
		t.Skipf("no home directory: %v", errHome)
	}
	if got := expandPath("~/.claude/tasks"); got != filepath.Join(home, ".claude/tasks") {
		t.Fatalf("expandPath(~) = %q", got)
	}
	uid := os.Getuid()
	want := "/private/tmp/claude-" + itoa(uid)
	if got := expandPath("/private/tmp/claude-<uid>"); got != want {
		t.Fatalf("expandPath(<uid>) = %q, want %q", got, want)
	}
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	digits := make([]byte, 0, 12)
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}

func TestNewClaudeCodeTasksLivenessDefaults(t *testing.T) {
	liveness := NewClaudeCodeTasksLiveness(nil, nil)
	if len(liveness.StateDirs) != 1 || liveness.StateDirs[0] == DefaultTaskStateDirs[0] {
		t.Fatalf("StateDirs = %v, want the expanded default", liveness.StateDirs)
	}
	if len(liveness.OutputDirs) != 1 || liveness.OutputDirs[0] == DefaultTaskOutputDirs[0] {
		t.Fatalf("OutputDirs = %v, want the expanded default", liveness.OutputDirs)
	}
}
