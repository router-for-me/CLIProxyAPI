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

// DefaultTaskOutputDirs is where Claude Code records one file per background
// agent or shell. The <uid> placeholder expands to the current process user id.
//
// The layout is <root>/<project-slug>/<session-uuid>/tasks/*.output, where the
// project slug is the working directory with every "/" replaced by "-", for
// example /Users/me/src becomes -Users-me-src. The slug is matched with a
// wildcard rather than derived, because the proxy does not know the client's
// working directory.
var DefaultTaskOutputDirs = []string{"/private/tmp/claude-<uid>"}

// DefaultTaskStateDirs is where Claude Code writes per-session TodoWrite state.
//
// This is the secondary signal only. These files are the user-facing todo list,
// not subagent state: a session with a running subagent frequently has no todo
// file at all, and a stale todo left at in_progress would keep a finished
// session looking alive. The task output files above are the authority.
var DefaultTaskStateDirs = []string{"~/.claude/tasks"}

// terminalTaskStatuses are the todo states that mean no further turn is coming
// from that item. Anything else counts as live, because a false negative
// silently disables the feature while a false positive costs one cheap read.
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
// It exists for clients whose agent state the proxy cannot observe. Combined
// with a session that is genuinely idle it will keep probing until max-probes,
// which is the documented cost ceiling for that choice.
type AlwaysLive struct{}

// Live always reports true.
func (AlwaysLive) Live(string, time.Duration) bool { return true }

// ClaudeCodeTasksLiveness reports session liveness from Claude Code's on-disk
// agent state.
//
// The primary and authoritative signal is the per-agent task output file:
//
//	<output dir>/<project-slug>/<session-uuid>/tasks/*.output
//
// Every background agent and shell of a session has one. A running agent keeps
// writing to it, so its modification time advances. Some of these files are
// symlinks into ~/.claude/projects/<slug>/<session>/subagents/agent-*.jsonl,
// where the real writes land; the symlink's own timestamp lags the target's, so
// liveness follows the link and reads the target.
//
// A file counts as live only while it was written inside the idle window. The
// window is not the cache TTL: an agent that has produced nothing for an hour is
// finished, not busy. Its flip side is that a genuinely silent agent, one doing
// long work that emits nothing, looks idle once the window passes, so the window
// should exceed the longest silence worth paying a probe for.
//
// The TodoWrite state files are consulted only as a secondary fallback.
type ClaudeCodeTasksLiveness struct {
	// OutputDirs are the roots holding <project>/<session>/tasks/*.output files.
	// This is the primary signal.
	OutputDirs []string
	// StateDirs are the roots holding per-session TodoWrite state directories.
	// Secondary signal only; see DefaultTaskStateDirs.
	StateDirs []string

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

// Live reports whether the session still has an agent running. The window is the
// configured agent idle window.
func (l *ClaudeCodeTasksLiveness) Live(sessionID string, window time.Duration) bool {
	if l == nil || strings.TrimSpace(sessionID) == "" {
		return false
	}
	// Primary: a task output file written inside the idle window.
	if l.hasRecentTaskOutput(sessionID, window) {
		return true
	}
	// Secondary: a TodoWrite item that never reached a terminal status.
	return l.hasRunningTaskState(sessionID)
}

func (l *ClaudeCodeTasksLiveness) hasRecentTaskOutput(sessionID string, window time.Duration) bool {
	if window <= 0 {
		window = 10 * time.Minute
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
				// os.Stat follows symlinks, which is required: these entries are
				// often links to the agent transcript, and only the target's
				// timestamp advances as the agent writes.
				info, errStat := os.Stat(entry)
				if errStat != nil {
					continue
				}
				if info.ModTime().After(cutoff) {
					log.Debugf("cache-keepalive: liveness hit | session=%s source=task-output file=%s modified=%s",
						truncateSession(sessionID), entry, info.ModTime().Format(time.RFC3339))
					return true
				}
			}
		}
	}
	return false
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
					log.Debugf("cache-keepalive: liveness hit | session=%s source=todo-state file=%s status=%s",
						truncateSession(sessionID), entry, status)
					return true
				}
			}
		}
	}
	return false
}

// sessionDirNames returns the directory spellings Claude Code uses for a session.
// Task output directories use the full session UUID; the secondary TodoWrite
// directories are also seen as "session-" plus the first eight characters.
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
