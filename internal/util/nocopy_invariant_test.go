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

// skippedWalkDirs are directories the source walker never descends into. The
// build and tool dirs are excluded because they hold cloned third-party or
// generated Go sources (build output, caches, temporary GOPATH/module trees)
// that must not be treated as product code governed by these invariants.
var skippedWalkDirs = map[string]struct{}{
	".git": {}, "vendor": {}, "node_modules": {}, "testdata": {},
	".tmp_build": {}, ".go-cache": {}, ".go-tmp": {}, ".gocache": {},
}

// forEachSourceFile visits every non-test Go file in the repository.
func forEachSourceFile(t *testing.T, root string, visit func(rel string, data []byte)) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if _, skip := skippedWalkDirs[d.Name()]; skip {
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

// TestForEachSourceFileSkipsBuildDirs pins the walker's directory exclusions
// and its non-exclusion of arbitrary hidden dirs. Every listed build/tool dir
// must be skipped even when it holds a poisoned *.go file, while a hidden
// source dir with no special meaning must still be scanned.
func TestForEachSourceFileSkipsBuildDirs(t *testing.T) {
	root := t.TempDir()
	poison := "-- FORBIDDEN: ReplaceInPlace --\n"
	// Every dir the walker must skip, seeded with a poisoned Go file.
	for name := range skippedWalkDirs {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte(poison), 0o644); err != nil {
			t.Fatalf("write skip fixture: %v", err)
		}
	}
	// A hidden dir with no special meaning must still be scanned.
	hidden := filepath.Join(root, ".hidden-source")
	if err := os.MkdirAll(hidden, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", hidden, err)
	}
	if err := os.WriteFile(filepath.Join(hidden, "x.go"), []byte("// .hidden-source scanned\n"), 0o644); err != nil {
		t.Fatalf("write hidden fixture: %v", err)
	}

	var skipped, kept []string
	forEachSourceFile(t, root, func(rel string, data []byte) {
		if strings.Contains(string(data), "FORBIDDEN") {
			skipped = append(skipped, rel)
			return
		}
		if strings.Contains(string(data), ".hidden-source scanned") {
			kept = append(kept, rel)
		}
	})
	if len(skipped) != 0 {
		t.Fatalf("blocked dirs were walked: %v", skipped)
	}
	if len(kept) != 1 {
		t.Fatalf("expected hidden-source dir to be scanned once, got %v", kept)
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
	regexp.MustCompile(`^\s*[a-zA-Z_][A-Za-z0-9_.]*\[[a-zA-Z0-9_]+\] = 0$`),
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
	"internal/runtime/executor/claude_executor_request.go":  {2, "shifts []string headers to insert a part; no byte of any payload is rewritten"},
	"internal/runtime/executor/helps/claude_mcp_alias.go":   {1, "copies an HMAC sum into a local fixed-size digest array"},
	"internal/client/codex/live/tcp_proxy.go":               {1, "copies header and payload into a freshly allocated frame"},
	"internal/runtime/executor/devin_protobuf.go":           {1, "copies a protobuf payload into a freshly allocated Connect frame"},
	"internal/auth/cursor/proto/connect.go":                 {1, "copies a protobuf payload into a freshly allocated Connect frame"},
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
		for _, line := range strings.Split(string(data), "\n") {
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
