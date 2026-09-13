package httpx_test

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Keep literal response codes in the Go transports and platform translated. Parse Go
// syntax, including package-local problem helpers; comments and field errors are not
// response codes. Dynamically computed codes remain the responsibility of their domain tests.
func TestServerProblemCodesHaveTurkishMessages(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	raw, err := os.ReadFile(filepath.Join(root, "web/packages/i18n/src/locales/tr.json"))
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		Problems map[string]string `json:"problems"`
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	helpers := map[string]int{}
	err = filepath.WalkDir(filepath.Join(root, "internal"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		files[path] = file
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !strings.Contains(strings.ToLower(fn.Name.Name), "problem") {
				continue
			}
			index := 0
			for _, field := range fn.Type.Params.List {
				for _, name := range field.Names {
					if name.Name == "code" {
						helpers[filepath.Dir(path)+":"+fn.Name.Name] = index
					}
					index++
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	codes := map[string]token.Pos{}
	record := func(expr ast.Expr) {
		literal, ok := expr.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return
		}
		code, err := strconv.Unquote(literal.Value)
		if err != nil {
			t.Fatal(err)
		}
		if code != "" {
			codes[code] = literal.Pos()
		}
	}
	for path, file := range files {
		ast.Inspect(file, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.CompositeLit:
				name := ""
				switch typ := value.Type.(type) {
				case *ast.SelectorExpr:
					name = typ.Sel.Name
				case *ast.Ident:
					name = typ.Name
				}
				if name != "Problem" {
					break
				}
				for _, element := range value.Elts {
					kv, ok := element.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					key, ok := kv.Key.(*ast.Ident)
					if ok && key.Name == "Code" {
						record(kv.Value)
					}
				}
			case *ast.CallExpr:
				name, ok := value.Fun.(*ast.Ident)
				if !ok {
					break
				}
				index, ok := helpers[filepath.Dir(path)+":"+name.Name]
				if ok && index < len(value.Args) {
					record(value.Args[index])
				}
			}
			return true
		})
	}
	if len(codes) < 50 {
		t.Fatalf("unexpectedly few response codes discovered: %d", len(codes))
	}
	for code, pos := range codes {
		if strings.TrimSpace(catalog.Problems[code]) == "" {
			t.Errorf("missing problems.%s (%s)", code, fset.Position(pos))
		}
	}
}
