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

// arity returns how many arguments a message formats: a plural message is
// selected by its first argument even when a form prints none.
func arity(value any) int {
	n, next := 0, 1
	for _, verb := range verbs(value) {
		if match := argumentIndex.FindStringSubmatch(verb); match != nil {
			next, _ = strconv.Atoi(match[1])
		}
		n = max(n, next)
		next++
	}
	if _, plural := value.(map[string]string); plural {
		n = max(n, 1)
	}
	return n
}

// translateCall returns the key literal of an i18n.T call, or of a T call
// inside this package, with the number of arguments formatted into it, -1
// when they are spread from a slice.
func translateCall(node ast.Node, inPackage bool) (key string, args int, ok bool) {
	call, isCall := node.(*ast.CallExpr)
	if !isCall || len(call.Args) < 2 {
		return "", 0, false
	}
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		pkg, isIdent := fun.X.(*ast.Ident)
		if !isIdent || pkg.Name != "i18n" || fun.Sel.Name != "T" {
			return "", 0, false
		}
	case *ast.Ident:
		if !inPackage || fun.Name != "T" {
			return "", 0, false
		}
	default:
		return "", 0, false
	}
	literal, isLiteral := call.Args[1].(*ast.BasicLit)
	if !isLiteral || literal.Kind != token.STRING {
		return "", 0, false
	}
	key, err := strconv.Unquote(literal.Value)
	if err != nil {
		return "", 0, false
	}
	args = len(call.Args) - 2
	if call.Ellipsis.IsValid() {
		args = -1
	}
	return key, args, true
}

// TestMessageKeysMatchTheSourceCatalog keeps the code and locales/en.json in
// step: every key the code translates exists and gets as many arguments as
// its message formats, and every catalog key is used, either by a T call or
// as a string literal handed to one later.
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
		inPackage := filepath.Dir(path) == filepath.Join(root, "internal", "i18n")
		ast.Inspect(file, func(node ast.Node) bool {
			if literal, ok := node.(*ast.BasicLit); ok && literal.Kind == token.STRING {
				if value, err := strconv.Unquote(literal.Value); err == nil {
					if _, ok := catalog[value]; ok {
						used[value] = true
					}
				}
				return true
			}
			key, args, ok := translateCall(node, inPackage)
			if !ok {
				return true
			}
			position := fileSet.Position(node.Pos())
			message, exists := catalog[key]
			switch {
			case !exists:
				t.Errorf("%s: key %q is not in en.json", position, key)
			case args >= 0 && args != arity(message):
				t.Errorf("%s: key %q is given %d arguments, its message formats %d", position, key, args, arity(message))
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
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
