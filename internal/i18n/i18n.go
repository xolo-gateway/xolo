package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"golang.org/x/text/feature/plural"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
	"golang.org/x/text/message/catalog"
)

//go:embed locales/*.json
var files embed.FS

// Source is the language every message key is written in first. Its catalog
// defines the key set and is the fallback for a key another catalog lacks.
var Source = language.English

// nameKey is the native name of a language, shown in the language selector.
const nameKey = "locale.name"

// pluralForms are the CLDR plural categories a plural message may define.
var pluralForms = []string{"zero", "one", "two", "few", "many", "other"}

// Bundle holds the message catalogs of every supported language.
type Bundle struct {
	catalog *catalog.Builder
	tags    []language.Tag
	names   map[language.Tag]string
	matcher language.Matcher
}

// Load reads the embedded catalogs.
func Load() (*Bundle, error) {
	locales, err := fs.Sub(files, "locales")
	if err != nil {
		return nil, fmt.Errorf("open embedded locales: %w", err)
	}
	return load(locales)
}

func load(locales fs.FS) (*Bundle, error) {
	paths, err := fs.Glob(locales, "*.json")
	if err != nil {
		return nil, fmt.Errorf("list locales: %w", err)
	}
	catalogs := make(map[language.Tag]map[string]any, len(paths))
	bundle := &Bundle{catalog: catalog.NewBuilder(), names: map[language.Tag]string{}}
	for _, name := range paths {
		tag, err := language.Parse(strings.TrimSuffix(name, ".json"))
		if err != nil || tag.String()+".json" != name {
			return nil, fmt.Errorf("locale file %s must be named after a canonical BCP 47 tag", name)
		}
		data, err := fs.ReadFile(locales, name)
		if err != nil {
			return nil, fmt.Errorf("read locale %s: %w", name, err)
		}
		messages, err := decode(data)
		if err != nil {
			return nil, fmt.Errorf("decode locale %s: %w", name, err)
		}
		localName, ok := messages[nameKey].(string)
		if !ok || localName == "" {
			return nil, fmt.Errorf("locale %s must define %q", tag, nameKey)
		}
		catalogs[tag] = messages
		bundle.names[tag] = localName
		bundle.tags = append(bundle.tags, tag)
	}
	source, ok := catalogs[Source]
	if !ok {
		return nil, fmt.Errorf("locale %s is required", Source)
	}
	for tag, messages := range catalogs {
		// A key missing from a translation renders in the source language
		// rather than as the bare key.
		for key, value := range source {
			if translated, ok := messages[key]; ok {
				value = translated
			}
			if err := bundle.catalog.Set(tag, key, compile(value)); err != nil {
				return nil, fmt.Errorf("set %s message %q: %w", tag, key, err)
			}
		}
	}
	// The source language comes first: it is what the matcher falls back to.
	slices.SortFunc(bundle.tags, func(a, b language.Tag) int {
		switch {
		case a == b:
			return 0
		case a == Source:
			return -1
		case b == Source:
			return 1
		}
		return strings.Compare(a.String(), b.String())
	})
	bundle.matcher = language.NewMatcher(bundle.tags)
	return bundle, nil
}

func compile(value any) catalog.Message {
	forms, ok := value.(map[string]string)
	if !ok {
		return catalog.String(value.(string))
	}
	cases := make([]any, 0, 2*len(forms))
	for _, form := range pluralForms {
		if text, ok := forms[form]; ok {
			cases = append(cases, form, text)
		}
	}
	return plural.Selectf(1, "", cases...)
}

// decode returns each message as a string or as plural forms keyed by CLDR
// category.
func decode(data []byte) (map[string]any, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	messages := make(map[string]any, len(raw))
	for key, value := range raw {
		var text string
		if err := json.Unmarshal(value, &text); err == nil {
			messages[key] = text
			continue
		}
		var forms map[string]string
		if err := json.Unmarshal(value, &forms); err != nil {
			return nil, fmt.Errorf("message %q must be a string or an object of plural forms", key)
		}
		if _, ok := forms["other"]; !ok {
			return nil, fmt.Errorf("message %q must define the plural form \"other\"", key)
		}
		for form := range forms {
			if !slices.Contains(pluralForms, form) {
				return nil, fmt.Errorf("message %q has unknown plural form %q", key, form)
			}
		}
		messages[key] = forms
	}
	return messages, nil
}

// Supported returns the supported languages, the source language first.
func (b *Bundle) Supported() []language.Tag { return slices.Clone(b.tags) }

// Lookup returns the supported language whose canonical tag is value.
func (b *Bundle) Lookup(value string) (language.Tag, bool) {
	tag, err := language.Parse(value)
	if err != nil || tag.String() != value || !slices.Contains(b.tags, tag) {
		return language.Und, false
	}
	return tag, true
}

// Match negotiates an Accept-Language header value. It returns fallback when
// no supported language is acceptable.
func (b *Bundle) Match(acceptLanguage string, fallback language.Tag) language.Tag {
	preferred, _, err := language.ParseAcceptLanguage(acceptLanguage)
	if err != nil || len(preferred) == 0 {
		return fallback
	}
	_, index, confidence := b.matcher.Match(preferred...)
	if confidence == language.No {
		return fallback
	}
	return b.tags[index]
}

// Printer formats messages in tag.
func (b *Bundle) Printer(tag language.Tag) *message.Printer {
	return message.NewPrinter(tag, message.Catalog(b.catalog))
}

// Name returns the native name of a supported language.
func (b *Bundle) Name(tag language.Tag) string { return b.names[tag] }
