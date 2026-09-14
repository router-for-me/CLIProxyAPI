package util

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// inPlaceSJSONTokens are the sjson knobs that let a write reuse the caller's
// backing array instead of allocating a new one.
var inPlaceSJSONTokens = []string{"ReplaceInPlace", "Optimistic"}

// inPlaceSJSONAllowlist holds files that are allowed to opt into in-place
// sjson writes. A file may only be added here once it is proven that no
// no-copy GJSON result (GetGJSONBytesNoCopy / ParseGJSONBytesNoCopy) derived
// from the same buffer can still be alive at that point.
var inPlaceSJSONAllowlist = map[string]struct{}{}

// forEachSourceFile visits every non-test Go file in the repository.
func forEachSourceFile(t *testing.T, root string, visit func(rel string, data []byte)) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			switch d.Name() {
			case "vendor", "node_modules", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, errRel := filepath.Rel(root, path)
		if errRel != nil {
			return errRel
		}
		data, errRead := os.ReadFile(path)
		if errRead != nil {
			return errRead
		}
		visit(filepath.ToSlash(rel), data)
		return nil
	})
	if err != nil {
		t.Fatalf("walk repository: %v", err)
	}
}

// TestNoInPlaceSJSONWrites protects the invariant that request payload buffers
// stay immutable for their whole lifetime.
//
// GetGJSONBytesNoCopy and ParseGJSONBytesNoCopy hand out gjson.Result values
// whose Raw and Str alias the caller's []byte. Go strings must never change,
// so any in-place mutation of that buffer turns already-derived results into
// silently wrong data: re-parsing sees the new bytes, and strings that were
// used as map keys keep a hash computed from the old ones. The race detector
// cannot see this, and normal tests rarely trigger it, so the invariant is
// enforced statically here instead.
func TestNoInPlaceSJSONWrites(t *testing.T) {
	root := repoRoot(t)
	var offenders []string
	forEachSourceFile(t, root, func(rel string, data []byte) {
		if _, allowed := inPlaceSJSONAllowlist[rel]; allowed {
			return
		}
		for _, token := range inPlaceSJSONTokens {
			if strings.Contains(string(data), token) {
				offenders = append(offenders, rel+" uses "+token)
			}
		}
	})
	if len(offenders) > 0 {
		t.Fatalf("in-place sjson writes would corrupt no-copy GJSON results that alias the same buffer:\n  %s\n"+
			"Either keep the default (allocating) sjson call, or prove no no-copy result derived from that buffer is still alive and add the file to inPlaceSJSONAllowlist.",
			strings.Join(offenders, "\n  "))
	}
}

// inPlaceByteWritePatterns match the realistic ways Go code overwrites bytes
// of an existing buffer: copying into a slice expression, or zeroing elements
// in a loop. They do not catch every possible form, so they are a tripwire for
// new code rather than a proof of absence.
var inPlaceByteWritePatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bcopy\([a-zA-Z_][A-Za-z0-9_.]*\[`),
	regexp.MustCompile(`^\s*[a-zA-Z_][A-Za-z0-9_.]*\[[a-zA-Z0-9_]+\] = 0\r?$`),
}

// reviewedInPlaceByteWrites records the reviewed in-place byte writes per file.
// The count is part of the contract: a new write inside an already reviewed file
// must be reviewed too, so the count must be updated deliberately. Each reason
// states why the write cannot corrupt a no-copy GJSON result, either because the
// buffer is private to the writer or because every reader copies out first.
type reviewedInPlaceByteWrite struct {
	count  int
	reason string
}

var reviewedInPlaceByteWrites = map[string]reviewedInPlaceByteWrite{
	"internal/runtime/executor/claude_signing.go":           {2, "writes CCH digits into bytes.Clone(body); the caller's body is never touched"},
	"internal/runtime/executor/claude_executor_cloaking.go": {1, "shifts []string headers to prepend a block; no byte of any payload is rewritten"},
	"internal/runtime/executor/claude_executor_request.go":  {3, "shifts []string headers to insert a part; no byte of any payload is rewritten"},
	"internal/runtime/executor/helps/claude_mcp_alias.go":   {1, "copies an HMAC sum into a local fixed-size digest array"},
	"internal/client/codex/live/tcp_proxy.go":               {1, "copies header and payload into a freshly allocated frame"},
	"internal/home/client.go":                               {1, "zeroes a secret buffer after json.Unmarshal has copied every value out"},
	"internal/pluginstore/auth.go":                          {1, "zeroes a locally built credential buffer after base64 encoding copied it out"},
}

// TestInPlaceByteWritesAreReviewed keeps the set of in-place byte writes small
// and justified. Any change to the set, including a new write in an already
// reviewed file, fails until the author proves that no no-copy GJSON result
// derived from that buffer can still be alive and records it above.
func TestInPlaceByteWritesAreReviewed(t *testing.T) {
	root := repoRoot(t)
	found := make(map[string][]string)
	forEachSourceFile(t, root, func(rel string, data []byte) {
		normalized := strings.ReplaceAll(string(data), "\r\n", "\n")
		for _, line := range strings.Split(normalized, "\n") {
			for _, pattern := range inPlaceByteWritePatterns {
				if pattern.MatchString(line) {
					found[rel] = append(found[rel], strings.TrimSpace(line))
				}
			}
		}
	})
	for rel, lines := range found {
		reviewed, ok := reviewedInPlaceByteWrites[rel]
		if !ok {
			t.Errorf("unreviewed in-place byte write in %s:\n  %s\nProve that no no-copy GJSON result derived from that buffer is still alive, then record it in reviewedInPlaceByteWrites.",
				rel, strings.Join(lines, "\n  "))
			continue
		}
		if len(lines) != reviewed.count {
			t.Errorf("%s has %d in-place byte write(s), reviewed %d (%s):\n  %s",
				rel, len(lines), reviewed.count, reviewed.reason, strings.Join(lines, "\n  "))
		}
	}
	for rel := range reviewedInPlaceByteWrites {
		if _, ok := found[rel]; !ok {
			t.Errorf("stale entry in reviewedInPlaceByteWrites: %s no longer contains an in-place byte write", rel)
		}
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, errStat := os.Stat(filepath.Join(dir, "go.mod")); errStat == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above working directory")
		}
		dir = parent
	}
}

func TestInPlaceByteWritePatterns_CRLF(t *testing.T) {
	crlfLine := "\traw[index] = 0\r"
	matched := false
	for _, pattern := range inPlaceByteWritePatterns {
		if pattern.MatchString(crlfLine) {
			matched = true
			break
		}
	}
	if !matched {
		t.Fatalf("inPlaceByteWritePatterns failed to match CRLF line %q", crlfLine)
	}
}

func TestForEachSourceFile_SkipsDotDirs(t *testing.T) {
	tempDir := t.TempDir()
	dotDir := filepath.Join(tempDir, ".gomodcache")
	if err := os.MkdirAll(dotDir, 0755); err != nil {
		t.Fatal(err)
	}
	sampleFile := filepath.Join(dotDir, "sample.go")
	if err := os.WriteFile(sampleFile, []byte("package sample\n"), 0644); err != nil {
		t.Fatal(err)
	}
	var visited []string
	forEachSourceFile(t, tempDir, func(rel string, data []byte) {
		visited = append(visited, rel)
	})
	if len(visited) > 0 {
		t.Fatalf("forEachSourceFile should have skipped dot directories, but visited: %v", visited)
	}
}
