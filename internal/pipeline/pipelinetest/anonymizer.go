package pipelinetest

import (
	"context"
	goanon "github.com/bornholm/go-anon"
	"github.com/bornholm/go-anon/pkg/anonymizer"
	"github.com/bornholm/go-anon/pkg/ner"
)

// regexRecognizer implements anonymizer.Recognizer using only go-anon's
// builtin regex patterns (EMAIL, IBAN, PHONE, API keys, JWT, ...) — no NER
// model required.
type regexRecognizer struct {
	patterns []ner.RegexPattern
}

func (r *regexRecognizer) Recognize(_ context.Context, text string) ([]ner.Entity, error) {
	filter := ner.RegexEntityFilter(r.patterns)
	return filter(text, nil), nil
}

// NewRegexAnonymizer creates a *goanon.Anonymizer that detects entities using
// only go-anon's builtin regex patterns (EMAIL, IPV4/6, IBAN, SIRET/SIREN,
// PHONE) plus secret patterns (JWT, API keys, ...), with consistent
// placeholder numbering. It requires no NER model and no network access,
// making it suitable for isolated tests of the real go-anon
// anonymize/deanonymize behavior.
func NewRegexAnonymizer() *anonymizer.Anonymizer {
	patterns := append([]ner.RegexPattern{}, goanon.BuiltinRegexPatterns...)
	patterns = append(patterns, goanon.SecretPatterns()...)
	return goanon.NewAnonymizer(&regexRecognizer{patterns: patterns}, goanon.Config{
		Strategy:      goanon.Consistent,
		ConsistentMap: true,
	})
}
