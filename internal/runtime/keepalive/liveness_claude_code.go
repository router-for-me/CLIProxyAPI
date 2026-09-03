package keepalive

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// DefaultTaskStateDirs is where Claude Code writes per-session task state JSON.
var DefaultTaskStateDirs = []string{"~/.claude/tasks"}

// DefaultTaskOutputDirs is where Claude Code writes per-task output files. The
// <uid> placeholder expands to the current process user id.
var DefaultTaskOutputDirs = []string{"/private/tmp/claude-<uid>"}

// terminalTaskStatuses are the task states that mean no further turn is coming
// from that task. Anything else — pending, in_progress, or a status this build
// has not seen — counts as live, because a false negative silently disables the
// feature while a false positive costs one cheap cache read.
var terminalTaskStatuses = map[string]struct{}{
	"completed": {},
	"complete":  {},
	"failed":    {},
	"cancelled": {},
	"canceled":  {},
	"killed":    {},
	"aborted":   {},
	"error":     {},
}

// AlwaysLive is the "always" liveness strategy: it never blocks a probe.
//
// It exists for clients whose agent state the proxy cannot observe. Combined with
// a session that is genuinely idle it will keep probing until max-probes, which
// is the documented cost ceiling for that choice.
type AlwaysLive struct{}

// Live always reports true.
func (AlwaysLive) Live(string, time.Duration) bool { return true }

// ClaudeCodeTasksLiveness reports session liveness from Claude Code's on-disk
// task state.
//
// Two independent signals are consulted, and either one is enough:
//
//   - a task state file under <state dir>/<session>/*.json whose status is not
//     terminal;
//   - a task output file under <output dir>/<project>/<session>/tasks/*.output
//     modified within the TTL window.
//
// Claude Code names a session's state directory either by the full session UUID
// or by a "session-" prefix plus the first eight characters of the UUID, so both
// spellings are searched.
type ClaudeCodeTasksLiveness struct {
	// StateDirs are the roots holding per-session task state directories.
	StateDirs []string
	// OutputDirs are the roots holding <project>/<session>/tasks/*.output files.
	OutputDirs []string

	now func() time.Time
}

// NewClaudeCodeTasksLiveness builds a liveness checker, falling back to the
// built-in paths when the operator configured none.
func NewClaudeCodeTasksLiveness(stateDirs, outputDirs []string) *ClaudeCodeTasksLiveness {
	if len(stateDirs) == 0 {
		stateDirs = DefaultTaskStateDirs
	}
	if len(outputDirs) == 0 {
		outputDirs = DefaultTaskOutputDirs
	}
	return &ClaudeCodeTasksLiveness{
		StateDirs:  expandPaths(stateDirs),
		OutputDirs: expandPaths(outputDirs),
		now:        time.Now,
	}
}

// Live reports whether the session still has a task in flight.
func (l *ClaudeCodeTasksLiveness) Live(sessionID string, window time.Duration) bool {
	if l == nil || strings.TrimSpace(sessionID) == "" {
		return false
	}
	if l.hasRunningTaskState(sessionID) {
		return true
	}
	return l.hasRecentTaskOutput(sessionID, window)
}

func (l *ClaudeCodeTasksLiveness) hasRunningTaskState(sessionID string) bool {
	for _, root := range l.StateDirs {
		for _, dirName := range sessionDirNames(sessionID) {
			entries, errGlob := filepath.Glob(filepath.Join(root, dirName, "*.json"))
			if errGlob != nil {
				continue
			}
			for _, entry := range entries {
				data, errRead := os.ReadFile(entry)
				if errRead != nil {
					continue
				}
				status := strings.ToLower(strings.TrimSpace(gjson.GetBytes(data, "status").String()))
				if status == "" {
					continue
				}
				if _, terminal := terminalTaskStatuses[status]; !terminal {
					log.Debugf("cache-keepalive: liveness hit | session=%s file=%s status=%s", truncateSession(sessionID), entry, status)
					return true
				}
			}
		}
	}
	return false
}

func (l *ClaudeCodeTasksLiveness) hasRecentTaskOutput(sessionID string, window time.Duration) bool {
	if window <= 0 {
		window = time.Hour
	}
	now := time.Now
	if l.now != nil {
		now = l.now
	}
	cutoff := now().Add(-window)
	for _, root := range l.OutputDirs {
		for _, dirName := range sessionDirNames(sessionID) {
			entries, errGlob := filepath.Glob(filepath.Join(root, "*", dirName, "tasks", "*.output"))
			if errGlob != nil {
				continue
			}
			for _, entry := range entries {
				info, errStat := os.Stat(entry)
				if errStat != nil {
					continue
				}
				if info.ModTime().After(cutoff) {
					log.Debugf("cache-keepalive: liveness hit | session=%s file=%s modified=%s", truncateSession(sessionID), entry, info.ModTime())
					return true
				}
			}
		}
	}
	return false
}

// sessionDirNames returns the directory spellings Claude Code uses for a session.
func sessionDirNames(sessionID string) []string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	names := []string{sessionID}
	if len(sessionID) > 8 {
		names = append(names, "session-"+sessionID[:8])
	}
	return names
}

func expandPaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if expanded := expandPath(path); expanded != "" {
			out = append(out, expanded)
		}
	}
	return out
}

// expandPath resolves a leading ~ and the <uid> placeholder.
func expandPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	path = strings.ReplaceAll(path, "<uid>", strconv.Itoa(os.Getuid()))
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, errHome := os.UserHomeDir()
		if errHome != nil {
			return path
		}
		if path == "~" {
			return home
		}
		return filepath.Join(home, path[2:])
	}
	return path
}
