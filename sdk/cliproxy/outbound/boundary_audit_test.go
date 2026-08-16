package outbound

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestProviderCodeHasNoUnaccountedDirectHTTPClientDo(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "../../.."))
	allowed := map[string]bool{
		"internal/api/handlers/management/config_basic.go":              true,
		"internal/runtime/executor/helps/antigravity_grounding_urls.go": true,
		"sdk/cliproxy/outbound/outbound.go":                             true,
	}
	roots := []string{
		"internal/api/handlers/management",
		"internal/auth",
		"internal/client/codex/live",
		"internal/misc",
		"internal/runtime/executor",
		"sdk/auth",
		"sdk/cliproxy",
	}
	fset := token.NewFileSet()
	for _, root := range roots {
		errWalk := filepath.WalkDir(filepath.Join(repoRoot, root), func(path string, entry fs.DirEntry, errWalk error) error {
			if errWalk != nil {
				return errWalk
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, errRel := filepath.Rel(repoRoot, path)
			if errRel != nil {
				return errRel
			}
			rel = filepath.ToSlash(rel)
			file, errParse := parser.ParseFile(fset, path, nil, 0)
			if errParse != nil {
				return errParse
			}
			ast.Inspect(file, func(node ast.Node) bool {
				call, okCall := node.(*ast.CallExpr)
				if !okCall {
					return true
				}
				selector, okSelector := call.Fun.(*ast.SelectorExpr)
				if !okSelector || selector.Sel.Name != "Do" {
					return true
				}
				receiver := directHTTPClientReceiverName(selector.X)
				if !strings.Contains(strings.ToLower(receiver), "client") || allowed[rel] {
					return true
				}
				position := fset.Position(call.Pos())
				t.Errorf("direct HTTP client.Do bypasses outbound finalization at %s:%d", rel, position.Line)
				return true
			})
			return nil
		})
		if errWalk != nil {
			t.Fatalf("audit %s: %v", root, errWalk)
		}
	}
}

func directHTTPClientReceiverName(expr ast.Expr) string {
	switch receiver := expr.(type) {
	case *ast.Ident:
		return receiver.Name
	case *ast.SelectorExpr:
		return receiver.Sel.Name
	default:
		return ""
	}
}
