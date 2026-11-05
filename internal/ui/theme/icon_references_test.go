package theme

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

// Asset filenames and the public theme icon names differ. Check the actual
// view references so a migration cannot silently produce empty icon buttons.
func TestViewIconReferencesExist(t *testing.T) {
	arguments := map[string]int{"Icon": 1, "IconButton": 2, "CommandButton": 3}
	err := filepath.WalkDir("..", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		positions := token.NewFileSet()
		file, err := parser.ParseFile(positions, path, nil, 0)
		if err != nil {
			return err
		}
		aliases := map[string]bool{}
		for _, imp := range file.Imports {
			name, _ := strconv.Unquote(imp.Path.Value)
			if name == "github.com/nyakang/nyadb/internal/ui/theme" {
				alias := "theme"
				if imp.Name != nil {
					alias = imp.Name.Name
				}
				aliases[alias] = true
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := ""
			switch fun := call.Fun.(type) {
			case *ast.SelectorExpr:
				if owner, ok := fun.X.(*ast.Ident); ok && aliases[owner.Name] {
					name = fun.Sel.Name
				}
			case *ast.Ident:
				if file.Name.Name == "theme" {
					name = fun.Name
				}
			}
			index, ok := arguments[name]
			if !ok || len(call.Args) <= index {
				return true
			}
			literal, ok := call.Args[index].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			icon, err := strconv.Unquote(literal.Value)
			if err == nil && icon != "" && Symbols[icon] == nil {
				t.Errorf("%s: unregistered icon %q", positions.Position(literal.Pos()), icon)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
