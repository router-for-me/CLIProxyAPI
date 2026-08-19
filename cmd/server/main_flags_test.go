package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

func TestServerFlagsAreRegisteredOnce(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}

	seen := make(map[string]token.Pos)
	duplicates := make(map[string][2]token.Pos)

	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) < 2 {
			return true
		}

		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !isFlagRegistration(selector) {
			return true
		}

		name, ok := stringLiteral(call.Args[1])
		if !ok {
			return true
		}

		if first, exists := seen[name]; exists {
			duplicates[name] = [2]token.Pos{first, call.Args[1].Pos()}
			return true
		}
		seen[name] = call.Args[1].Pos()
		return true
	})

	if len(duplicates) == 0 {
		return
	}

	for name, positions := range duplicates {
		t.Errorf(
			"flag %q registered more than once at %s and %s",
			name,
			fset.Position(positions[0]),
			fset.Position(positions[1]),
		)
	}
}

func isFlagRegistration(selector *ast.SelectorExpr) bool {
	ident, ok := selector.X.(*ast.Ident)
	if !ok || ident.Name != "flag" {
		return false
	}

	switch selector.Sel.Name {
	case "BoolVar", "DurationVar", "Float64Var", "Func", "Int64Var", "IntVar",
		"StringVar", "TextVar", "Uint64Var", "UintVar", "Var":
		return true
	default:
		return false
	}
}

func stringLiteral(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}

	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return value, true
}
