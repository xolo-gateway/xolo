package i18n

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// dynamicPrefixes are the key groups completed at runtime from an identifier;
// every other catalog key must appear literally in a template or a Go file.
var dynamicPrefixes = []string{"locale.", "rbac."}

var (
	translateCall = regexp.MustCompile(`\bT\([^,()]*(?:\([^()]*\))?, "([^"]+)"`)
	keyLiteral    = regexp.MustCompile(`"([a-z][a-z0-9_]*(?:\.[a-z0-9_]+)+)"`)
)

// TestMessageKeysMatchTheSourceCatalog keeps the code and locales/en.json in
// step: every key the code translates exists, and every catalog key is used.
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
		if !strings.HasSuffix(path, ".templ") && (!strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, "_templ.go")) {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, match := range translateCall.FindAllStringSubmatch(string(content), -1) {
			// A key ending with a dot is a prefix completed at runtime.
			if _, ok := catalog[match[1]]; !ok && !strings.HasSuffix(match[1], ".") {
				t.Errorf("%s: key %q is not in en.json", path, match[1])
			}
		}
		for _, match := range keyLiteral.FindAllStringSubmatch(string(content), -1) {
			used[match[1]] = true
		}
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
