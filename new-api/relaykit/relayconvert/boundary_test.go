package relayconvert

// boundary_test.go enforces the relaykit extraction dependency boundary
// (plans/relaykit-extraction-plan.md): packages that will move into the
// relaykit module must not grow imports of host-only packages. Entries in
// allowedViolations are the known couplings scheduled for removal in
// Phase 1/2 — shrink this list, never grow it.

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const modulePrefix = "github.com/QuantumNous/new-api/"

// Packages (relative to the relaykit module root) covered by the boundary.
var kitDirs = []string{
	"relayconvert",
	"dto",
	"types",
	"reasonmap",
}

// Import prefixes forbidden inside the kit module: the entire host module
// (everything outside relaykit/) and gin.
var forbiddenPrefixes = []string{
	modulePrefix,
	"github.com/gin-gonic/gin",
}

// hostModuleExceptions are host-prefix imports that are actually the kit's
// own packages (the kit module path nests under the host path).
const kitModulePrefix = modulePrefix + "relaykit/"

// Known pre-existing couplings, removed phase by phase. Key: "dir|import".
// All initial violations have been cleared; keep the map so future
// exemptions (if ever needed) are explicit and reviewed.
var allowedViolations = map[string]bool{}

func TestRelaykitBoundary(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()

	for _, dir := range kitDirs {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, imp := range file.Imports {
				importPath := strings.Trim(imp.Path.Value, `"`)
				for _, prefix := range forbiddenPrefixes {
					if importPath != prefix && !strings.HasPrefix(importPath, prefix) {
						continue
					}
					if strings.HasPrefix(importPath, kitModulePrefix) {
						continue
					}
					if allowedViolations[dir+"|"+importPath] {
						continue
					}
					rel, _ := filepath.Rel(root, path)
					t.Errorf("%s imports %q — forbidden inside future relaykit package %s (see plans/relaykit-extraction-plan.md)", rel, importPath, dir)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", dir, err)
		}
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above test directory")
		}
		dir = parent
	}
}
