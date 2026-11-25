package chat

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Inspect production Go sources throughout the repository, including future packages.
func TestProductionDoesNotConstructSystemRoles(t *testing.T) {
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		isSystem := func(expr ast.Expr) bool {
			lit, ok := expr.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return false
			}
			value, err := strconv.Unquote(lit.Value)
			return err == nil && strings.TrimSpace(value) == "system"
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.CompositeLit:
				name := ""
				switch typ := n.Type.(type) {
				case *ast.Ident:
					name = typ.Name
				case *ast.SelectorExpr:
					name = typ.Sel.Name
				}
				index := -1
				if name == "ChatMessage" {
					index = 0
				}
				if name == "codexInputItem" {
					index = 1
				}
				if index >= 0 && len(n.Elts) > index && isSystem(n.Elts[index]) {
					t.Errorf("%s: constructed system role", fset.Position(n.Pos()))
				}
			case *ast.KeyValueExpr:
				if key, ok := n.Key.(*ast.Ident); ok && key.Name == "Role" && isSystem(n.Value) {
					t.Errorf("%s: constructed system role", fset.Position(n.Pos()))
				}
			case *ast.AssignStmt:
				for i, lhs := range n.Lhs {
					if i < len(n.Rhs) {
						if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "Role" && isSystem(n.Rhs[i]) {
							t.Errorf("%s: assigned system role", fset.Position(n.Pos()))
						}
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
