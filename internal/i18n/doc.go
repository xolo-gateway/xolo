// Package i18n holds the message catalogs of the user interface and the
// language negotiation shared by the adapters.
//
// Each supported language is one embedded locales/<tag>.json file named after
// its canonical BCP 47 tag. A file maps stable message keys to either a
// golang.org/x/text/message format string or an object of CLDR plural forms
// ("zero", "one", "two", "few", "many", "other"; "other" is required) selected
// by the first argument. Every file defines "locale.name", the language's
// native name.
//
// English (locales/en.json) is the source: it defines the key set and is the
// fallback for a key another catalog lacks. Tests require every catalog to
// define exactly the source keys with the same formatting verbs, in every
// plural form, since the form the count selects gets all the arguments.
//
// To add a language, copy locales/en.json to locales/<tag>.json and translate
// it. Loading discovers the file; no code changes.
//
// The locale middleware resolves one language per request and stores it with
// Bundle.WithLanguage; templates and their components then format copy with
// T(ctx, key, args...) and list the selector entries with Options, so a
// component package needs no dependency on the adapter. A context without a
// resolved language, such as a handler called directly in a test, renders in
// Unlocalized (French).
package i18n
