package i18n

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// dynamicPrefixes are the key groups completed at runtime from an identifier;
// every other catalog key must appear literally in a Go file, templates
// included through their generated code.
var dynamicPrefixes = []string{"locale.", "rbac."}

// argumentIndex matches the explicit argument index of a verb, as in %[2]d.
var argumentIndex = regexp.MustCompile(`^%\[(\d+)\]`)

// arity returns how many arguments a message formats, the most of any of its
// plural forms: a plural message is also selected by its first argument even
// when a form prints none.
func arity(value any) int {
	forms, plural := value.(map[string]string)
	if !plural {
		return textArity(value.(string))
	}
	n := 1
	for _, text := range forms {
		n = max(n, textArity(text))
	}
	return n
}

func textArity(text string) int {
	n, next := 0, 1
	for _, verb := range verbs(text) {
		if match := argumentIndex.FindStringSubmatch(verb); match != nil {
			next, _ = strconv.Atoi(match[1])
		}
		n = max(n, next)
		next++
	}
	return n
}

// translateCall reports whether node is an i18n.T call, or a T call inside
// this package.
func translateCall(node ast.Node, inPackage bool) (*ast.CallExpr, bool) {
	call, isCall := node.(*ast.CallExpr)
	if !isCall || len(call.Args) < 2 {
		return nil, false
	}
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		pkg, isIdent := fun.X.(*ast.Ident)
		return call, isIdent && pkg.Name == "i18n" && fun.Sel.Name == "T"
	case *ast.Ident:
		return call, inPackage && fun.Name == "T"
	}
	return nil, false
}

// stringLiteral returns the value of a string literal expression.
func stringLiteral(expr ast.Expr) (string, bool) {
	literal, ok := expr.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(literal.Value)
	return value, err == nil
}

// TestMessageKeysMatchTheSourceCatalog keeps the code and locales/en.json in
// step: every key the code translates exists and gets as many arguments as
// its message formats, and every catalog key is used, either by a T call or
// as a string literal in a package that translates keys from a table.
func TestMessageKeysMatchTheSourceCatalog(t *testing.T) {
	data, err := fs.ReadFile(files, "locales/en.json")
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := decode(data)
	if err != nil {
		t.Fatal(err)
	}
	used := map[string]bool{}
	// stored lists, per package directory, the string literals equal to a
	// key; translatesStored marks the packages with a T call on a non-literal
	// key.
	stored := map[string][]string{}
	translatesStored := map[string]bool{}
	root := filepath.Join("..", "..")
	fileSet := token.NewFileSet()
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if name := entry.Name(); name == "node_modules" || name == "frontend" || name == ".git" || name == "tools" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fileSet, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		dir := filepath.Dir(path)
		inPackage := dir == filepath.Join(root, "internal", "i18n")
		ast.Inspect(file, func(node ast.Node) bool {
			if value, ok := stringLiteral(asExpr(node)); ok {
				if _, isKey := catalog[value]; isKey {
					stored[dir] = append(stored[dir], value)
				}
				return true
			}
			call, ok := translateCall(node, inPackage)
			if !ok {
				return true
			}
			key, literal := stringLiteral(call.Args[1])
			if !literal {
				// The key comes from a table: the package's literals that
				// equal a key are the candidates.
				translatesStored[dir] = true
				return true
			}
			used[key] = true
			position := fileSet.Position(call.Pos())
			message, exists := catalog[key]
			args := len(call.Args) - 2
			switch {
			case !exists:
				t.Errorf("%s: key %q is not in en.json", position, key)
			case !call.Ellipsis.IsValid() && args != arity(message):
				t.Errorf("%s: key %q is given %d arguments, its message formats %d", position, key, args, arity(message))
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// A key held in a table counts as used only in a package that translates
	// keys it does not spell out: elsewhere, a literal equal to a key is a
	// coincidence, not a translation.
	for dir, keys := range stored {
		if translatesStored[dir] {
			for _, key := range keys {
				used[key] = true
			}
		}
	}
	for key := range catalog {
		dynamic := slices.ContainsFunc(dynamicPrefixes, func(prefix string) bool { return strings.HasPrefix(key, prefix) })
		if !dynamic && !used[key] {
			t.Errorf("key %q in en.json is unused", key)
		}
	}
}

func TestArityCountsPositionalAndIndexedVerbs(t *testing.T) {
	for _, tc := range []struct {
		message any
		want    int
	}{
		{"plain", 0},
		{"100%% sure", 0},
		{"%s of %d", 2},
		{"%[2]s before %[1]s", 2},
		{"%[2]s then %s", 3},
		{map[string]string{"one": "one item", "other": "%d items"}, 1},
		{map[string]string{"other": "items"}, 1},
	} {
		if got := arity(tc.message); got != tc.want {
			t.Errorf("arity(%v)=%d, want %d", tc.message, got, tc.want)
		}
	}
}

// asExpr returns node as an expression, or nil.
func asExpr(node ast.Node) ast.Expr {
	expr, _ := node.(ast.Expr)
	return expr
}
