package clawmaster

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Which project a request was made for, so token counts can be split by project. The launcher names
// its project from the directory it was started in (projectLabel), the local forwarder sends that
// name on each tunnel it opens to the claw-master proxy (the CONNECT, which the proxy reads in
// the clear; the requests inside the tunnel are Claude's own), and the proxy keeps it with the
// tunnel, as it keeps the client's name. Only a directory's name leaves the box, never its path.

// projectHeader carries the project on the forwarder's CONNECT to the claw-master proxy.
const projectHeader = "X-Claw-Master-Project"

// connectProject is the project a CONNECT named, under its header's current name or its name
// before the rename.
func connectProject(header http.Header) string {
	if project := header.Get(projectHeader); project != "" {
		return project
	}
	return header.Get(legacyProjectHeader)
}

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
// name without its random part (stableProject) while fewer than maxProjectLabels have been seen, else
// "other".
func projectMetricLabel(project string) string {
	if project == "" {
		return "none"
	}
	project = stableProject(project)
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

// stableProject is a project name without its random part, so that a tool running each job in a new
// temporary directory (OpenClaw's openclaw-isolated-completion-<uuid>, mktemp's tmp.XXXXXXXXXX, a
// timestamped build directory) counts as one project instead of using up maxProjectLabels. The name
// is cut at its first random-looking segment (randomSegment); one with nothing before it is "random".
func stableProject(project string) string {
	start := 0
	for i := 0; i <= len(project); i++ {
		if i < len(project) && !isProjectSeparator(project[i]) {
			continue
		}
		if randomSegment(project[start:i]) {
			if kept := strings.TrimRight(project[:start], "-._"); kept != "" {
				return kept
			}
			return "random"
		}
		start = i + 1
	}
	return project
}

func isProjectSeparator(c byte) bool { return c == '-' || c == '_' || c == '.' }

// randomSegment reports whether a part of a name between separators looks generated rather than
// chosen: eight or more characters that are all digits (a timestamp, a counter), or hex with both a
// digit and a letter (a UUID's first part, a hash), or letters and digits mixed in both cases or
// alternating often (mktemp, a base-62 id). Words with a digit or two (web3, x86, s3, go1) are names.
func randomSegment(seg string) bool {
	if len(seg) < 8 {
		return false
	}
	var digits, lower, upper, hexLetters, changes int
	prevDigit := false
	for i := 0; i < len(seg); i++ {
		c := seg[i]
		isDigit := c >= '0' && c <= '9'
		switch {
		case isDigit:
			digits++
		case c >= 'a' && c <= 'z':
			lower++
			if c <= 'f' {
				hexLetters++
			}
		case c >= 'A' && c <= 'Z':
			upper++
		default:
			return false
		}
		if i > 0 && isDigit != prevDigit {
			changes++
		}
		prevDigit = isDigit
	}
	letters := lower + upper
	switch {
	case digits == len(seg):
		return true
	case digits > 0 && upper == 0 && hexLetters == lower && lower > 0:
		return true
	case digits > 0 && lower > 0 && upper > 0:
		return true
	case digits >= 3 && letters > 0 && changes >= 4:
		return true
	}
	return false
}
