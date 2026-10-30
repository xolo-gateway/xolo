package i18n

import (
	"context"
	"encoding/json"
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"golang.org/x/text/language"
)

func TestEmbeddedCatalogsLoad(t *testing.T) {
	bundle, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	want := []language.Tag{language.English, language.Spanish, language.French}
	if got := bundle.Supported(); !slices.Equal(got, want) {
		t.Fatalf("supported=%v, want %v", got, want)
	}
	for tag, name := range map[language.Tag]string{language.English: "English", language.French: "Français", language.Spanish: "Español"} {
		if got := bundle.Name(tag); got != name {
			t.Errorf("name %s=%q, want %q", tag, got, name)
		}
	}
	if got := bundle.Printer(language.French).Sprintf("locale.label"); got != "Langue" {
		t.Fatalf("french message=%q", got)
	}
}

var verbPattern = regexp.MustCompile(`%(?:\[\d+\])?[-+# 0]*\d*(?:\.\d+)?[a-zA-Z%]`)

// verbs returns the formatting verbs of every form of a message, so a
// translation cannot drop or reorder an argument.
func verbs(value any) []string {
	var texts []string
	switch value := value.(type) {
	case string:
		texts = []string{value}
	case map[string]string:
		texts = []string{value["other"]}
	}
	var result []string
	for _, text := range texts {
		for _, verb := range verbPattern.FindAllString(text, -1) {
			if verb != "%%" {
				result = append(result, verb)
			}
		}
	}
	return result
}

func readCatalogs(t *testing.T) map[string]map[string]any {
	t.Helper()
	paths, err := fs.Glob(files, "locales/*.json")
	if err != nil {
		t.Fatal(err)
	}
	catalogs := map[string]map[string]any{}
	for _, path := range paths {
		data, err := fs.ReadFile(files, path)
		if err != nil {
			t.Fatal(err)
		}
		messages, err := decode(data)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		catalogs[strings.TrimSuffix(strings.TrimPrefix(path, "locales/"), ".json")] = messages
	}
	return catalogs
}

func TestCatalogsMatchTheSourceKeysAndVerbs(t *testing.T) {
	catalogs := readCatalogs(t)
	source := catalogs[Source.String()]
	if len(source) == 0 {
		t.Fatal("source catalog is empty")
	}
	for locale, messages := range catalogs {
		for key, value := range source {
			translated, ok := messages[key]
			if !ok {
				t.Errorf("%s: missing key %q", locale, key)
				continue
			}
			if got, want := verbs(translated), verbs(value); !slices.Equal(got, want) {
				t.Errorf("%s: key %q has verbs %v, want %v", locale, key, got, want)
			}
			// T formats whichever form the count selects with the same
			// arguments, so every form must format them all.
			if forms, plural := translated.(map[string]string); plural {
				for form, text := range forms {
					if got, want := verbs(text), verbs(value); !slices.Equal(got, want) {
						t.Errorf("%s: key %q form %q has verbs %v, want %v", locale, key, form, got, want)
					}
				}
			}
		}
		for key := range messages {
			if _, ok := source[key]; !ok {
				t.Errorf("%s: key %q is not in the source catalog", locale, key)
			}
		}
	}
}

// TestCatalogsEscapeTheirPercentSigns catches a literal "%" left unescaped:
// T formats every message, so "(%)" renders as "%!)(MISSING)", and the verb
// comparison above does not see it, matching verbs only.
func TestCatalogsEscapeTheirPercentSigns(t *testing.T) {
	for locale, messages := range readCatalogs(t) {
		for key, value := range messages {
			texts := []string{}
			switch value := value.(type) {
			case string:
				texts = append(texts, value)
			case map[string]string:
				for _, text := range value {
					texts = append(texts, text)
				}
			}
			for _, text := range texts {
				if rest := verbPattern.ReplaceAllString(text, ""); strings.Contains(rest, "%") {
					t.Errorf("%s: key %q has an unescaped %%: %q", locale, key, text)
				}
			}
		}
	}
}

func TestCatalogFilesAreValidJSONWithoutDuplicateKeys(t *testing.T) {
	paths, _ := fs.Glob(files, "locales/*.json")
	for _, path := range paths {
		data, _ := fs.ReadFile(files, path)
		decoder := json.NewDecoder(strings.NewReader(string(data)))
		seen := map[string]bool{}
		if _, err := decoder.Token(); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		for decoder.More() {
			token, err := decoder.Token()
			if err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			key := token.(string)
			if seen[key] {
				t.Errorf("%s: duplicate key %q", path, key)
			}
			seen[key] = true
			var skip json.RawMessage
			if err := decoder.Decode(&skip); err != nil {
				t.Fatalf("%s: %v", path, err)
			}
		}
	}
}

func TestMatchNegotiatesAcceptLanguage(t *testing.T) {
	bundle, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for header, want := range map[string]language.Tag{
		"fr-CA,fr;q=0.9,en;q=0.8": language.French,
		"es-419":                  language.Spanish,
		"de,es;q=0.5":             language.Spanish,
		"en;q=0.2,fr;q=0.9":       language.French,
		"de":                      language.French,
		"":                        language.French,
		"not a header;;;":         language.French,
	} {
		if got := bundle.Match(header, language.French); got != want {
			t.Errorf("Match(%q)=%v, want %v", header, got, want)
		}
	}
}

func TestLookupAcceptsOnlySupportedCanonicalTags(t *testing.T) {
	bundle, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if tag, ok := bundle.Lookup("es"); !ok || tag != language.Spanish {
		t.Fatalf("Lookup(es)=%v,%v", tag, ok)
	}
	for _, value := range []string{"", "de", "fr-CA", "FR", "x", "../en"} {
		if tag, ok := bundle.Lookup(value); ok {
			t.Errorf("Lookup(%q)=%v", value, tag)
		}
	}
}

func TestClosestServesTheNearestSupportedLanguage(t *testing.T) {
	bundle, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for tag, want := range map[string]language.Tag{
		"fr":         language.French,
		"fr-CA":      language.French,
		"es-419":     language.Spanish,
		"en-Latn-US": language.English,
	} {
		if got, ok := bundle.Closest(tag); !ok || got != want {
			t.Errorf("Closest(%q)=%v,%v, want %v", tag, got, ok, want)
		}
	}
	for _, tag := range []string{"", "pt-BR", "de", "it", "not a tag"} {
		if got, ok := bundle.Closest(tag); ok {
			t.Errorf("Closest(%q)=%v", tag, got)
		}
	}
}

func TestPluralFormsFollowCLDR(t *testing.T) {
	bundle, err := load(fstest.MapFS{
		"en.json": {Data: []byte(`{"locale.name":"English","items":{"one":"%d item","other":"%d items"}}`)},
		"fr.json": {Data: []byte(`{"locale.name":"Français","items":{"one":"%d élément","other":"%d éléments"}}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		tag   language.Tag
		count int
		want  string
	}{
		{language.English, 0, "0 items"},
		{language.English, 1, "1 item"},
		{language.French, 0, "0 élément"},
		{language.French, 1, "1 élément"},
		{language.French, 2, "2 éléments"},
	} {
		if got := bundle.Printer(test.tag).Sprintf("items", test.count); got != test.want {
			t.Errorf("%s %d=%q, want %q", test.tag, test.count, got, test.want)
		}
	}
}

func TestMissingTranslationFallsBackToSource(t *testing.T) {
	bundle, err := load(fstest.MapFS{
		"en.json": {Data: []byte(`{"locale.name":"English","greeting":"Hello"}`)},
		"fr.json": {Data: []byte(`{"locale.name":"Français"}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := bundle.Printer(language.French).Sprintf("greeting"); got != "Hello" {
		t.Fatalf("fallback=%q", got)
	}
}

func TestLoadRejectsInvalidCatalogs(t *testing.T) {
	for name, locales := range map[string]fstest.MapFS{
		"malformed json":     {"en.json": {Data: []byte(`{`)}},
		"unknown plural":     {"en.json": {Data: []byte(`{"locale.name":"English","x":{"several":"a","other":"b"}}`)}},
		"missing other":      {"en.json": {Data: []byte(`{"locale.name":"English","x":{"one":"a"}}`)}},
		"wrong value type":   {"en.json": {Data: []byte(`{"locale.name":"English","x":1}`)}},
		"missing name":       {"en.json": {Data: []byte(`{"x":"y"}`)}},
		"missing source":     {"fr.json": {Data: []byte(`{"locale.name":"Français"}`)}},
		"non canonical file": {"en.json": {Data: []byte(`{"locale.name":"English"}`)}, "FR.json": {Data: []byte(`{"locale.name":"Français"}`)}},
		"invalid tag":        {"en.json": {Data: []byte(`{"locale.name":"English"}`)}, "zz-invalid-tag.json": {Data: []byte(`{"locale.name":"?"}`)}},
	} {
		if _, err := load(locales); err == nil {
			t.Errorf("%s: load succeeded", name)
		}
	}
}

func TestContextCarriesTheLanguage(t *testing.T) {
	bundle, err := load(fstest.MapFS{
		"en.json": {Data: []byte(`{"locale.name":"English","greeting":"Hello %s"}`)},
		"fr.json": {Data: []byte(`{"locale.name":"Français","greeting":"Bonjour %s"}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := T(context.Background(), "locale.label"); got != "Langue" {
		t.Fatalf("without language=%q", got)
	}
	if got := Language(context.Background()); got != Unlocalized {
		t.Fatalf("default language=%v", got)
	}
	ctx := bundle.WithLanguage(context.Background(), language.French)
	if got := T(ctx, "greeting", "Ada"); got != "Bonjour Ada" {
		t.Fatalf("french=%q", got)
	}
	want := []Option{{Tag: "en", Name: "English"}, {Tag: "fr", Name: "Français", Current: true}}
	if got := Options(ctx); !slices.Equal(got, want) || Language(ctx) != language.French {
		t.Fatalf("options=%v language=%v", got, Language(ctx))
	}
}
