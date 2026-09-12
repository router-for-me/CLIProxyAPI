//go:build darwin || linux

package keepalive

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// writeSymlinkedTaskOutput mirrors the real layout, where a subagent's output
// file is a symlink into the project transcript directory and only the target's
// timestamp advances as the agent writes.
func writeSymlinkedTaskOutput(t *testing.T, root, project, sessionDir, name string, targetModTime, linkModTime time.Time) {
	t.Helper()
	targetDir := filepath.Join(root, "transcripts")
	if errMkdir := os.MkdirAll(targetDir, 0o755); errMkdir != nil {
		t.Fatalf("MkdirAll: %v", errMkdir)
	}
	target := filepath.Join(targetDir, "agent-"+name+".jsonl")
	if errWrite := os.WriteFile(target, []byte("{}\n"), 0o644); errWrite != nil {
		t.Fatalf("WriteFile: %v", errWrite)
	}
	if errChtimes := os.Chtimes(target, targetModTime, targetModTime); errChtimes != nil {
		t.Fatalf("Chtimes: %v", errChtimes)
	}

	linkDir := filepath.Join(root, project, sessionDir, "tasks")
	if errMkdir := os.MkdirAll(linkDir, 0o755); errMkdir != nil {
		t.Fatalf("MkdirAll: %v", errMkdir)
	}
	link := filepath.Join(linkDir, name+".output")
	if errLink := os.Symlink(target, link); errLink != nil {
		t.Fatalf("Symlink: %v", errLink)
	}
	// The link's own timestamp must be set without following it. os.Chtimes
	// follows symlinks and would rewrite the target, defeating the fixture.
	stamp := unix.NsecToTimespec(linkModTime.UnixNano())
	if errUtimes := unix.UtimesNanoAt(unix.AT_FDCWD, link, []unix.Timespec{stamp, stamp}, unix.AT_SYMLINK_NOFOLLOW); errUtimes != nil {
		t.Skipf("cannot set symlink timestamps on this platform: %v", errUtimes)
	}
}

func TestClaudeCodeTasksLivenessFollowsSymlinkToTheAgentTranscript(t *testing.T) {
	outputRoot := t.TempDir()
	now := time.Now()
	// The link is stale, the transcript it points at is fresh. Following the link
	// is the difference between "agent running" and "agent gone".
	writeSymlinkedTaskOutput(t, outputRoot, "-Users-me-src", testSession, "a320d50c23cf12b0a",
		now.Add(-30*time.Second), now.Add(-40*time.Minute))

	liveness := &ClaudeCodeTasksLiveness{OutputDirs: []string{outputRoot}, now: func() time.Time { return now }}
	if !liveness.Live(testSession, 10*time.Minute) {
		t.Fatalf("Live() = false, want true: the symlink target was written 30s ago")
	}
}

func TestClaudeCodeTasksLivenessStaleSymlinkTargetIsNotLive(t *testing.T) {
	outputRoot := t.TempDir()
	now := time.Now()
	writeSymlinkedTaskOutput(t, outputRoot, "-Users-me-src", testSession, "a320d50c23cf12b0a",
		now.Add(-30*time.Minute), now.Add(-30*time.Minute))

	liveness := &ClaudeCodeTasksLiveness{OutputDirs: []string{outputRoot}, now: func() time.Time { return now }}
	if liveness.Live(testSession, 10*time.Minute) {
		t.Fatalf("Live() = true, want false: the agent has written nothing for 30 minutes")
	}
}
