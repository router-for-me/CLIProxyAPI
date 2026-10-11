package claudemaster

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Which project a request was made for, so token counts can be split by project. The launcher names
// its project from the directory it was started in (projectLabel), the local forwarder sends that
// name on each tunnel it opens to the claude-master proxy (the CONNECT, which the proxy reads in
// the clear; the requests inside the tunnel are Claude's own), and the proxy keeps it with the
// tunnel, as it keeps the client's name. Only a directory's name leaves the box, never its path.

// projectHeader carries the project on the forwarder's CONNECT to the claude-master proxy.
const projectHeader = "X-Claude-Master-Project"

// projectMaxLen bounds a project name; longer ones are cut.
const projectMaxLen = 64

// sanitizeProject keeps a project name to letters, digits, '.', '_' and '-' (anything else becomes
// '-'), at most projectMaxLen long, without leading or trailing separators: safe in a header line and
// as a metric attribute.
func sanitizeProject(raw string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(raw) {
		if b.Len() >= projectMaxLen {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-._")
}

// projectLabel names the project a launch is for: the name of the git repository containing cwd
// (the main repository's, for a linked worktree), else the name of cwd itself; empty when cwd is
// unknown.
func projectLabel(cwd string) string {
	if cwd == "" {
		return ""
	}
	dir := filepath.Clean(cwd)
	for {
		git := filepath.Join(dir, ".git")
		if info, err := os.Lstat(git); err == nil {
			if info.Mode().IsRegular() {
				if main := worktreeMain(git); main != "" {
					return sanitizeProject(filepath.Base(main))
				}
			}
			return sanitizeProject(filepath.Base(dir))
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return sanitizeProject(filepath.Base(filepath.Clean(cwd)))
		}
		dir = parent
	}
}

// worktreeMain is the main repository of a linked worktree, from its .git file
// ("gitdir: <main>/.git/worktrees/<name>"); empty when the file says otherwise.
func worktreeMain(gitFile string) string {
	data, err := os.ReadFile(gitFile)
	if err != nil || len(data) > 4096 {
		return ""
	}
	path, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
	if !ok {
		return ""
	}
	path = filepath.Clean(strings.TrimSpace(path))
	worktrees := filepath.Dir(path)
	if filepath.Base(worktrees) != "worktrees" || filepath.Base(filepath.Dir(worktrees)) != ".git" {
		return ""
	}
	return filepath.Dir(filepath.Dir(worktrees))
}

type projectCtxKey struct{}

// projectFromContext is the project a request's tunnel named, or "".
func projectFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	project, _ := ctx.Value(projectCtxKey{}).(string)
	return project
}

// maxProjectLabels bounds the distinct project values one process exports: each is its own billable
// CloudWatch metric per token type, and any client could send any name.
const maxProjectLabels = 100

var projectLabels struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

// projectMetricLabel is the project's value on the metrics: "none" when the tunnel named none, the
// name while fewer than maxProjectLabels have been seen, else "other".
func projectMetricLabel(project string) string {
	if project == "" {
		return "none"
	}
	projectLabels.mu.Lock()
	defer projectLabels.mu.Unlock()
	if projectLabels.seen == nil {
		projectLabels.seen = make(map[string]struct{})
	}
	if _, ok := projectLabels.seen[project]; ok {
		return project
	}
	if len(projectLabels.seen) >= maxProjectLabels {
		return "other"
	}
	projectLabels.seen[project] = struct{}{}
	return project
}
