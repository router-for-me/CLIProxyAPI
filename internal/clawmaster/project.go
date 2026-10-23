package clawmaster

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
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
// temporary directory counts as one project instead of using up maxProjectLabels. The name is cut
// where the first generated part starts; one with nothing before it is "random". Generated means
// exactly these forms (anything else is a name, so a real name is never merged into another):
//   - a UUID, whole or cut short by projectMaxLen (OpenClaw's openclaw-isolated-completion-<uuid>);
//   - a segment of eight or more characters that is hex with at least one digit (a hash, an id);
//   - a segment of eight or more digits, or a compact ISO time (20261023T153000Z);
//   - the segment after "tmp" (mktemp's tmp.XXXXXXXXXX).
func stableProject(project string) string {
	cut := len(project)
	if loc := uuidInName.FindStringIndex(project); loc != nil {
		cut = loc[0]
	}
	start, prev := 0, ""
	for i := 0; i <= cut; i++ {
		if i < cut && !isProjectSeparator(project[i]) {
			continue
		}
		seg := project[start:i]
		if generatedSegment(seg) || (strings.EqualFold(prev, "tmp") && seg != "") {
			cut = start
			break
		}
		prev, start = seg, i+1
	}
	if kept := strings.TrimRight(project[:cut], "-._"); kept != "" {
		return kept
	}
	if cut == len(project) {
		return project
	}
	return "random"
}

// uuidInName is a UUID starting a segment, its last group possibly cut short by projectMaxLen.
var uuidInName = regexp.MustCompile(`(?:^|[-._])([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{1,12})(?:$|[-._])`)

// compactISOTime is a basic-format ISO 8601 date and time (20261023T153000Z).
var compactISOTime = regexp.MustCompile(`^[0-9]{8}T[0-9]{4,6}Z?$`)

func isProjectSeparator(c byte) bool { return c == '-' || c == '_' || c == '.' }

// generatedSegment reports whether a part of a name between separators is a hash, an id or a time:
// eight or more characters that are all digits, or hex with a digit, or a compact ISO time.
func generatedSegment(seg string) bool {
	if len(seg) < 8 {
		return false
	}
	if compactISOTime.MatchString(seg) {
		return true
	}
	digits := 0
	for i := 0; i < len(seg); i++ {
		c := seg[i]
		switch {
		case c >= '0' && c <= '9':
			digits++
		case c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return digits > 0
}
