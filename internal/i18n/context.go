package i18n

import (
	"context"
	"sync"

	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

type contextKey struct{}

// localization is the request language with its printer and the bundle it
// was resolved from.
type localization struct {
	bundle  *Bundle
	tag     language.Tag
	printer *message.Printer
}

// Option is one supported language for a language selector.
type Option struct {
	Tag, Name string
	Current   bool
}

// WithLanguage returns a context that renders messages in tag.
func (b *Bundle) WithLanguage(ctx context.Context, tag language.Tag) context.Context {
	return context.WithValue(ctx, contextKey{}, localization{bundle: b, tag: tag, printer: b.Printer(tag)})
}

// Unlocalized is the language of a context no request language was resolved
// for, such as a handler called directly or a background render: French, the
// language Xolo was written in.
var Unlocalized = language.French

var unlocalized = sync.OnceValue(func() localization {
	bundle, err := Load()
	if err != nil {
		panic("load embedded locales: " + err.Error())
	}
	return localization{bundle: bundle, tag: Unlocalized, printer: bundle.Printer(Unlocalized)}
})

func from(ctx context.Context) localization {
	if l, ok := ctx.Value(contextKey{}).(localization); ok {
		return l
	}
	return unlocalized()
}

// T formats the message key in the context language, or in Unlocalized
// without one.
func T(ctx context.Context, key string, args ...any) string {
	return from(ctx).printer.Sprintf(key, args...)
}

// Language returns the context language, or Unlocalized without one.
func Language(ctx context.Context) language.Tag { return from(ctx).tag }

// Options lists the supported languages by native name, the context language
// marked current.
func Options(ctx context.Context) []Option {
	l := from(ctx)
	options := make([]Option, 0, len(l.bundle.tags))
	for _, tag := range l.bundle.tags {
		options = append(options, Option{Tag: tag.String(), Name: l.bundle.Name(tag), Current: tag == l.tag})
	}
	return options
}
